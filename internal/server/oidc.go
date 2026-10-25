package server

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// cacheTTL is how long the provider discovery document and the JWKS are reused
// before they are fetched again.
const cacheTTL = 10 * time.Minute

// OIDC holds OpenID Connect provider settings.
type OIDC struct {
	Issuer   string
	ClientID string
}

// providerMetadata is the part of the OIDC discovery document the server uses.
// Endpoint paths differ per provider (Keycloak, Pocket ID, ...), so they are
// read from the document instead of being hardcoded.
type providerMetadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// Identity is the authenticated user derived from an OIDC ID token.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Username      string
}

// Verifier validates OIDC ID tokens against the provider's JWKS endpoint.
type Verifier struct {
	issuer   string
	clientID string
	client   *http.Client
	keys     map[string]*rsa.PublicKey
	mu       sync.RWMutex
	fetched  time.Time
	meta     *providerMetadata
	metaAt   time.Time
}

func NewVerifier(oidc OIDC) *Verifier {
	return &Verifier{
		issuer:   oidc.Issuer,
		clientID: oidc.ClientID,
		client:   &http.Client{Timeout: 10 * time.Second},
		keys:     map[string]*rsa.PublicKey{},
	}
}

// Verify parses and validates an ID token, returning the identity claims. An
// empty issuer disables OIDC and accepts any non-empty bearer token (dev only).
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	if v.issuer == "" {
		if token == "" {
			return Identity{}, ErrUnauthenticated
		}
		return Identity{Subject: "dev-user", Email: "dev-user@dev.local", Username: "dev-user"}, nil
	}
	if err := v.ensureKeys(ctx); err != nil {
		return Identity{}, err
	}
	tok, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		v.mu.RLock()
		key := v.keys[kid]
		v.mu.RUnlock()
		if key == nil {
			return nil, fmt.Errorf("unknown key id: %s", kid)
		}
		return key, nil
	})
	if err != nil {
		return Identity{}, err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok || !tok.Valid {
		return Identity{}, ErrUnauthenticated
	}
	iss, _ := claims["iss"].(string)
	if iss != v.issuer {
		return Identity{}, fmt.Errorf("issuer mismatch: %q != %q", iss, v.issuer)
	}
	if v.clientID == "" {
		return Identity{}, fmt.Errorf("oidc client id not configured")
	}
	// "aud" is a string or an array of strings, depending on the provider.
	aud, err := claims.GetAudience()
	if err != nil || !slices.Contains(aud, v.clientID) {
		return Identity{}, fmt.Errorf("audience mismatch")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Identity{}, ErrUnauthenticated
	}
	id := Identity{Subject: sub}
	id.Email, _ = claims["email"].(string)
	id.Username, _ = claims["preferred_username"].(string)
	switch verified := claims["email_verified"].(type) {
	case bool:
		id.EmailVerified = verified
	case string:
		id.EmailVerified = verified == "true"
	}
	return id, nil
}

func (v *Verifier) ensureKeys(ctx context.Context) error {
	v.mu.RLock()
	fresh := len(v.keys) > 0 && time.Since(v.fetched) < cacheTTL
	v.mu.RUnlock()
	if fresh {
		return nil
	}
	// The JWKS URL comes from the discovery document. Fetch it before taking
	// the lock, metadata() needs the lock itself. Without a discovery document
	// the default location is used, as before.
	meta, err := v.metadata(ctx)
	if err != nil {
		meta = &providerMetadata{}
	}
	jwksURL := meta.JWKSURI
	if jwksURL == "" {
		jwksURL = strings.TrimRight(v.issuer, "/") + "/.well-known/jwks.json"
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.keys) > 0 && time.Since(v.fetched) < cacheTTL {
		return nil
	}
	body, err := v.get(ctx, jwksURL)
	if err != nil {
		return err
	}
	keys, err := parseJWKS(body)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("no usable signing key in %s", jwksURL)
	}
	v.keys = keys
	v.fetched = time.Now()
	return nil
}

// metadata returns the provider's OIDC discovery document, cached for cacheTTL.
func (v *Verifier) metadata(ctx context.Context) (*providerMetadata, error) {
	v.mu.RLock()
	meta := v.meta
	fresh := meta != nil && time.Since(v.metaAt) < cacheTTL
	v.mu.RUnlock()
	if fresh {
		return meta, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.meta != nil && time.Since(v.metaAt) < cacheTTL {
		return v.meta, nil
	}
	body, err := v.get(ctx, strings.TrimRight(v.issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	var m providerMetadata
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	v.meta = &m
	v.metaAt = time.Now()
	return v.meta, nil
}

// get fetches a URL and returns the response body.
func (v *Verifier) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// parseJWKS decodes a JWKS document into RSA verification keys. A key without
// a "use" field is accepted: Pocket ID omits it.
func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, raw := range jwks.Keys {
		var jwk struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		}
		if err := json.Unmarshal(raw, &jwk); err != nil {
			continue
		}
		if jwk.Kty != "RSA" || (jwk.Use != "" && jwk.Use != "sig") || jwk.Kid == "" {
			continue
		}
		key, err := jwkToRSAPublicKey(jwk.N, jwk.E)
		if err != nil {
			continue
		}
		keys[jwk.Kid] = key
	}
	return keys, nil
}

// jwkToRSAPublicKey decodes base64url JWK modulus/exponent into an RSA key.
func jwkToRSAPublicKey(nB64, eB64 string) (*rsa.PublicKey, error) {
	n, err := decodeBase64URL(nB64)
	if err != nil {
		return nil, err
	}
	eBytes, err := decodeBase64URL(eB64)
	if err != nil {
		return nil, err
	}
	e := int(big.NewInt(0).SetBytes(eBytes).Int64())
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: e}, nil
}

func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// ErrUnauthenticated is returned when no valid identity can be established.
var ErrUnauthenticated = fmt.Errorf("unauthenticated")

// OAuth2 returns an oauth2.Config for the authorization code flow. The
// endpoints come from the provider's discovery document, so any OIDC provider
// works (Keycloak, Pocket ID, ...) and not just the ones with fixed paths.
func (v *Verifier) OAuth2(ctx context.Context) (*oauth2.Config, error) {
	meta, err := v.metadata(ctx)
	if err != nil {
		return nil, err
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("oidc discovery: no authorization/token endpoint at %s", v.issuer)
	}
	return &oauth2.Config{
		ClientID:    v.clientID,
		Endpoint:    oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint},
		Scopes:      []string{"openid", "email", "profile"},
		RedirectURL: "",
	}, nil
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

type ctxKey int

const ctxUserKey ctxKey = iota

func userID(r *http.Request) Identity {
	if v, ok := r.Context().Value(ctxUserKey).(Identity); ok {
		return v
	}
	return Identity{}
}

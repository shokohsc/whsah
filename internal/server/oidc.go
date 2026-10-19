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
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// OIDC holds OpenID Connect provider settings.
type OIDC struct {
	Issuer   string
	ClientID string
}

// Identity is the authenticated user derived from an OIDC ID token.
type Identity struct {
	Subject  string
	Email    string
	Username string
}

// Verifier validates OIDC ID tokens against the provider's JWKS endpoint.
type Verifier struct {
	issuer   string
	clientID string
	client   *http.Client
	keys     map[string]*rsa.PublicKey
	mu       sync.RWMutex
	fetched  time.Time
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
	if v.clientID != "" {
		if aud, _ := claims["aud"].(string); aud != v.clientID {
			return Identity{}, fmt.Errorf("audience mismatch")
		}
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Identity{}, ErrUnauthenticated
	}
	id := Identity{Subject: sub}
	id.Email, _ = claims["email"].(string)
	id.Username, _ = claims["preferred_username"].(string)
	return id, nil
}

func (v *Verifier) ensureKeys(ctx context.Context) error {
	v.mu.RLock()
	fresh := len(v.keys) > 0 && time.Since(v.fetched) < 10*time.Minute
	v.mu.RUnlock()
	if fresh {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.keys) > 0 && time.Since(v.fetched) < 10*time.Minute {
		return nil
	}
	jwksURL := strings.TrimRight(v.issuer, "/") + "/.well-known/jwks.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var jwks struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &jwks); err != nil {
		return err
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
		if jwk.Kty != "RSA" || jwk.Use != "sig" || jwk.Kid == "" {
			continue
		}
		key, err := jwkToRSAPublicKey(jwk.N, jwk.E)
		if err != nil {
			continue
		}
		keys[jwk.Kid] = key
	}
	v.keys = keys
	v.fetched = time.Now()
	return nil
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
var ErrUnauthenticated = fmt.Errorf("unauthenticated")// OAuth2 returns an oauth2.Config for the authorization code flow.
func (v *Verifier) OAuth2() *oauth2.Config {
	return &oauth2.Config{
		ClientID:    v.clientID,
		Endpoint:    oauth2.Endpoint{AuthURL: strings.TrimRight(v.issuer, "/") + "/protocol/openid-connect/auth", TokenURL: strings.TrimRight(v.issuer, "/") + "/protocol/openid-connect/token"},
		Scopes:      []string{"openid", "email", "profile"},
		RedirectURL: "",
	}
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

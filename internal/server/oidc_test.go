package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// pocketIDProvider serves a Pocket ID shaped provider: a discovery document
// with the Pocket ID endpoint paths and a JWKS without a "use" field.
func pocketIDProvider(t *testing.T, key *rsa.PrivateKey, kid string) *httptest.Server {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
			srv.URL, srv.URL+"/authorize", srv.URL+"/api/oidc/token", srv.URL+"/.well-known/jwks.json")
	})
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"keys":[{"kty":"RSA","alg":"RS256","kid":%q,"n":%q,"e":%q}]}`, kid, n, e)
	})
	return srv
}

func signIDToken(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestVerifyDevModeEmptyIssuer(t *testing.T) {
	v := NewVerifier(OIDC{})
	// empty token -> unauthenticated
	if _, err := v.Verify(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty token in dev mode")
	}
	// any non-empty token -> dev-user
	id, err := v.Verify(context.Background(), "any-token")
	if err != nil {
		t.Fatalf("expected no error in dev mode, got: %v", err)
	}
	if id.Subject != "dev-user" {
		t.Fatalf("expected dev-user, got %q", id.Subject)
	}
}

func TestVerifyRejectsBadToken(t *testing.T) {
	v := NewVerifier(OIDC{Issuer: "https://example.local"})
	_, err := v.Verify(context.Background(), "not-a-jwt")
	if err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestOAuth2UsesDiscoveredEndpoints(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := pocketIDProvider(t, key, "kid-1")

	v := NewVerifier(OIDC{Issuer: srv.URL, ClientID: "vdi-server"})
	oc, err := v.OAuth2(context.Background())
	if err != nil {
		t.Fatalf("OAuth2: %v", err)
	}
	if oc.Endpoint.AuthURL != srv.URL+"/authorize" {
		t.Fatalf("auth url = %q, want %q", oc.Endpoint.AuthURL, srv.URL+"/authorize")
	}
	if oc.Endpoint.TokenURL != srv.URL+"/api/oidc/token" {
		t.Fatalf("token url = %q, want %q", oc.Endpoint.TokenURL, srv.URL+"/api/oidc/token")
	}
}

func TestOAuth2DiscoveryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	v := NewVerifier(OIDC{Issuer: srv.URL, ClientID: "vdi-server"})
	if _, err := v.OAuth2(context.Background()); err == nil {
		t.Fatal("expected error when the provider has no discovery document")
	}
}

func TestVerifyAcceptsPocketIDToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := pocketIDProvider(t, key, "kid-1")
	v := NewVerifier(OIDC{Issuer: srv.URL, ClientID: "vdi-server"})

	signed := signIDToken(t, key, "kid-1", jwt.MapClaims{
		"iss":                srv.URL,
		"sub":                "8f14e45f-ea1c-4a3b-9f2b-1b6f5f0a2c3d",
		"aud":                "vdi-server",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"preferred_username": "alice",
		"email":              "alice@example.local",
	})
	id, err := v.Verify(context.Background(), signed)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "8f14e45f-ea1c-4a3b-9f2b-1b6f5f0a2c3d" {
		t.Fatalf("subject = %q", id.Subject)
	}
	if id.Username != "alice" || id.Email != "alice@example.local" {
		t.Fatalf("unexpected identity: %+v", id)
	}
}

func TestVerifyAcceptsAudienceArray(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	srv := pocketIDProvider(t, key, "kid-1")
	v := NewVerifier(OIDC{Issuer: srv.URL, ClientID: "vdi-server"})

	signed := signIDToken(t, key, "kid-1", jwt.MapClaims{
		"iss": srv.URL,
		"sub": "sub-1",
		"aud": []string{"other-client", "vdi-server"},
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := v.Verify(context.Background(), signed); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestParseJWKSAcceptsKeyWithoutUse(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())

	keys, err := parseJWKS([]byte(fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"k1","n":%q,"e":%q}]}`, n, e)))
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if _, ok := keys["k1"]; !ok {
		t.Fatal("expected key without \"use\" to be accepted")
	}

	keys, err = parseJWKS([]byte(fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"enc","kid":"k2","n":%q,"e":%q}]}`, n, e)))
	if err != nil {
		t.Fatalf("parseJWKS: %v", err)
	}
	if len(keys) != 0 {
		t.Fatal("expected encryption key to be skipped")
	}
}

func TestAgeAllowed(t *testing.T) {
	cases := []struct {
		age  int
		pegi string
		want bool
	}{
		{2, "3", false},
		{3, "3", true},
		{6, "7", false},
		{7, "7", true},
		{11, "12", false},
		{12, "12", true},
		{15, "16", false},
		{16, "16", true},
		{17, "18", false},
		{18, "18", true},
		{50, "3", true},
		{1, "18", false},
		{99, "unknown", true}, // unknown rating -> floor 0 -> allowed
	}
	for _, c := range cases {
		if got := ageAllowed(c.age, c.pegi); got != c.want {
			t.Errorf("ageAllowed(%d, %q) = %v, want %v", c.age, c.pegi, got, c.want)
		}
	}
}

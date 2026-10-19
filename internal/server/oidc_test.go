package server

import (
	"context"
	"testing"
)

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

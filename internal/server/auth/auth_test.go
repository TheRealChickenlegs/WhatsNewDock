package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct horse battery staple" {
		t.Error("hash must not equal plaintext")
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("expected verify to succeed")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Error("expected verify to fail for wrong password")
	}
}

func TestIssueAndValidateSession(t *testing.T) {
	m := &Manager{cfg: &config.AuthConfig{SessionTTL: time.Hour}, secret: []byte("secret")}
	token, err := m.IssueSession("alice", RoleAdmin, "local")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := m.ValidateSession(token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.Username != "alice" || claims.Role != RoleAdmin || claims.Source != "local" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestValidateSessionRejectsTampered(t *testing.T) {
	m := &Manager{cfg: &config.AuthConfig{SessionTTL: time.Hour}, secret: []byte("secret")}
	token, _ := m.IssueSession("alice", RoleViewer, "local")
	if _, err := m.ValidateSession(token + "x"); err == nil {
		t.Error("expected error for tampered token")
	}
	other := &Manager{cfg: &config.AuthConfig{SessionTTL: time.Hour}, secret: []byte("other")}
	if _, err := other.ValidateSession(token); err == nil {
		t.Error("expected error when validated with a different secret")
	}
}

func TestValidateSessionExpired(t *testing.T) {
	m := &Manager{secret: []byte("secret")}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Username: "alice",
		Role:     RoleViewer,
		Source:   "local",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := m.ValidateSession(signed); err == nil {
		t.Error("expected expired session error")
	}
}

func TestHashToken(t *testing.T) {
	h := HashToken("wnd_secret")
	if h == "wnd_secret" {
		t.Error("hash must not equal plaintext")
	}
	if HashToken("wnd_secret") != h {
		t.Error("hash must be deterministic")
	}
	if HashToken("other") == h {
		t.Error("different inputs must hash differently")
	}
}

func TestOIDCEnabledRequiresMode(t *testing.T) {
	m := &Manager{cfg: &config.AuthConfig{Mode: config.AuthLocal, OIDC: config.OIDCConfig{IssuerURL: "https://issuer"}}}
	if m.OIDCEnabled() {
		t.Error("OIDC must not be enabled in local mode")
	}
	m.cfg.Mode = config.AuthOIDC
	if !m.OIDCEnabled() {
		t.Error("OIDC must be enabled in oidc mode with an issuer")
	}
	m.cfg.OIDC.IssuerURL = ""
	if m.OIDCEnabled() {
		t.Error("OIDC must not be enabled without an issuer")
	}
}

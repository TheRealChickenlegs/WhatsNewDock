// Package auth provides authentication for the web UI: local username/password
// accounts and optional OpenID Connect (e.g. PocketID). Sessions are signed
// JWTs delivered as HttpOnly cookies.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Role constants.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// Claims is the JWT payload for a web session.
type Claims struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Source   string `json:"source"` // "local" | "oidc"
	jwt.RegisteredClaims
}

// Manager handles authentication state and operations.
type Manager struct {
	cfg      config.AuthConfig
	st       *store.Store
	baseURL  string
	secret   []byte
	provider *oidc.Provider
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// New builds an auth Manager, loading or creating the session secret.
func New(cfg config.AuthConfig, st *store.Store, baseURL string) (*Manager, error) {
	m := &Manager{cfg: cfg, st: st, baseURL: baseURL}
	secret, err := m.loadSecret()
	if err != nil {
		return nil, err
	}
	m.secret = secret
	return m, nil
}

func (m *Manager) loadSecret() ([]byte, error) {
	if m.cfg.SessionSecret != "" {
		return []byte(m.cfg.SessionSecret), nil
	}
	got, err := m.st.GetSetting("session_secret")
	if err != nil {
		return nil, err
	}
	if got != "" {
		return []byte(got), nil
	}
	// Generate and persist a random 32-byte secret.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate session secret: %w", err)
	}
	secret := base64.RawStdEncoding.EncodeToString(buf)
	if err := m.st.SetSetting("session_secret", secret); err != nil {
		return nil, err
	}
	return []byte(secret), nil
}

// HashPassword derives a bcrypt hash.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// VerifyPassword checks a plaintext password against a bcrypt hash.
func VerifyPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// IssueSession signs a session token for the given identity.
func (m *Manager) IssueSession(username, role, source string) (string, error) {
	ttl := m.cfg.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now()
	claims := Claims{
		Username: username,
		Role:     role,
		Source:   source,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "whatsnewdock",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(m.secret)
}

// ValidateSession parses and verifies a session token.
func (m *Manager) ValidateSession(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// AuthenticateLocal verifies local credentials and returns the user on success.
func (m *Manager) AuthenticateLocal(ctx context.Context, username, password string) (*store.User, error) {
	_ = ctx
	u, err := m.st.GetUserByUsername(username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		// Burn a bcrypt comparison to reduce username-enumeration timing signal.
		_ = VerifyPassword("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy", "dummy")
		return nil, errors.New("invalid credentials")
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return nil, errors.New("invalid credentials")
	}
	return u, nil
}

// OIDCEnabled reports whether OIDC is configured.
func (m *Manager) OIDCEnabled() bool {
	return m.cfg.OIDC.IssuerURL != ""
}

// ensureOIDC lazily initialises the OIDC provider and oauth2 config.
func (m *Manager) ensureOIDC(ctx context.Context) error {
	if m.provider != nil {
		return nil
	}
	issuer := strings.TrimSuffix(m.cfg.OIDC.IssuerURL, "/")
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return fmt.Errorf("init oidc provider: %w", err)
	}
	redirect := m.cfg.OIDC.RedirectURL
	if redirect == "" {
		redirect = strings.TrimSuffix(m.baseURL, "/") + "/api/auth/oidc/callback"
	}
	scopes := m.cfg.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	m.provider = provider
	m.oauth = &oauth2.Config{
		ClientID:     m.cfg.OIDC.ClientID,
		ClientSecret: m.cfg.OIDC.ClientSecret,
		RedirectURL:  redirect,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}
	m.verifier = provider.Verifier(&oidc.Config{ClientID: m.cfg.OIDC.ClientID})
	return nil
}

// OIDCAuthURL returns the authorization URL to redirect the user to.
func (m *Manager) OIDCAuthURL(ctx context.Context, state string) (string, error) {
	if err := m.ensureOIDC(ctx); err != nil {
		return "", err
	}
	return m.oauth.AuthCodeURL(state), nil
}

// OIDCUsername represents the resolved identity of an OIDC user.
type OIDCUsername struct {
	Username string
	Role     string
}

// ExchangeOIDC exchanges an authorization code and returns the user identity.
func (m *Manager) ExchangeOIDC(ctx context.Context, code string) (*OIDCUsername, error) {
	if err := m.ensureOIDC(ctx); err != nil {
		return nil, err
	}
	tok, err := m.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("oidc: no id_token in response")
	}
	idToken, err := m.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("oidc id_token verification: %w", err)
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc claims: %w", err)
	}
	username, _ := claims[m.cfg.OIDC.UsernameClaim].(string)
	if username == "" {
		username = idToken.Subject
	}
	role := m.cfg.OIDC.DefaultRole
	if role != RoleAdmin && role != RoleViewer {
		role = RoleViewer
	}
	return &OIDCUsername{Username: username, Role: role}, nil
}

// OIDCEnabledProviders is a debug helper.
func (m *Manager) OIDCEnabledProviders() bool { return m.OIDCEnabled() }

// HashToken hashes an agent token for storage (never store tokens in plaintext).
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

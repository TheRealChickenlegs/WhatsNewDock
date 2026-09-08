package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
)

const settingsAuthKey = "settings.auth"

// runtimeAuth is the user-editable authentication configuration persisted to
// the database. Environment variables provide the initial values; edits made
// through the UI override them from then on.
type runtimeAuth struct {
	Mode       config.AuthMode `json:"mode"`
	SessionTTL string          `json:"session_ttl"`
	OIDC       runtimeOIDC     `json:"oidc"`
}

type runtimeOIDC struct {
	IssuerURL     string   `json:"issuer_url"`
	ClientID      string   `json:"client_id"`
	ClientSecret  string   `json:"client_secret"`
	RedirectURL   string   `json:"redirect_url"`
	Scopes        []string `json:"scopes"`
	UsernameClaim string   `json:"username_claim"`
	DefaultRole   string   `json:"default_role"`
}

// loadAuthSettings overlays persisted auth settings onto the live config.
func (s *Server) loadAuthSettings() error {
	raw, err := s.st.GetSetting(settingsAuthKey)
	if err != nil || raw == "" {
		return err
	}
	var ra runtimeAuth
	if err := json.Unmarshal([]byte(raw), &ra); err != nil {
		return err
	}
	applyRuntimeAuth(&s.cfg.Auth, ra)
	return nil
}

func (s *Server) saveAuthSettings(ra runtimeAuth) error {
	b, err := json.Marshal(ra)
	if err != nil {
		return err
	}
	return s.st.SetSetting(settingsAuthKey, string(b))
}

// applyRuntimeAuth copies a persisted auth snapshot into the live config.
func applyRuntimeAuth(dst *config.AuthConfig, ra runtimeAuth) {
	if ra.Mode == config.AuthLocal || ra.Mode == config.AuthOIDC || ra.Mode == config.AuthBoth {
		dst.Mode = ra.Mode
	}
	if d, err := time.ParseDuration(ra.SessionTTL); err == nil && d > 0 {
		dst.SessionTTL = d
	}
	dst.OIDC.IssuerURL = ra.OIDC.IssuerURL
	dst.OIDC.ClientID = ra.OIDC.ClientID
	dst.OIDC.ClientSecret = ra.OIDC.ClientSecret
	dst.OIDC.RedirectURL = ra.OIDC.RedirectURL
	dst.OIDC.Scopes = ra.OIDC.Scopes
	dst.OIDC.UsernameClaim = ra.OIDC.UsernameClaim
	dst.OIDC.DefaultRole = ra.OIDC.DefaultRole
}

// authSettingsResponse is the redacted auth configuration returned to admins.
type authSettingsResponse struct {
	Mode       config.AuthMode `json:"mode"`
	SessionTTL string          `json:"session_ttl"`
	OIDC       struct {
		IssuerURL       string   `json:"issuer_url"`
		ClientID        string   `json:"client_id"`
		ClientSecretSet bool     `json:"client_secret_set"`
		RedirectURL     string   `json:"redirect_url"`
		Scopes          []string `json:"scopes"`
		UsernameClaim   string   `json:"username_claim"`
		DefaultRole     string   `json:"default_role"`
	} `json:"oidc"`
}

func (s *Server) handleGetAuthSettings(w http.ResponseWriter, r *http.Request) {
	resp := authSettingsResponse{Mode: s.cfg.Auth.Mode, SessionTTL: s.cfg.Auth.SessionTTL.String()}
	resp.OIDC.IssuerURL = s.cfg.Auth.OIDC.IssuerURL
	resp.OIDC.ClientID = s.cfg.Auth.OIDC.ClientID
	resp.OIDC.ClientSecretSet = s.cfg.Auth.OIDC.ClientSecret != ""
	resp.OIDC.RedirectURL = s.cfg.Auth.OIDC.RedirectURL
	resp.OIDC.Scopes = s.cfg.Auth.OIDC.Scopes
	resp.OIDC.UsernameClaim = s.cfg.Auth.OIDC.UsernameClaim
	resp.OIDC.DefaultRole = s.cfg.Auth.OIDC.DefaultRole
	writeJSON(w, http.StatusOK, resp)
}

type putAuthSettingsRequest struct {
	Mode       config.AuthMode `json:"mode"`
	SessionTTL string          `json:"session_ttl"`
	OIDC       struct {
		IssuerURL     string   `json:"issuer_url"`
		ClientID      string   `json:"client_id"`
		ClientSecret  string   `json:"client_secret"`
		RedirectURL   string   `json:"redirect_url"`
		Scopes        []string `json:"scopes"`
		UsernameClaim string   `json:"username_claim"`
		DefaultRole   string   `json:"default_role"`
	} `json:"oidc"`
}

func (s *Server) handlePutAuthSettings(w http.ResponseWriter, r *http.Request) {
	var req putAuthSettingsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Mode != config.AuthLocal && req.Mode != config.AuthOIDC && req.Mode != config.AuthBoth {
		writeError(w, http.StatusBadRequest, "invalid auth mode")
		return
	}

	oidcEnabled := req.Mode == config.AuthOIDC || req.Mode == config.AuthBoth
	if oidcEnabled {
		if strings.TrimSpace(req.OIDC.IssuerURL) == "" {
			writeError(w, http.StatusBadRequest, "OIDC issuer URL is required when OIDC is enabled")
			return
		}
		if strings.TrimSpace(req.OIDC.ClientID) == "" {
			writeError(w, http.StatusBadRequest, "OIDC client ID is required when OIDC is enabled")
			return
		}
		if strings.TrimSpace(req.OIDC.ClientSecret) == "" && s.cfg.Auth.OIDC.ClientSecret == "" {
			writeError(w, http.StatusBadRequest, "OIDC client secret is required when OIDC is enabled")
			return
		}
	}

	ttl := s.cfg.Auth.SessionTTL
	if req.SessionTTL != "" {
		d, err := time.ParseDuration(req.SessionTTL)
		if err != nil || d <= 0 {
			writeError(w, http.StatusBadRequest, "invalid session_ttl")
			return
		}
		ttl = d
	}

	ra := runtimeAuth{
		Mode:       req.Mode,
		SessionTTL: ttl.String(),
		OIDC: runtimeOIDC{
			IssuerURL:     strings.TrimSpace(req.OIDC.IssuerURL),
			ClientID:      strings.TrimSpace(req.OIDC.ClientID),
			ClientSecret:  s.cfg.Auth.OIDC.ClientSecret, // keep existing unless a new one is supplied
			RedirectURL:   strings.TrimSpace(req.OIDC.RedirectURL),
			Scopes:        req.OIDC.Scopes,
			UsernameClaim: strings.TrimSpace(req.OIDC.UsernameClaim),
			DefaultRole:   req.OIDC.DefaultRole,
		},
	}
	if req.OIDC.ClientSecret != "" {
		ra.OIDC.ClientSecret = req.OIDC.ClientSecret
	}
	if ra.OIDC.DefaultRole != auth.RoleAdmin && ra.OIDC.DefaultRole != auth.RoleViewer {
		ra.OIDC.DefaultRole = auth.RoleViewer
	}

	applyRuntimeAuth(&s.cfg.Auth, ra)
	s.auth.InvalidateOIDC()

	if err := s.saveAuthSettings(ra); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

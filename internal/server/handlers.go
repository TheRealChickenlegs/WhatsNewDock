package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// --- JSON helpers ---------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
}

// --- Auth handlers --------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth.Mode == config.AuthOIDC {
		writeError(w, http.StatusBadRequest, "local login disabled; use OIDC")
		return
	}
	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	u, err := s.auth.AuthenticateLocal(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := s.auth.IssueSession(u.Username, u.Role, "local")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	s.setSessionCookie(w, r, token)
	_ = s.st.AddEvent(s.eventNow("auth", u.Username, "", "", "local login"))
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": u.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	claims, err := s.auth.ValidateSession(cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      claims.Username,
		"role":          claims.Role,
		"source":        claims.Source,
	})
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":    string(s.cfg.Auth.Mode),
		"oidc_enabled": s.auth.OIDCEnabled(),
	})
}

func (s *Server) handleOIDCURL(w http.ResponseWriter, r *http.Request) {
	if !s.auth.OIDCEnabled() {
		writeError(w, http.StatusNotFound, "oidc not configured")
		return
	}
	state := randomHex(16)
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "wnd_oidc_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "wnd_oidc_redirect",
		Value:    redirect,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	url, err := s.auth.OIDCAuthURL(r.Context(), state)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oidc init failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if err := r.URL.Query().Get("error"); err != "" {
		http.Redirect(w, r, "/login?error="+err, http.StatusFound)
		return
	}
	stateCookie, err := r.Cookie("wnd_oidc_state")
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "invalid oidc state")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}
	identity, err := s.auth.ExchangeOIDC(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "oidc exchange failed: "+err.Error())
		return
	}
	token, err := s.auth.IssueSession(identity.Username, identity.Role, "oidc")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session error")
		return
	}
	s.setSessionCookie(w, r, token)
	_ = s.st.AddEvent(s.eventNow("auth", identity.Username, "", "", "oidc login"))

	redirect := "/"
	if rc, err := r.Cookie("wnd_oidc_redirect"); err == nil && rc.Value != "" {
		redirect = rc.Value
	}
	http.SetCookie(w, &http.Cookie{Name: "wnd_oidc_state", Value: "", Path: "/", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: "wnd_oidc_redirect", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, redirect, http.StatusFound)
}

// --- small helpers --------------------------------------------------------

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) eventNow(kind, actor, serverID, containerID, msg string) *store.Event {
	return &store.Event{Kind: kind, Actor: actor, ServerID: serverID, ContainerID: containerID, Message: msg, Timestamp: time.Now().UTC()}
}

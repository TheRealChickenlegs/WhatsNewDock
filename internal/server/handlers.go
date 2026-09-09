package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	if !s.loginLimiter.allow(remoteIP(r).String()) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again later")
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
	redirect := safeRedirect(r.URL.Query().Get("redirect"))
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure derived from TLS
		Name:     "wnd_oidc_state",
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure derived from TLS
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
	// Pocket ID 2.7.0+ form-posts the callback parameters (code, state, error)
	// rather than returning them in the URL query string, so read them from
	// either the query string or the form body.
	if e := r.FormValue("error"); e != "" {
		slog.Warn("oidc provider returned error", "error", e)
		http.Redirect(w, r, "/login?error=oidc_error", http.StatusFound)
		return
	}
	stateParam := r.FormValue("state")
	stateCookie, err := r.Cookie("wnd_oidc_state")
	cookieValue := ""
	if err == nil {
		cookieValue = stateCookie.Value
	}
	if cookieValue == "" || cookieValue != stateParam {
		slog.Warn("oidc state validation failed",
			"method", r.Method,
			"cookie_present", cookieValue != "",
			"state_param_present", stateParam != "",
			"cookie_len", len(cookieValue),
			"state_param_len", len(stateParam))
		writeError(w, http.StatusBadRequest, "invalid oidc state")
		return
	}
	code := r.FormValue("code")
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
	if rc, err := r.Cookie("wnd_oidc_redirect"); err == nil {
		redirect = safeRedirect(rc.Value)
	}
	clearCookie(w, "wnd_oidc_state")
	clearCookie(w, "wnd_oidc_redirect")
	http.Redirect(w, r, redirect, http.StatusFound) // #nosec G710 -- redirect validated to a same-site path via safeRedirect
}

// --- small helpers --------------------------------------------------------

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// safeRedirect restricts a user-supplied post-login redirect to a same-site
// absolute path, preventing open-redirect attacks.
func safeRedirect(raw string) string {
	if raw == "" {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/"
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, "\\\r\n") {
		return "/"
	}
	return raw
}

func (s *Server) eventNow(kind, actor, serverID, containerID, msg string) *store.Event {
	return &store.Event{Kind: kind, Actor: actor, ServerID: serverID, ContainerID: containerID, Message: msg, Timestamp: time.Now().UTC()}
}

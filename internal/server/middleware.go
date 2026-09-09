package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
)

type ctxKey int

const (
	ctxClaims ctxKey = iota
)

const sessionCookie = "wnd_session"

func (s *Server) currentUser(r *http.Request) *auth.Claims {
	if c, ok := r.Context().Value(ctxClaims).(*auth.Claims); ok {
		return c
	}
	return nil
}

// recoverMiddleware converts panics into 500s and logs the stack.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "err", rec, "stack", string(debug.Stack()))
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets defensive response headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// requestLogger logs each request at debug level.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start).String())
	})
}

// requireSession enforces a valid session cookie and, for mutating methods, a
// same-origin marker header (lightweight CSRF protection).
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutating(r.Method) && r.Header.Get("X-Requested-With") != "whatsnewdock" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing CSRF header"})
			return
		}
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		claims, err := s.auth.ValidateSession(cookie.Value)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxClaims, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAdmin enforces admin role on top of a valid session.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.currentUser(r)
		if u == nil || u.Role != auth.RoleAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required"})
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// setSessionCookie writes the session cookie, respecting TLS.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := s.isTLS(r)
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite set; Secure derived from TLS
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.Auth.SessionTTL.Seconds()),
	})
}

// clearCookie removes a cookie by name.
func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	clearCookie(w, sessionCookie)
}

func (s *Server) isTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Only honour X-Forwarded-Proto from addresses within the configured
	// trusted-proxy CIDRs, otherwise any client could spoof the scheme.
	if s.isTrustedProxy(r) {
		return r.Header.Get("X-Forwarded-Proto") == "https"
	}
	// No TLS and not behind a configured proxy: plain HTTP, unless we only
	// ever expect HTTPS and no trusted proxies are configured at all.
	if s.cfg.TrustedProxies == "" {
		return strings.HasPrefix(s.cfg.BaseURL, "https://")
	}
	return false
}

// isTrustedProxy reports whether the request's remote address falls within a
// configured trusted-proxy CIDR.
func (s *Server) isTrustedProxy(r *http.Request) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	ip := remoteIP(r)
	if ip == nil {
		return false
	}
	for _, cidr := range s.trustedProxies {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteIP extracts the client IP from r.RemoteAddr.
func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// parseTrustedCIDRs parses a comma-separated list of CIDR notations, logging
// and skipping any that fail to parse.
func parseTrustedCIDRs(raw string) []*net.IPNet {
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			slog.Warn("ignoring invalid trusted proxy CIDR", "cidr", part, "err", err)
			continue
		}
		out = append(out, cidr)
	}
	return out
}

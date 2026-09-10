package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/whatsnewdock/whatsnewdock/internal/webui"
)

// routes composes the HTTP router.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// --- Public auth endpoints -------------------------------------------
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/auth/config", s.handleAuthConfig)
	mux.HandleFunc("GET /api/auth/oidc/url", s.handleOIDCURL)
	mux.HandleFunc("GET /api/auth/oidc/callback", s.handleOIDCCallback)

	// --- Agent endpoints (bearer-token auth) -----------------------------
	mux.HandleFunc("POST /api/agent/v1/report", s.handleAgentReport)
	mux.HandleFunc("GET /api/agent/v1/commands", s.handleAgentCommands)
	mux.HandleFunc("POST /api/agent/v1/commands/{id}/result", s.handleAgentCommandResult)

	// --- Session-protected data API --------------------------------------
	mux.Handle("GET /api/v1/overview", s.requireSession(http.HandlerFunc(s.handleOverview)))
	mux.Handle("GET /api/v1/servers", s.requireSession(http.HandlerFunc(s.handleListServers)))
	mux.Handle("GET /api/v1/servers/{id}", s.requireSession(http.HandlerFunc(s.handleGetServer)))
	mux.Handle("POST /api/v1/servers", s.requireAdmin(http.HandlerFunc(s.handleCreateServer)))
	mux.Handle("PATCH /api/v1/servers/{id}", s.requireAdmin(http.HandlerFunc(s.handleUpdateServer)))
	mux.Handle("DELETE /api/v1/servers/{id}", s.requireAdmin(http.HandlerFunc(s.handleDeleteServer)))

	mux.Handle("GET /api/v1/stacks", s.requireSession(http.HandlerFunc(s.handleListStacks)))
	mux.Handle("GET /api/v1/containers", s.requireSession(http.HandlerFunc(s.handleListContainers)))
	mux.Handle("GET /api/v1/containers/{id}", s.requireSession(http.HandlerFunc(s.handleGetContainer)))
	mux.Handle("GET /api/v1/containers/{id}/releases", s.requireSession(http.HandlerFunc(s.handleContainerReleases)))
	mux.Handle("POST /api/v1/containers/{id}/update", s.requireAdmin(http.HandlerFunc(s.handleRequestUpdate)))
	mux.Handle("GET /api/v1/containers/{id}/update/status", s.requireSession(http.HandlerFunc(s.handleUpdateStatus)))
	mux.Handle("POST /api/v1/containers/{id}/pin", s.requireAdmin(http.HandlerFunc(s.handleSetPin)))
	mux.Handle("GET /api/v1/updates", s.requireSession(http.HandlerFunc(s.handleListUpdates)))

	mux.Handle("POST /api/v1/check", s.requireAdmin(http.HandlerFunc(s.handleTriggerCheck)))
	mux.Handle("GET /api/v1/events", s.requireSession(http.HandlerFunc(s.handleListEvents)))

	mux.Handle("GET /api/v1/settings", s.requireAdmin(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/v1/settings", s.requireAdmin(http.HandlerFunc(s.handlePutSettings)))

	mux.Handle("GET /api/v1/auth/settings", s.requireAdmin(http.HandlerFunc(s.handleGetAuthSettings)))
	mux.Handle("PUT /api/v1/auth/settings", s.requireAdmin(http.HandlerFunc(s.handlePutAuthSettings)))

	mux.Handle("GET /api/v1/users", s.requireAdmin(http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /api/v1/users", s.requireAdmin(http.HandlerFunc(s.handleCreateUser)))
	mux.Handle("PATCH /api/v1/users/{id}", s.requireAdmin(http.HandlerFunc(s.handleUpdateUser)))
	mux.Handle("DELETE /api/v1/users/{id}", s.requireAdmin(http.HandlerFunc(s.handleDeleteUser)))

	// --- Static SPA ------------------------------------------------------
	mux.Handle("/", s.spaHandler())

	return securityHeaders(requestLogger(recoverMiddleware(mux)))
}

// spaHandler serves the embedded frontend with client-side routing fallback.
func (s *Server) spaHandler() http.Handler {
	webFS, err := webui.FS()
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// Try to serve the exact file; fall back to index.html for routes.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(webFS, path); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

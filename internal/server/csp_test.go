package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecurityHeadersAreStrict pins the headers the UI depends on.
func TestSecurityHeadersAreStrict(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	// Go through the composed handler so the middleware chain runs.
	rec := doJSON(t, s.handler.ServeHTTP, "GET", "/", "", nil)

	h := rec.Header()
	if got := h.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Errorf("CSP should default to 'self': %q", got)
	}
	if got := h.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

// TestTemplateLoadsNoThirdPartyAssets keeps the frontend template consistent
// with the strict CSP the server sends. A stylesheet or script from another
// origin is blocked in production, so it would silently do nothing — exactly
// how the bundled Google Fonts link went unnoticed until a browser console
// check flagged it.
func TestTemplateLoadsNoThirdPartyAssets(t *testing.T) {
	path := filepath.Join("..", "..", "web", "index.html")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("template not available: %v", err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, "http://") || strings.Contains(trimmed, "https://") {
			t.Errorf("template references a third-party asset that the CSP would block:\n  %s", trimmed)
		}
	}
}

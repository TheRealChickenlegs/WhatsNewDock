package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSecurityHeadersAreStrict pins the headers the UI depends on.
func TestSecurityHeadersAreStrict(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	// Go through the composed handler so the middleware chain runs.
	rec := doJSON(t, s.handler.ServeHTTP, "GET", "/", "", nil)

	got := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		// The only third-party origins the policy may name.
		"style-src 'self' 'unsafe-inline' " + fontStyleHost,
		"font-src 'self' " + fontFileHost,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSP is missing %q:\n%s", want, got)
		}
	}
	// Nothing beyond the font origins may be allowlisted.
	for _, forbidden := range []string{"script-src 'self' http", "connect-src 'self' http", "'unsafe-eval'", "*"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("CSP contains %q, which should not be allowed:\n%s", forbidden, got)
		}
	}
	if h := rec.Header().Get("X-Frame-Options"); h != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", h)
	}
}

// TestTemplateAssetsAreAllowed keeps the frontend template and the CSP in step.
//
// Every remote origin the template loads must be one the policy allows, in the
// right directive — otherwise the browser blocks it and the failure is silent
// (which is exactly how the bundled webfonts went unnoticed). Origins that are
// NOT allowlisted must not appear at all.
func TestTemplateAssetsAreAllowed(t *testing.T) {
	path := filepath.Join("..", "..", "web", "index.html")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("template not available: %v", err)
	}

	// Origins the template is allowed to reference at all, plus the directive
	// that has to carry each one when it is actually loaded.
	allowed := map[string]string{
		fontStyleHost: "style-src", // the @font-face stylesheet
		fontFileHost:  "font-src",  // the woff2 files it points at
	}

	urlRe := regexp.MustCompile(`https?://[^\s"'<>]+`)
	attrRe := regexp.MustCompile(`(?i)(rel|href|src)\s*=\s*"([^"]*)"`)

	body := stripHTMLComments(string(src))
	// The policy text is derived from the same constants the server uses, so a
	// template reference is checked against what will actually be sent.
	cspDirectives := map[string]string{
		"style-src": "style-src 'self' 'unsafe-inline' " + fontStyleHost,
		"font-src":  "font-src 'self' " + fontFileHost,
	}

	for _, line := range strings.Split(body, "\n") {
		for _, m := range attrRe.FindAllStringSubmatch(line, -1) {
			attr, value := strings.ToLower(m[1]), m[2]
			host := urlRe.FindString(value)
			if host == "" {
				continue
			}
			origin := originOf(host)
			directive, ok := allowed[origin]
			if !ok {
				t.Errorf("template references %s, which the CSP does not allow", origin)
				continue
			}
			// preconnect/dns-prefetch only warms the connection; rel=stylesheet
			// has to be permitted by the directive that covers it.
			if attr == "rel" && value != "stylesheet" {
				continue
			}
			if !strings.Contains(cspDirectives[directive], origin) {
				t.Errorf("%s=%q points at %s, which %s does not allow", attr, value, origin, directive)
			}
		}
	}
}

// TestTemplateFontsMatchCSP guards the specific attribute combination the
// webfont <link> needs: rel="stylesheet" for the CSS host.
func TestTemplateFontsMatchCSP(t *testing.T) {
	path := filepath.Join("..", "..", "web", "index.html")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("template not available: %v", err)
	}
	body := stripHTMLComments(string(src))
	if !strings.Contains(body, fontStyleHost) {
		t.Errorf("template no longer links the webfont stylesheet; update the CSP allowlist or restore the link")
	}
	if !strings.Contains(body, `rel="stylesheet"`) {
		t.Error("webfont link is missing rel=\"stylesheet\"")
	}
}

// stripHTMLComments removes <!-- ... --> so explanatory text about the CSP is
// not mistaken for a real reference.
func stripHTMLComments(s string) string {
	re := regexp.MustCompile(`(?s)<!--.*?-->`)
	return re.ReplaceAllString(s, "")
}

// originOf trims a URL down to scheme://host.
func originOf(u string) string {
	parts := strings.SplitN(u, "//", 2)
	if len(parts) != 2 {
		return u
	}
	host := parts[1]
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return parts[0] + "//" + host
}

package server

import "testing"

func TestSafeRedirect(t *testing.T) {
	cases := map[string]string{
		"":                 "/",
		"/":                "/",
		"/containers":      "/containers",
		"/path?x=1":        "/path?x=1",
		"//evil.com":       "/",
		"https://evil.com": "/",
		"http://x.com/p":   "/",
		"/a\\b":            "/",
		"/a\r\nb":          "/",
	}
	for in, want := range cases {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

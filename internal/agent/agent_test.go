package agent

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

// TestServerErrorKeepsTheServersMessage: when the server explains itself, keep
// that; when the body is a proxy's error page, drop it.
func TestServerErrorKeepsTheServersMessage(t *testing.T) {
	got := serverError(resp(409, `{"error":"no newer version is known"}`))
	if got.Error() != "server returned 409: no newer version is known" {
		t.Errorf("got %q", got.Error())
	}

	// The 502 that prompted this: an HTML page from the reverse proxy.
	html := "<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center></body></html>"
	got = serverError(resp(502, html))
	if strings.Contains(got.Error(), "<html") || strings.Contains(got.Error(), "center") {
		t.Errorf("proxy HTML leaked into the log: %q", got.Error())
	}
	if got.Error() != "server returned 502" {
		t.Errorf("got %q", got.Error())
	}
}

// TestTransientSeparatesRestartsFromFaults is what decides whether a failure is
// a warning or an error line.
func TestTransientSeparatesRestartsFromFaults(t *testing.T) {
	// A restart, a deploy, a proxy with no upstream: retry quietly.
	for _, status := range []int{502, 503, 504} {
		if !transient(serverError(resp(status, ""))) {
			t.Errorf("%d should be transient", status)
		}
	}
	// These are real problems somebody has to fix.
	for _, status := range []int{400, 401, 403, 404, 500} {
		if transient(serverError(resp(status, ""))) {
			t.Errorf("%d should not be transient", status)
		}
	}
	// A refused connection is the server not being up yet.
	if !transient(&netOpError{}) {
		t.Error("a connection error should be transient")
	}
	if transient(errors.New("something else")) {
		t.Error("an unclassified error should not be transient")
	}
}

// netOpError stands in for a dial failure.
type netOpError struct{}

func (e *netOpError) Error() string   { return "dial tcp: connect: connection refused" }
func (e *netOpError) Timeout() bool   { return false }
func (e *netOpError) Temporary() bool { return true }

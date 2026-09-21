package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
)

// TestApplyWithoutUpdateIsRefused is the guard that keeps a stale "update
// available" from redeploying the app by accident.
func TestApplyWithoutUpdateIsRefused(t *testing.T) {
	t.Run("no docker connection", func(t *testing.T) {
		s := newAgentOnlyTestServer(t)
		rec := httptest.NewRecorder()
		s.handleApplySelfUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/selfupdate/apply", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Docker connection") {
			t.Errorf("unhelpful refusal: %s", rec.Body.String())
		}
	})

	t.Run("nothing newer known", func(t *testing.T) {
		s := newAgentOnlyTestServer(t)
		s.docker = testDockerClient(t)
		rec := httptest.NewRecorder()
		s.handleApplySelfUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/selfupdate/apply", nil))
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no newer version") {
			t.Errorf("unhelpful refusal: %s", rec.Body.String())
		}
	})
}

// TestApplyAllowsABuildSignal: a rebuilt tag has no version pair, so the guard
// must not require one — it only needs a signal.
func TestApplyAllowsABuildSignal(t *testing.T) {
	s := newAgentOnlyTestServer(t)
	s.docker = testDockerClient(t)
	// Drive the checker to a build result through its own seam.
	done := make(chan struct{})
	s.selfUpdate = selfupdate.New(
		func(context.Context) selfupdate.Current {
			return selfupdate.Current{Version: "dev", Image: "ghcr.io/x/wnd:latest", Digest: "sha256:old"}
		},
		nil,
		func(context.Context, string, string) (bool, string, error) { return true, "sha256:new", nil })
	close(done)
	s.selfUpdate.Check(context.Background())

	res := s.selfUpdate.Cached()
	if res.Kind != selfupdate.KindBuild || !res.UpdateAvailable {
		t.Fatalf("setup failed: %+v", res)
	}

	rec := httptest.NewRecorder()
	s.handleApplySelfUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/selfupdate/apply", nil))
	// It gets as far as trying to reach Docker, which is enough: the guard let a
	// build signal through rather than refusing it for having no version pair.
	if rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "no newer version") {
		t.Fatalf("a build signal was refused: %s", rec.Body.String())
	}
}

// TestSelfUpdateReportsDisabledState checks the endpoint the sidebar card reads
// even when the check has never run.
func TestSelfUpdateReportsDisabledState(t *testing.T) {
	s := newAgentOnlyTestServer(t)
	s.cfg.SelfUpdate.Enabled = false

	rec := httptest.NewRecorder()
	s.handleSelfUpdate(rec, httptest.NewRequest(http.MethodGet, "/api/v1/selfupdate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"enabled":false`) {
		t.Errorf("enabled state missing: %s", body)
	}
	if !strings.Contains(body, `"update_available":false`) {
		t.Errorf("a fresh instance must not claim an update: %s", body)
	}
}

// testDockerClient returns a client that is never connected to anything: enough
// for handlers that only need the capability to be present.
func testDockerClient(t *testing.T) *dockerx.Client {
	t.Helper()
	c, err := dockerx.New(config.DockerConfig{Host: "unix:///nonexistent/whatsnewdock-selfupdate-test.sock"})
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

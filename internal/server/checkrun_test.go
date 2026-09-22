package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestManualCheckIgnoresDeadRequestContext is the regression test for the
// reported "clicking Check now shows nothing" bug: net/http cancels the request
// context as soon as the handler returns, so the background check was handed a
// context that was already dead and aborted before it could look anything up.
func TestManualCheckIgnoresDeadRequestContext(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // what the request context looks like once the handler has returned

	r := httptest.NewRequest(http.MethodPost, "/api/v1/check", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.handleTriggerCheck(rec, r)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	finished := time.Now().Add(5 * time.Second)
	for {
		running, _, _, done, errMsg := s.checks.status()
		if !running && !done.IsZero() {
			if errMsg != "" {
				t.Fatalf("check failed on a dead request context: %s", errMsg)
			}
			return
		}
		if time.Now().After(finished) {
			t.Fatal("check never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCheckRunTrackerCoalesces(t *testing.T) {
	var tr checkRunTracker
	if !tr.begin("manual") {
		t.Fatal("first begin should start a run")
	}
	if tr.begin("scheduled") {
		t.Fatal("second begin should report a run already in flight")
	}
	running, trigger, startedAt, _, _ := tr.status()
	if !running || trigger != "manual" || startedAt.IsZero() {
		t.Fatalf("status = %v %q %v", running, trigger, startedAt)
	}

	tr.end(nil)
	running, _, _, finishedAt, errMsg := tr.status()
	if running || finishedAt.IsZero() || errMsg != "" {
		t.Fatalf("after success: running=%v finished=%v err=%q", running, finishedAt, errMsg)
	}

	if !tr.begin("manual") {
		t.Fatal("begin after finish should start a run")
	}
	tr.end(errors.New("boom"))
	if _, _, _, _, errMsg := tr.status(); !strings.Contains(errMsg, "boom") {
		t.Fatalf("err = %q, want it to record the failure", errMsg)
	}
}

func TestCheckStatusHandler(t *testing.T) {
	s := newAgentOnlyTestServer(t)
	rec := httptest.NewRecorder()
	s.handleCheckStatus(rec, httptest.NewRequest(http.MethodGet, "/api/v1/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, key := range []string{`"running"`, `"updates_available"`, `"last_error"`} {
		if !strings.Contains(body, key) {
			t.Errorf("response %s missing %s", body, key)
		}
	}
}

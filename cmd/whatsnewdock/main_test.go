package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestIsCleanShutdown pins the difference between "we were asked to stop" and
// "we fell over". A container being replaced — which is what a self-update does
// to every agent — sends SIGTERM, and that must not read as a crash.
func TestIsCleanShutdown(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if !isCleanShutdown(err) {
			t.Errorf("%v should count as a clean shutdown", err)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), fmt.Errorf("wrapped: %w", context.Canceled)} {
		if err == nil {
			continue
		}
		want := errors.Is(err, context.Canceled)
		if got := isCleanShutdown(err); got != want {
			t.Errorf("isCleanShutdown(%v) = %v, want %v", err, got, want)
		}
	}
}

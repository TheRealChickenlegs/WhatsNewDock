package dockerx

import "testing"

const oldID = "old111"

func TestClassifyRespawn(t *testing.T) {
	cases := []struct {
		name  string
		found *containerRef
		want  respawnState
	}{
		{"missing", nil, respawnGone},
		{"same id running", &containerRef{ID: oldID, State: "running"}, respawnOldStillThere},
		{"same id exited", &containerRef{ID: oldID, State: "exited"}, respawnOldStillThere},
		{"new id running", &containerRef{ID: "new222", State: "running"}, respawnRunning},
		{"new id created", &containerRef{ID: "new222", State: "created"}, respawnNotRunning},
		{"new id restarting", &containerRef{ID: "new222", State: "restarting"}, respawnNotRunning},
		{"new id exited", &containerRef{ID: "new222", State: "exited"}, respawnNotRunning},
	}
	for _, c := range cases {
		if got := classifyRespawn(c.found, oldID); got != c.want {
			t.Errorf("%s: classifyRespawn = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDecideRespawnOutcome(t *testing.T) {
	// A running container under a new id succeeds immediately.
	if got := decideRespawnOutcome(&containerRef{ID: "new222", State: "running"}, oldID, false); !got.Done || !got.OK || got.ID != "new222" {
		t.Errorf("running new container = %+v", got)
	}
	// Before the deadline everything else keeps polling.
	for _, found := range []*containerRef{
		{ID: "new222", State: "created"},
		{ID: oldID, State: "running"},
		nil,
	} {
		if got := decideRespawnOutcome(found, oldID, false); got.Done {
			t.Errorf("should keep polling: %+v (%+v)", found, got)
		}
	}
	// At the deadline, only a currently-running new container counts.
	if got := decideRespawnOutcome(&containerRef{ID: "new222", State: "running"}, oldID, true); !got.Done || !got.OK {
		t.Errorf("running new container at deadline = %+v", got)
	}
	for _, found := range []*containerRef{
		{ID: "new222", State: "restarting"},
		{ID: "new222", State: "exited"},
		{ID: oldID, State: "running"},
		nil,
	} {
		got := decideRespawnOutcome(found, oldID, true)
		if !got.Done || got.OK {
			t.Errorf("deadline with %+v = %+v, want done-but-failed", found, got)
		}
	}
}

// TestExactNameMatch guards against the daemon's regex-contains name filter: a
// lookup for "web" must not be satisfied by "web-prev-1234" (the rollback name
// this package creates) or by "my-web".
func TestExactNameMatch(t *testing.T) {
	cases := []struct {
		names []string
		name  string
		want  bool
	}{
		{[]string{"/immich-server"}, "immich-server", true},
		{[]string{"immich-server"}, "immich-server", true},
		{[]string{"/other", "/immich-server"}, "immich-server", true},
		{[]string{"/immich-server-old"}, "immich-server", false},
		{[]string{"/my-immich-server"}, "immich-server", false},
		{[]string{"/immich-server-prev-1700000000"}, "immich-server", false},
		{nil, "immich-server", false},
		{[]string{}, "immich-server", false},
	}
	for _, c := range cases {
		if got := exactNameMatch(c.names, c.name); got != c.want {
			t.Errorf("exactNameMatch(%v, %q) = %v, want %v", c.names, c.name, got, c.want)
		}
	}
}

func TestIsGone(t *testing.T) {
	if isGone(nil) {
		t.Error("nil is not a gone-error")
	}
	for _, msg := range []string{
		"Error response from daemon: No such container: abc",
		"no container with name or id \"web\" found",
		"container not found",
	} {
		if !isGone(errString(msg)) {
			t.Errorf("%q should be treated as gone", msg)
		}
	}
	if isGone(errString("permission denied")) {
		t.Error("an unrelated error must not be swallowed")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

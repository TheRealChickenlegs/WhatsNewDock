package dockerx

import "strings"

// This file holds the pure decision logic for waiting on a systemd-managed
// container to come back after an update. Keeping it free of Docker API calls
// means the interesting cases — a crash-looping new image, a container that
// never returns, the old container still sitting under the same name — are all
// unit-testable without a daemon.

// containerRef is the minimal view of a container used while polling.
type containerRef struct {
	ID    string
	State string
}

// respawnState classifies what a lookup found. oldID is the container that was
// stopped.
type respawnState int

const (
	// respawnGone: nothing is using the name yet.
	respawnGone respawnState = iota
	// respawnOldStillThere: the container we stopped is still the one under
	// this name, so systemd has not acted yet.
	respawnOldStillThere
	// respawnNotRunning: a new container exists but is not running
	// (created/exited/restarting) — either still settling or crash-looping.
	respawnNotRunning
	// respawnRunning: a new container is up.
	respawnRunning
)

func classifyRespawn(found *containerRef, oldID string) respawnState {
	if found == nil {
		return respawnGone
	}
	if found.ID == oldID {
		return respawnOldStillThere
	}
	if found.State == "running" {
		return respawnRunning
	}
	return respawnNotRunning
}

// respawnOutcome is the decision after one poll: keep waiting, succeed, or give
// up as a failure.
type respawnOutcome struct {
	Done bool
	OK   bool
	ID   string
}

// decideRespawnOutcome decides whether to stop polling.
//
// A running container under a new id is an immediate success. Everything else
// keeps polling until the deadline, and once the deadline is reached only a
// *currently running* new container counts as success — a container that merely
// reappeared but never reached running (a crash-loop on a broken new image) is a
// failure, never a false success.
func decideRespawnOutcome(found *containerRef, oldID string, deadlineReached bool) respawnOutcome {
	if classifyRespawn(found, oldID) == respawnRunning {
		return respawnOutcome{Done: true, OK: true, ID: found.ID}
	}
	if !deadlineReached {
		return respawnOutcome{}
	}
	return respawnOutcome{Done: true}
}

// exactNameMatch reports whether names contains name exactly. Container list
// name filters are regex-contains on both Docker and Podman, so a lookup for
// "web" also returns "web-prev-1234" (the rollback name this package creates)
// and "my-web". Every name lookup must therefore be confirmed exactly.
func exactNameMatch(names []string, name string) bool {
	for _, n := range names {
		if strings.TrimPrefix(n, "/") == name {
			return true
		}
	}
	return false
}

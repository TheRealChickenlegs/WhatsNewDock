// Package selfupdate checks whether a newer WhatsNewDock release has been
// published, so the app can tell its operator that an upgrade exists.
//
// The check is deliberately tiny and dependency-free: it compares the version
// this binary was built with against the newest published release tag, and it
// never touches the running deployment. Applying an update is a separate,
// explicit action.
package selfupdate

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is the newest published release for the repository.
type Release struct {
	Tag         string
	URL         string
	Name        string
	PublishedAt time.Time
	Prerelease  bool
}

// Result describes the outcome of the most recent check.
type Result struct {
	Current         string    `json:"current"`
	Latest          string    `json:"latest"`
	UpdateAvailable bool      `json:"update_available"`
	URL             string    `json:"url,omitempty"`
	Name            string    `json:"name,omitempty"`
	PublishedAt     time.Time `json:"published_at,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	// Error reports why the last check failed. A failed check is never treated
	// as "up to date" — the UI says the check failed instead.
	Error string `json:"error,omitempty"`
}

// Fetcher retrieves the newest published release. It is injected so the check
// can be exercised without network access.
type Fetcher func(ctx context.Context) (*Release, error)

// Checker runs the update check and remembers the last answer.
type Checker struct {
	current string
	fetch   Fetcher

	mu     sync.Mutex
	result Result
}

// New builds a checker for a running version.
func New(current string, fetch Fetcher) *Checker {
	c := &Checker{current: strings.TrimSpace(current), fetch: fetch}
	c.result = Result{Current: c.current}
	return c
}

// Check fetches the newest release and records the outcome. It is safe to call
// concurrently: the last result is always available via Cached.
func (c *Checker) Check(ctx context.Context) Result {
	res := Result{Current: c.current, CheckedAt: time.Now().UTC()}

	rel, err := c.fetch(ctx)
	switch {
	case err != nil:
		res.Error = err.Error()
	case rel == nil || rel.Tag == "":
		res.Error = "no published release found"
	default:
		res.Latest = rel.Tag
		res.URL = rel.URL
		res.Name = rel.Name
		res.PublishedAt = rel.PublishedAt
		// A build that is not a release (a branch build or "dev") cannot be
		// ordered against a tag, so it is reported as up to date rather than
		// nagging with a comparison that means nothing.
		res.UpdateAvailable = IsNewer(rel.Tag, c.current)
	}

	c.mu.Lock()
	c.result = res
	c.mu.Unlock()
	return res
}

// Cached returns the most recent result without touching the network.
func (c *Checker) Cached() Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

// ---------------------------------------------------------------- versions

// IsNewer reports whether candidate is a strictly newer release than current.
// Versions that cannot be ordered (a branch name, "dev", a bare commit) never
// count as newer.
func IsNewer(candidate, current string) bool {
	return CompareVersions(candidate, current) > 0
}

// CompareVersions orders two version strings, returning >0 when a is newer than
// b, 0 when they are equal or cannot be compared, and <0 when a is older.
//
// It accepts the shapes this project actually produces: "v0.1.1", "0.1.1",
// "v0.1.1-rc1" and the git-describe form "v0.1.1-3-gabc1234". Anything without
// a leading numeric component — "dev", "main", a commit sha — is unordered and
// compares equal to everything.
func CompareVersions(a, b string) int {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0
	}
	for i := 0; i < 3; i++ {
		if av.nums[i] != bv.nums[i] {
			if av.nums[i] > bv.nums[i] {
				return 1
			}
			return -1
		}
	}
	// Same release: a prerelease sorts before the release itself.
	switch {
	case av.pre == bv.pre:
		return 0
	case av.pre == "":
		return 1
	case bv.pre == "":
		return -1
	case av.pre > bv.pre:
		return 1
	default:
		return -1
	}
}

type version struct {
	nums [3]int
	pre  string
}

// parseVersion splits a version into up to three numeric components plus a
// prerelease suffix. It reports false for anything that is not version-shaped.
func parseVersion(s string) (version, bool) {
	var v version
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return v, false
	}
	// Build metadata is irrelevant to ordering.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		parts = parts[:3]
	}
	for i, p := range parts {
		// A non-numeric component means this is not a version at all.
		if p == "" {
			return version{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return version{}, false
		}
		v.nums[i] = n
	}
	return v, true
}

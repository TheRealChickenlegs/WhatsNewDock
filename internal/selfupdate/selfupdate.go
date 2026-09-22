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
	"regexp"
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

// Kind is which signal found an update, which decides what the UI says and
// what gets deployed.
type Kind string

const (
	// KindNone means nothing newer was found.
	KindNone Kind = ""
	// KindRelease means a newer published release exists.
	KindRelease Kind = "release"
	// KindBuild means the image tag being run has been rebuilt: same tag, new
	// digest. This is what a moving tag like ":latest" looks like.
	KindBuild Kind = "build"
)

// Result describes the outcome of the most recent check.
type Result struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	Kind            Kind   `json:"kind,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	// Target is the image reference the update would deploy. For a release it is
	// the running reference retagged; for a build it is the running reference
	// itself, re-pulled.
	Target string `json:"target,omitempty"`
	// Image is the reference this container is running.
	Image string `json:"image,omitempty"`
	// Digests are recorded for the build signal, so the UI can explain why.
	CurrentDigest string    `json:"current_digest,omitempty"`
	LatestDigest  string    `json:"latest_digest,omitempty"`
	URL           string    `json:"url,omitempty"`
	Name          string    `json:"name,omitempty"`
	PublishedAt   time.Time `json:"published_at,omitempty"`
	CheckedAt     time.Time `json:"checked_at,omitempty"`
	// Error reports why the last check failed. A failed check is never treated
	// as "up to date" — the UI says the check failed instead.
	Error string `json:"error,omitempty"`
}

// Current describes the build this process is running.
type Current struct {
	Version string
	// Image is the reference the container was created from, e.g.
	// "ghcr.io/owner/whatsnewdock:latest". Empty when it cannot be determined.
	Image string
	// Digest is what the running image resolved to. Empty for a locally built
	// image, which is how we know not to offer a registry-backed update.
	Digest string
}

// Fetcher retrieves the newest published release. It is injected so the check
// can be exercised without network access.
type Fetcher func(ctx context.Context) (*Release, error)

// DigestFetcher reports whether the registry's current manifest for an image
// reference differs from the digest the container is running. It is what makes
// a moving tag like ":latest" checkable.
type DigestFetcher func(ctx context.Context, imageRef, currentDigest string) (moved bool, latestDigest string, err error)

// Checker runs the update check and remembers the last answer.
type Checker struct {
	// currentFn describes the running build; it is re-read on every check
	// because the container can be redeployed underneath us.
	currentFn func(ctx context.Context) Current
	fetch     Fetcher
	digest    DigestFetcher

	mu     sync.Mutex
	result Result
}

// New builds a checker. fetch and digest may be nil, in which case that signal
// is simply never fired.
func New(currentFn func(ctx context.Context) Current, fetch Fetcher, digest DigestFetcher) *Checker {
	c := &Checker{currentFn: currentFn, fetch: fetch, digest: digest}
	if cur := currentFn(context.Background()); true {
		c.result = Result{Current: cur.Version}
	}
	return c
}

// Check fetches the newest release and records the outcome. It is safe to call
// concurrently: the last result is always available via Cached.
func (c *Checker) Check(ctx context.Context) Result {
	cur := c.currentFn(ctx)
	res := Result{Current: cur.Version, Image: cur.Image, CurrentDigest: cur.Digest,
		CheckedAt: time.Now().UTC()}

	// Signal 1: a newer published release. A build that is not a release (a
	// branch build or "dev") cannot be ordered against a tag, so it simply
	// never fires here rather than nagging with a comparison that means
	// nothing.
	if c.fetch != nil {
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
			if IsNewer(rel.Tag, cur.Version) {
				res.Kind = KindRelease
				// Knowing a release exists is useful even when we cannot deploy
				// it ourselves: the UI then shows the command to run by hand.
				if cur.Image != "" {
					res.Target = Retag(cur.Image, rel.Tag)
				}
			}
		}
	}

	// Signal 2: the tag being run has been rebuilt. Only meaningful for an image
	// that came from a registry — a locally built one has no digest to compare.
	if res.Kind == KindNone && c.digest != nil && cur.Image != "" && cur.Digest != "" {
		if moved, latest, err := c.digest(ctx, cur.Image, cur.Digest); err == nil && moved {
			res.Kind = KindBuild
			res.Target = cur.Image
			res.LatestDigest = latest
			// A build is the same tag; there is no version pair to show.
			res.Latest = ""
		}
	}

	res.UpdateAvailable = res.Kind != KindNone
	if !res.UpdateAvailable {
		res.Target = ""
	}

	c.mu.Lock()
	c.result = res
	c.mu.Unlock()
	return res
}

// Retag replaces the tag or digest of an image reference, keeping the registry
// and repository. It returns "" when either part is missing.
func Retag(image, tag string) string {
	if image == "" || tag == "" {
		return ""
	}
	base := image
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	// A colon after the last slash is a tag; one before it is a registry port.
	if i := strings.LastIndexByte(base, ':'); i > strings.LastIndexByte(base, '/') {
		base = base[:i]
	}
	return base + ":" + tag
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

// describeRe matches the suffix git describe appends to a tag: the number of
// commits since it, then the abbreviated commit.
var describeRe = regexp.MustCompile(`^\d+-g[0-9a-fA-F]+$`)

// CompareVersions orders two version strings, returning >0 when a is newer than
// b, 0 when they are equal or cannot be compared, and <0 when a is older.
//
// It accepts the shapes this project actually produces: "v0.1.1", "0.1.1",
// "v0.1.2-rc1" and the git-describe form "v0.1.1-3-gabc1234". Anything without
// a leading numeric component — "dev", "main", a commit sha — is unordered and
// compares equal to everything.
//
// A git-describe suffix is not a prerelease: "v0.1.1-3-gabc1234" is v0.1.1 plus
// three commits, so it *contains* that release and must not sort below it.
// Treating it as a prerelease would offer the operator a downgrade to the very
// tag their build is already past.
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
	// Fold the git-describe and dirty markers away, so only a real prerelease
	// is left to affect ordering.
	v.pre = strings.TrimSuffix(v.pre, "-dirty")
	if v.pre == "dirty" || describeRe.MatchString(v.pre) {
		v.pre = ""
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

// TagOf returns the tag of an image reference, or "" when it has none.
func TagOf(image string) string {
	base := image
	if i := strings.IndexByte(base, '@'); i >= 0 {
		base = base[:i]
	}
	slash := strings.LastIndexByte(base, '/')
	colon := strings.LastIndexByte(base, ':')
	if colon <= slash {
		return ""
	}
	return base[colon+1:]
}

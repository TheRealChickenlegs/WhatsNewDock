package selfupdate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.2", "v0.1.1", 1},
		{"v0.1.1", "v0.1.2", -1},
		{"v0.1.1", "v0.1.1", 0},
		{"0.1.1", "v0.1.1", 0},
		{"V0.2.0", "v0.1.9", 1},
		{"v0.2", "v0.2.0", 0},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.10.0", "v0.9.0", 1},
		// A prerelease sorts before its release.
		{"v0.2.0-rc1", "v0.2.0", -1},
		{"v0.2.0", "v0.2.0-rc1", 1},
		// A git-describe build is its tag plus commits, so it holds that
		// release. Sorting it below would offer a downgrade to the tag the
		// build is already past.
		{"v0.1.1-3-gabc1234", "v0.1.1", 0},
		{"v0.1.1", "v0.1.1-3-gabc1234", 0},
		{"v0.1.1-3-gabc1234-dirty", "v0.1.1", 0},
		{"v0.1.1-dirty", "v0.1.1", 0},
		// But a real prerelease still sorts below its release.
		{"v0.1.2-rc1", "v0.1.2", -1},
		// And a describe build of an older tag is still older than a newer one.
		{"v0.1.1-9-gabc1234", "v0.1.2", -1},
		// Build metadata does not affect ordering.
		{"v0.1.1+build7", "v0.1.1", 0},
		// Unorderable shapes compare equal, so they never claim an update.
		{"main", "v0.1.1", 0},
		{"dev", "v0.1.1", 0},
		{"85b994c", "v0.1.1", 0},
		{"", "v0.1.1", 0},
		{"v", "v0.1.1", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !IsNewer("v0.1.2", "v0.1.1") {
		t.Error("a higher tag should be newer")
	}
	if IsNewer("v0.1.1", "v0.1.1") {
		t.Error("the same version is not newer")
	}
	// This is the case that matters for a dev build or a bare commit: it must
	// not nag about an update it cannot reason about. (A main build now carries
	// a git-describe version, which *is* orderable — see the describe cases.)
	for _, current := range []string{"dev", "main", "85b994c"} {
		if IsNewer("v0.1.2", current) {
			t.Errorf("%q cannot be compared against a tag and must not report an update", current)
		}
	}
}

const testImage = "ghcr.io/x/whatsnewdock:latest"

func fixedCurrent(v string) func(context.Context) Current {
	return func(context.Context) Current { return Current{Version: v} }
}

func TestCheckerRecordsResult(t *testing.T) {
	rel := &Release{Tag: "v0.1.2", URL: "https://example.com/rel", Name: "0.1.2",
		PublishedAt: time.Unix(1700000000, 0).UTC()}
	c := New(fixedCurrent("v0.1.1"), func(context.Context) (*Release, error) { return rel, nil }, nil)

	got := c.Check(context.Background())
	if !got.UpdateAvailable || got.Latest != "v0.1.2" || got.Current != "v0.1.1" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.URL != rel.URL || got.CheckedAt.IsZero() || got.Error != "" {
		t.Errorf("result missing details: %+v", got)
	}
	if got.Kind != KindRelease {
		t.Errorf("kind = %q, want release", got.Kind)
	}
	// The answer is available without another fetch.
	if cached := c.Cached(); cached.Latest != "v0.1.2" || !cached.UpdateAvailable {
		t.Errorf("cached = %+v", cached)
	}
}

// TestCheckerBuildSignal covers a moving tag: the version is not a release, so
// the release signal cannot fire, and the registry digest is what notices.
func TestCheckerBuildSignal(t *testing.T) {
	c := New(
		func(context.Context) Current {
			return Current{Version: "v0.1.1-3-gabc1234", Image: testImage, Digest: "sha256:old"}
		},
		func(context.Context) (*Release, error) { return &Release{Tag: "v0.1.1"}, nil },
		func(_ context.Context, ref, current string) (bool, string, error) {
			if ref != testImage {
				t.Errorf("digest lookup used %q", ref)
			}
			return true, "sha256:new", nil
		})

	got := c.Check(context.Background())
	if got.Kind != KindBuild || !got.UpdateAvailable {
		t.Fatalf("result = %+v", got)
	}
	// A moving tag redeploys itself: same reference, re-pulled. Never a release
	// tag, which for a main build could be older than what is running.
	if got.Target != testImage {
		t.Errorf("target = %q, want the same reference %q", got.Target, testImage)
	}
	if got.Latest != "" {
		t.Errorf("a build has no version pair to show, got latest %q", got.Latest)
	}
	if got.LatestDigest != "sha256:new" {
		t.Errorf("latest digest = %q", got.LatestDigest)
	}
}

// TestCheckerReleaseWinsOverBuild pins the precedence: when a real release is
// available it is the better thing to deploy, even if the tag also moved.
func TestCheckerReleaseWinsOverBuild(t *testing.T) {
	c := New(
		func(context.Context) Current {
			return Current{Version: "v0.1.1", Image: "ghcr.io/x/whatsnewdock:0.1.1", Digest: "sha256:old"}
		},
		func(context.Context) (*Release, error) { return &Release{Tag: "v0.1.2"}, nil },
		func(context.Context, string, string) (bool, string, error) { return true, "sha256:new", nil })

	got := c.Check(context.Background())
	if got.Kind != KindRelease {
		t.Fatalf("kind = %q, want release", got.Kind)
	}
	if got.Target != "ghcr.io/x/whatsnewdock:v0.1.2" {
		t.Errorf("target = %q", got.Target)
	}
}

// TestCheckerBuildSignalNeedsADigest keeps a locally built image from being
// nagged about: nothing to compare against means nothing to report.
func TestCheckerBuildSignalNeedsADigest(t *testing.T) {
	calls := 0
	c := New(
		func(context.Context) Current { return Current{Version: "dev", Image: "whatsnewdock:dev"} },
		nil,
		func(context.Context, string, string) (bool, string, error) { calls++; return true, "x", nil })

	if got := c.Check(context.Background()); got.UpdateAvailable {
		t.Errorf("a digest-less image must not report an update: %+v", got)
	}
	if calls != 0 {
		t.Errorf("the registry was queried for a local image (%d times)", calls)
	}
}

func TestRetag(t *testing.T) {
	cases := []struct{ image, tag, want string }{
		{"ghcr.io/x/whatsnewdock:0.1.1", "v0.1.2", "ghcr.io/x/whatsnewdock:v0.1.2"},
		{"ghcr.io/x/whatsnewdock", "v0.1.2", "ghcr.io/x/whatsnewdock:v0.1.2"},
		{"localhost:5000/wnd:0.1.1", "v0.1.2", "localhost:5000/wnd:v0.1.2"},
		{"wnd@sha256:abc", "v0.1.2", "wnd:v0.1.2"},
		{"", "v0.1.2", ""},
		{"wnd:1", "", ""},
	}
	for _, c := range cases {
		if got := Retag(c.image, c.tag); got != c.want {
			t.Errorf("Retag(%q, %q) = %q, want %q", c.image, c.tag, got, c.want)
		}
	}
}

func TestCheckerUpToDate(t *testing.T) {
	c := New(fixedCurrent("v0.1.1"), func(context.Context) (*Release, error) {
		return &Release{Tag: "v0.1.1"}, nil
	}, nil)
	if got := c.Check(context.Background()); got.UpdateAvailable {
		t.Errorf("same version reported as an update: %+v", got)
	}
}

// TestCheckerFailureIsNotUpToDate is the honest-failure rule: a check that could
// not run must never look like "you are current".
func TestCheckerFailureIsNotUpToDate(t *testing.T) {
	c := New(fixedCurrent("v0.1.1"), func(context.Context) (*Release, error) {
		return nil, errors.New("github unreachable")
	}, nil)
	got := c.Check(context.Background())
	if got.UpdateAvailable {
		t.Error("a failed check must not report an update")
	}
	if got.Error == "" {
		t.Error("a failed check must say so rather than looking up to date")
	}
	if got.CheckedAt.IsZero() {
		t.Error("a failed check should still record when it ran")
	}
}

func TestCheckerNoRelease(t *testing.T) {
	c := New(fixedCurrent("v0.1.1"), func(context.Context) (*Release, error) { return nil, nil }, nil)
	got := c.Check(context.Background())
	if got.Error == "" || got.Latest != "" {
		t.Errorf("result = %+v", got)
	}
}

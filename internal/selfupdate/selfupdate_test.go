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
		// git describe: N commits after a tag is still that tag, un-released.
		{"v0.1.1-3-gabc1234", "v0.1.1", -1},
		{"v0.1.1", "v0.1.1-3-gabc1234", 1},
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
	// This is the case that matters for a :latest or dev build: it must not
	// nag about an update it cannot reason about.
	for _, current := range []string{"dev", "main", "85b994c"} {
		if IsNewer("v0.1.2", current) {
			t.Errorf("%q cannot be compared against a tag and must not report an update", current)
		}
	}
}

func TestCheckerRecordsResult(t *testing.T) {
	rel := &Release{Tag: "v0.1.2", URL: "https://example.com/rel", Name: "0.1.2",
		PublishedAt: time.Unix(1700000000, 0).UTC()}
	c := New("v0.1.1", func(context.Context) (*Release, error) { return rel, nil })

	got := c.Check(context.Background())
	if !got.UpdateAvailable || got.Latest != "v0.1.2" || got.Current != "v0.1.1" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got.URL != rel.URL || got.CheckedAt.IsZero() || got.Error != "" {
		t.Errorf("result missing details: %+v", got)
	}
	// The answer is available without another fetch.
	if cached := c.Cached(); cached.Latest != "v0.1.2" || !cached.UpdateAvailable {
		t.Errorf("cached = %+v", cached)
	}
}

func TestCheckerUpToDate(t *testing.T) {
	c := New("v0.1.1", func(context.Context) (*Release, error) {
		return &Release{Tag: "v0.1.1"}, nil
	})
	if got := c.Check(context.Background()); got.UpdateAvailable {
		t.Errorf("same version reported as an update: %+v", got)
	}
}

// TestCheckerFailureIsNotUpToDate is the honest-failure rule: a check that could
// not run must never look like "you are current".
func TestCheckerFailureIsNotUpToDate(t *testing.T) {
	c := New("v0.1.1", func(context.Context) (*Release, error) {
		return nil, errors.New("github unreachable")
	})
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
	c := New("v0.1.1", func(context.Context) (*Release, error) { return nil, nil })
	got := c.Check(context.Background())
	if got.Error == "" || got.Latest != "" {
		t.Errorf("result = %+v", got)
	}
}

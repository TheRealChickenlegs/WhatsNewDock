package updater

import (
	"testing"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/changelog"
	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

func TestParseVer(t *testing.T) {
	cases := map[string]string{
		"1.25.0":         "1.25.0",
		"v1.25.0":        "1.25.0",
		"1.25":           "1.25.0",
		"v1.25":          "1.25.0",
		"1.25.0-alpha.1": "1.25.0-alpha.1",
		"release-2.10.3": "2.10.3",
	}
	for in, want := range cases {
		v, err := parseVer(in)
		if err != nil {
			t.Fatalf("parseVer(%q) error: %v", in, err)
		}
		if v.String() != want {
			t.Errorf("parseVer(%q) = %s, want %s", in, v.String(), want)
		}
	}
}

func rel(tag string, pre bool) changelog.Release {
	return changelog.Release{Tag: tag, PublishedAt: time.Now(), Prerelease: pre}
}

func TestComputeUpdateSemver(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{IncludePreReleases: false}}
	rels := []changelog.Release{
		rel("v1.28.0", false),
		rel("v1.27.0", false),
		rel("v1.26.0", false),
		rel("v1.25.0", false),
	}
	c := store.Container{ImageTag: "v1.25.0"}
	upd, ok := u.computeUpdate(t.Context(), c, nil, rels)
	if !ok || upd == nil {
		t.Fatal("expected an update")
	}
	if upd.LatestTag != "v1.28.0" || upd.VersionsBehind != 3 {
		t.Errorf("got latest=%s behind=%d, want latest=v1.28.0 behind=3", upd.LatestTag, upd.VersionsBehind)
	}
}

func TestComputeUpdateUpToDate(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{IncludePreReleases: false}}
	rels := []changelog.Release{
		rel("v1.28.0", false),
		rel("v1.27.0", false),
	}
	c := store.Container{ImageTag: "v1.28.0"}
	upd, ok := u.computeUpdate(t.Context(), c, nil, rels)
	if ok || upd != nil {
		t.Errorf("expected no update, got %+v", upd)
	}
}

func TestComputeUpdateSkipsPrereleases(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{IncludePreReleases: false}}
	rels := []changelog.Release{
		rel("v2.0.0-rc.1", true),
		rel("v1.9.0", false),
		rel("v1.8.0", false),
	}
	c := store.Container{ImageTag: "v1.8.0"}
	upd, ok := u.computeUpdate(t.Context(), c, nil, rels)
	if !ok || upd == nil {
		t.Fatal("expected an update to v1.9.0")
	}
	if upd.LatestTag != "v1.9.0" || upd.VersionsBehind != 1 {
		t.Errorf("got latest=%s behind=%d, want latest=v1.9.0 behind=1", upd.LatestTag, upd.VersionsBehind)
	}
}

func TestIsMutableTag(t *testing.T) {
	for _, tag := range []string{"latest", "stable", "LTS", "main", "rolling"} {
		if !isMutableTag(tag) {
			t.Errorf("expected %q to be mutable", tag)
		}
	}
	if isMutableTag("1.25.0") {
		t.Error("1.25.0 should not be mutable")
	}
}

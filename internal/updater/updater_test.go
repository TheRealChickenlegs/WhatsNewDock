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

func TestParseVerRejectsNonVersions(t *testing.T) {
	// Tags that merely contain a number are not versions.
	for _, tag := range []string{"server-cuda13", "b10549", "alpine", "bookworm", "slim"} {
		if _, err := parseVer(tag); err == nil {
			t.Errorf("parseVer(%q) should have failed but succeeded", tag)
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

func TestIsFloatingTag(t *testing.T) {
	for _, tag := range []string{"latest", "stable", "LTS", "main", "rolling", "v2", "2", "v2.14", "2.14", "v1", "12"} {
		if !isFloatingTag(tag) {
			t.Errorf("expected %q to be floating", tag)
		}
	}
	for _, tag := range []string{"1.25.0", "v2.14.0", "2.14.1", "v2.14.0-beta.1", "2024.01.01", "release-2.10.3"} {
		if isFloatingTag(tag) {
			t.Errorf("expected %q to NOT be floating", tag)
		}
	}
}

func TestFloatingLine(t *testing.T) {
	maj, min, hasMinor, ok := floatingLine("v2")
	if !ok || maj != 2 || hasMinor {
		t.Errorf("floatingLine(v2) = %d,%d,%v,%v", maj, min, hasMinor, ok)
	}
	maj, min, hasMinor, ok = floatingLine("v2.14")
	if !ok || maj != 2 || min != 14 || !hasMinor {
		t.Errorf("floatingLine(v2.14) = %d,%d,%v,%v", maj, min, hasMinor, ok)
	}
	if _, _, _, ok := floatingLine("latest"); ok {
		t.Error("floatingLine(latest) should report ok=false")
	}
}

func TestComputeUpdateRegistryDescriptiveTag(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{}}
	src := &changelog.Source{Type: changelog.SourceRegistry, Registry: "docker.io", Repository: "yanwk/comfyui-boot"}
	rels := []changelog.Release{
		{Tag: "cu126-slim-20260907"},
		{Tag: "latest"},
		{Tag: "cu130-megapak-pt211"},
	}
	c := store.Container{ImageTag: "cu130-megapak-pt211", Registry: "docker.io", Repository: "yanwk/comfyui-boot"}
	// A descriptive variant tag must not be reported as "N versions behind"
	// based on its position in the (unversioned) tag list. With no digest
	// available the fallback returns "no update".
	upd, ok := u.computeUpdate(t.Context(), c, src, rels)
	if ok || upd != nil {
		t.Errorf("expected no update for descriptive registry tag, got %+v", upd)
	}
}

func TestNewestInLine(t *testing.T) {
	ordered := make([]versioned, 0, 5)
	for _, tag := range []string{"v3.0.0", "v2.14.0", "v2.13.0", "v2.9.1", "v1.8.0"} {
		v, err := parseVer(tag)
		if err != nil {
			t.Fatal(err)
		}
		ordered = append(ordered, versioned{rel: changelog.Release{Tag: tag}, ver: v})
	}
	if got := newestInLine("v2", ordered); got != "v2.14.0" {
		t.Errorf("newestInLine(v2) = %q, want v2.14.0", got)
	}
	if got := newestInLine("v1", ordered); got != "v1.8.0" {
		t.Errorf("newestInLine(v1) = %q, want v1.8.0", got)
	}
	if got := newestInLine("latest", ordered); got != "v3.0.0" {
		t.Errorf("newestInLine(latest) = %q, want v3.0.0", got)
	}
}

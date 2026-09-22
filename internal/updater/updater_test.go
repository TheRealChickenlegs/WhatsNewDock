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

// A tag classifies as a version pin exactly when parseVer accepts it: that is
// the discriminator computeUpdate uses to choose between the release list and
// the registry digest. Getting it wrong in the "no version" direction is what
// made minor-pinned containers silent.
func TestTagClassification(t *testing.T) {
	pins := []string{
		"1.25.0", "v2.14.0", "2.14.1", "v2.14.0-beta.1", "2024.01.01",
		"release-2.10.3", "v2", "2", "v2.14", "2.14", "v1", "12", "1.25-alpine",
	}
	for _, tag := range pins {
		if _, err := parseVer(tag); err != nil {
			t.Errorf("expected %q to be treated as a version pin, got error: %v", tag, err)
		}
	}
	floating := []string{"", "latest", "stable", "LTS", "main", "rolling", "mainline", "alpine", "server-cuda13", "bookworm-slim", "b10549"}
	for _, tag := range floating {
		if _, err := parseVer(tag); err == nil {
			t.Errorf("expected %q to be treated as a floating tag, but it parsed as a version", tag)
		}
	}
}

// TestComputeUpdateMinorPin reproduces the reported bug: a container pinned to
// a partial version ("nginx:1.25") sat silent while 1.31 was available, because
// the partial version was misread as a floating tag and the registry digest for
// the exact tag still matched.
func TestComputeUpdateMinorPin(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{}}
	src := &changelog.Source{Type: changelog.SourceRegistry, Registry: "docker.io", Repository: "library/nginx"}
	// A Docker Hub tag listing is not version-ordered and mixes unversioned
	// aliases in with the releases.
	rels := []changelog.Release{
		rel("mainline", false),
		rel("1.27.4-alpine", false),
		rel("1.31.6", false),
		rel("stable", false),
		rel("1.25.5", false),
		rel("1.30.5", false),
		rel("1.25.5-alpine", false),
	}
	c := store.Container{ImageTag: "1.25", Registry: "docker.io", Repository: "library/nginx"}
	upd, ok := u.computeUpdate(t.Context(), c, src, rels)
	if !ok || upd == nil {
		t.Fatal("expected an update for a minor-pinned tag")
	}
	if upd.LatestTag != "1.31.6" {
		t.Errorf("LatestTag = %q, want 1.31.6", upd.LatestTag)
	}
	// 1.31.6 and 1.30.5 are newer lines; 1.25.5 is a same-line patch the
	// running "1.25" tag already tracks, and the alpine builds are excluded.
	if upd.VersionsBehind != 2 {
		t.Errorf("VersionsBehind = %d, want 2", upd.VersionsBehind)
	}
}

// A variant suffix is part of the pin, so the suggested target keeps it.
func TestComputeUpdateKeepsVariantSuffix(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{}}
	src := &changelog.Source{Type: changelog.SourceRegistry, Registry: "docker.io", Repository: "library/nginx"}
	rels := []changelog.Release{
		rel("1.27.4-alpine", false),
		rel("1.31.6", false),
		rel("1.25.5-alpine", false),
	}
	c := store.Container{ImageTag: "1.25-alpine", Registry: "docker.io", Repository: "library/nginx"}
	upd, ok := u.computeUpdate(t.Context(), c, src, rels)
	if !ok || upd == nil {
		t.Fatal("expected an update for a variant-pinned tag")
	}
	if upd.LatestTag != "1.27.4-alpine" {
		t.Errorf("LatestTag = %q, want 1.27.4-alpine", upd.LatestTag)
	}
	if upd.VersionsBehind != 1 {
		t.Errorf("VersionsBehind = %d, want 1 (the Debian build must not count)", upd.VersionsBehind)
	}
}

// The newest release for the pinned variant makes it up to date without a
// registry round trip.
func TestComputeUpdateMinorPinUpToDate(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{}}
	rels := []changelog.Release{rel("1.31.6", false), rel("1.31.5", false)}
	c := store.Container{ImageTag: "1.31"}
	if upd, ok := u.computeUpdate(t.Context(), c, &changelog.Source{Type: changelog.SourceRegistry, Registry: "docker.io"}, rels); ok {
		t.Errorf("expected no update, got %+v", upd)
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

// A variant tag that merely ends in digits must not be read as a version, or it
// outranks the real releases: "alpine3.24" parses as 3.24.0, which is newer than
// 1.31.6 and used to be offered as the update target.
func TestParseVerRejectsVariantSuffixDigits(t *testing.T) {
	for _, tag := range []string{"alpine3.24", "alpine3.24-slim", "trixie-perl", "cuda13", "php8.2", "pt211"} {
		if _, err := parseVer(tag); err == nil {
			t.Errorf("parseVer(%q) should have failed", tag)
		}
	}
	// A version that follows a separator still counts.
	for tag, want := range map[string]string{
		"release-2.10.3": "2.10.3",
		"node-18.19.0":   "18.19.0",
		"v1.2.3":         "1.2.3",
		"1.2.3-alpine":   "1.2.3-alpine",
	} {
		v, err := parseVer(tag)
		if err != nil {
			t.Errorf("parseVer(%q) error: %v", tag, err)
			continue
		}
		if v.String() != want {
			t.Errorf("parseVer(%q) = %s, want %s", tag, v, want)
		}
	}
}

// The update target must be the newest real release, not a variant tag from the
// registry listing.
func TestComputeUpdateIgnoresVariantTagsInListing(t *testing.T) {
	u := &Updater{cfg: &config.UpdatesConfig{}}
	src := &changelog.Source{Type: changelog.SourceRegistry, Registry: "docker.io", Repository: "library/nginx"}
	rels := []changelog.Release{
		rel("alpine3.24", false),
		rel("alpine3.24-slim", false),
		rel("trixie-perl", false),
		rel("mainline", false),
		rel("1.31.6", false),
		rel("1.30.5", false),
	}
	c := store.Container{ImageTag: "1.25", Registry: "docker.io", Repository: "library/nginx"}
	upd, ok := u.computeUpdate(t.Context(), c, src, rels)
	if !ok || upd == nil {
		t.Fatal("expected an update")
	}
	if upd.LatestTag != "1.31.6" {
		t.Errorf("LatestTag = %q, want 1.31.6", upd.LatestTag)
	}
}

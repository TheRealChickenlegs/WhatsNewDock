// Package updater detects available container image updates and aggregates
// their changelogs by comparing a container's current tag against upstream
// releases (or, for floating tags, against the registry's current digest).
package updater

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/whatsnewdock/whatsnewdock/internal/changelog"
	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Updater orchestrates update checks against the store and changelog client.
type Updater struct {
	st  *store.Store
	ch  *changelog.Client
	cfg *config.UpdatesConfig

	mu    sync.Mutex
	cache map[string][]changelog.Release
}

// New builds an Updater. cfg is held by pointer so runtime settings changes
// are observed by subsequent check cycles.
func New(st *store.Store, ch *changelog.Client, cfg *config.UpdatesConfig) *Updater {
	return &Updater{st: st, ch: ch, cfg: cfg, cache: map[string][]changelog.Release{}}
}

// ResetCache clears the per-cycle release cache (call at the start of a cycle).
func (u *Updater) ResetCache() {
	u.mu.Lock()
	u.cache = map[string][]changelog.Release{}
	u.mu.Unlock()
}

// CheckAll runs a full update-check cycle across every container.
func (u *Updater) CheckAll(ctx context.Context) error {
	// Fail loudly on an already-dead context rather than walking the whole
	// fleet and reporting success with no data.
	if err := ctx.Err(); err != nil {
		return err
	}
	u.ResetCache()
	containers, err := u.st.ListContainers(store.ContainerFilter{})
	if err != nil {
		return err
	}

	// First pass: resolve sources and prefetch releases per unique repo key,
	// so many containers sharing an image only hit the upstream API once.
	type pending struct {
		c       store.Container
		src     *changelog.Source
		repoKey string
	}
	var items []pending
	seen := map[string]*changelog.Source{}

	for _, cw := range containers {
		c := cw.Container
		if c.Pinned || u.isIgnored(c) {
			// No update may be offered for pinned/ignored containers.
			if cw.Update != nil {
				_ = u.st.DeleteUpdate(c.ID)
			}
			continue
		}
		ref, err := reference.Parse(c.Image)
		if err != nil {
			continue
		}
		src := u.ch.ResolveSource(ref, c.Labels)
		if src == nil {
			continue
		}
		key := repoKey(src)
		items = append(items, pending{c: c, src: src, repoKey: key})
		if _, ok := seen[key]; !ok {
			seen[key] = src
		}
	}

	// Fetch releases for each unique source.
	for key, src := range seen {
		rels, err := u.ch.FetchReleases(ctx, src)
		if err != nil {
			// A check whose context was cancelled must be reported as failed.
			// Swallowing it is how an aborted check used to look like a
			// successful one that simply found nothing.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// Record the fetch failure as "no data" but keep going.
			rels = nil
		}
		u.mu.Lock()
		u.cache[key] = rels
		u.mu.Unlock()
		if rels != nil {
			_ = u.st.UpsertReleases(key, toStoreReleases(key, src, rels))
		}
	}

	// Second pass: compute and persist per-container updates.
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		u.mu.Lock()
		rels := u.cache[it.repoKey]
		u.mu.Unlock()
		if err := u.applyContainer(ctx, it.c, it.src, it.repoKey, rels); err != nil {
			return err
		}
	}
	return nil
}

func (u *Updater) applyContainer(ctx context.Context, c store.Container, src *changelog.Source, key string, rels []changelog.Release) error {
	upd, ok := u.computeUpdate(ctx, c, src, rels)
	if !ok || upd == nil {
		return u.st.DeleteUpdate(c.ID)
	}
	upd.ContainerID = c.ID
	upd.RepoKey = key
	upd.Source = string(src.Type)
	upd.SourceURL = sourceURL(src, c)
	upd.CheckedAt = time.Now().UTC()
	return u.st.UpsertUpdate(upd)
}

// computeUpdate decides whether an update exists and how far behind we are.
//
// Two signals are available and each answers a different question. The release
// list answers "is there a newer version to move the pin to?"; the registry
// digest answers "has the tag the container already tracks moved under it?".
// Which one applies depends on the tag:
//
//   - A version pin ("1.25.3", "1.25", "v2", "1.25-alpine") is compared
//     against the release list. Partial pins only move for a newer version
//     line, since within their own line they still track the moving tag — the
//     digest covers that — and only releases with the same variant suffix are
//     candidates, so "1.25-alpine" is never offered the Debian build.
//   - A tag with no version in it ("latest", "stable", "mainline",
//     "server-cuda13") is a moving target, so the digest decides.
func (u *Updater) computeUpdate(ctx context.Context, c store.Container, src *changelog.Source, rels []changelog.Release) (*store.Update, bool) {
	// Filter prereleases unless configured to include them.
	filtered := make([]changelog.Release, 0, len(rels))
	for _, r := range rels {
		if r.Prerelease && !u.cfg.IncludePreReleases {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) == 0 {
		// No release notes: fall back to registry digest/tag check.
		return u.registryFallback(ctx, c)
	}

	// Version pins, complete or partial. Reading a partial version as a
	// floating tag is how a container on "nginx:1.25" could sit silent while
	// the newest release was 1.31: its digest for "1.25" was up to date, and
	// nothing asked whether a newer line existed.
	if cur, err := parseVer(c.ImageTag); err == nil {
		// The suffix is part of what the operator chose, so only releases
		// carrying the same suffix are candidates.
		variant := cur.Prerelease()
		precision := versionPrecision(c.ImageTag)

		behind, latest := 0, ""
		for _, v := range versionedReleases(filtered) {
			if v.ver.Prerelease() != variant || !isUpdateTarget(cur, precision, v.ver) {
				continue
			}
			behind++
			if latest == "" {
				// Newest first: the first match is the update target.
				latest = v.rel.Tag
			}
		}
		if behind > 0 {
			return &store.Update{CurrentTag: c.ImageTag, LatestTag: latest, VersionsBehind: behind}, true
		}
		// Nothing newer to move to, but the tag may have been re-pushed in
		// place — exactly what happens when the container tracks a partial tag
		// like "1.31" and a new patch lands. Let the digest decide.
		return u.registryFallback(ctx, c)
	}

	// The tag carries no version. A release list from a VCS source is
	// version-ordered, so a tag that names one of its releases (build numbers
	// like "b10549") can be placed by position.
	ordered := orderBySemver(filtered)
	if src != nil && src.Type != changelog.SourceRegistry {
		idx := -1
		for i, r := range ordered {
			if r.rel.Tag == c.ImageTag {
				idx = i
				break
			}
		}
		if idx == 0 {
			return nil, false
		}
		if idx > 0 {
			return &store.Update{CurrentTag: c.ImageTag, LatestTag: ordered[0].rel.Tag, VersionsBehind: idx}, true
		}
	}

	// Otherwise the registry digest is the only signal. When it has moved,
	// label the update with the newest release so the card can name a version
	// rather than just repeating the tag.
	upd, ok := u.registryFallback(ctx, c)
	if !ok || upd == nil {
		return nil, false
	}
	if vs := versionedReleases(filtered); len(vs) > 0 {
		upd.LatestTag = vs[0].rel.Tag
	}
	return upd, true
}

// registryFallback compares the running image digest against the registry's
// current manifest for the tag (accurate for floating tags like "latest" or
// "v2"). It uses the container's own registry/repository, which are parsed
// from the image reference and available regardless of the changelog source.
func (u *Updater) registryFallback(ctx context.Context, c store.Container) (*store.Update, bool) {
	if c.ImageDigest == "" || c.Registry == "" || c.Repository == "" {
		return nil, false
	}
	upToDate, err := u.ch.ManifestUpToDate(ctx, c.Registry, c.Repository, c.ImageTag, c.ImageDigest)
	if err != nil {
		return nil, false
	}
	if upToDate {
		return nil, false
	}
	return &store.Update{CurrentTag: c.ImageTag, LatestTag: c.ImageTag, VersionsBehind: 1}, true
}

type versioned struct {
	rel changelog.Release
	ver *semver.Version
}

func parseVer(tag string) (*semver.Version, error) {
	s := strings.TrimSpace(tag)
	if v, err := semver.NewVersion(s); err == nil {
		return v, nil
	}
	// Loose coercion: strip a leading "v", extract the leading numeric
	// dotted version, pad missing segments and drop leading zeros.
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	start := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return nil, fmt.Errorf("no numeric version in %q", tag)
	}
	// The version must be the whole tag or start after a separator. Without
	// this, a variant tag that merely ends in digits is read as a version:
	// "alpine3.24" would become 3.24.0 and outrank a real 1.31.6 release.
	if start > 0 && !strings.ContainsRune("-_", rune(s[start-1])) {
		return nil, fmt.Errorf("no version at the start of %q", tag)
	}
	s = s[start:]
	var build string
	if i := strings.Index(s, "+"); i >= 0 {
		build = s[i+1:]
		s = s[:i]
	}
	var pre string
	if i := strings.Index(s, "-"); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		// A lone number extracted from a non-version tag (e.g. "b10549" or
		// "server-cuda13") is not a version.
		return nil, fmt.Errorf("no dotted version in %q", tag)
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	for i := range parts {
		parts[i] = strings.TrimLeft(parts[i], "0")
		if parts[i] == "" {
			parts[i] = "0"
		}
	}
	joined := strings.Join(parts, ".")
	if pre != "" {
		joined += "-" + pre
	}
	if build != "" {
		joined += "+" + build
	}
	return semver.NewVersion(joined)
}

// versionPrecision returns how many numeric components the tag spells out: 1
// for "v2", 2 for "1.25", 3 (or more) for "1.25.3". Anything that does not
// start with a dotted numeric version counts as 3, i.e. an explicit pin.
func versionPrecision(tag string) int {
	s := strings.ToLower(strings.TrimSpace(tag))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return 3
	}
	s = s[i:]
	if j := strings.IndexAny(s, "-+"); j >= 0 {
		s = s[:j]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return 3
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return 3
		}
	}
	return len(parts)
}

// isUpdateTarget reports whether release version v is a reason to move off the
// running tag. For a complete pin any greater version counts. For a partial pin
// such as "1.25" only a greater version line does: within the pinned line the
// running container already tracks the moving tag, so a same-line patch is not
// something the operator has to act on — the registry digest covers that case.
func isUpdateTarget(cur *semver.Version, precision int, v *semver.Version) bool {
	switch precision {
	case 1:
		return v.Major() > cur.Major()
	case 2:
		return v.Major() > cur.Major() || (v.Major() == cur.Major() && v.Minor() > cur.Minor())
	default:
		return v.GreaterThan(cur)
	}
}

// versionedReleases parses each release tag on its own and returns the
// parseable ones, newest first. Unlike orderBySemver it does not give up when a
// registry tag listing mixes in tags that carry no version ("mainline",
// "alpine", "stable"): those are simply skipped, so a minor-pinned container
// (for example "nginx:1.25") is still compared against the releases that do
// have versions.
func versionedReleases(rels []changelog.Release) []versioned {
	out := make([]versioned, 0, len(rels))
	for _, r := range rels {
		v, err := parseVer(r.Tag)
		if err != nil {
			continue
		}
		out = append(out, versioned{rel: r, ver: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ver.GreaterThan(out[j].ver) })
	return out
}

func orderBySemver(rels []changelog.Release) []versioned {
	out := make([]versioned, 0, len(rels))
	all := true
	for _, r := range rels {
		v, err := parseVer(r.Tag)
		if err != nil {
			all = false
			break
		}
		out = append(out, versioned{rel: r, ver: v})
	}
	if !all {
		// Preserve upstream order (newest first).
		out = out[:0]
		for _, r := range rels {
			out = append(out, versioned{rel: r})
		}
		return out
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ver.GreaterThan(out[j].ver) })
	return out
}

var mutableTags = map[string]bool{
	"latest": true, "stable": true, "lts": true, "main": true, "master": true,
	"dev": true, "develop": true, "nightly": true, "edge": true, "rolling": true,
	"beta": true, "canary": true, "unstable": true, "testing": true, "next": true,
}

// hasNumericVersion reports whether a tag begins with a dotted numeric version,
// with or without a leading v and with any -variant suffix ignored: "1",
// "v2", "1.25", "1.25.3-alpine" all do; "mainline", "alpine" and "b10549" do
// not.
func hasNumericVersion(t string) bool {
	s := strings.TrimPrefix(strings.TrimPrefix(t, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

func repoKey(src *changelog.Source) string {
	switch src.Type {
	case changelog.SourceGitHub:
		return "gh:" + src.Repo
	case changelog.SourceGitLab:
		return "gl:" + src.Repo
	case changelog.SourceGitea:
		return "gt:" + src.Repo
	case changelog.SourceRegistry:
		return "reg:" + src.Registry + "/" + src.Repository
	}
	return "none"
}

func sourceURL(src *changelog.Source, c store.Container) string {
	switch src.Type {
	case changelog.SourceGitHub:
		return "https://github.com/" + src.Repo
	case changelog.SourceGitLab:
		base := src.BaseURL
		if base == "" {
			base = "https://gitlab.com"
		}
		return strings.TrimSuffix(base, "/") + "/" + src.Repo
	case changelog.SourceGitea:
		if src.WebURL != "" {
			return src.WebURL + "/" + src.Repo
		}
		return strings.TrimSuffix(src.BaseURL, "/api/v1") + "/" + src.Repo
	case changelog.SourceRegistry:
		if src.Registry == "docker.io" {
			return "https://hub.docker.com/r/" + src.Repository
		}
		return "https://" + src.Registry + "/" + src.Repository
	}
	_ = c
	return ""
}

func toStoreReleases(key string, src *changelog.Source, rels []changelog.Release) []store.Release {
	out := make([]store.Release, 0, len(rels))
	for _, r := range rels {
		out = append(out, store.Release{
			RepoKey:     key,
			Tag:         r.Tag,
			Title:       r.Title,
			Body:        r.Body,
			URL:         r.URL,
			PublishedAt: r.PublishedAt,
			Prerelease:  r.Prerelease,
		})
	}
	_ = src
	return out
}

// ResolveRepoKey returns the changelog repo key for a container, or "" when
// no source can be determined. Used to fetch stored changelog history.
func (u *Updater) ResolveRepoKey(c store.Container) string {
	ref, err := reference.Parse(c.Image)
	if err != nil {
		return ""
	}
	src := u.ch.ResolveSource(ref, c.Labels)
	if src == nil {
		return ""
	}
	return repoKey(src)
}

func (u *Updater) isIgnored(c store.Container) bool {
	for _, g := range u.cfg.IgnoreImages {
		if m, _ := path.Match(g, c.Image); m {
			return true
		}
		if m, _ := path.Match(g, c.ImageName); m {
			return true
		}
	}
	return false
}

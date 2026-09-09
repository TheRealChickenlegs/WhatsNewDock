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

	// Order releases newest-first by semver when possible.
	ordered := orderBySemver(filtered)

	// Floating tags ("latest", "v2", "2.14", …) move over time, so the only
	// reliable signal is whether the running image's digest still matches the
	// registry's current manifest for that tag. When it has moved, label the
	// update with the newest release in the same version line.
	if isFloatingTag(c.ImageTag) {
		upd, ok := u.registryFallback(ctx, c)
		if !ok || upd == nil {
			return nil, false
		}
		if target := newestInLine(c.ImageTag, ordered); target != "" {
			upd.LatestTag = target
		}
		return upd, true
	}

	// Pinned tags: compare against the ordered release list.
	latest := ordered[0].rel.Tag
	cur, curErr := parseVer(c.ImageTag)
	if curErr == nil {
		behind := 0
		for _, v := range ordered {
			if v.ver != nil && v.ver.GreaterThan(cur) {
				behind++
			}
		}
		if behind == 0 {
			return nil, false
		}
		return &store.Update{CurrentTag: c.ImageTag, LatestTag: latest, VersionsBehind: behind}, true
	}

	// Non-semver current tag. Registry tag listings (Docker Hub, GHCR, …) are
	// not version-ordered — they mix variant and date-based tags — so the
	// digest comparison is the only reliable update signal for them.
	if src.Type == changelog.SourceRegistry {
		return u.registryFallback(ctx, c)
	}

	// VCS sources (GitHub/GitLab/Gitea) have version-ordered release lists, so
	// fall back to the tag's position (e.g. build-number tags like "b10549").
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
		return &store.Update{CurrentTag: c.ImageTag, LatestTag: latest, VersionsBehind: idx}, true
	}
	// The tag is neither a version nor a release name (e.g. a descriptive
	// variant tag like "server-cuda13"). Treat it as floating and rely on the
	// registry digest to decide whether an update exists.
	return u.registryFallback(ctx, c)
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

// isFloatingTag reports whether a tag is a moving target rather than a pinned
// version. In addition to the known mutable aliases ("latest", "stable", …),
// a partial semver with 1 or 2 numeric components ("2", "v2", "2.14") is a
// floating line tag that tracks the newest release in that line.
func isFloatingTag(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	if t == "" || mutableTags[t] {
		return true
	}
	s := strings.TrimPrefix(strings.TrimPrefix(t, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 1 && len(parts) != 2 {
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

// floatingLine extracts the major and optional minor component of a floating
// tag. ok is false when the tag has no numeric line (e.g. "latest").
func floatingLine(tag string) (maj, min uint64, hasMinor, ok bool) {
	s := strings.ToLower(strings.TrimSpace(tag))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) == 0 || parts[0] == "" {
		return 0, 0, false, false
	}
	m, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false, false
	}
	maj = m
	if len(parts) >= 2 && parts[1] != "" {
		if n, err := strconv.ParseUint(parts[1], 10, 64); err == nil {
			return maj, n, true, true
		}
	}
	return maj, 0, false, true
}

// newestInLine returns the newest release in the same major(.minor) line as the
// floating tag, or the newest release overall when the tag has no numeric line.
func newestInLine(tag string, ordered []versioned) string {
	if len(ordered) == 0 {
		return ""
	}
	maj, min, hasMinor, ok := floatingLine(tag)
	if !ok {
		return ordered[0].rel.Tag
	}
	for _, v := range ordered {
		if v.ver == nil {
			continue
		}
		if v.ver.Major() != maj {
			continue
		}
		if hasMinor && v.ver.Minor() != min {
			continue
		}
		return v.rel.Tag
	}
	return ordered[0].rel.Tag
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

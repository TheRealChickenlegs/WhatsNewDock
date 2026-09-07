// Package changelog aggregates release notes from a variety of upstream
// sources (GitHub, GitLab, Gitea/Forgejo) and falls back to registry tag
// listing when no release-notes source can be determined.
package changelog

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
)

// SourceType enumerates supported upstream providers.
type SourceType string

const (
	SourceGitHub   SourceType = "github"
	SourceGitLab   SourceType = "gitlab"
	SourceGitea    SourceType = "gitea"
	SourceRegistry SourceType = "registry"
	SourceNone     SourceType = "none"
)

// Release is a single upstream release / changelog entry.
type Release struct {
	Tag         string    `json:"tag"`
	Title       string    `json:"title"`
	Body        string    `json:"body"` // markdown
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
}

// Source describes where changelogs for an image should be fetched from.
type Source struct {
	Type       SourceType
	Repo       string // "owner/repo" or GitLab project path
	BaseURL    string // API base URL (self-hosted); empty => provider default
	WebURL     string // human-facing project URL
	Registry   string // registry host (for registry source)
	Repository string // repository path (for registry source)
	Token      string // optional auth token
}

// Client performs release fetches with sensible rate limiting.
type Client struct {
	http        *http.Client
	githubToken string
	gitlabToken string
	registries  map[string]config.RegistryConfig
	limiter     *limiter
}

// New builds a changelog Client from update configuration.
func New(cfg config.UpdatesConfig) *Client {
	return &Client{
		http:        &http.Client{Timeout: 20 * time.Second},
		githubToken: cfg.GitHubToken,
		gitlabToken: cfg.GitLabToken,
		registries:  cfg.Registries,
		limiter:     newLimiter(400 * time.Millisecond),
	}
}

// ResolveSource determines the best changelog source for an image reference,
// using its registry, labels (org.opencontainers.image.source) and configured
// registry hints. It returns nil when no source can be determined.
func (c *Client) ResolveSource(ref reference.Reference, labels map[string]string) *Source {
	// 1. Registry-specific inference.
	switch {
	case ref.IsGitHubRegistry():
		return &Source{Type: SourceGitHub, Repo: ref.Repository, WebURL: "https://github.com/" + ref.Repository}
	case ref.IsGitLabRegistry():
		base := c.registryBase(ref.Registry)
		return &Source{Type: SourceGitLab, Repo: ref.Repository, BaseURL: base}
	}

	// 2. Configured registry hints (custom registries, repo maps).
	if rc, ok := c.registries[ref.Registry]; ok {
		if mapped, ok := rc.RepoMap[ref.Repository]; ok && mapped != "" {
			return sourceFromHint(rc, mapped)
		}
		if mapped, ok := rc.RepoMap["*"]; ok && mapped != "" {
			return sourceFromHint(rc, mapped)
		}
		if rc.Source != "" || rc.BaseURL != "" {
			return sourceFromHint(rc, ref.Repository)
		}
	}

	// 3. Label-provided source URL.
	if src := labels["org.opencontainers.image.source"]; src != "" {
		if s := sourceFromURL(src); s != nil {
			return s
		}
	}

	// 4. Fall back to registry tag listing (no release notes) for known hosts.
	switch ref.Registry {
	case "docker.io":
		return &Source{Type: SourceRegistry, Registry: "docker.io", Repository: ref.Repository}
	case "ghcr.io", "registry.gitlab.com", "gcr.io", "public.ecr.aws", "quay.io":
		return &Source{Type: SourceRegistry, Registry: ref.Registry, Repository: ref.Repository}
	}
	return nil
}

func sourceFromHint(rc config.RegistryConfig, repo string) *Source {
	src := &Source{Repo: repo, BaseURL: rc.BaseURL}
	switch strings.ToLower(rc.Source) {
	case "github":
		src.Type = SourceGitHub
		src.WebURL = "https://github.com/" + repo
	case "gitlab":
		src.Type = SourceGitLab
	case "gitea", "forgejo":
		src.Type = SourceGitea
	default:
		if rc.BaseURL != "" && !strings.Contains(rc.BaseURL, "api.github.com") {
			src.Type = SourceGitea // Gitea/Forgejo share the GitHub-style API
			if src.WebURL == "" {
				src.WebURL = strings.TrimSuffix(rc.BaseURL, "/api/v1")
			}
			return src
		}
		src.Type = SourceGitHub
		src.WebURL = "https://github.com/" + repo
	}
	return src
}

func (c *Client) registryBase(registry string) string {
	if rc, ok := c.registries[registry]; ok && rc.BaseURL != "" {
		return rc.BaseURL
	}
	return ""
}

// sourceFromURL parses an org.opencontainers.image.source URL.
func sourceFromURL(raw string) *Source {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Host)
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	// Strip leading "/-/tree/" or trailing tree-ish components is complex;
	// keep it simple: take the first two path segments as owner/repo.
	segs := strings.Split(path, "/")
	if len(segs) < 2 {
		return nil
	}
	repo := segs[0] + "/" + segs[1]
	switch {
	case host == "github.com":
		return &Source{Type: SourceGitHub, Repo: repo, WebURL: "https://github.com/" + repo}
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return &Source{Type: SourceGitLab, Repo: repo, BaseURL: "https://gitlab.com"}
	case strings.Contains(host, "gitea") || strings.Contains(host, "forgejo") || host == "codeberg.org":
		return &Source{Type: SourceGitea, Repo: repo, BaseURL: "https://" + host}
	}
	return nil
}

// FetchReleases retrieves recent releases for the source, newest first.
func (c *Client) FetchReleases(ctx context.Context, src *Source) ([]Release, error) {
	if src == nil {
		return nil, nil
	}
	switch src.Type {
	case SourceGitHub:
		return c.githubReleases(ctx, src)
	case SourceGitLab:
		return c.gitlabReleases(ctx, src)
	case SourceGitea:
		return c.giteaReleases(ctx, src)
	case SourceRegistry:
		return c.registryTags(ctx, src)
	default:
		return nil, nil
	}
}

// SetGitHubToken updates the GitHub token at runtime (used by settings API).
func (c *Client) SetGitHubToken(t string) { c.githubToken = t }

// SetGitLabToken updates the GitLab token at runtime.
func (c *Client) SetGitLabToken(t string) { c.gitlabToken = t }

// limiter is a minimal per-client rate limiter.
type limiter struct {
	mu   sync.Mutex
	last time.Time
	min  time.Duration
}

func newLimiter(min time.Duration) *limiter { return &limiter{min: min} }

func (l *limiter) Wait() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := l.min - time.Since(l.last); d > 0 {
		time.Sleep(d)
	}
	l.last = time.Now()
}

// apiErr wraps a non-2xx response.
func apiErr(status int, url string) error {
	return fmt.Errorf("upstream API %s returned %d", url, status)
}

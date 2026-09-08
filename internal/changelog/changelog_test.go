package changelog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
)

func newTestClient() *Client { return New(config.UpdatesConfig{}) }

func parse(t *testing.T, raw string) reference.Reference {
	t.Helper()
	ref, err := reference.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return ref
}

func TestResolveSourceGHCR(t *testing.T) {
	src := newTestClient().ResolveSource(parse(t, "ghcr.io/owner/repo:latest"), nil)
	if src == nil || src.Type != SourceGitHub || src.Repo != "owner/repo" {
		t.Fatalf("got %+v, want github owner/repo", src)
	}
	if src.WebURL != "https://github.com/owner/repo" {
		t.Errorf("web url = %q", src.WebURL)
	}
}

func TestResolveSourceGitLabRegistry(t *testing.T) {
	src := newTestClient().ResolveSource(parse(t, "registry.gitlab.com/group/sub/proj:1.0"), nil)
	if src == nil || src.Type != SourceGitLab || src.Repo != "group/sub/proj" {
		t.Fatalf("got %+v, want gitlab group/sub/proj", src)
	}
}

func TestResolveSourceDockerHub(t *testing.T) {
	src := newTestClient().ResolveSource(parse(t, "nginx:latest"), nil)
	if src == nil || src.Type != SourceRegistry {
		t.Fatalf("got %+v, want registry source", src)
	}
	if src.Registry != "docker.io" || src.Repository != "library/nginx" {
		t.Errorf("registry/repository = %q/%q", src.Registry, src.Repository)
	}
}

func TestResolveSourceFromLabel(t *testing.T) {
	labels := map[string]string{"org.opencontainers.image.source": "https://github.com/owner/repo"}
	src := newTestClient().ResolveSource(parse(t, "nginx:latest"), labels)
	if src == nil || src.Type != SourceGitHub || src.Repo != "owner/repo" {
		t.Fatalf("got %+v, want github owner/repo from label", src)
	}
}

func TestResolveSourceConfigHint(t *testing.T) {
	c := New(config.UpdatesConfig{
		Registries: map[string]config.RegistryConfig{
			"registry.example.com": {Source: "gitea", BaseURL: "https://registry.example.com"},
		},
	})
	src := c.ResolveSource(parse(t, "registry.example.com/team/app:1.0"), nil)
	if src == nil || src.Type != SourceGitea || src.Repo != "team/app" {
		t.Fatalf("got %+v, want gitea team/app", src)
	}
}

func TestSourceFromURL(t *testing.T) {
	cases := map[string]SourceType{
		"https://github.com/owner/repo":        SourceGitHub,
		"https://github.com/owner/repo.git":    SourceGitHub,
		"https://gitlab.com/group/proj":        SourceGitLab,
		"https://codeberg.org/owner/repo":      SourceGitea,
		"https://gitea.example.com/owner/repo": SourceGitea,
	}
	for raw, want := range cases {
		s := sourceFromURL(raw)
		if s == nil || s.Type != want {
			t.Errorf("sourceFromURL(%q) = %+v, want %s", raw, s, want)
		}
	}
	if s := sourceFromURL("https://example.com/not-enough"); s != nil {
		t.Errorf("sourceFromURL(short path) = %+v, want nil", s)
	}
}

func TestGitHubReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"tag_name":"v1.2.0","name":"One","body":"body one","html_url":"https://github.com/o/r/releases/tag/v1.2.0","published_at":"2024-01-02T00:00:00Z","prerelease":false,"draft":false},
			{"tag_name":"v1.1.0","name":"Draft","body":"hidden","html_url":"","published_at":"2024-01-01T00:00:00Z","prerelease":false,"draft":true}
		]`)
	}))
	defer srv.Close()

	rels, err := newTestClient().githubReleases(context.Background(), &Source{Type: SourceGitHub, Repo: "owner/repo", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("githubReleases: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 release (draft filtered), got %d", len(rels))
	}
	if rels[0].Tag != "v1.2.0" || rels[0].Title != "One" || rels[0].Body != "body one" {
		t.Errorf("release = %+v", rels[0])
	}
}

func TestGitLabReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"tag_name":"v1.0.0","name":"Rel","description":"desc","released_at":"2024-01-01T00:00:00Z","_links":{"self":"https://gitlab.com/g/p/-/releases/v1.0.0"},"upcoming_release":false},
			{"tag_name":"v1.1.0","name":"Upcoming","description":"","released_at":"2024-01-02T00:00:00Z","_links":{"self":"https://gitlab.com/g/p/-/releases/v1.1.0"},"upcoming_release":true}
		]`)
	}))
	defer srv.Close()

	rels, err := newTestClient().gitlabReleases(context.Background(), &Source{Type: SourceGitLab, Repo: "g/p", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("gitlabReleases: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 release (upcoming filtered), got %d", len(rels))
	}
	if rels[0].Tag != "v1.0.0" || rels[0].Body != "desc" {
		t.Errorf("release = %+v", rels[0])
	}
}

func TestGiteaReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"tag_name":"v2.0.0","name":"Two","body":"gitea body","html_url":"https://gitea/o/r/releases/tag/v2.0.0","published_at":"2024-02-01T00:00:00Z","prerelease":false,"draft":false}
		]`)
	}))
	defer srv.Close()

	rels, err := newTestClient().giteaReleases(context.Background(), &Source{Type: SourceGitea, Repo: "o/r", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("giteaReleases: %v", err)
	}
	if len(rels) != 1 || rels[0].Tag != "v2.0.0" || rels[0].Body != "gitea body" {
		t.Errorf("releases = %+v", rels)
	}
}

func TestFetchReleasesUnknownSource(t *testing.T) {
	rels, err := newTestClient().FetchReleases(context.Background(), &Source{Type: "bogus"})
	if err != nil || rels != nil {
		t.Errorf("expected nil,nil got %v,%v", rels, err)
	}
}

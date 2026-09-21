package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const githubDefaultBase = "https://api.github.com"

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

func (c *Client) githubReleases(ctx context.Context, src *Source) ([]Release, error) {
	base := src.BaseURL
	if base == "" {
		base = githubDefaultBase
	}
	var out []Release
	for page := 1; page <= 3; page++ {
		u := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", base, src.Repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.githubToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.githubToken)
		}
		c.limiter.Wait()
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			return out, nil
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, apiErr(resp.StatusCode, u)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var rels []githubRelease
		if err := json.Unmarshal(body, &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if r.Draft {
				continue
			}
			out = append(out, Release{
				Tag:         r.TagName,
				Title:       firstNonEmpty(r.Name, r.TagName),
				Body:        r.Body,
				URL:         r.HTMLURL,
				PublishedAt: parseTime(r.PublishedAt),
				Prerelease:  r.Prerelease,
			})
		}
		if len(rels) < 100 {
			break
		}
	}
	return out, nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// LatestRelease returns the newest published release for a GitHub repository,
// or nil when the repository has no releases (or does not exist).
//
// It skips prereleases, so the answer is the release an operator should be
// running rather than the newest tag in the repository.
func (c *Client) LatestRelease(ctx context.Context, repo string) (*Release, error) {
	if repo == "" {
		return nil, nil
	}
	u := fmt.Sprintf("%s/repos/%s/releases?per_page=20", githubDefaultBase, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.githubToken)
	}
	c.limiter.Wait()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp.StatusCode, u)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var rels []githubRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, err
	}
	// The list is newest-first; take the first that is published and not a
	// prerelease, falling back to the newest published release of any kind.
	var fallback *Release
	for _, r := range rels {
		if r.Draft {
			continue
		}
		rel := &Release{
			Tag:         r.TagName,
			Title:       firstNonEmpty(r.Name, r.TagName),
			Body:        r.Body,
			URL:         r.HTMLURL,
			PublishedAt: parseTime(r.PublishedAt),
			Prerelease:  r.Prerelease,
		}
		if !r.Prerelease {
			return rel, nil
		}
		if fallback == nil {
			fallback = rel
		}
	}
	return fallback, nil
}

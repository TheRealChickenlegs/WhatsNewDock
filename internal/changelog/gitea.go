package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (c *Client) giteaReleases(ctx context.Context, src *Source) ([]Release, error) {
	base := src.BaseURL
	if base == "" {
		return nil, fmt.Errorf("gitea source requires a base URL")
	}
	var out []Release
	for page := 1; page <= 3; page++ {
		u := fmt.Sprintf("%s/api/v1/repos/%s/releases?limit=50&page=%d", base, src.Repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if src.Token != "" {
			req.Header.Set("Authorization", "token "+src.Token)
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
		var rels []githubRelease // Gitea mirrors the GitHub release shape
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
		if len(rels) < 50 {
			break
		}
	}
	return out, nil
}

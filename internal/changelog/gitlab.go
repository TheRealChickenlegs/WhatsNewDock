package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const gitlabDefaultBase = "https://gitlab.com"

type gitlabRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleasedAt  string `json:"released_at"`
	Links       struct {
		Self string `json:"self"`
	} `json:"_links"`
	UpcomingRelease bool `json:"upcoming_release"`
}

func (c *Client) gitlabReleases(ctx context.Context, src *Source) ([]Release, error) {
	base := src.BaseURL
	if base == "" {
		base = gitlabDefaultBase
	}
	u := fmt.Sprintf("%s/api/v4/projects/%s/releases?per_page=100", base, url.PathEscape(src.Repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.gitlabToken != "" {
		req.Header.Set("PRIVATE-TOKEN", c.gitlabToken)
	}
	c.limiter.Wait()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, nil
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
	var rels []gitlabRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(rels))
	for _, r := range rels {
		if r.UpcomingRelease {
			continue
		}
		out = append(out, Release{
			Tag:         r.TagName,
			Title:       firstNonEmpty(r.Name, r.TagName),
			Body:        r.Description,
			URL:         r.Links.Self,
			PublishedAt: parseTime(r.ReleasedAt),
		})
	}
	return out, nil
}

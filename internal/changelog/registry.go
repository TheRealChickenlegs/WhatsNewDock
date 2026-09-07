package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// registryTags lists available tags for an image as a fallback when no
// release-notes source can be determined. Returned releases carry only tags
// (no notes); the caller sorts them by version.
func (c *Client) registryTags(ctx context.Context, src *Source) ([]Release, error) {
	switch src.Registry {
	case "docker.io":
		return c.dockerHubTags(ctx, src.Repository)
	default:
		return c.genericRegistryTags(ctx, src.Registry, src.Repository)
	}
}

type hubTag struct {
	Name        string `json:"name"`
	LastUpdated string `json:"last_updated"`
	Digest      string `json:"digest"`
}

type hubResponse struct {
	Results []hubTag `json:"results"`
	Next    string   `json:"next"`
}

func (c *Client) dockerHubTags(ctx context.Context, repo string) ([]Release, error) {
	var out []Release
	pageURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/?page_size=100&page=1&ordering=last_updated", repo)
	for pageURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		c.limiter.Wait()
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return out, nil
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, apiErr(resp.StatusCode, pageURL)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var hr hubResponse
		if err := json.Unmarshal(body, &hr); err != nil {
			return nil, err
		}
		for _, t := range hr.Results {
			out = append(out, Release{
				Tag:         t.Name,
				URL:         fmt.Sprintf("https://hub.docker.com/r/%s/tags", repo),
				PublishedAt: parseTime(t.LastUpdated),
			})
		}
		if hr.Next == "" || len(out) >= 300 {
			break
		}
		pageURL = hr.Next
	}
	return out, nil
}

// genericRegistryTags implements the Docker Registry v2 anonymous token flow
// (used for ghcr.io, gcr.io, public.ecr.aws, quay.io, and self-hosted).
func (c *Client) genericRegistryTags(ctx context.Context, registry, repo string) ([]Release, error) {
	listURL := fmt.Sprintf("https://%s/v2/%s/tags/list?n=500", registry, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	c.limiter.Wait()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		token, err := c.fetchRegistryToken(ctx, resp.Header.Get("Www-Authenticate"))
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, nil
		}
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		req2.Header.Set("Authorization", "Bearer "+token)
		c.limiter.Wait()
		resp2, err := c.http.Do(req2)
		if err != nil {
			return nil, err
		}
		body, _ = io.ReadAll(io.LimitReader(resp2.Body, 8<<20))
		_ = resp2.Body.Close()
		if resp2.StatusCode == http.StatusNotFound || resp2.StatusCode == http.StatusUnauthorized {
			return nil, nil
		}
		if resp2.StatusCode != http.StatusOK {
			return nil, apiErr(resp2.StatusCode, listURL)
		}
	} else if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	} else if resp.StatusCode != http.StatusOK {
		return nil, apiErr(resp.StatusCode, listURL)
	}

	var parsed struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]Release, 0, len(parsed.Tags))
	for _, t := range parsed.Tags {
		out = append(out, Release{Tag: t, URL: fmt.Sprintf("https://%s/v2/%s", registry, repo)})
	}
	return out, nil
}

// manifestAccept lists the manifest media types we accept.
const manifestAccept = "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v1+prettyjws"

// ManifestDigest returns the registry's current content digest for a tag.
// It is used to detect updates for floating tags ("latest") without notes.
func (c *Client) ManifestDigest(ctx context.Context, registry, repo, tag string) (string, error) {
	host := registry
	if registry == "docker.io" {
		host = "registry-1.docker.io"
	}
	u := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	c.limiter.Wait()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := c.fetchRegistryToken(ctx, resp.Header.Get("Www-Authenticate"))
		_ = resp.Body.Close()
		if err != nil || token == "" {
			return "", err
		}
		req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req2.Header.Set("Accept", manifestAccept)
		req2.Header.Set("Authorization", "Bearer "+token)
		c.limiter.Wait()
		resp2, err := c.http.Do(req2)
		if err != nil {
			return "", err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode == http.StatusNotFound || resp2.StatusCode == http.StatusUnauthorized {
			return "", nil
		}
		if resp2.StatusCode != http.StatusOK {
			return "", apiErr(resp2.StatusCode, u)
		}
		return resp2.Header.Get("Docker-Content-Digest"), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp.StatusCode, u)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

// fetchRegistryToken parses the WWW-Authenticate challenge and fetches an
// anonymous pull token from the registry's token realm.
func (c *Client) fetchRegistryToken(ctx context.Context, challenge string) (string, error) {
	challenge = strings.TrimPrefix(challenge, "Bearer ")
	params := map[string]string{}
	for _, part := range strings.Split(challenge, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		params[kv[0]] = strings.Trim(kv[1], `"`)
	}
	realm, ok := params["realm"]
	if !ok || realm == "" {
		return "", fmt.Errorf("registry token challenge missing realm")
	}
	q := url.Values{}
	for _, k := range []string{"service", "scope", "account"} {
		if v, ok := params[k]; ok {
			q.Set(k, v)
		}
	}
	u := realm
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	c.limiter.Wait()
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", apiErr(resp.StatusCode, u)
	}
	var tr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", err
	}
	return tr.Token, nil
}

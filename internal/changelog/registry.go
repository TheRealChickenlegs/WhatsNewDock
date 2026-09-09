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

// registryGet performs a GET against a Docker Registry v2 endpoint, handling
// the anonymous token flow (401 -> fetch token -> retry with Bearer). It
// returns the response body and Docker-Content-Digest header; ok is false when
// the resource doesn't exist (404) or remains unauthorized after the retry.
func (c *Client) registryGet(ctx context.Context, url, accept string) (body []byte, digest string, ok bool, err error) {
	do := func(auth string) ([]byte, int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, "", err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		c.limiter.Wait()
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, "", err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, 0, "", err
		}
		return b, resp.StatusCode, resp.Header.Get("Docker-Content-Digest"), nil
	}

	body, status, digest, err := do("")
	if err != nil {
		return nil, "", false, err
	}
	if status == http.StatusUnauthorized {
		// Retry with an anonymous pull token. The challenge is only available
		// from the 401 response headers, so re-fetch it separately.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		c.limiter.Wait()
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, "", false, err
		}
		challenge := resp.Header.Get("Www-Authenticate")
		_ = resp.Body.Close()

		token, err := c.fetchRegistryToken(ctx, challenge)
		if err != nil || token == "" {
			return nil, "", false, err
		}
		body, status, digest, err = do(token)
		if err != nil {
			return nil, "", false, err
		}
	}
	if status == http.StatusNotFound || status == http.StatusUnauthorized {
		return nil, "", false, nil
	}
	if status != http.StatusOK {
		return nil, "", false, apiErr(status, url)
	}
	return body, digest, true, nil
}

// genericRegistryTags lists tags using the Docker Registry v2 API (ghcr.io,
// gcr.io, public.ecr.aws, quay.io, and self-hosted).
func (c *Client) genericRegistryTags(ctx context.Context, registry, repo string) ([]Release, error) {
	listURL := fmt.Sprintf("https://%s/v2/%s/tags/list?n=500", registry, repo)
	body, _, ok, err := c.registryGet(ctx, listURL, "")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
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

// ManifestUpToDate reports whether the running image digest still matches the
// registry's current manifest for the tag. It accounts for multi-arch images:
// the tag may resolve to a manifest list / OCI index, while the container runs
// one of its platform-specific manifests.
func (c *Client) ManifestUpToDate(ctx context.Context, registry, repo, tag, imageDigest string) (bool, error) {
	body, digest, err := c.fetchManifest(ctx, registry, repo, tag)
	if err != nil || digest == "" {
		return false, err
	}
	if strings.EqualFold(digest, imageDigest) {
		return true, nil
	}
	// The tag may point to a manifest list / OCI index whose platform
	// manifests carry the digest the container is actually running.
	var idx struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(body, &idx); err == nil {
		for _, m := range idx.Manifests {
			if strings.EqualFold(m.Digest, imageDigest) {
				return true, nil
			}
		}
	}
	return false, nil
}

// fetchManifest retrieves the manifest body and its content digest for a tag
// using the Docker Registry v2 anonymous token flow.
func (c *Client) fetchManifest(ctx context.Context, registry, repo, tag string) ([]byte, string, error) {
	host := registry
	if registry == "docker.io" {
		host = "registry-1.docker.io"
	}
	u := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, tag)
	body, digest, _, err := c.registryGet(ctx, u, manifestAccept)
	return body, digest, err
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

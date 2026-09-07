// Package reference normalises Docker image references into registry,
// repository and tag components used throughout the application.
package reference

import (
	"errors"
	"strings"

	"github.com/distribution/reference"
)

// Reference is a parsed, normalised Docker image reference.
type Reference struct {
	Registry   string `json:"registry"`   // e.g. "docker.io", "ghcr.io"
	Repository string `json:"repository"` // e.g. "library/nginx", "owner/repo"
	Name       string `json:"name"`       // e.g. "docker.io/library/nginx"
	Tag        string `json:"tag"`        // e.g. "latest" ("" if digest-only)
	Digest     string `json:"digest"`     // e.g. "sha256:..." ("" if tagged)
	Original   string `json:"original"`   // the raw reference string
}

// Parse normalises a raw image reference string.
func Parse(raw string) (Reference, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Reference{}, errors.New("empty image reference")
	}
	named, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return Reference{}, err
	}
	r := Reference{
		Registry:   reference.Domain(named),
		Repository: reference.Path(named),
		Name:       named.Name(),
		Original:   raw,
	}
	if tagged, ok := named.(reference.Tagged); ok {
		r.Tag = tagged.Tag()
	}
	if digested, ok := named.(reference.Digested); ok {
		r.Digest = digested.Digest().String()
	}
	// Docker semantics: an untagged, undigested reference means ":latest".
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, nil
}

// String returns the full normalised name including tag.
func (r Reference) String() string {
	if r.Tag != "" {
		return r.Name + ":" + r.Tag
	}
	if r.Digest != "" {
		return r.Name + "@" + r.Digest
	}
	return r.Name
}

// IsGitHubRegistry reports whether the registry is GitHub Container Registry.
func (r Reference) IsGitHubRegistry() bool {
	return r.Registry == "ghcr.io"
}

// IsGitLabRegistry reports whether the registry is GitLab's container registry.
func (r Reference) IsGitLabRegistry() bool {
	return r.Registry == "registry.gitlab.com"
}

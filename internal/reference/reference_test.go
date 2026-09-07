package reference

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw        string
		registry   string
		repository string
		tag        string
	}{
		{"nginx", "docker.io", "library/nginx", "latest"},
		{"nginx:latest", "docker.io", "library/nginx", "latest"},
		{"nginx:1.25.0", "docker.io", "library/nginx", "1.25.0"},
		{"library/nginx:1.25", "docker.io", "library/nginx", "1.25"},
		{"ghcr.io/org/app:v1.2.3", "ghcr.io", "org/app", "v1.2.3"},
		{"registry.gitlab.com/group/sub/proj:2.0", "registry.gitlab.com", "group/sub/proj", "2.0"},
		{"quay.io/prometheus/node-exporter:v1.8.0", "quay.io", "prometheus/node-exporter", "v1.8.0"},
	}
	for _, c := range cases {
		r, err := Parse(c.raw)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.raw, err)
		}
		if r.Registry != c.registry || r.Repository != c.repository || r.Tag != c.tag {
			t.Errorf("Parse(%q) = %+v, want registry=%s repository=%s tag=%s",
				c.raw, r, c.registry, c.repository, c.tag)
		}
	}
}

func TestParseDigest(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	r, err := Parse("nginx@" + digest)
	if err != nil {
		t.Fatalf("Parse digest error: %v", err)
	}
	if r.Digest != digest {
		t.Errorf("digest = %q", r.Digest)
	}
	if r.Tag != "" {
		t.Errorf("expected empty tag for digest ref, got %q", r.Tag)
	}
}

func TestIsGitHubRegistry(t *testing.T) {
	r, _ := Parse("ghcr.io/org/app:1.0")
	if !r.IsGitHubRegistry() {
		t.Error("expected ghcr.io to be GitHub registry")
	}
}

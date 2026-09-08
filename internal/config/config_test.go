package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Mode != ModeServer {
		t.Errorf("mode = %v, want server", c.Mode)
	}
	if c.Port != 8080 {
		t.Errorf("port = %d, want 8080", c.Port)
	}
	if c.Host != "0.0.0.0" {
		t.Errorf("host = %q", c.Host)
	}
	if c.Docker.Host != "unix:///var/run/docker.sock" {
		t.Errorf("docker host = %q", c.Docker.Host)
	}
	if !c.Docker.EnableRecreate {
		t.Error("enable_recreate should default to true")
	}
	if c.Updates.ChangelogCount != 5 {
		t.Errorf("changelog_count = %d, want 5", c.Updates.ChangelogCount)
	}
	if c.Updates.Interval != 6*time.Hour {
		t.Errorf("update interval = %v, want 6h", c.Updates.Interval)
	}
	if c.Auth.Mode != AuthLocal {
		t.Errorf("auth mode = %v, want local", c.Auth.Mode)
	}
	if c.Auth.OIDC.UsernameClaim != "preferred_username" {
		t.Errorf("username claim = %q", c.Auth.OIDC.UsernameClaim)
	}
	if c.Auth.OIDC.DefaultRole != "viewer" {
		t.Errorf("default role = %q", c.Auth.OIDC.DefaultRole)
	}
	if c.Auth.SessionTTL != 24*time.Hour {
		t.Errorf("session ttl = %v, want 24h", c.Auth.SessionTTL)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("WND_MODE", "agent")
	t.Setenv("WND_PORT", "9000")
	t.Setenv("WND_CHANGELOG_COUNT", "10")
	t.Setenv("WND_INCLUDE_PRERELEASES", "true")
	t.Setenv("WND_AUTH_MODE", "oidc")
	t.Setenv("WND_OIDC_ISSUER", "https://pocketid.example.com")
	t.Setenv("WND_AGENT_SERVER_URL", "https://server.example.com")
	t.Setenv("WND_AGENT_TOKEN", "wnd_token")

	c, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Mode != ModeAgent {
		t.Errorf("mode = %v, want agent", c.Mode)
	}
	if c.Port != 9000 {
		t.Errorf("port = %d, want 9000", c.Port)
	}
	if c.Updates.ChangelogCount != 10 {
		t.Errorf("changelog_count = %d, want 10", c.Updates.ChangelogCount)
	}
	if !c.Updates.IncludePreReleases {
		t.Error("include_prereleases should be true")
	}
	if c.Auth.Mode != AuthOIDC {
		t.Errorf("auth mode = %v, want oidc", c.Auth.Mode)
	}
	if c.Auth.OIDC.IssuerURL != "https://pocketid.example.com" {
		t.Errorf("issuer = %q", c.Auth.OIDC.IssuerURL)
	}
	if c.Agent.ServerURL != "https://server.example.com" {
		t.Errorf("agent server url = %q", c.Agent.ServerURL)
	}
}

func TestLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "mode: server\nport: 8081\nupdates:\n  changelog_count: 7\n  include_prereleases: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(&Flags{ConfigPath: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Port != 8081 {
		t.Errorf("port = %d, want 8081", c.Port)
	}
	if c.Updates.ChangelogCount != 7 {
		t.Errorf("changelog_count = %d, want 7", c.Updates.ChangelogCount)
	}
	if !c.Updates.IncludePreReleases {
		t.Error("include_prereleases should be true")
	}
}

func TestLoadFlagsOverrideEnv(t *testing.T) {
	t.Setenv("WND_PORT", "9000")
	c, err := Load(&Flags{Port: 1234})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Port != 1234 {
		t.Errorf("port = %d, want 1234 (flag overrides env)", c.Port)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad mode", func(c *Config) { c.Mode = "bogus" }},
		{"bad port", func(c *Config) { c.Port = 0 }},
		{"agent without server url", func(c *Config) { c.Mode = ModeAgent }},
		{"agent without token", func(c *Config) {
			c.Mode = ModeAgent
			c.Agent.ServerURL = "https://server.example.com"
		}},
		{"oidc without issuer", func(c *Config) { c.Auth.Mode = AuthOIDC }},
		{"bad changelog count", func(c *Config) { c.Updates.ChangelogCount = 0 }},
		{"bad base url", func(c *Config) { c.BaseURL = "notaurl" }},
	}
	for _, tc := range cases {
		c := Default()
		tc.mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected validation error", tc.name)
		}
	}
}

func TestValidateOK(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("default config should validate: %v", err)
	}
}

func TestParseFlags(t *testing.T) {
	f := ParseFlags([]string{"--mode", "agent", "--port", "9999", "--version", "--config", "/tmp/c.yaml"})
	if f.Mode != "agent" {
		t.Errorf("mode = %q", f.Mode)
	}
	if f.Port != 9999 {
		t.Errorf("port = %d", f.Port)
	}
	if !f.Version {
		t.Error("version should be set")
	}
	if f.ConfigPath != "/tmp/c.yaml" {
		t.Errorf("config path = %q", f.ConfigPath)
	}
}

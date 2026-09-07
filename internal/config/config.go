// Package config loads and normalises WhatsNewDock configuration from
// defaults, an optional YAML file, environment variables and command-line
// flags (flags take precedence).
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode describes the operating mode of a WhatsNewDock instance.
type Mode string

const (
	ModeServer Mode = "server"
	ModeAgent  Mode = "agent"
)

// AuthMode describes which authentication mechanisms are enabled.
type AuthMode string

const (
	AuthLocal AuthMode = "local"
	AuthOIDC  AuthMode = "oidc"
	AuthBoth  AuthMode = "both"
)

// Config is the top-level runtime configuration.
type Config struct {
	Mode     Mode   `yaml:"mode"    json:"mode"`
	Host     string `yaml:"host"    json:"host"`
	Port     int    `yaml:"port"    json:"port"`
	BaseURL  string `yaml:"base_url" json:"base_url"`
	DataDir  string `yaml:"data_dir" json:"data_dir"`
	LogLevel string `yaml:"log_level" json:"log_level"`

	// TrustedProxies is a comma-separated list of CIDRs to trust for
	// X-Forwarded-* headers when behind a reverse proxy.
	TrustedProxies string `yaml:"trusted_proxies" json:"trusted_proxies"`

	Docker  DockerConfig  `yaml:"docker"  json:"docker"`
	Agent   AgentConfig   `yaml:"agent"   json:"agent"`
	Updates UpdatesConfig `yaml:"updates" json:"updates"`
	Auth    AuthConfig    `yaml:"auth"    json:"auth"`

	// InitialAdminUser/InitialAdminPassword bootstrap the first local admin
	// account when the user table is empty.
	InitialAdminUser     string `yaml:"initial_admin_user"     json:"initial_admin_user"`
	InitialAdminPassword string `yaml:"initial_admin_password" json:"initial_admin_password"`
}

// DockerConfig controls how the Docker Engine API is reached.
type DockerConfig struct {
	// Host is the docker host URI (unix:///var/run/docker.sock by default).
	// Set to the URL of a docker-socket-proxy for hardened deployments.
	Host string `yaml:"host" json:"host"`
	// APIVersion pins the Docker API version; empty negotiates.
	APIVersion string `yaml:"api_version" json:"api_version"`
	// TLS enables TLS for TCP docker hosts.
	TLS     bool   `yaml:"tls" json:"tls"`
	TLSCA   string `yaml:"tls_ca"   json:"tls_ca"`
	TLSCert string `yaml:"tls_cert" json:"tls_cert"`
	TLSKey  string `yaml:"tls_key"  json:"tls_key"`
	// EnableRecreate allows the one-click update feature (container recreate).
	EnableRecreate bool `yaml:"enable_recreate" json:"enable_recreate"`
}

// AgentConfig configures agent mode (reporting to a central server).
type AgentConfig struct {
	ServerURL string        `yaml:"server_url" json:"server_url"`
	Token     string        `yaml:"token"      json:"token"`
	Name      string        `yaml:"name"       json:"name"`
	Interval  time.Duration `yaml:"interval"   json:"interval"`
	// TLSSkipVerify disables certificate verification for the server URL
	// (development only; prefer a real certificate or mTLS in production).
	TLSSkipVerify bool `yaml:"tls_skip_verify" json:"tls_skip_verify"`
}

// UpdatesConfig controls update detection and changelog aggregation.
type UpdatesConfig struct {
	// Interval is how often update checks run on the server.
	Interval time.Duration `yaml:"interval" json:"interval"`
	// ChangelogCount is how many past release changelogs to retain/show
	// per image. Default 5.
	ChangelogCount int `yaml:"changelog_count" json:"changelog_count"`
	// GitHubToken is an optional PAT/GITHUB_TOKEN used to raise GitHub API
	// rate limits when aggregating release notes.
	GitHubToken string `yaml:"github_token" json:"github_token"`
	// GitLabToken is an optional token for self-hosted/private GitLab.
	GitLabToken string `yaml:"gitlab_token" json:"gitlab_token"`
	// Registries is a map of custom registry host -> source hints (see docs).
	Registries map[string]RegistryConfig `yaml:"registries" json:"registries"`
	// IncludePreReleases controls whether pre-releases are offered as updates.
	IncludePreReleases bool `yaml:"include_prereleases" json:"include_prereleases"`
	// IgnoreImages is a list of image name globs never offered for update.
	IgnoreImages []string `yaml:"ignore_images" json:"ignore_images"`
}

// RegistryConfig maps a registry host to a changelog source hint.
type RegistryConfig struct {
	// Source, when set, forces a source type ("github", "gitlab", "gitea").
	Source string `yaml:"source" json:"source"`
	// BaseURL overrides the API base URL (for self-hosted GitLab/Gitea).
	BaseURL string `yaml:"base_url" json:"base_url"`
	// RepoMap maps image repository names to upstream "owner/repo" values.
	RepoMap map[string]string `yaml:"repo_map" json:"repo_map"`
}

// AuthConfig configures authentication.
type AuthConfig struct {
	Mode AuthMode `yaml:"mode" json:"mode"`
	// SessionSecret signs session cookies/JWTs. Auto-generated and persisted
	// when empty.
	SessionSecret string `yaml:"session_secret" json:"session_secret"`
	// SessionTTL is how long a web session stays valid.
	SessionTTL time.Duration `yaml:"session_ttl" json:"session_ttl"`
	// OIDC holds OpenID Connect settings (optional).
	OIDC OIDCConfig `yaml:"oidc" json:"oidc"`
}

// OIDCConfig holds OpenID Connect provider settings (e.g. PocketID).
type OIDCConfig struct {
	IssuerURL    string `yaml:"issuer_url" json:"issuer_url"`
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"client_secret"`
	// RedirectURL is the full callback URL; defaults to <BaseURL>/api/auth/oidc/callback.
	RedirectURL string   `yaml:"redirect_url" json:"redirect_url"`
	Scopes      []string `yaml:"scopes" json:"scopes"`
	// UsernameClaim is the ID-token claim used as the display username.
	UsernameClaim string `yaml:"username_claim" json:"username_claim"`
	// DefaultRole is assigned to OIDC users: "admin" or "viewer".
	DefaultRole string `yaml:"default_role" json:"default_role"`
}

// Default returns a Config populated with safe defaults.
func Default() *Config {
	return &Config{
		Mode:     ModeServer,
		Host:     "0.0.0.0",
		Port:     8080,
		DataDir:  "/data",
		LogLevel: "info",
		Docker: DockerConfig{
			Host:           "unix:///var/run/docker.sock",
			EnableRecreate: true,
		},
		Agent: AgentConfig{
			Interval: 30 * time.Second,
		},
		Updates: UpdatesConfig{
			Interval:       6 * time.Hour,
			ChangelogCount: 5,
		},
		Auth: AuthConfig{
			Mode:       AuthLocal,
			SessionTTL: 24 * time.Hour,
			OIDC: OIDCConfig{
				Scopes:        []string{"openid", "profile", "email"},
				UsernameClaim: "preferred_username",
				DefaultRole:   "viewer",
			},
		},
	}
}

// Flags describes the command-line flags this binary accepts.
type Flags struct {
	ConfigPath string
	Mode       string
	Host       string
	Port       int
	DataDir    string
	Version    bool
}

// ParseFlags registers and parses command-line flags.
func ParseFlags(args []string) *Flags {
	f := &Flags{}
	fs := flag.NewFlagSet("whatsnewdock", flag.ContinueOnError)
	fs.StringVar(&f.ConfigPath, "config", "", "path to a YAML config file")
	fs.StringVar(&f.Mode, "mode", "", "operating mode: server|agent")
	fs.StringVar(&f.Host, "host", "", "listen address")
	fs.IntVar(&f.Port, "port", 0, "listen port")
	fs.StringVar(&f.DataDir, "data-dir", "", "data directory")
	fs.BoolVar(&f.Version, "version", false, "print version and exit")
	// Ignore unknown flags to keep the CLI forgiving of future additions.
	_ = fs.Parse(args)
	return f
}

// Load builds the final Config from defaults, file, environment and flags.
func Load(f *Flags) (*Config, error) {
	cfg := Default()

	if f != nil && f.ConfigPath != "" {
		if err := loadFile(cfg, f.ConfigPath); err != nil {
			return nil, err
		}
	} else if p := os.Getenv("WND_CONFIG"); p != "" {
		if err := loadFile(cfg, p); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)

	if f != nil {
		if f.Mode != "" {
			cfg.Mode = Mode(f.Mode)
		}
		if f.Host != "" {
			cfg.Host = f.Host
		}
		if f.Port != 0 {
			cfg.Port = f.Port
		}
		if f.DataDir != "" {
			cfg.DataDir = f.DataDir
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadFile(cfg *Config, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	return nil
}

// applyEnv overlays WND_* environment variables onto the config.
func applyEnv(cfg *Config) {
	setString := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	setInt := func(dst *int, key string) {
		if v, ok := os.LookupEnv(key); ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	setBool := func(dst *bool, key string) {
		if v, ok := os.LookupEnv(key); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	setDur := func(dst *time.Duration, key string) {
		if v, ok := os.LookupEnv(key); ok {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}

	setString((*string)(&cfg.Mode), "WND_MODE")
	setString(&cfg.Host, "WND_HOST")
	setInt(&cfg.Port, "WND_PORT")
	setString(&cfg.BaseURL, "WND_BASE_URL")
	setString(&cfg.DataDir, "WND_DATA_DIR")
	setString(&cfg.LogLevel, "WND_LOG_LEVEL")
	setString(&cfg.TrustedProxies, "WND_TRUSTED_PROXIES")

	setString(&cfg.Docker.Host, "WND_DOCKER_HOST")
	setString(&cfg.Docker.APIVersion, "WND_DOCKER_API_VERSION")
	setBool(&cfg.Docker.TLS, "WND_DOCKER_TLS")
	setString(&cfg.Docker.TLSCA, "WND_DOCKER_TLS_CA")
	setString(&cfg.Docker.TLSCert, "WND_DOCKER_TLS_CERT")
	setString(&cfg.Docker.TLSKey, "WND_DOCKER_TLS_KEY")
	setBool(&cfg.Docker.EnableRecreate, "WND_DOCKER_ENABLE_RECREATE")

	setString(&cfg.Agent.ServerURL, "WND_AGENT_SERVER_URL")
	setString(&cfg.Agent.Token, "WND_AGENT_TOKEN")
	setString(&cfg.Agent.Name, "WND_AGENT_NAME")
	setDur(&cfg.Agent.Interval, "WND_AGENT_INTERVAL")
	setBool(&cfg.Agent.TLSSkipVerify, "WND_AGENT_TLS_SKIP_VERIFY")

	setDur(&cfg.Updates.Interval, "WND_UPDATE_INTERVAL")
	setInt(&cfg.Updates.ChangelogCount, "WND_CHANGELOG_COUNT")
	setString(&cfg.Updates.GitHubToken, "WND_GITHUB_TOKEN")
	setString(&cfg.Updates.GitLabToken, "WND_GITLAB_TOKEN")
	setBool(&cfg.Updates.IncludePreReleases, "WND_INCLUDE_PRERELEASES")
	if v := os.Getenv("WND_IGNORE_IMAGES"); v != "" {
		cfg.Updates.IgnoreImages = splitList(v)
	}

	setString((*string)(&cfg.Auth.Mode), "WND_AUTH_MODE")
	setString(&cfg.Auth.SessionSecret, "WND_AUTH_SESSION_SECRET")
	setDur(&cfg.Auth.SessionTTL, "WND_AUTH_SESSION_TTL")
	setString(&cfg.Auth.OIDC.IssuerURL, "WND_OIDC_ISSUER")
	setString(&cfg.Auth.OIDC.ClientID, "WND_OIDC_CLIENT_ID")
	setString(&cfg.Auth.OIDC.ClientSecret, "WND_OIDC_CLIENT_SECRET")
	setString(&cfg.Auth.OIDC.RedirectURL, "WND_OIDC_REDIRECT_URL")
	if v := os.Getenv("WND_OIDC_SCOPES"); v != "" {
		cfg.Auth.OIDC.Scopes = splitList(v)
	}
	setString(&cfg.Auth.OIDC.UsernameClaim, "WND_OIDC_USERNAME_CLAIM")
	setString(&cfg.Auth.OIDC.DefaultRole, "WND_OIDC_DEFAULT_ROLE")

	setString(&cfg.InitialAdminUser, "WND_INITIAL_ADMIN_USER")
	setString(&cfg.InitialAdminPassword, "WND_INITIAL_ADMIN_PASSWORD")
}

func splitList(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate checks the configuration for consistency.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeServer, ModeAgent:
	default:
		return fmt.Errorf("invalid mode %q (want server or agent)", c.Mode)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d", c.Port)
	}
	if c.Mode == ModeAgent && c.Agent.ServerURL == "" {
		return errors.New("agent mode requires WND_AGENT_SERVER_URL")
	}
	if c.Mode == ModeAgent && c.Agent.Token == "" {
		return errors.New("agent mode requires WND_AGENT_TOKEN")
	}
	switch c.Auth.Mode {
	case AuthLocal, AuthOIDC, AuthBoth:
	default:
		return fmt.Errorf("invalid auth mode %q (want local, oidc or both)", c.Auth.Mode)
	}
	if (c.Auth.Mode == AuthOIDC || c.Auth.Mode == AuthBoth) && c.Auth.OIDC.IssuerURL == "" {
		return errors.New("OIDC auth requires WND_OIDC_ISSUER")
	}
	if c.Updates.ChangelogCount < 1 {
		return errors.New("changelog count must be >= 1")
	}
	if c.Updates.ChangelogCount > 100 {
		return errors.New("changelog count must be <= 100")
	}
	if c.BaseURL != "" {
		if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
			return errors.New("base_url must start with http:// or https://")
		}
	}
	return nil
}

// Address returns the host:port listen address.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

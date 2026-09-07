// Package store provides the SQLite-backed persistence layer for WhatsNewDock.
package store

import "time"

// Server represents a monitored Docker host (local or remote agent).
type Server struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	IsLocal        bool      `json:"is_local"`
	Status         string    `json:"status"` // "online" | "offline"
	LastSeen       time.Time `json:"last_seen"`
	DockerVersion  string    `json:"docker_version"`
	OS             string    `json:"os"`
	Arch           string    `json:"arch"`
	CPUs           int       `json:"cpus"`
	MemoryBytes    int64     `json:"memory_bytes"`
	Labels         []string  `json:"labels"`
	AgentTokenHash string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// StackKind discriminates between Docker Compose projects and Swarm stacks.
type StackKind string

const (
	StackCompose StackKind = "compose"
	StackSwarm   StackKind = "swarm"
)

// Stack groups containers that belong to one compose project or swarm stack.
type Stack struct {
	ID       string    `json:"id"`
	ServerID string    `json:"server_id"`
	Name     string    `json:"name"`
	Kind     StackKind `json:"kind"`
	Count    int       `json:"count"`
}

// Container mirrors the relevant state of a Docker container.
type Container struct {
	ID             string            `json:"id"`
	ServerID       string            `json:"server_id"`
	StackID        string            `json:"stack_id,omitempty"`
	StackName      string            `json:"stack_name,omitempty"`
	DockerID       string            `json:"docker_id"`
	Name           string            `json:"name"`
	Image          string            `json:"image"`      // full reference as configured
	ImageName      string            `json:"image_name"` // registry/repository (no tag)
	ImageTag       string            `json:"image_tag"`
	ImageDigest    string            `json:"image_digest"`
	Registry       string            `json:"registry"`
	Repository     string            `json:"repository"`
	State          string            `json:"state"` // running | exited | created | ...
	Status         string            `json:"status"`
	Running        bool              `json:"running"`
	RestartPolicy  string            `json:"restart_policy"`
	ComposeService string            `json:"compose_service"`
	CreatedAt      time.Time         `json:"created_at"`
	StartedAt      time.Time         `json:"started_at"`
	Labels         map[string]string `json:"labels"`
	Ports          []string          `json:"ports"`
	Pinned         bool              `json:"pinned"` // do not offer updates
	UpdatedAt      time.Time         `json:"updated_at"`
}

// Update describes an available update for a container.
type Update struct {
	ID             string    `json:"id"`
	ContainerID    string    `json:"container_id"`
	RepoKey        string    `json:"repo_key"`
	CurrentTag     string    `json:"current_tag"`
	LatestTag      string    `json:"latest_tag"`
	VersionsBehind int       `json:"versions_behind"`
	Source         string    `json:"source"` // "github" | "gitlab" | "gitea" | "registry" | "none"
	SourceURL      string    `json:"source_url"`
	CheckedAt      time.Time `json:"checked_at"`
}

// Release is a single changelog entry (a release/tag).
type Release struct {
	ID          string    `json:"id"`
	RepoKey     string    `json:"repo_key"`
	Tag         string    `json:"tag"`
	Title       string    `json:"title"`
	Body        string    `json:"body"` // markdown release notes
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
}

// User is a local account.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Role         string    `json:"role"` // "admin" | "viewer"
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Event is an audit-log entry.
type Event struct {
	ID          int64     `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	Kind        string    `json:"kind"` // "update_requested" | "update_done" | "auth" | ...
	Actor       string    `json:"actor"`
	ServerID    string    `json:"server_id,omitempty"`
	ContainerID string    `json:"container_id,omitempty"`
	Message     string    `json:"message"`
}

// Command is a queued action for a remote agent (e.g. update a container).
type Command struct {
	ID          string    `json:"id"`
	ServerID    string    `json:"server_id"`
	Kind        string    `json:"kind"`         // "update"
	ContainerID string    `json:"container_id"` // docker container id on the agent host
	TargetImage string    `json:"target_image"`
	Status      string    `json:"status"` // pending | done | failed
	Result      string    `json:"result"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

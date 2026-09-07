package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// Store wraps the SQLite database and exposes typed query methods.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes, avoid SQLITE_BUSY
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for advanced/tx use.
func (s *Store) DB() *sql.DB { return s.db }

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY);

CREATE TABLE IF NOT EXISTS servers (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	is_local       INTEGER NOT NULL DEFAULT 0,
	status         TEXT NOT NULL DEFAULT 'offline',
	last_seen      TEXT,
	docker_version TEXT,
	os             TEXT,
	arch           TEXT,
	cpus           INTEGER NOT NULL DEFAULT 0,
	memory_bytes   INTEGER NOT NULL DEFAULT 0,
	labels         TEXT NOT NULL DEFAULT '[]',
	agent_token_hash TEXT,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS stacks (
	id        TEXT PRIMARY KEY,
	server_id TEXT NOT NULL,
	name      TEXT NOT NULL,
	kind      TEXT NOT NULL,
	UNIQUE(server_id, name)
);

CREATE TABLE IF NOT EXISTS containers (
	id              TEXT PRIMARY KEY,
	server_id       TEXT NOT NULL,
	stack_id        TEXT,
	docker_id       TEXT NOT NULL,
	name            TEXT NOT NULL,
	image           TEXT NOT NULL,
	image_name      TEXT NOT NULL,
	image_tag       TEXT NOT NULL,
	image_digest    TEXT,
	registry        TEXT NOT NULL,
	repository      TEXT NOT NULL,
	state           TEXT NOT NULL,
	status          TEXT,
	running         INTEGER NOT NULL DEFAULT 0,
	restart_policy  TEXT,
	compose_service TEXT,
	created_at      TEXT,
	started_at      TEXT,
	labels          TEXT NOT NULL DEFAULT '{}',
	ports           TEXT NOT NULL DEFAULT '[]',
	pinned          INTEGER NOT NULL DEFAULT 0,
	updated_at      TEXT NOT NULL,
	UNIQUE(server_id, docker_id)
);

CREATE TABLE IF NOT EXISTS updates (
	id              TEXT PRIMARY KEY,
	container_id    TEXT NOT NULL UNIQUE,
	repo_key        TEXT,
	current_tag     TEXT,
	latest_tag      TEXT,
	versions_behind INTEGER NOT NULL DEFAULT 0,
	source          TEXT NOT NULL DEFAULT 'none',
	source_url      TEXT,
	checked_at      TEXT,
	FOREIGN KEY(container_id) REFERENCES containers(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS releases (
	id           TEXT PRIMARY KEY,
	repo_key     TEXT NOT NULL,
	tag          TEXT NOT NULL,
	title        TEXT,
	body         TEXT,
	url          TEXT,
	published_at TEXT,
	prerelease   INTEGER NOT NULL DEFAULT 0,
	UNIQUE(repo_key, tag)
);

CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	role          TEXT NOT NULL DEFAULT 'viewer',
	password_hash TEXT NOT NULL,
	created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp    TEXT NOT NULL,
	kind         TEXT NOT NULL,
	actor        TEXT NOT NULL,
	server_id    TEXT,
	container_id TEXT,
	message      TEXT
);

CREATE TABLE IF NOT EXISTS commands (
	id           TEXT PRIMARY KEY,
	server_id    TEXT NOT NULL,
	kind         TEXT NOT NULL,
	container_id TEXT NOT NULL,
	target_image TEXT,
	status       TEXT NOT NULL DEFAULT 'pending',
	result       TEXT,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_containers_server ON containers(server_id);
CREATE INDEX IF NOT EXISTS idx_containers_stack  ON containers(stack_id);
CREATE INDEX IF NOT EXISTS idx_updates_container ON updates(container_id);
CREATE INDEX IF NOT EXISTS idx_releases_repokey  ON releases(repo_key);
CREATE INDEX IF NOT EXISTS idx_stacks_server     ON stacks(server_id);
CREATE INDEX IF NOT EXISTS idx_commands_server   ON commands(server_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// --- time helpers -------------------------------------------------------

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, v)
	return t
}

// --- settings ------------------------------------------------------------

// GetSetting returns a stored setting value, or "" when absent.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting upserts a setting key/value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

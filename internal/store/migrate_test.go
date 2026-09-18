package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// legacySchema is the pre-agentless-endpoint schema: it has none of the
// columns added by migrations 1-3, and it already contains the (unused)
// schema_migrations table that older releases shipped.
const legacySchema = `
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY);

CREATE TABLE servers (
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

CREATE TABLE stacks (
	id        TEXT PRIMARY KEY,
	server_id TEXT NOT NULL,
	name      TEXT NOT NULL,
	kind      TEXT NOT NULL,
	UNIQUE(server_id, name)
);

CREATE TABLE containers (
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

CREATE TABLE updates (
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

CREATE TABLE users (
	id            TEXT PRIMARY KEY,
	username      TEXT NOT NULL UNIQUE,
	role          TEXT NOT NULL DEFAULT 'viewer',
	password_hash TEXT NOT NULL,
	created_at    TEXT NOT NULL
);

CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE events (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp    TEXT NOT NULL,
	kind         TEXT NOT NULL,
	actor        TEXT NOT NULL,
	server_id    TEXT,
	container_id TEXT,
	message      TEXT
);

CREATE TABLE commands (
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
`

// seedLegacy writes a database in the old shape with a local server, an agent
// server, a stack and a container, so we can prove the upgrade preserves data.
func seedLegacy(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	now := ts(time.Now().UTC())
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO servers(id, name, is_local, status, labels, agent_token_hash, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, []any{"local-1", "dockerhost", 1, "online", "[]", nil, now, now}},
		{`INSERT INTO servers(id, name, is_local, status, labels, agent_token_hash, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, []any{"agent-1", "edge", 0, "online", "[]", "hash", now, now}},
		{`INSERT INTO stacks(id, server_id, name, kind) VALUES(?, ?, ?, ?)`, []any{"stk-1", "agent-1", "web", "compose"}},
		{`INSERT INTO containers(id, server_id, stack_id, docker_id, name, image, image_name, image_tag,
			registry, repository, state, running, labels, ports, pinned, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{"ctr-1", "agent-1", "stk-1", "deadbeef", "nginx", "nginx:1.25", "docker.io/library/nginx",
				"1.25", "docker.io", "library/nginx", "running", 1, "{}", "[]", 0, now}},
		{`INSERT INTO updates(id, container_id, repo_key, current_tag, latest_tag, versions_behind, source, checked_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, []any{"upd-1", "ctr-1", "gh:nginx/nginx", "1.25", "1.28", 3, "github", now}},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
	}
}

// TestMigrationUpgradesLegacyDatabase is the important one: an existing
// deployment must start up unchanged after the upgrade, with every row intact.
func TestMigrationUpgradesLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacy(t, path)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer st.Close()

	servers, err := st.ListServers()
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
	byID := map[string]Server{}
	for _, s := range servers {
		byID[s.ID] = s
	}
	// The pre-existing local row is reclassified, and remains the local one.
	if got := byID["local-1"]; !got.IsLocal || got.Kind != ServerLocal {
		t.Errorf("local server = local:%v kind:%q", got.IsLocal, got.Kind)
	}
	// Everything else stays an agent with its token hash untouched.
	if got := byID["agent-1"]; got.IsLocal || got.Kind != ServerAgent {
		t.Errorf("agent server = local:%v kind:%q", got.IsLocal, got.Kind)
	}
	if got, err := st.GetServerByTokenHash("hash"); err != nil || got == nil || got.ID != "agent-1" {
		t.Errorf("agent token hash lost: %+v, %v", got, err)
	}

	containers, err := st.ListContainers(ContainerFilter{})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0]
	if c.Name != "nginx" || c.StackName != "web" {
		t.Errorf("container data lost: %+v", c)
	}
	if c.Update == nil || c.Update.LatestTag != "1.28" {
		t.Errorf("update data lost: %+v", c.Update)
	}
	// Migrated containers are not systemd-managed.
	if c.Managed || c.SystemdUnit != "" {
		t.Errorf("migrated container should not be managed: %q %v", c.SystemdUnit, c.Managed)
	}
}

// TestMigrationIsIdempotent re-opens the same database repeatedly: a second run
// must be a no-op rather than an error, since startup depends on it.
func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent.db")
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		versions := map[int]int{}
		rows, err := st.db.Query(`SELECT version FROM schema_migrations`)
		if err != nil {
			t.Fatalf("read migrations: %v", err)
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				t.Fatalf("scan version: %v", err)
			}
			versions[v]++
		}
		rows.Close()
		for _, want := range []int{1, 2, 3} {
			if versions[want] != 1 {
				t.Errorf("open #%d: migration %d recorded %d times, want 1", i, want, versions[want])
			}
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close #%d: %v", i, err)
		}
	}
}

// TestMigrationToleratesPartialUpgrade covers a database where one of the new
// columns already exists (e.g. a hand-edited schema). The guarded ALTER must
// still succeed.
func TestMigrationToleratesPartialUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	seedLegacy(t, path)

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE servers ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent'`); err != nil {
		t.Fatalf("pre-add column: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE containers ADD COLUMN managed INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("pre-add column: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open partially-upgraded database: %v", err)
	}
	defer st.Close()

	srv, err := st.GetServer("local-1")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if srv.Kind != ServerLocal {
		t.Errorf("kind = %q, want local", srv.Kind)
	}
}

// TestDirectServerRoundTrip checks that endpoint settings survive storage and
// that TLS material is never needed to be inline PEM.
func TestDirectServerRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "direct.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	srv := &Server{
		ID: "direct-1", Name: "edge", Kind: ServerDirect, Status: "offline",
		DockerHost: "tcp://10.0.0.5:2376", TLSCA: "/tls/ca.pem",
		TLSCert: "/tls/cert.pem", TLSKey: "/tls/key.pem", NameCustom: true,
	}
	if err := st.CreateServer(srv); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetServer("direct-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != ServerDirect || !got.TLSEnabled() {
		t.Errorf("kind=%q tls=%v", got.Kind, got.TLSEnabled())
	}
	if got.DockerHost != "tcp://10.0.0.5:2376" || got.TLSCA != "/tls/ca.pem" {
		t.Errorf("endpoint not persisted: %+v", got)
	}

	list, err := st.ListServersByKind(ServerDirect)
	if err != nil || len(list) != 1 || list[0].ID != "direct-1" {
		t.Fatalf("ListServersByKind = %+v, %v", list, err)
	}

	// Rewriting the endpoint updates it in place.
	if err := st.UpdateServerEndpoint("direct-1", "tcp://10.0.0.6:2376", "", "", ""); err != nil {
		t.Fatalf("update endpoint: %v", err)
	}
	got2, _ := st.GetServer("direct-1")
	if got2.DockerHost != "tcp://10.0.0.6:2376" || got2.TLSEnabled() {
		t.Errorf("endpoint not updated: %+v", got2)
	}
}

// TestAgentReportCannotReclassifyDirectEndpoint guards the invariant that a
// direct endpoint keeps its kind even if something reports for its id.
func TestAgentReportCannotReclassifyDirectEndpoint(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "kinds.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if err := st.CreateServer(&Server{
		ID: "direct-1", Name: "edge", Kind: ServerDirect, Status: "offline",
		DockerHost: "tcp://10.0.0.5:2376", NameCustom: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate an agent-style report arriving for the same row.
	report := &Server{ID: "direct-1", Name: "hostname-from-daemon", Kind: ServerAgent, Status: "online"}
	if err := st.ReplaceServerSnapshot(report, nil, nil); err != nil {
		t.Fatalf("replace snapshot: %v", err)
	}
	got, _ := st.GetServer("direct-1")
	if got.Kind != ServerDirect {
		t.Errorf("kind was reclassified to %q", got.Kind)
	}
	if got.DockerHost != "tcp://10.0.0.5:2376" {
		t.Errorf("endpoint lost: %q", got.DockerHost)
	}
}

// TestOperatorChosenNameSurvivesSnapshots covers the rename regression: a
// daemon-reported hostname must not overwrite a name the operator set.
func TestOperatorChosenNameSurvivesSnapshots(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "names.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	srv := &Server{ID: "s1", Name: "local", IsLocal: true, Status: "online"}
	if err := st.CreateServer(srv); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Before a rename, reports may set the name (unchanged behaviour).
	if err := st.ReplaceServerSnapshot(&Server{ID: "s1", Name: "dockerhost", IsLocal: true}, nil, nil); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got, _ := st.GetServer("s1"); got.Name != "dockerhost" {
		t.Fatalf("name = %q, want dockerhost", got.Name)
	}

	if err := st.UpdateServerName("s1", "Prod Swarm"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := st.ReplaceServerSnapshot(&Server{ID: "s1", Name: "dockerhost", IsLocal: true}, nil, nil); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got, _ := st.GetServer("s1")
	if got.Name != "Prod Swarm" || !got.NameCustom {
		t.Errorf("renamed server reverted: name=%q custom=%v", got.Name, got.NameCustom)
	}
}

// TestSystemdManagedContainersPersist checks the Podman/Quadlet columns.
func TestSystemdManagedContainersPersist(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "quadlet.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	srv := &Server{ID: "s1", Name: "podman-host", IsLocal: true, Status: "online"}
	if err := st.CreateServer(srv); err != nil {
		t.Fatalf("create: %v", err)
	}
	containers := []*Container{
		{DockerID: "c1", Name: "web", Image: "nginx:1", ImageName: "docker.io/library/nginx",
			ImageTag: "1", Registry: "docker.io", Repository: "library/nginx", State: "running",
			Running: true, SystemdUnit: "web.service", Managed: true},
		{DockerID: "c2", Name: "plain", Image: "nginx:1", ImageName: "docker.io/library/nginx",
			ImageTag: "1", Registry: "docker.io", Repository: "library/nginx", State: "running", Running: true},
	}
	if err := st.ReplaceServerSnapshot(srv, nil, containers); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got, err := st.ListContainers(ContainerFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := map[string]Container{}
	for _, c := range got {
		byName[c.Name] = c.Container
	}
	if !byName["web"].Managed || byName["web"].SystemdUnit != "web.service" {
		t.Errorf("managed container lost its unit: %+v", byName["web"])
	}
	if byName["plain"].Managed || byName["plain"].SystemdUnit != "" {
		t.Errorf("plain container wrongly marked managed: %+v", byName["plain"])
	}
}

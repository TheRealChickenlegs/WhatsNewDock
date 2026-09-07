package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func newID() string { return uuid.NewString() }

// ContainerWithUpdate is a Container enriched with its server/stack names and
// an optional available update.
type ContainerWithUpdate struct {
	Container
	ServerName string  `json:"server_name"`
	Update     *Update `json:"update,omitempty"`
}

// ContainerFilter narrows a container listing.
type ContainerFilter struct {
	ServerID   string
	StackID    string
	Registry   string
	Repository string
	State      string // "" | "running" | "stopped"
	HasUpdate  bool
	Search     string
	Limit      int
}

// UpdateFilter narrows an update listing.
type UpdateFilter struct {
	ServerID string
	StackID  string
	Search   string
}

// ---------------------------------------------------------------------------
// Servers
// ---------------------------------------------------------------------------

func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(`SELECT id, name, is_local, status, last_seen, docker_version,
		os, arch, cpus, memory_bytes, labels, created_at, updated_at FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Server
	for rows.Next() {
		var v Server
		var lastSeen sql.NullString
		var dockerVer, osName, archName, labelsRaw sql.NullString
		var createdStr, updatedStr string
		if err := rows.Scan(&v.ID, &v.Name, &v.IsLocal, &v.Status, &lastSeen, &dockerVer,
			&osName, &archName, &v.CPUs, &v.MemoryBytes, &labelsRaw, &createdStr, &updatedStr); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			v.LastSeen = parseTS(lastSeen.String)
		}
		v.DockerVersion = dockerVer.String
		v.OS = osName.String
		v.Arch = archName.String
		v.Labels = unmarshalList(labelsRaw.String)
		v.CreatedAt = parseTS(createdStr)
		v.UpdatedAt = parseTS(updatedStr)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(id string) (*Server, error) {
	rows, err := s.db.Query(`SELECT id, name, is_local, status, last_seen, docker_version,
		os, arch, cpus, memory_bytes, labels, created_at, updated_at FROM servers WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var v Server
	var lastSeen, dockerVer, osName, archName, labelsRaw sql.NullString
	var createdStr, updatedStr string
	if err := rows.Scan(&v.ID, &v.Name, &v.IsLocal, &v.Status, &lastSeen, &dockerVer,
		&osName, &archName, &v.CPUs, &v.MemoryBytes, &labelsRaw, &createdStr, &updatedStr); err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		v.LastSeen = parseTS(lastSeen.String)
	}
	v.DockerVersion = dockerVer.String
	v.OS = osName.String
	v.Arch = archName.String
	v.Labels = unmarshalList(labelsRaw.String)
	v.CreatedAt = parseTS(createdStr)
	v.UpdatedAt = parseTS(updatedStr)
	return &v, nil
}

func (s *Store) DeleteServer(id string) error {
	_, err := s.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------------------
// Stacks
// ---------------------------------------------------------------------------

func (s *Store) ListStacks(serverID string) ([]Stack, error) {
	q := `SELECT st.id, st.server_id, st.name, st.kind,
		(SELECT COUNT(*) FROM containers c WHERE c.stack_id = st.id) AS cnt
		FROM stacks st`
	args := []any{}
	if serverID != "" {
		q += ` WHERE st.server_id = ?`
		args = append(args, serverID)
	}
	q += ` ORDER BY st.name`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stack
	for rows.Next() {
		var v Stack
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Name, &v.Kind, &v.Count); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Containers
// ---------------------------------------------------------------------------

func (s *Store) ListContainers(f ContainerFilter) ([]ContainerWithUpdate, error) {
	where, args := buildContainerWhere(f)
	q := `
	SELECT c.id, c.server_id, c.stack_id, c.docker_id, c.name, c.image, c.image_name, c.image_tag,
		c.image_digest, c.registry, c.repository, c.state, c.status, c.running, c.restart_policy,
		c.compose_service, c.created_at, c.started_at, c.labels, c.ports, c.pinned, c.updated_at,
		srv.name AS server_name, st.name AS stack_name,
		u.id, u.repo_key, u.current_tag, u.latest_tag, u.versions_behind, u.source, u.source_url, u.checked_at
	FROM containers c
	JOIN servers srv ON srv.id = c.server_id
	LEFT JOIN stacks st ON st.id = c.stack_id
	LEFT JOIN updates u ON u.container_id = c.id
	` + where + `
	ORDER BY srv.name, st.name, c.name`
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return s.scanContainers(q, args...)
}

func (s *Store) GetContainer(id string) (*ContainerWithUpdate, error) {
	q := `
	SELECT c.id, c.server_id, c.stack_id, c.docker_id, c.name, c.image, c.image_name, c.image_tag,
		c.image_digest, c.registry, c.repository, c.state, c.status, c.running, c.restart_policy,
		c.compose_service, c.created_at, c.started_at, c.labels, c.ports, c.pinned, c.updated_at,
		srv.name AS server_name, st.name AS stack_name,
		u.id, u.repo_key, u.current_tag, u.latest_tag, u.versions_behind, u.source, u.source_url, u.checked_at
	FROM containers c
	JOIN servers srv ON srv.id = c.server_id
	LEFT JOIN stacks st ON st.id = c.stack_id
	LEFT JOIN updates u ON u.container_id = c.id
	WHERE c.id = ?`
	items, err := s.scanContainers(q, id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) SetContainerPinned(id string, pinned bool) error {
	_, err := s.db.Exec(`UPDATE containers SET pinned = ? WHERE id = ?`, boolInt(pinned), id)
	return err
}

func buildContainerWhere(f ContainerFilter) (string, []any) {
	var conds []string
	var args []any
	add := func(c string, v any) {
		conds = append(conds, c)
		args = append(args, v)
	}
	if f.ServerID != "" {
		add("c.server_id = ?", f.ServerID)
	}
	if f.StackID != "" {
		add("c.stack_id = ?", f.StackID)
	}
	if f.Registry != "" {
		add("c.registry = ?", f.Registry)
	}
	if f.Repository != "" {
		add("c.repository = ?", f.Repository)
	}
	switch f.State {
	case "running":
		conds = append(conds, "c.running = 1")
	case "stopped":
		conds = append(conds, "c.running = 0")
	}
	if f.HasUpdate {
		conds = append(conds, "u.id IS NOT NULL")
	}
	if f.Search != "" {
		like := "%" + f.Search + "%"
		conds = append(conds, "(c.name LIKE ? OR c.image LIKE ? OR srv.name LIKE ?)")
		args = append(args, like, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// scanContainers maps rows from the shared container+update query.
func (s *Store) scanContainers(q string, args ...any) ([]ContainerWithUpdate, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContainerWithUpdate
	for rows.Next() {
		var c ContainerWithUpdate
		var stackID, imageDigest, status, restartPolicy, composeService, createdRaw, startedRaw, stackName sql.NullString
		var uID, uRepoKey, uCur, uLatest, uSource, uSourceURL, uChecked sql.NullString
		var uBehind sql.NullInt64
		var labelsRaw, portsRaw string
		var updatedRaw string

		if err := rows.Scan(
			&c.ID, &c.ServerID, &stackID, &c.DockerID, &c.Name, &c.Image, &c.ImageName, &c.ImageTag,
			&imageDigest, &c.Registry, &c.Repository, &c.State, &status, &c.Running, &restartPolicy,
			&composeService, &createdRaw, &startedRaw, &labelsRaw, &portsRaw, &c.Pinned, &updatedRaw,
			&c.ServerName, &stackName,
			&uID, &uRepoKey, &uCur, &uLatest, &uBehind, &uSource, &uSourceURL, &uChecked,
		); err != nil {
			return nil, err
		}
		c.StackID = stackID.String
		c.StackName = stackName.String
		c.ImageDigest = imageDigest.String
		c.Status = status.String
		c.RestartPolicy = restartPolicy.String
		c.ComposeService = composeService.String
		c.UpdatedAt = parseTS(updatedRaw)
		if createdRaw.Valid {
			c.CreatedAt = parseTS(createdRaw.String)
		}
		if startedRaw.Valid {
			c.StartedAt = parseTS(startedRaw.String)
		}
		c.Labels = unmarshalMap(labelsRaw)
		c.Ports = unmarshalList(portsRaw)

		if uID.Valid {
			c.Update = &Update{
				ID:             uID.String,
				ContainerID:    c.ID,
				RepoKey:        uRepoKey.String,
				CurrentTag:     uCur.String,
				LatestTag:      uLatest.String,
				VersionsBehind: int(uBehind.Int64),
				Source:         uSource.String,
				SourceURL:      uSourceURL.String,
				CheckedAt:      parseTS(uChecked.String),
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Updates
// ---------------------------------------------------------------------------

// UpsertUpdate inserts or replaces an available-update record.
func (s *Store) UpsertUpdate(u *Update) error {
	if u.ID == "" {
		u.ID = newID()
	}
	_, err := s.db.Exec(`
		INSERT INTO updates(id, container_id, repo_key, current_tag, latest_tag, versions_behind, source, source_url, checked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(container_id) DO UPDATE SET
			repo_key = excluded.repo_key, current_tag = excluded.current_tag,
			latest_tag = excluded.latest_tag, versions_behind = excluded.versions_behind,
			source = excluded.source, source_url = excluded.source_url, checked_at = excluded.checked_at`,
		u.ID, u.ContainerID, u.RepoKey, u.CurrentTag, u.LatestTag, u.VersionsBehind, u.Source, u.SourceURL, ts(u.CheckedAt))
	return err
}

// DeleteUpdate removes an update record for a container (e.g. when pinned).
func (s *Store) DeleteUpdate(containerID string) error {
	_, err := s.db.Exec(`DELETE FROM updates WHERE container_id = ?`, containerID)
	return err
}

func (s *Store) ListUpdates(f UpdateFilter) ([]ContainerWithUpdate, error) {
	cf := ContainerFilter{ServerID: f.ServerID, StackID: f.StackID, Search: f.Search, HasUpdate: true}
	return s.ListContainers(cf)
}

// ---------------------------------------------------------------------------
// Releases (changelog entries)
// ---------------------------------------------------------------------------

func (s *Store) UpsertReleases(repoKey string, releases []Release) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM releases WHERE repo_key = ?`, repoKey); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO releases(id, repo_key, tag, title, body, url, published_at, prerelease)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range releases {
		if r.ID == "" {
			r.ID = newID()
		}
		if _, err := stmt.Exec(r.ID, repoKey, r.Tag, r.Title, r.Body, r.URL, ts(r.PublishedAt), boolInt(r.Prerelease)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListReleases returns releases for a repo key, newest first. A limit <= 0
// returns all stored releases.
func (s *Store) ListReleases(repoKey string, limit int) ([]Release, error) {
	q := `SELECT id, repo_key, tag, title, body, url, published_at, prerelease
		FROM releases WHERE repo_key = ? ORDER BY published_at DESC, tag DESC`
	args := []any{repoKey}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		var title, body, url, published sql.NullString
		if err := rows.Scan(&r.ID, &r.RepoKey, &r.Tag, &title, &body, &url, &published, &r.Prerelease); err != nil {
			return nil, err
		}
		r.Title = title.String
		r.Body = body.String
		r.URL = url.String
		if published.Valid {
			r.PublishedAt = parseTS(published.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *Store) CreateUser(u *User) error {
	if u.ID == "" {
		u.ID = newID()
	}
	_, err := s.db.Exec(`INSERT INTO users(id, username, role, password_hash, created_at) VALUES(?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.Role, u.PasswordHash, ts(timeNow()))
	return err
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	var u User
	var created string
	err := s.db.QueryRow(`SELECT id, username, role, password_hash, created_at FROM users WHERE username = ?`,
		username).Scan(&u.ID, &u.Username, &u.Role, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = parseTS(created)
	return &u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, role, password_hash, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created string
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.PasswordHash, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = parseTS(created)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUser(id string) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) UpdateUserRole(id, role string) error {
	_, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, role, id)
	return err
}

func (s *Store) UpdateUserPassword(id, hash string) error {
	_, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// CountUsers returns the number of local users (used to gate first-run setup).
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

func (s *Store) AddEvent(ev *Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = timeNow()
	}
	res, err := s.db.Exec(`INSERT INTO events(timestamp, kind, actor, server_id, container_id, message)
		VALUES(?, ?, ?, ?, ?, ?)`, ts(ev.Timestamp), ev.Kind, ev.Actor, nullable(ev.ServerID), nullable(ev.ContainerID), ev.Message)
	if err != nil {
		return err
	}
	ev.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, timestamp, kind, actor, server_id, container_id, message
		FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var serverID, containerID, msg sql.NullString
		var stamp string
		if err := rows.Scan(&ev.ID, &stamp, &ev.Kind, &ev.Actor, &serverID, &containerID, &msg); err != nil {
			return nil, err
		}
		ev.Timestamp = parseTS(stamp)
		ev.ServerID = serverID.String
		ev.ContainerID = containerID.String
		ev.Message = msg.String
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Snapshot replacement (agent report)
// ---------------------------------------------------------------------------

// ReplaceServerSnapshot atomically replaces a server's stacks and containers
// with a fresh report, preserving container pinned state and stable IDs.
func (s *Store) ReplaceServerSnapshot(srv *Server, stacks []*Stack, containers []*Container) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Upsert server.
	if _, err := tx.Exec(`
		INSERT INTO servers(id, name, is_local, status, last_seen, docker_version, os, arch, cpus, memory_bytes, labels, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, status = excluded.status, last_seen = excluded.last_seen,
			docker_version = excluded.docker_version, os = excluded.os, arch = excluded.arch,
			cpus = excluded.cpus, memory_bytes = excluded.memory_bytes, labels = excluded.labels,
			updated_at = excluded.updated_at`,
		srv.ID, srv.Name, boolInt(srv.IsLocal), srv.Status, ts(srv.LastSeen), srv.DockerVersion,
		srv.OS, srv.Arch, srv.CPUs, srv.MemoryBytes, marshalList(srv.Labels), ts(srv.CreatedAt), ts(srv.UpdatedAt),
	); err != nil {
		return err
	}

	// Resolve stable stack IDs by (server_id, name).
	stackIDByName := map[string]string{}
	{
		rows, err := tx.Query(`SELECT id, name FROM stacks WHERE server_id = ?`, srv.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			stackIDByName[name] = id
		}
		rows.Close()
	}

	seenStacks := map[string]bool{}
	for _, st := range stacks {
		if st.ID == "" {
			if existing, ok := stackIDByName[st.Name]; ok {
				st.ID = existing
			} else {
				st.ID = newID()
			}
		}
		seenStacks[st.ID] = true
		if _, err := tx.Exec(`
			INSERT INTO stacks(id, server_id, name, kind) VALUES(?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, kind = excluded.kind`,
			st.ID, srv.ID, st.Name, st.Kind); err != nil {
			return err
		}
	}
	// Delete stacks that disappeared.
	for name, id := range stackIDByName {
		if !seenStacks[id] {
			if _, err := tx.Exec(`DELETE FROM stacks WHERE id = ?`, id); err != nil {
				return err
			}
			_ = name
		}
	}

	// Final name -> id map (includes newly created stacks).
	finalStackID := make(map[string]string, len(stacks))
	for _, st := range stacks {
		if st.Name != "" {
			finalStackID[st.Name] = st.ID
		}
	}

	// Resolve stable container IDs + preserve pinned by (server_id, docker_id).
	type existingC struct {
		id     string
		pinned bool
	}
	existing := map[string]existingC{}
	{
		rows, err := tx.Query(`SELECT id, docker_id, pinned FROM containers WHERE server_id = ?`, srv.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e existingC
			var dockerID string
			if err := rows.Scan(&e.id, &dockerID, &e.pinned); err != nil {
				rows.Close()
				return err
			}
			existing[dockerID] = e
		}
		rows.Close()
	}

	seenContainers := map[string]bool{}
	for _, c := range containers {
		if prev, ok := existing[c.DockerID]; ok {
			c.ID = prev.id
			c.Pinned = c.Pinned || prev.pinned // preserve pinned
		} else if c.ID == "" {
			c.ID = newID()
		}
		// Resolve the stack id from the stack name when present.
		if c.StackID == "" && c.StackName != "" {
			c.StackID = finalStackID[c.StackName]
		}
		seenContainers[c.ID] = true
		if _, err := tx.Exec(`
			INSERT INTO containers(id, server_id, stack_id, docker_id, name, image, image_name, image_tag,
				image_digest, registry, repository, state, status, running, restart_policy, compose_service,
				created_at, started_at, labels, ports, pinned, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				stack_id = excluded.stack_id, name = excluded.name, image = excluded.image,
				image_name = excluded.image_name, image_tag = excluded.image_tag, image_digest = excluded.image_digest,
				registry = excluded.registry, repository = excluded.repository, state = excluded.state,
				status = excluded.status, running = excluded.running, restart_policy = excluded.restart_policy,
				compose_service = excluded.compose_service, created_at = excluded.created_at,
				started_at = excluded.started_at, labels = excluded.labels, ports = excluded.ports,
				updated_at = excluded.updated_at`,
			c.ID, srv.ID, nullable(c.StackID), c.DockerID, c.Name, c.Image, c.ImageName, c.ImageTag,
			nullable(c.ImageDigest), c.Registry, c.Repository, c.State, nullable(c.Status), boolInt(c.Running),
			nullable(c.RestartPolicy), nullable(c.ComposeService), nullableTime(c.CreatedAt), nullableTime(c.StartedAt),
			marshalMap(c.Labels), marshalList(c.Ports), boolInt(c.Pinned), ts(c.UpdatedAt),
		); err != nil {
			return err
		}
	}
	// Delete containers that disappeared (FK cascade removes their updates).
	for dockerID, e := range existing {
		if !seenContainers[e.id] {
			if _, err := tx.Exec(`DELETE FROM containers WHERE id = ?`, e.id); err != nil {
				return err
			}
			_ = dockerID
		}
	}

	return tx.Commit()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ts(t)
}

func timeNow() time.Time { return time.Now().UTC() }

func marshalMap(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func unmarshalMap(s string) map[string]string {
	if s == "" {
		return map[string]string{}
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(s), &m)
	if m == nil {
		m = map[string]string{}
	}
	return m
}

func marshalList(v []string) string {
	if v == nil {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalList(s string) []string {
	if s == "" {
		return []string{}
	}
	var v []string
	_ = json.Unmarshal([]byte(s), &v)
	if v == nil {
		v = []string{}
	}
	return v
}

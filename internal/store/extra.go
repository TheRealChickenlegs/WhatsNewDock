package store

import (
	"database/sql"
	"errors"
)

// GetServerByTokenHash looks up a remote server by its hashed agent token.
func (s *Store) GetServerByTokenHash(hash string) (*Server, error) {
	v, err := scanServer(s.db.QueryRow(serverSelect+` WHERE agent_token_hash = ?`, hash))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return v, nil
}

// ContainerIDByDockerID returns the WhatsNewDock container id for a given
// server and docker container id, or sql.ErrNoRows when not found.
func (s *Store) ContainerIDByDockerID(serverID, dockerID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM containers WHERE server_id = ? AND docker_id = ?`, serverID, dockerID).Scan(&id)
	return id, err
}

// CreateServer inserts a new server row (local, remote agent or direct
// endpoint). A direct endpoint's TLS material is stored as mounted file paths,
// never as inline PEM.
func (s *Store) CreateServer(srv *Server) error {
	if srv.ID == "" {
		srv.ID = newID()
	}
	if srv.Kind == "" {
		if srv.IsLocal {
			srv.Kind = ServerLocal
		} else {
			srv.Kind = ServerAgent
		}
	}
	now := ts(timeNow())
	if srv.CreatedAt.IsZero() {
		srv.CreatedAt = parseTS(now)
	}
	_, err := s.db.Exec(`INSERT INTO servers(id, name, is_local, kind, status, agent_token_hash, docker_host, tls_ca, tls_cert, tls_key, name_custom, labels, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.Name, boolInt(srv.IsLocal), string(srv.Kind), srv.Status,
		nullable(srv.AgentTokenHash), nullable(srv.DockerHost), nullable(srv.TLSCA), nullable(srv.TLSCert), nullable(srv.TLSKey),
		boolInt(srv.NameCustom), marshalList(srv.Labels), now, now)
	return err
}

// UpdateServerName renames a server and marks the name as operator-chosen, so
// later snapshot reports cannot overwrite it.
func (s *Store) UpdateServerName(id, name string) error {
	_, err := s.db.Exec(`UPDATE servers SET name = ?, name_custom = 1, updated_at = ? WHERE id = ?`, name, ts(timeNow()), id)
	return err
}

// UpdateServerEndpoint rewrites a direct endpoint's connection settings. It is
// only meaningful for servers of kind 'direct'.
func (s *Store) UpdateServerEndpoint(id, dockerHost, ca, cert, key string) error {
	_, err := s.db.Exec(`UPDATE servers SET docker_host = ?, tls_ca = ?, tls_cert = ?, tls_key = ?, updated_at = ? WHERE id = ?`,
		nullable(dockerHost), nullable(ca), nullable(cert), nullable(key), ts(timeNow()), id)
	return err
}

// ListServersByKind returns every server of the given kind, ordered by name.
func (s *Store) ListServersByKind(kind ServerKind) ([]Server, error) {
	rows, err := s.db.Query(serverSelect+` WHERE kind = ? ORDER BY name`, string(kind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// MarkServerOffline flips a server's status to offline.
func (s *Store) MarkServerOffline(id string) error {
	_, err := s.db.Exec(`UPDATE servers SET status = 'offline' WHERE id = ?`, id)
	return err
}

// DeleteServerCascade removes a server and all of its stacks, containers,
// updates and queued commands.
func (s *Store) DeleteServerCascade(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM commands WHERE server_id = ?`, id); err != nil {
		return err
	}
	// Removing containers cascades to their update rows.
	if _, err := tx.Exec(`DELETE FROM containers WHERE server_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM stacks WHERE server_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM servers WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkAllOffline flips every non-local server to offline (called on startup,
// since agents will report in again and set themselves online).
func (s *Store) MarkAllOffline() error {
	_, err := s.db.Exec(`UPDATE servers SET status = 'offline' WHERE is_local = 0`)
	return err
}

// ---------------------------------------------------------------------------
// Commands
// ---------------------------------------------------------------------------

// CreateCommand enqueues a command for a remote agent.
func (s *Store) CreateCommand(cmd *Command) error {
	if cmd.ID == "" {
		cmd.ID = newID()
	}
	now := ts(timeNow())
	_, err := s.db.Exec(`INSERT INTO commands(id, server_id, kind, container_id, target_image, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 'pending', ?, ?)`,
		cmd.ID, cmd.ServerID, cmd.Kind, cmd.ContainerID, nullable(cmd.TargetImage), now, now)
	return err
}

// ListPendingCommands returns commands awaiting execution by an agent.
func (s *Store) ListPendingCommands(serverID string) ([]Command, error) {
	rows, err := s.db.Query(`SELECT id, server_id, kind, container_id, target_image, status, result, created_at, updated_at
		FROM commands WHERE server_id = ? AND status = 'pending' ORDER BY created_at ASC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		var c Command
		var target, result sql.NullString
		var created, updated string
		if err := rows.Scan(&c.ID, &c.ServerID, &c.Kind, &c.ContainerID, &target, &c.Status, &result, &created, &updated); err != nil {
			return nil, err
		}
		c.TargetImage = target.String
		c.Result = result.String
		c.CreatedAt = parseTS(created)
		c.UpdatedAt = parseTS(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCommandResult records the outcome of a command execution.
func (s *Store) UpdateCommandResult(id, status, result string) error {
	_, err := s.db.Exec(`UPDATE commands SET status = ?, result = ?, updated_at = ? WHERE id = ?`,
		status, result, ts(timeNow()), id)
	return err
}

// GetCommand returns a single queued command, whatever its status, so a caller
// can follow one it queued (an agent self-update, for instance) to completion.
func (s *Store) GetCommand(id string) (*Command, error) {
	var c Command
	var target, result sql.NullString
	var created, updated string
	err := s.db.QueryRow(`SELECT id, server_id, kind, container_id, target_image, status, result, created_at, updated_at
		FROM commands WHERE id = ?`, id).
		Scan(&c.ID, &c.ServerID, &c.Kind, &c.ContainerID, &target, &c.Status, &result, &created, &updated)
	if err != nil {
		return nil, err
	}
	c.TargetImage = target.String
	c.Result = result.String
	c.CreatedAt = parseTS(created)
	c.UpdatedAt = parseTS(updated)
	return &c, nil
}

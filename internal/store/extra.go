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

// CreateServer inserts a new server row (local or remote agent).
func (s *Store) CreateServer(srv *Server) error {
	if srv.ID == "" {
		srv.ID = newID()
	}
	now := ts(timeNow())
	if srv.CreatedAt.IsZero() {
		srv.CreatedAt = parseTS(now)
	}
	_, err := s.db.Exec(`INSERT INTO servers(id, name, is_local, status, agent_token_hash, labels, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.ID, srv.Name, boolInt(srv.IsLocal), srv.Status, nullable(srv.AgentTokenHash), marshalList(srv.Labels), now, now)
	return err
}

// UpdateServerName renames a server.
func (s *Store) UpdateServerName(id, name string) error {
	_, err := s.db.Exec(`UPDATE servers SET name = ? WHERE id = ?`, name, id)
	return err
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

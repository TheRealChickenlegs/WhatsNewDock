package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/protocol"
	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

var errUnauthorized = errors.New("unauthorized agent")

// serverFromBearer authenticates an agent by its bearer token.
func (s *Server) serverFromBearer(r *http.Request) (*store.Server, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, errUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	if token == "" {
		return nil, errUnauthorized
	}
	srv, err := s.st.GetServerByTokenHash(auth.HashToken(token))
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, errUnauthorized
	}
	return srv, nil
}

func (s *Server) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromBearer(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var rep protocol.Report
	if err := readJSON(r, &rep); err != nil {
		writeError(w, http.StatusBadRequest, "invalid report")
		return
	}

	now := time.Now().UTC()
	info := rep.Info
	info.ID = srv.ID
	info.IsLocal = false
	info.Status = "online"
	info.LastSeen = now
	if info.Name == "" {
		info.Name = srv.Name
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = now
	}
	info.UpdatedAt = now

	stacks := make([]*store.Stack, 0, len(rep.Stacks))
	for i := range rep.Stacks {
		st := rep.Stacks[i]
		stacks = append(stacks, &st)
	}
	containers := make([]*store.Container, 0, len(rep.Containers))
	for i := range rep.Containers {
		c := rep.Containers[i]
		c.ServerID = srv.ID
		c.UpdatedAt = now
		containers = append(containers, &c)
	}

	if err := s.st.ReplaceServerSnapshot(&info, stacks, containers); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentCommands(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromBearer(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	cmds, err := s.st.ListPendingCommands(srv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]protocol.Command, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, protocol.Command{
			ID:          c.ID,
			Kind:        c.Kind,
			ContainerID: c.ContainerID,
			TargetImage: c.TargetImage,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgentCommandResult(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromBearer(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	var res protocol.CommandResult
	if err := readJSON(r, &res); err != nil {
		writeError(w, http.StatusBadRequest, "invalid result")
		return
	}
	status := res.Status
	if status != "done" && status != "failed" {
		status = "failed"
	}
	// Verify the command belongs to this agent's server.
	cmds, err := s.st.ListPendingCommands(srv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	owned := false
	dockerID := ""
	for _, c := range cmds {
		if c.ID == id {
			owned = true
			dockerID = c.ContainerID
			break
		}
	}
	if !owned {
		writeError(w, http.StatusNotFound, "command not found")
		return
	}
	if err := s.st.UpdateCommandResult(id, status, res.Message); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "done" {
		_ = s.st.AddEvent(s.eventNow("update_done", "agent", srv.ID, "", res.Message))
	} else {
		_ = s.st.AddEvent(s.eventNow("update_failed", "agent", srv.ID, "", res.Message))
	}
	// Update the in-memory job so the UI can surface completion.
	if cid, err := s.st.ContainerIDByDockerID(srv.ID, dockerID); err == nil {
		if status == "done" {
			s.jobs.set(cid, &updateJob{ContainerID: cid, Status: jobDone, Progress: 100, Message: "Update complete"})
		} else {
			s.jobs.set(cid, &updateJob{ContainerID: cid, Status: jobFailed, Progress: -1, Message: res.Message})
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

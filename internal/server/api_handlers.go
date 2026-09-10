package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// effectiveOnline reports whether a server is currently considered online.
func effectiveOnline(srv store.Server) bool {
	if srv.IsLocal {
		return true
	}
	return srv.Status == "online" && time.Since(srv.LastSeen) <= offlineThreshold
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	servers, err := s.st.ListServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stacks, err := s.st.ListStacks("")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	containers, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var online, running, updates, behindTotal int
	for _, srv := range servers {
		if effectiveOnline(srv) {
			online++
		}
	}
	for _, c := range containers {
		if c.Running {
			running++
		}
		if c.Update != nil {
			updates++
			behindTotal += c.Update.VersionsBehind
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"servers":               len(servers),
		"servers_online":        online,
		"stacks":                len(stacks),
		"containers":            len(containers),
		"containers_running":    running,
		"updates_available":     updates,
		"versions_behind_total": behindTotal,
	})
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.st.ListServers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(servers))
	for _, srv := range servers {
		out = append(out, serverJSON(srv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	srv, err := s.st.GetServer(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, serverJSON(*srv))
}

func serverJSON(srv store.Server) map[string]any {
	return map[string]any{
		"id":             srv.ID,
		"name":           srv.Name,
		"is_local":       srv.IsLocal,
		"online":         effectiveOnline(srv),
		"last_seen":      srv.LastSeen,
		"docker_version": srv.DockerVersion,
		"os":             srv.OS,
		"arch":           srv.Arch,
		"cpus":           srv.CPUs,
		"memory_bytes":   srv.MemoryBytes,
		"labels":         srv.Labels,
	}
}

type createServerRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	token := randomToken()
	srv := &store.Server{
		Name:           req.Name,
		IsLocal:        false,
		Status:         "offline",
		AgentTokenHash: auth.HashToken(token),
	}
	if err := s.st.CreateServer(srv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := s.currentUser(r)
	actor := ""
	if u != nil {
		actor = u.Username
	}
	_ = s.st.AddEvent(s.eventNow("server_added", actor, srv.ID, "", "added server "+req.Name))
	writeJSON(w, http.StatusOK, map[string]any{"id": srv.ID, "name": srv.Name, "token": token})
}

func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.st.UpdateServerName(r.PathValue("id"), req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	srv, err := s.st.GetServer(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if srv.IsLocal {
		writeError(w, http.StatusBadRequest, "the local server cannot be deleted")
		return
	}
	if err := s.st.DeleteServerCascade(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := s.currentUser(r)
	actor := ""
	if u != nil {
		actor = u.Username
	}
	_ = s.st.AddEvent(s.eventNow("server_deleted", actor, id, "", "deleted server "+srv.Name))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.st.ListStacks(r.URL.Query().Get("server"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if stacks == nil {
		stacks = []store.Stack{}
	}
	writeJSON(w, http.StatusOK, stacks)
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ContainerFilter{
		ServerID:   q.Get("server"),
		StackID:    q.Get("stack"),
		Registry:   q.Get("registry"),
		Repository: q.Get("repository"),
		State:      q.Get("state"),
		Search:     q.Get("search"),
	}
	if v := q.Get("has_update"); v != "" {
		f.HasUpdate, _ = strconv.ParseBool(v)
	}
	containers, err := s.st.ListContainers(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if containers == nil {
		containers = []store.ContainerWithUpdate{}
	}
	writeJSON(w, http.StatusOK, containers)
}

func (s *Server) handleGetContainer(w http.ResponseWriter, r *http.Request) {
	c, err := s.st.GetContainer(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleContainerReleases(w http.ResponseWriter, r *http.Request) {
	c, err := s.st.GetContainer(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	repoKey := ""
	if c.Update != nil {
		repoKey = c.Update.RepoKey
	} else {
		repoKey = s.updater.ResolveRepoKey(c.Container)
	}
	resp := map[string]any{
		"container": c,
		"update":    c.Update,
		"releases":  []store.Release{},
	}
	if repoKey != "" {
		rels, err := s.st.ListReleases(repoKey, 0)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["releases"] = rels
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListUpdates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.UpdateFilter{
		ServerID: q.Get("server"),
		StackID:  q.Get("stack"),
		Search:   q.Get("search"),
	}
	items, err := s.st.ListUpdates(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []store.ContainerWithUpdate{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleRequestUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Docker.EnableRecreate {
		writeError(w, http.StatusForbidden, "container updates are disabled (WND_DOCKER_ENABLE_RECREATE)")
		return
	}
	c, err := s.st.GetContainer(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	if c.Update == nil {
		writeError(w, http.StatusBadRequest, "no update available for this container")
		return
	}
	target := c.Image
	if c.Update.LatestTag != "" && c.Update.LatestTag != c.ImageTag {
		target = c.ImageName + ":" + c.Update.LatestTag
	}

	srv, err := s.st.GetServer(c.ServerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server lookup failed")
		return
	}
	u := s.currentUser(r)
	actor := ""
	if u != nil {
		actor = u.Username
	}

	if srv.IsLocal {
		if s.docker == nil {
			writeError(w, http.StatusInternalServerError, "local docker client unavailable")
			return
		}
		s.jobs.set(c.ID, &updateJob{ContainerID: c.ID, Name: c.Name, Status: jobQueued, Progress: -1, Message: "Queued"})
		go func() {
			// Use a fresh context: the request context is cancelled as soon as
			// this handler returns, which would abort the update.
			ctx := context.Background()
			err := s.docker.RecreateContainer(ctx, c.DockerID, target, func(stage string, percent int) {
				job := &updateJob{ContainerID: c.ID, Name: c.Name, Progress: percent}
				if stage == "pulling" {
					job.Status = jobPulling
					job.Message = "Pulling image…"
				} else {
					job.Status = jobRecreating
					job.Message = "Recreating container…"
				}
				s.jobs.set(c.ID, job)
			})
			if err != nil {
				s.jobs.set(c.ID, &updateJob{ContainerID: c.ID, Name: c.Name, Status: jobFailed, Progress: -1, Message: err.Error()})
				_ = s.st.AddEvent(s.eventNow("update_failed", actor, c.ServerID, c.ID, c.Name+" -> "+target+": "+err.Error()))
				return
			}
			s.jobs.set(c.ID, &updateJob{ContainerID: c.ID, Name: c.Name, Status: jobDone, Progress: 100, Message: "Update complete"})
			_ = s.st.AddEvent(s.eventNow("update_done", actor, c.ServerID, c.ID, c.Name+" updated to "+target))
		}()
		_ = s.st.AddEvent(s.eventNow("update_requested", actor, c.ServerID, c.ID, c.Name+" -> "+target))
		writeJSON(w, http.StatusOK, map[string]any{"queued": true, "local": true})
		return
	}

	// Remote agent: enqueue a command and track it as a queued job.
	s.jobs.set(c.ID, &updateJob{ContainerID: c.ID, Name: c.Name, Status: jobQueued, Progress: -1, Message: "Queued"})
	cmd := &store.Command{
		ServerID:    c.ServerID,
		Kind:        "update",
		ContainerID: c.DockerID,
		TargetImage: target,
	}
	if err := s.st.CreateCommand(cmd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.st.AddEvent(s.eventNow("update_requested", actor, c.ServerID, c.ID, c.Name+" -> "+target))
	writeJSON(w, http.StatusOK, map[string]any{"queued": true, "local": false, "command_id": cmd.ID})
}

func (s *Server) handleSetPin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pinned bool `json:"pinned"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	id := r.PathValue("id")
	if err := s.st.SetContainerPinned(id, req.Pinned); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Pinned {
		_ = s.st.DeleteUpdate(id)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"pinned": req.Pinned})
}

func (s *Server) handleTriggerCheck(w http.ResponseWriter, r *http.Request) {
	// Run in the background and return immediately.
	go func() {
		if err := s.triggerCheck(r.Context()); err != nil {
			_ = s.st.AddEvent(s.eventNow("check_failed", "system", "", "", err.Error()))
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.st.ListEvents(200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []store.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "wnd_" + hex.EncodeToString(b)
}

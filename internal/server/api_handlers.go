package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
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

// executorFor returns the Docker client that can run an update for a server, or
// nil when the update has to be delegated to a remote agent. Only the local host
// and direct endpoints are reachable from this process.
func (s *Server) executorFor(srv *store.Server) (*dockerx.Client, error) {
	if srv.IsLocal {
		if s.docker == nil {
			return nil, errors.New("local docker client unavailable")
		}
		return s.docker, nil
	}
	if srv.Kind == store.ServerDirect {
		return s.endpoints.get(*srv)
	}
	// Everything else is an agent: the update is queued as a command. This is
	// the pre-existing path and must stay the default.
	return nil, nil
}

// updateBlockedReason explains why a container cannot be updated at all, or
// returns "" when the update may proceed.
//
// Two kinds of container are owned by something else and are handled rather
// than recreated:
//
//   - a swarm task belongs to a service, which the orchestrator rolls for us;
//   - a systemd-managed (Podman Quadlet) container is updated by its unit,
//     which only works while it is running because the unit's teardown removes
//     a stopped container and nothing in the Engine API can start a unit again.
//
// Refusing here turns a long timeout into an immediate, actionable message.
func updateBlockedReason(c *store.ContainerWithUpdate, srv *store.Server) string {
	// Swarm tasks: only a manager can act, and only when the operator has
	// granted the services API to the socket proxy.
	if c.SwarmTaskID != "" {
		role := store.SwarmNone
		if srv != nil && srv.SwarmRole != "" {
			role = srv.SwarmRole
		}
		switch {
		case role == store.SwarmNone:
			// The host stopped reporting as a swarm member; fall through to the
			// generic message rather than guessing.
			return "cannot update " + c.Name + ": it is a swarm task, but the host is not currently reporting as a swarm manager."
		case role != store.SwarmManager:
			return "cannot update " + swarmName(c) + ": this is a swarm worker node. Add a manager node and update the service there."
		case !srv.SwarmServices:
			return "cannot update " + swarmName(c) + ": swarm service updates are opt-in. Grant the services API to your socket proxy (set SERVICES=1 alongside POST=1) and restart it, then try again."
		}
		return ""
	}

	if !c.Managed || c.Running {
		return ""
	}
	if c.SystemdUnit == "" {
		return "cannot update " + c.Name + ": it is managed by systemd and is not running. " +
			"Start its unit first so it comes up on the newly pulled image."
	}
	return "cannot update " + c.Name + ": it is managed by the systemd unit " + c.SystemdUnit +
		" and is not running. Start it first (`systemctl start " + c.SystemdUnit +
		"`) so it comes up on the newly pulled image."
}

// swarmName describes a swarm task by its service, which is what the operator
// recognises (replica names like web.3 are generated).
func swarmName(c *store.ContainerWithUpdate) string {
	if c.SwarmServiceName != "" {
		return "service " + c.SwarmServiceName
	}
	return c.Name
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
		out = append(out, s.serverJSON(srv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	srv, err := s.st.GetServer(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	writeJSON(w, http.StatusOK, s.serverJSON(*srv))
}

// agentBuildState says whether an agent is running what this deployment runs.
//
// The reference is the controller's own version, or the newest release when one
// is waiting — so during a pending upgrade every agent correctly reads as behind
// until it has caught up, rather than looking current because it matches the old
// controller.
func (s *Server) agentBuildState(srv store.Server) string {
	if srv.Kind != store.ServerAgent {
		return ""
	}
	if srv.AgentVersion == "" {
		return "unknown"
	}
	reference := s.version
	if res := s.selfUpdate.Cached(); res.Latest != "" && selfupdate.IsNewer(res.Latest, reference) {
		reference = res.Latest
	}
	if selfupdate.IsNewer(reference, srv.AgentVersion) {
		return "behind"
	}
	return "current"
}

func (s *Server) serverJSON(srv store.Server) map[string]any {
	out := map[string]any{
		"id":             srv.ID,
		"name":           srv.Name,
		"is_local":       srv.IsLocal,
		"kind":           string(srv.Kind),
		"online":         effectiveOnline(srv),
		"last_seen":      srv.LastSeen,
		"docker_version": srv.DockerVersion,
		"os":             srv.OS,
		"arch":           srv.Arch,
		"cpus":           srv.CPUs,
		"memory_bytes":   srv.MemoryBytes,
		"labels":         srv.Labels,
		"swarm_role":     string(srv.SwarmRole),
		"swarm_services": srv.SwarmServices,
	}
	if srv.Kind == store.ServerAgent {
		out["agent_version"] = srv.AgentVersion
		out["agent_image"] = srv.AgentImage
		out["agent_build"] = s.agentBuildState(srv)
	}
	// Only direct endpoints expose connection details; TLS material is never
	// returned (it is referenced by file path on the host).
	if srv.Kind == store.ServerDirect {
		out["docker_host"] = srv.DockerHost
		out["tls"] = srv.TLSEnabled()
	}
	return out
}

type createServerRequest struct {
	Name string `json:"name"`
	// Kind selects how the host is reached: "agent" (default) or "direct".
	Kind string `json:"kind"`
	// Direct-endpoint settings. TLS material is a file path on the server host.
	DockerHost string `json:"docker_host"`
	TLSCA      string `json:"tls_ca"`
	TLSCert    string `json:"tls_cert"`
	TLSKey     string `json:"tls_key"`
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
	u := s.currentUser(r)
	actor := ""
	if u != nil {
		actor = u.Username
	}

	if req.Kind == string(store.ServerDirect) {
		spec, err := validateEndpoint(req.DockerHost, req.TLSCA, req.TLSCert, req.TLSKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		srv := &store.Server{
			Name:       req.Name,
			IsLocal:    false,
			Kind:       store.ServerDirect,
			Status:     "offline",
			DockerHost: spec.Host,
			TLSCA:      spec.TLSCA,
			TLSCert:    spec.TLSCert,
			TLSKey:     spec.TLSKey,
			NameCustom: true,
		}
		if err := s.st.CreateServer(srv); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Prime the endpoint immediately so the new card shows real data.
		go s.snapshotDirect(context.Background(), *srv)
		_ = s.st.AddEvent(s.eventNow("server_added", actor, srv.ID, "", "added direct endpoint "+req.Name+" ("+spec.Host+")"))
		writeJSON(w, http.StatusOK, map[string]any{"id": srv.ID, "name": srv.Name, "kind": string(store.ServerDirect)})
		return
	}

	if req.Kind != "" && req.Kind != string(store.ServerAgent) {
		writeError(w, http.StatusBadRequest, "kind must be \"agent\" or \"direct\"")
		return
	}

	token := randomToken()
	srv := &store.Server{
		Name:           req.Name,
		IsLocal:        false,
		Kind:           store.ServerAgent,
		Status:         "offline",
		AgentTokenHash: auth.HashToken(token),
	}
	if err := s.st.CreateServer(srv); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.st.AddEvent(s.eventNow("server_added", actor, srv.ID, "", "added server "+req.Name))
	writeJSON(w, http.StatusOK, map[string]any{"id": srv.ID, "name": srv.Name, "kind": string(store.ServerAgent), "token": token})
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
	id := r.PathValue("id")
	srv, err := s.st.GetServer(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err := s.st.UpdateServerName(id, req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	srv.Name = req.Name
	srv.NameCustom = true

	// A direct endpoint may also have its connection settings rewritten. An
	// omitted docker_host leaves the existing endpoint untouched.
	if srv.Kind == store.ServerDirect && strings.TrimSpace(req.DockerHost) != "" {
		spec, err := validateEndpoint(req.DockerHost, req.TLSCA, req.TLSCert, req.TLSKey)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.st.UpdateServerEndpoint(id, spec.Host, spec.TLSCA, spec.TLSCert, spec.TLSKey); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Drop the cached client so the new settings take effect at once.
		s.endpoints.forget(id)
		srv.DockerHost, srv.TLSCA, srv.TLSCert, srv.TLSKey = spec.Host, spec.TLSCA, spec.TLSCert, spec.TLSKey
		go s.snapshotDirect(context.Background(), *srv)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type testEndpointRequest struct {
	DockerHost string `json:"docker_host"`
	TLSCA      string `json:"tls_ca"`
	TLSCert    string `json:"tls_cert"`
	TLSKey     string `json:"tls_key"`
}

// handleTestEndpoint validates Docker API connectivity for a direct endpoint,
// either from the request body (before saving) or from a stored server row with
// per-field overrides.
func (s *Server) handleTestEndpoint(w http.ResponseWriter, r *http.Request) {
	var req testEndpointRequest
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	if id := r.PathValue("id"); id != "" {
		srv, err := s.st.GetServer(id)
		if err != nil {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		if srv.IsLocal {
			if s.docker == nil {
				writeError(w, http.StatusServiceUnavailable, "local docker client unavailable")
				return
			}
			s.reportEndpointCheck(w, r, s.docker)
			return
		}
		if srv.Kind != store.ServerDirect {
			writeError(w, http.StatusBadRequest, "only direct endpoints can be tested")
			return
		}
		if strings.TrimSpace(req.DockerHost) == "" {
			req.DockerHost = srv.DockerHost
		}
		if strings.TrimSpace(req.TLSCA) == "" {
			req.TLSCA = srv.TLSCA
		}
		if strings.TrimSpace(req.TLSCert) == "" {
			req.TLSCert = srv.TLSCert
		}
		if strings.TrimSpace(req.TLSKey) == "" {
			req.TLSKey = srv.TLSKey
		}
	}
	spec, err := validateEndpoint(req.DockerHost, req.TLSCA, req.TLSCert, req.TLSKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dc, err := dockerx.New(config.DockerConfig{
		Host: spec.Host, TLS: spec.TLSEnabled(),
		TLSCA: spec.TLSCA, TLSCert: spec.TLSCert, TLSKey: spec.TLSKey,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer func() { _ = dc.Close() }()
	s.reportEndpointCheck(w, r, dc)
}

func (s *Server) reportEndpointCheck(w http.ResponseWriter, r *http.Request, dc *dockerx.Client) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := dc.Ping(ctx); err != nil {
		writeError(w, http.StatusBadGateway, "could not reach the Docker API: "+err.Error())
		return
	}
	info, err := dc.Info(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "connected, but reading host info failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"name":           info.Name,
		"docker_version": info.DockerVersion,
		"os":             info.OS,
		"arch":           info.Arch,
		"cpus":           info.CPUs,
		"memory_bytes":   info.MemoryBytes,
	})
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
	s.endpoints.forget(id)
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

// handleAbout reports build/version information for the About page.
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "WhatsNewDock",
		"version": s.version,
	})
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

	// Containers owned by something else — a swarm service or a systemd unit —
	// are handled through their owner, and refuse up front when that is not
	// possible.
	if reason := updateBlockedReason(c, srv); reason != "" {
		writeError(w, http.StatusConflict, reason)
		return
	}

	// Direct endpoints are polled by this server, so updates run here exactly
	// like local ones. Anything else is an agent and stays on the command queue.
	exec, err := s.executorFor(srv)
	if err != nil {
		writeError(w, http.StatusBadGateway, "endpoint unavailable: "+err.Error())
		return
	}

	if exec != nil {
		s.jobs.set(c.ID, &updateJob{ContainerID: c.ID, Name: c.Name, Status: jobQueued, Progress: -1, Message: "Queued"})
		go func() {
			// Use a fresh context: the request context is cancelled as soon as
			// this handler returns, which would abort the update.
			ctx := context.Background()
			err := exec.RecreateContainer(ctx, c.DockerID, target, func(stage string, percent int) {
				job := &updateJob{ContainerID: c.ID, Name: c.Name, Progress: percent}
				switch stage {
				case "pulling":
					job.Status = jobPulling
					job.Message = "Pulling image…"
				case "updating":
					job.Status = jobUpdating
					job.Message = "Rolling out the service…"
				default:
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
		writeJSON(w, http.StatusOK, map[string]any{
			"queued": true,
			"local":  srv.IsLocal,
			"direct": srv.Kind == store.ServerDirect,
			"swarm":  c.SwarmTaskID != "",
		})
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
	// Run in the background and return immediately. The context must outlive
	// the response: net/http cancels the request context as soon as this
	// handler returns, and a check that inherits it is aborted mid-flight,
	// which made "Check now" finish without ever reporting an update.
	ctx := context.WithoutCancel(r.Context())
	go func() {
		if err := s.runUpdateCheck(ctx, "manual"); err != nil {
			_ = s.st.AddEvent(s.eventNow("check_failed", "system", "", "", err.Error()))
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// handleCheckStatus reports the state of the current or most recent update
// check, so the UI can wait for a manual check to finish and show its result
// rather than reading the previous set of updates.
func (s *Server) handleCheckStatus(w http.ResponseWriter, r *http.Request) {
	running, trigger, startedAt, finishedAt, errMsg := s.checks.status()

	updates := 0
	if containers, err := s.st.ListContainers(store.ContainerFilter{}); err == nil {
		for _, c := range containers {
			if c.Update != nil {
				updates++
			}
		}
	}

	out := map[string]any{
		"running":           running,
		"trigger":           trigger,
		"updates_available": updates,
		"last_error":        errMsg,
	}
	if !startedAt.IsZero() {
		out["started_at"] = startedAt
	}
	if !finishedAt.IsZero() {
		out["finished_at"] = finishedAt
	}
	writeJSON(w, http.StatusOK, out)
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

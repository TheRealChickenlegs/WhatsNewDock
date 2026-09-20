package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/protocol"
	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// newAgentOnlyTestServer builds a real Server with no local Docker access at
// all: exactly the shape of a deployment that monitors remote hosts through
// agents (and, since the same instance may also poll direct endpoints, of the
// bundled compose stack when the socket-proxy is unreachable).
func newAgentOnlyTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Docker.Host = "" // no local Docker client
	cfg.Docker.EnableRecreate = true
	cfg.Auth.SessionSecret = "test-session-secret-0123456789"
	cfg.InitialAdminUser = "admin"
	cfg.InitialAdminPassword = "test-password"
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

// TestAgentDeploymentUnaffected is the compatibility guard for the whole
// objective: a token-authenticated agent must still be able to report a
// snapshot, have its containers (and compose stacks) show up, and have update
// requests queued as commands rather than executed by the server.
func TestAgentDeploymentUnaffected(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	// A deployment with no local Docker must not invent a local server row.
	servers, err := s.st.ListServers()
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected no server rows, got %d", len(servers))
	}
	if s.docker != nil {
		t.Fatal("test server should have no local docker client")
	}

	// 1. Register an agent server exactly as the UI does.
	const token = "wnd_integrationtoken"
	srv := &store.Server{
		Name:           "edge",
		Kind:           store.ServerAgent,
		Status:         "offline",
		AgentTokenHash: auth.HashToken(token),
	}
	if err := s.st.CreateServer(srv); err != nil {
		t.Fatalf("create agent server: %v", err)
	}

	// 2. The agent pushes a snapshot over the bearer-authenticated route.
	report := protocol.Report{
		AgentName: "edge",
		Version:   "test",
		Info:      store.Server{Name: "edge-host", DockerVersion: "27.0.0", OS: "linux", Arch: "amd64", CPUs: 4},
		Stacks:    []store.Stack{{Name: "web", Kind: store.StackCompose}},
		Containers: []store.Container{{
			DockerID: "aaa111", Name: "nginx", Image: "nginx:1.25",
			ImageName: "docker.io/library/nginx", ImageTag: "1.25",
			Registry: "docker.io", Repository: "library/nginx",
			State: "running", Running: true, StackName: "web",
			Labels: map[string]string{"com.docker.compose.project": "web"},
		}},
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	rec := doJSON(t, s.handleAgentReport, http.MethodPost, "/api/agent/v1/report", string(body),
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("agent report = %d: %s", rec.Code, rec.Body.String())
	}

	// A bad token is still rejected.
	rec = doJSON(t, s.handleAgentReport, http.MethodPost, "/api/agent/v1/report", string(body),
		map[string]string{"Authorization": "Bearer wnd_wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d, want 401", rec.Code)
	}

	// 3. The snapshot landed with its compose stack intact and kind preserved.
	servers, err = s.st.ListServers()
	if err != nil || len(servers) != 1 {
		t.Fatalf("list servers = %d, %v", len(servers), err)
	}
	if servers[0].Kind != store.ServerAgent {
		t.Errorf("agent kind = %q, want agent", servers[0].Kind)
	}
	if servers[0].Status != "online" {
		t.Errorf("agent status = %q, want online", servers[0].Status)
	}
	containers, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil || len(containers) != 1 {
		t.Fatalf("list containers = %d, %v", len(containers), err)
	}
	c := containers[0]
	if c.Name != "nginx" || c.StackName != "web" {
		t.Errorf("container not reported correctly: %+v", c)
	}
	if c.Managed || c.SystemdUnit != "" {
		t.Errorf("plain compose container wrongly marked managed: %+v", c)
	}
	if c.ServerName != "edge-host" {
		t.Errorf("server name = %q", c.ServerName)
	}

	// 4. Requesting an update must queue a command for the agent — never run
	//    here, even though the server also owns a direct-endpoint path now.
	if err := s.st.UpsertUpdate(&store.Update{
		ContainerID: c.ID, RepoKey: "gh:nginx/nginx",
		CurrentTag: "1.25", LatestTag: "1.26", Source: "github",
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/"+c.ID+"/update", nil)
	req.SetPathValue("id", c.ID)
	rec2 := httptest.NewRecorder()
	s.handleRequestUpdate(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("update request = %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp struct {
		Queued    bool   `json:"queued"`
		Local     bool   `json:"local"`
		CommandID string `json:"command_id"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Queued || resp.Local || resp.CommandID == "" {
		t.Fatalf("agent update was not queued as a command: %s", rec2.Body.String())
	}
	cmds, err := s.st.ListPendingCommands(srv.ID)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("pending commands = %d, %v", len(cmds), err)
	}
	if cmds[0].Kind != "update" || cmds[0].ContainerID != "aaa111" || cmds[0].TargetImage != "docker.io/library/nginx:1.26" {
		t.Errorf("unexpected command: %+v", cmds[0])
	}

	// 5. The agent can poll and complete that command.
	rec = doJSON(t, s.handleAgentCommands, http.MethodGet, "/api/agent/v1/commands", "",
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("agent commands = %d: %s", rec.Code, rec.Body.String())
	}
	resultReq := httptest.NewRequest(http.MethodPost, "/api/agent/v1/commands/"+cmds[0].ID+"/result",
		bytes.NewBufferString(`{"status":"done","message":"ok"}`))
	resultReq.Header.Set("Content-Type", "application/json")
	resultReq.Header.Set("Authorization", "Bearer "+token)
	resultReq.SetPathValue("id", cmds[0].ID)
	rec = httptest.NewRecorder()
	s.handleAgentCommandResult(rec, resultReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("command result = %d: %s", rec.Code, rec.Body.String())
	}
	remaining, err := s.st.ListPendingCommands(srv.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("command not completed: %d, %v", len(remaining), err)
	}
}

// TestLocalHostStillRunsUpdatesInProcess is the other half of the routing
// invariant: the compose deployment's own host executes updates through the
// local docker client and must never enqueue an agent command.
//
// The docker client points at a socket that does not exist, so the recreate
// itself fails asynchronously — which is irrelevant here. What matters is that
// the request is handled locally and leaves the agent command queue empty.
func TestLocalHostStillRunsUpdatesInProcess(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Docker.Host = "unix:///nonexistent/whatsnewdock-test.sock"
	cfg.Docker.EnableRecreate = true
	cfg.Auth.SessionSecret = "test-session-secret-0123456789"
	cfg.InitialAdminUser = "admin"
	cfg.InitialAdminPassword = "test-password"
	s, err := New(cfg, "test")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer func() { _ = s.Close() }()
	if s.docker == nil {
		t.Fatal("expected a local docker client")
	}

	// Bootstrap must have created the local server row.
	servers, err := s.st.ListServers()
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers = %d, %v", len(servers), err)
	}
	if !servers[0].IsLocal || servers[0].Kind != store.ServerLocal {
		t.Fatalf("local row = %+v", servers[0])
	}
	local := servers[0]

	if err := s.st.ReplaceServerSnapshot(&local,
		[]*store.Stack{{Name: "web", Kind: store.StackCompose}},
		[]*store.Container{{
			DockerID: "local111", Name: "nginx", Image: "nginx:1.25",
			ImageName: "docker.io/library/nginx", ImageTag: "1.25",
			Registry: "docker.io", Repository: "library/nginx",
			State: "running", Running: true, StackName: "web",
		}}); err != nil {
		t.Fatalf("seed local snapshot: %v", err)
	}
	containers, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %d, %v", len(containers), err)
	}
	c := containers[0]
	if err := s.st.UpsertUpdate(&store.Update{
		ContainerID: c.ID, CurrentTag: "1.25", LatestTag: "1.26", Source: "github",
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/"+c.ID+"/update", nil)
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	s.handleRequestUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update request = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Queued bool `json:"queued"`
		Local  bool `json:"local"`
		Direct bool `json:"direct"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Queued || !resp.Local || resp.Direct {
		t.Fatalf("local update not handled in process: %s", rec.Body.String())
	}
	cmds, err := s.st.ListPendingCommands(local.ID)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("local update was delegated to an agent: %+v", cmds)
	}
}

// TestDirectEndpointRunsUpdatesInProcess completes the routing matrix: a direct
// endpoint is polled by this server, so its updates run here too and must not
// be queued for an agent that does not exist.
func TestDirectEndpointRunsUpdatesInProcess(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	direct := &store.Server{
		Name: "edge-nas", Kind: store.ServerDirect, Status: "offline",
		DockerHost: "unix:///nonexistent/whatsnewdock-direct.sock", NameCustom: true,
	}
	if err := s.st.CreateServer(direct); err != nil {
		t.Fatalf("create direct server: %v", err)
	}
	if err := s.st.ReplaceServerSnapshot(direct, nil, []*store.Container{{
		DockerID: "direct111", Name: "nginx", Image: "nginx:1.25",
		ImageName: "docker.io/library/nginx", ImageTag: "1.25",
		Registry: "docker.io", Repository: "library/nginx",
		State: "running", Running: true,
	}}); err != nil {
		t.Fatalf("seed direct snapshot: %v", err)
	}
	// The operator-chosen name survives the snapshot (kind and name alike).
	got, err := s.st.GetServer(direct.ID)
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if got.Kind != store.ServerDirect || got.Name != "edge-nas" || got.DockerHost != direct.DockerHost {
		t.Fatalf("direct server lost its identity: %+v", got)
	}

	containers, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %d, %v", len(containers), err)
	}
	c := containers[0]
	if err := s.st.UpsertUpdate(&store.Update{
		ContainerID: c.ID, CurrentTag: "1.25", LatestTag: "1.26", Source: "github",
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/"+c.ID+"/update", nil)
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	s.handleRequestUpdate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update request = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Queued bool `json:"queued"`
		Local  bool `json:"local"`
		Direct bool `json:"direct"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Queued || resp.Local || !resp.Direct {
		t.Fatalf("direct update not handled in process: %s", rec.Body.String())
	}
	cmds, err := s.st.ListPendingCommands(direct.ID)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("direct update was delegated to an agent: %+v", cmds)
	}
}

// TestQuadletUpdateRoutingOverHTTP covers the systemd-managed update path at the
// API boundary:
//
//   - a RUNNING systemd-managed container is updatable (the recreate is handed
//     to its unit), so it must not be refused;
//   - a STOPPED one is refused with an actionable message, because the unit's
//     teardown removed the container and the Engine API cannot start a unit;
//   - neither case may queue a command for an agent.
func TestQuadletUpdateRoutingOverHTTP(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	srv := &store.Server{Name: "podman-host", Kind: store.ServerDirect, Status: "online",
		DockerHost: "unix:///nonexistent/podman.sock", NameCustom: true}
	if err := s.st.CreateServer(srv); err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := s.st.ReplaceServerSnapshot(srv, nil, []*store.Container{{
		DockerID: "quadlet111", Name: "web", Image: "nginx:1.25",
		ImageName: "docker.io/library/nginx", ImageTag: "1.25",
		Registry: "docker.io", Repository: "library/nginx",
		State: "running", Running: true,
		SystemdUnit: "web.service", Managed: true,
	}}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	containers, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil || len(containers) != 1 {
		t.Fatalf("containers = %d, %v", len(containers), err)
	}
	c := containers[0]
	if err := s.st.UpsertUpdate(&store.Update{
		ContainerID: c.ID, CurrentTag: "1.25", LatestTag: "1.26", Source: "github",
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}

	requestUpdate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/"+c.ID+"/update", nil)
		req.SetPathValue("id", c.ID)
		rec := httptest.NewRecorder()
		s.handleRequestUpdate(rec, req)
		return rec
	}

	// A running Quadlet container is updated by handing the recreate to systemd.
	if rec := requestUpdate(); rec.Code != http.StatusOK {
		t.Fatalf("running systemd-managed container = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Now the container is reported stopped: refuse before the 90s timeout.
	if err := s.st.ReplaceServerSnapshot(srv, nil, []*store.Container{{
		DockerID: "quadlet111", Name: "web", Image: "nginx:1.25",
		ImageName: "docker.io/library/nginx", ImageTag: "1.25",
		Registry: "docker.io", Repository: "library/nginx",
		State: "exited", Running: false,
		SystemdUnit: "web.service", Managed: true,
	}}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	rec := requestUpdate()
	if rec.Code != http.StatusConflict {
		t.Fatalf("stopped systemd-managed container = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "web.service") ||
		!strings.Contains(body, "systemctl start web.service") {
		t.Errorf("unhelpful refusal: %s", body)
	}

	cmds, err := s.st.ListPendingCommands(srv.ID)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("a direct endpoint queued an agent command: %+v", cmds)
	}
}

// TestAgentReportCannotCreateDirectEndpointKind makes sure an agent report can
// never turn an existing row into a direct endpoint, so the routing decision
// cannot be steered by an agent.
func TestAgentReportCannotReclassifyKindOverHTTP(t *testing.T) {
	s := newAgentOnlyTestServer(t)

	const token = "wnd_reclasstoken"
	srv := &store.Server{
		Name: "edge", Kind: store.ServerAgent, Status: "offline",
		AgentTokenHash: auth.HashToken(token),
	}
	if err := s.st.CreateServer(srv); err != nil {
		t.Fatalf("create: %v", err)
	}
	body, _ := json.Marshal(protocol.Report{
		Info: store.Server{Name: "edge", Kind: store.ServerDirect, DockerHost: "tcp://evil:2375"},
	})
	rec := doJSON(t, s.handleAgentReport, http.MethodPost, "/api/agent/v1/report", string(body),
		map[string]string{"Authorization": "Bearer " + token})
	if rec.Code != http.StatusOK {
		t.Fatalf("report = %d: %s", rec.Code, rec.Body.String())
	}
	got, err := s.st.GetServer(srv.ID)
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if got.Kind != store.ServerAgent {
		t.Errorf("kind = %q, want agent", got.Kind)
	}
	if got.DockerHost != "" {
		t.Errorf("docker host was set by an agent report: %q", got.DockerHost)
	}
}

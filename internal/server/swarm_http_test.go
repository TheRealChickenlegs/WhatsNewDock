package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// swarmContainer builds a stored swarm task attached to a direct endpoint.
func swarmServerWithTask(t *testing.T, s *Server, role store.SwarmRole, services bool) store.ContainerWithUpdate {
	t.Helper()
	srv := &store.Server{
		Name: "swarm-host", Kind: store.ServerDirect, Status: "online",
		DockerHost: "unix:///nonexistent/swarm.sock", NameCustom: true,
		SwarmRole: role, SwarmServices: services,
	}
	if err := s.st.CreateServer(srv); err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := s.st.ReplaceServerSnapshot(srv, nil, []*store.Container{{
		DockerID: "task1", Name: "web.1", Image: "nginx:1.25",
		ImageName: "docker.io/library/nginx", ImageTag: "1.25",
		Registry: "docker.io", Repository: "library/nginx",
		State: "running", Running: true, Managed: true,
		SwarmServiceID: "svc1", SwarmServiceName: "web", SwarmTaskID: "task1",
	}}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	all, err := s.st.ListContainers(store.ContainerFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("containers = %d, %v", len(all), err)
	}
	c := all[0]
	if err := s.st.UpsertUpdate(&store.Update{
		ContainerID: c.ID, CurrentTag: "1.25", LatestTag: "1.26", Source: "github",
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	return c
}

func requestSwarmUpdate(t *testing.T, s *Server, c store.ContainerWithUpdate) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/containers/"+c.ID+"/update", nil)
	req.SetPathValue("id", c.ID)
	rec := httptest.NewRecorder()
	s.handleRequestUpdate(rec, req)
	return rec
}

// TestSwarmUpdateRouting covers the three answers a swarm task can get, and the
// contract that only the third one actually does anything.
func TestSwarmUpdateRouting(t *testing.T) {
	t.Run("manager with the services API granted updates the service", func(t *testing.T) {
		s := newAgentOnlyTestServer(t)
		c := swarmServerWithTask(t, s, store.SwarmManager, true)

		rec := requestSwarmUpdate(t, s, c)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Queued bool `json:"queued"`
			Swarm  bool `json:"swarm"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.Queued || !resp.Swarm {
			t.Errorf("swarm update not flagged as such: %s", rec.Body.String())
		}
		// A direct endpoint runs the work here, so nothing is queued for an agent.
		cmds, _ := s.st.ListPendingCommands(c.ServerID)
		if len(cmds) != 0 {
			t.Errorf("a swarm update on a direct endpoint queued an agent command: %+v", cmds)
		}
	})

	t.Run("manager without the services API is refused with the opt-in", func(t *testing.T) {
		s := newAgentOnlyTestServer(t)
		c := swarmServerWithTask(t, s, store.SwarmManager, false)

		rec := requestSwarmUpdate(t, s, c)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "SERVICES=1") || !strings.Contains(body, "opt-in") {
			t.Errorf("refusal should name the opt-in: %s", body)
		}
	})

	t.Run("worker is told to use a manager", func(t *testing.T) {
		s := newAgentOnlyTestServer(t)
		c := swarmServerWithTask(t, s, store.SwarmWorker, false)

		rec := requestSwarmUpdate(t, s, c)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "worker node") {
			t.Errorf("refusal should mention the worker: %s", rec.Body.String())
		}
	})
}

// TestSwarmTaskIsNeverTreatedAsAPlainContainer is the safety invariant: the
// server must not fall through to the container recreate path for a task.
func TestSwarmTaskIsNeverTreatedAsAPlainContainer(t *testing.T) {
	s := newAgentOnlyTestServer(t)
	c := swarmServerWithTask(t, s, store.SwarmManager, false)

	reason := updateBlockedReason(&c, &store.Server{SwarmRole: store.SwarmManager, SwarmServices: false})
	if reason == "" {
		t.Fatal("a swarm task must never be reported as updatable when the API is denied")
	}
	if strings.Contains(reason, "recreate") {
		t.Errorf("the refusal should not suggest recreating a task: %q", reason)
	}
}

// TestShippedComposeKeepsSwarmOptedOut pins the default: swarm compatibility
// must not silently widen what the socket proxy exposes. Enabling it is a
// deliberate, documented operator change.
func TestShippedComposeKeepsSwarmOptedOut(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "docker-compose.yml"),
		filepath.Join("..", "..", "deploy", "quadlet", "whatsnewdock-socket-proxy.container"),
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("deployment file not available: %v", err)
		}
		// Only active settings count: both files document the opt-in in
		// comments, which must not be mistaken for enabling it.
		var active strings.Builder
		for _, line := range strings.Split(string(src), "\n") {
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "#") {
				continue
			}
			active.WriteString(line)
			active.WriteByte('\n')
		}
		text := active.String()
		for _, section := range []string{"SERVICES", "TASKS", "SWARM", "NODES"} {
			// The proxy's denial can be written as `SERVICES: "0"` (compose) or
			// `Environment=SERVICES=0` (Quadlet); neither may enable it, and the
			// Quadlet unit simply omits them, which defaults to denied.
			if strings.Contains(text, section+`=1`) || strings.Contains(text, section+`: "1"`) {
				t.Errorf("%s enables %s; swarm must stay opt-in", path, section)
			}
		}
	}
}

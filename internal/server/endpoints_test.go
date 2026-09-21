package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

func TestNormalizeDockerHost(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "10.0.0.5:2376", want: "tcp://10.0.0.5:2376"},
		{in: "10.0.0.5", want: "tcp://10.0.0.5:2375"},
		{in: "tcp://10.0.0.5", want: "tcp://10.0.0.5:2375"},
		{in: "tcp://10.0.0.5:2376", want: "tcp://10.0.0.5:2376"},
		// https implies the conventional TLS port.
		{in: "https://10.0.0.5", want: "tcp://10.0.0.5:2376"},
		{in: "unix:///var/run/docker.sock", want: "unix:///var/run/docker.sock"},
		{in: "  tcp://host:1234  ", want: "tcp://host:1234"},
		{in: "ssh://user@host", wantErr: true},
		{in: "ftp://host", wantErr: true},
		{in: "", wantErr: true},
		{in: "tcp://", wantErr: true},
	}
	for _, c := range cases {
		got, err := normalizeDockerHost(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeDockerHost(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeDockerHost(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeDockerHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostIsLoopback(t *testing.T) {
	for _, host := range []string{"unix:///var/run/docker.sock", "tcp://127.0.0.1:2375", "tcp://localhost:2375", "tcp://[::1]:2375"} {
		if !hostIsLoopback(host) {
			t.Errorf("%s should be loopback", host)
		}
	}
	for _, host := range []string{"tcp://10.0.0.5:2375", "tcp://docker.internal:2375", "tcp://127.0.0.1.evil.com:2375"} {
		if hostIsLoopback(host) {
			t.Errorf("%s should not be loopback", host)
		}
	}
}

// TestValidateEndpointRefusesPlaintextRemote is the security gate: an
// unauthenticated Docker API must never be spoken over the network.
func TestValidateEndpointRefusesPlaintextRemote(t *testing.T) {
	if _, err := validateEndpoint("tcp://10.0.0.5:2375", "", "", ""); err == nil {
		t.Fatal("expected plaintext remote endpoint to be rejected")
	} else if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("error should mention TLS: %v", err)
	}
	// Loopback plaintext stays allowed (socket-proxy sidecars, dev setups).
	if _, err := validateEndpoint("tcp://127.0.0.1:2375", "", "", ""); err != nil {
		t.Errorf("loopback plaintext should be allowed: %v", err)
	}
	// So do unix sockets.
	if _, err := validateEndpoint("unix:///var/run/docker.sock", "", "", ""); err != nil {
		t.Errorf("unix socket should be allowed: %v", err)
	}
}

func TestValidateEndpointTLSMaterial(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	ca := writeFile("ca.pem")
	cert := writeFile("cert.pem")
	key := writeFile("key.pem")

	// Client certificates without a CA cannot be verified.
	if _, err := validateEndpoint("tcp://10.0.0.5:2376", "", cert, key); err == nil {
		t.Error("expected missing CA to be rejected")
	}
	// Cert and key must be supplied together.
	if _, err := validateEndpoint("tcp://10.0.0.5:2376", ca, cert, ""); err == nil {
		t.Error("expected cert without key to be rejected")
	}
	// A path that does not exist is reported at save time.
	if _, err := validateEndpoint("tcp://10.0.0.5:2376", filepath.Join(dir, "missing.pem"), "", ""); err == nil {
		t.Error("expected unreadable CA path to be rejected")
	}
	// A directory is not TLS material.
	if _, err := validateEndpoint("tcp://10.0.0.5:2376", dir, "", ""); err == nil {
		t.Error("expected directory to be rejected")
	}
	// CA-only (server-auth TLS) and full mTLS are both fine.
	if _, err := validateEndpoint("tcp://10.0.0.5:2376", ca, "", ""); err != nil {
		t.Errorf("CA-only TLS should be allowed: %v", err)
	}
	spec, err := validateEndpoint("10.0.0.5:2376", ca, cert, key)
	if err != nil {
		t.Fatalf("full mTLS should be allowed: %v", err)
	}
	if !spec.TLSEnabled() {
		t.Error("spec should report TLS enabled")
	}
	if _, err := os.Stat(spec.TLSCA); err != nil {
		t.Errorf("CA path not preserved: %v", err)
	}
}

// TestExecutorForRouting pins the compatibility invariant: only local and
// direct servers run here, every other server stays on the agent path.
func TestExecutorForRouting(t *testing.T) {
	docker, err := dockerx.New(config.DockerConfig{Host: "unix:///var/run/docker.sock"})
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer docker.Close()

	s := &Server{docker: docker, endpoints: newEndpointPool()}
	defer s.endpoints.closeAll()

	local := &store.Server{ID: "l", IsLocal: true, Kind: store.ServerLocal}
	if got, err := s.executorFor(local); err != nil || got != docker {
		t.Errorf("local: got %v, %v", got, err)
	}

	agent := &store.Server{ID: "a", Kind: store.ServerAgent}
	got, err := s.executorFor(agent)
	if err != nil || got != nil {
		t.Errorf("agent must stay on the command queue: got %v, %v", got, err)
	}
	// A server with no kind at all (a row written before the column existed)
	// must also fall back to the agent path.
	legacy := &store.Server{ID: "legacy"}
	if got, err := s.executorFor(legacy); err != nil || got != nil {
		t.Errorf("unknown kind must fall back to agent: got %v, %v", got, err)
	}

	direct := &store.Server{ID: "d", Kind: store.ServerDirect, DockerHost: "tcp://127.0.0.1:2375"}
	got, err = s.executorFor(direct)
	if err != nil || got == nil {
		t.Fatalf("direct endpoint should execute locally: %v", err)
	}
	// The client is pooled, so a second lookup returns the same instance.
	again, _ := s.executorFor(direct)
	if again != got {
		t.Error("direct endpoint client was not pooled")
	}
	// Changing the endpoint settings invalidates the cached client.
	changed := *direct
	changed.DockerHost = "tcp://127.0.0.1:2376"
	rebuilt, err := s.executorFor(&changed)
	if err != nil || rebuilt == nil {
		t.Fatalf("rebuild failed: %v", err)
	}
	if rebuilt == got {
		t.Error("endpoint change should rebuild the client")
	}
	// Forgetting a server drops its client.
	s.endpoints.forget("d")
	if _, ok := s.endpoints.entries["d"]; ok {
		t.Error("forget should drop the cached client")
	}
}

// TestExecutorForLocalWithoutDocker keeps the old failure mode: no socket, no
// local updates, but the UI keeps serving.
func TestExecutorForLocalWithoutDocker(t *testing.T) {
	s := &Server{endpoints: newEndpointPool()}
	defer s.endpoints.closeAll()
	if _, err := s.executorFor(&store.Server{ID: "l", IsLocal: true}); err == nil {
		t.Error("expected an error when the local docker client is absent")
	}
}

func TestUpdateBlockedReason(t *testing.T) {
	// Plain Docker/compose containers are never blocked.
	if reason := updateBlockedReason(&store.ContainerWithUpdate{}, &store.Server{}); reason != "" {
		t.Errorf("unmanaged container blocked: %q", reason)
	}

	// A running Quadlet container is updatable: the recreate is handed to its
	// systemd unit rather than being refused.
	running := &store.ContainerWithUpdate{}
	running.Managed = true
	running.Running = true
	running.SystemdUnit = "web.service"
	if reason := updateBlockedReason(running, &store.Server{}); reason != "" {
		t.Errorf("a running systemd-managed container should be updatable: %q", reason)
	}

	// A stopped one cannot be: the unit's teardown removed the container and
	// the Engine API cannot start a unit.
	stopped := &store.ContainerWithUpdate{}
	stopped.Managed = true
	stopped.Name = "web"
	stopped.SystemdUnit = "web.service"
	reason := updateBlockedReason(stopped, &store.Server{})
	if !strings.Contains(reason, "web.service") || !strings.Contains(reason, "systemctl start web.service") {
		t.Errorf("unhelpful message: %q", reason)
	}
	unnamed := &store.ContainerWithUpdate{}
	unnamed.Managed = true
	unnamed.Name = "web"
	if reason := updateBlockedReason(unnamed, &store.Server{}); !strings.Contains(reason, "systemd") {
		t.Errorf("unhelpful message: %q", reason)
	}
}

// TestUpdateBlockedReasonSwarm covers the opt-in boundary from the server's
// side: a swarm task is updatable exactly when the host is a manager and the
// operator has granted the services API.
func TestUpdateBlockedReasonSwarm(t *testing.T) {
	task := &store.ContainerWithUpdate{}
	task.Managed = true
	task.Running = true
	task.Name = "web.1"
	task.SwarmTaskID = "task1"
	task.SwarmServiceID = "svc1"
	task.SwarmServiceName = "web"

	managerGranted := &store.Server{SwarmRole: store.SwarmManager, SwarmServices: true}
	if reason := updateBlockedReason(task, managerGranted); reason != "" {
		t.Errorf("a swarm task on a granted manager should be updatable: %q", reason)
	}

	managerDenied := &store.Server{SwarmRole: store.SwarmManager, SwarmServices: false}
	reason := updateBlockedReason(task, managerDenied)
	if !strings.Contains(reason, "opt-in") || !strings.Contains(reason, "SERVICES=1") {
		t.Errorf("a denied manager should point at the opt-in: %q", reason)
	}
	if !strings.Contains(reason, "service web") {
		t.Errorf("the message should name the service, not the replica: %q", reason)
	}

	worker := &store.Server{SwarmRole: store.SwarmWorker}
	if reason := updateBlockedReason(task, worker); !strings.Contains(reason, "worker node") {
		t.Errorf("a worker should be told to use a manager: %q", reason)
	}

	// A stale report (host no longer reporting as swarm) must not silently
	// fall through to the container path.
	if reason := updateBlockedReason(task, &store.Server{}); !strings.Contains(reason, "not currently reporting") {
		t.Errorf("unexpected message: %q", reason)
	}
}

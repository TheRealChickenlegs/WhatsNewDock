package dockerx

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

func TestFormatPorts(t *testing.T) {
	ports := []container.Port{
		{PrivatePort: 53, Type: "udp"},
		{IP: "0.0.0.0", PublicPort: 8080, PrivatePort: 80, Type: "tcp"},
		{IP: "::", PublicPort: 443, PrivatePort: 443, Type: "tcp"},
	}
	got := formatPorts(ports)
	want := []string{"0.0.0.0:8080->80/tcp", "443->443/tcp", "53/udp"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildNetworkingConfig(t *testing.T) {
	settings := &types.NetworkSettings{
		Networks: map[string]*network.EndpointSettings{
			"bridge": {NetworkID: "net1", Aliases: []string{"alias1"}, IPAddress: "172.17.0.2"},
		},
	}
	cfg := buildNetworkingConfig(settings)
	if cfg == nil {
		t.Fatal("expected config")
	}
	ep, ok := cfg.EndpointsConfig["bridge"]
	if !ok {
		t.Fatalf("missing bridge endpoint, got %+v", cfg.EndpointsConfig)
	}
	if ep.NetworkID != "net1" || ep.IPAddress != "172.17.0.2" || len(ep.Aliases) != 1 || ep.Aliases[0] != "alias1" {
		t.Errorf("endpoint = %+v", ep)
	}
}

func TestBuildNetworkingConfigNil(t *testing.T) {
	if buildNetworkingConfig(nil) != nil {
		t.Error("expected nil for nil settings")
	}
	if buildNetworkingConfig(&types.NetworkSettings{}) != nil {
		t.Error("expected nil for empty settings")
	}
}

// TestContainerStack pins grouping precedence: Docker's own labels always win,
// and Podman pods are only a fallback.
func TestContainerStack(t *testing.T) {
	cases := []struct {
		name     string
		labels   map[string]string
		wantName string
		wantKind store.StackKind
	}{
		{name: "none", labels: map[string]string{}, wantName: ""},
		{
			name:     "compose",
			labels:   map[string]string{"com.docker.compose.project": "web"},
			wantName: "web", wantKind: store.StackCompose,
		},
		{
			name:     "swarm",
			labels:   map[string]string{"com.docker.stack.namespace": "prod", "com.docker.compose.project": "web"},
			wantName: "prod", wantKind: store.StackSwarm,
		},
		{
			// A Quadlet container joined to a compose project must not be
			// regrouped by the pod fallback.
			name: "compose wins over pod",
			labels: map[string]string{
				"com.docker.compose.project": "web",
				"io.kubernetes.pod.name":     "mypod",
			},
			wantName: "web", wantKind: store.StackCompose,
		},
		{
			name:     "pod fallback",
			labels:   map[string]string{"io.kubernetes.pod.name": "mypod"},
			wantName: "mypod", wantKind: store.StackPod,
		},
	}
	for _, c := range cases {
		gotName, gotKind := containerStack(c.labels)
		if gotName != c.wantName || (gotName != "" && gotKind != c.wantKind) {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.name, gotName, gotKind, c.wantName, c.wantKind)
		}
	}
}

// TestQuadletManagedDetection documents the exact label that gates one-click
// updates, and that its absence leaves a container updatable.
func TestQuadletManagedDetection(t *testing.T) {
	// A compose container on a Docker host: never managed.
	if unit := podmanSystemdUnit(map[string]string{"com.docker.compose.project": "web"}); unit != "" {
		t.Errorf("docker container reported as systemd-managed: %q", unit)
	}
	// A Podman container started by hand on a Podman host: also not managed.
	if unit := podmanSystemdUnit(map[string]string{"io.containers.autoupdate": "registry"}); unit != "" {
		t.Errorf("autoupdate-only container reported as systemd-managed: %q", unit)
	}
	// A Quadlet unit's container: managed.
	if unit := podmanSystemdUnit(map[string]string{"PODMAN_SYSTEMD_UNIT": "web.service"}); unit != "web.service" {
		t.Errorf("quadlet unit not detected: %q", unit)
	}
}

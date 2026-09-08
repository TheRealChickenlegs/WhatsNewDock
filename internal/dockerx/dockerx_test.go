package dockerx

import (
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
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

package dockerx

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
)

func TestSelfUpdateHelperConfig(t *testing.T) {
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{"whatsnewdock": {}},
	}
	cfg, host := SelfUpdateHelperConfig(
		"ghcr.io/therealchickenlegs/whatsnewdock:0.1.1", "abc123def456",
		"ghcr.io/therealchickenlegs/whatsnewdock:v0.1.2",
		HelperAccess{DockerHost: "tcp://socket-proxy:2375", Network: netCfg})

	// The helper runs the image that is already on the host, so the code doing
	// the swap is the code proven to run there — not the image being deployed.
	if cfg.Image != "ghcr.io/therealchickenlegs/whatsnewdock:0.1.1" {
		t.Errorf("helper image = %q", cfg.Image)
	}
	env := strings.Join(cfg.Env, " ")
	for _, want := range []string{
		"WND_MODE=" + string(config.ModeSelfUpdate),
		EnvSelfUpdateContainer + "=abc123def456",
		EnvSelfUpdateImage + "=ghcr.io/therealchickenlegs/whatsnewdock:v0.1.2",
		"WND_DOCKER_HOST=tcp://socket-proxy:2375",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("helper env missing %q: %v", want, cfg.Env)
		}
	}
	// It must reach the same Docker endpoint, so it joins our network.
	if string(host.NetworkMode) != "whatsnewdock" {
		t.Errorf("helper network = %q", host.NetworkMode)
	}
	// And it must clean itself up: the app is not around to do it afterwards.
	if !host.AutoRemove {
		t.Error("helper should be removed when it exits")
	}
	if cfg.Labels[SelfUpdateHelperLabel] != SelfUpdateHelperRole {
		t.Errorf("helper label = %v", cfg.Labels)
	}
}

func TestSelfUpdateHelperConfigNoNetwork(t *testing.T) {
	cfg, host := SelfUpdateHelperConfig("img:1", "id", "img:2",
		HelperAccess{DockerHost: "unix:///var/run/docker.sock"})
	if host.NetworkMode != "" {
		t.Errorf("no network should be set when we have none: %q", host.NetworkMode)
	}
	if len(cfg.Env) != 4 {
		t.Errorf("env = %v", cfg.Env)
	}
}

// TestHelperAccessCarriesTheSocket is the bug the live test caught: a
// socket-mounted deployment gave the helper no way to reach the daemon, so it
// started, failed and was removed before anyone could read its logs.
func TestHelperAccessCarriesTheSocket(t *testing.T) {
	insp := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				Binds:    []string{"/var/run/docker.sock:/var/run/docker.sock:ro", "/srv/app:/app"},
				GroupAdd: []string{"963"},
			},
		},
		Mounts: []container.MountPoint{
			{Type: mount.TypeBind, Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: false},
			{Type: mount.TypeVolume, Name: "data", Source: "/var/lib/docker/volumes/data", Destination: "/data", RW: true},
		},
	}
	acc := helperAccess(insp, "unix:///var/run/docker.sock")

	if len(acc.Binds) != 1 || acc.Binds[0] != "/var/run/docker.sock:/var/run/docker.sock:ro" {
		t.Errorf("socket bind not carried: %v", acc.Binds)
	}
	// The data volume must NOT be handed to the helper.
	for _, m := range acc.Mounts {
		if m.Target == "/data" {
			t.Error("the helper was given the data volume")
		}
	}
	// Only one form may be passed, or the daemon rejects the duplicate mount.
	if len(acc.Mounts) != 0 {
		t.Errorf("the bind form already covers the socket; mounts must be empty: %+v", acc.Mounts)
	}
	if len(acc.GroupAdd) != 1 || acc.GroupAdd[0] != "963" {
		t.Errorf("group not carried: %v", acc.GroupAdd)
	}

	// Against a socket proxy no mounts are needed — the network is enough — and
	// nothing is copied from the host config.
	proxy := helperAccess(insp, "tcp://socket-proxy:2375")
	if len(proxy.Binds) != 0 || len(proxy.Mounts) != 0 {
		t.Errorf("socket proxy should not inherit mounts: %+v", proxy)
	}
}

func TestBindDestination(t *testing.T) {
	for in, want := range map[string]string{
		"/var/run/docker.sock:/var/run/docker.sock:ro": "/var/run/docker.sock",
		"/a:/b":     "/b",
		"named:/c":  "/c",
		"/onlypath": "/onlypath",
	} {
		if got := bindDestination(in); got != want {
			t.Errorf("bindDestination(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHelperAccessUsesMountsWhenThereAreNoBinds covers a socket attached with
// --mount rather than -v, where the resolved mount is all we get.
func TestHelperAccessUsesMountsWhenThereAreNoBinds(t *testing.T) {
	insp := container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			HostConfig: &container.HostConfig{GroupAdd: []string{"963"}},
		},
		Mounts: []container.MountPoint{
			{Type: mount.TypeBind, Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: false},
		},
	}
	acc := helperAccess(insp, "unix:///var/run/docker.sock")
	if len(acc.Mounts) != 1 || acc.Mounts[0].Target != "/var/run/docker.sock" || !acc.Mounts[0].ReadOnly {
		t.Fatalf("mount not carried: %+v", acc.Mounts)
	}
	if len(acc.Binds) != 0 {
		t.Errorf("binds = %v", acc.Binds)
	}
	if len(acc.GroupAdd) != 1 {
		t.Errorf("group not carried: %v", acc.GroupAdd)
	}
}

// TestStartSelfUpdateCreatesHelper covers the launch path against a fake
// daemon: it must inspect us, pull first, and start exactly one helper.
func TestStartSelfUpdateCreatesHelper(t *testing.T) {
	self := baseContainer("self1", "whatsnewdock", "ghcr.io/x/whatsnewdock:0.1.1", "sha256:self", true)
	fake := newFakeDaemon(self)

	if err := startSelfUpdate(context.Background(), fake, "self1",
		"ghcr.io/x/whatsnewdock:v0.1.2", "tcp://proxy:2375"); err != nil {
		t.Fatalf("startSelfUpdate: %v", err)
	}
	if fake.createdCfg == nil {
		t.Fatal("no helper container was created")
	}
	if fake.createdCfg.Image != "ghcr.io/x/whatsnewdock:0.1.1" {
		t.Errorf("helper image = %q", fake.createdCfg.Image)
	}
	if !strings.Contains(fake.actionsString(), "create:whatsnewdock-self-update") {
		t.Errorf("actions = %s", fake.actionsString())
	}
	if !strings.Contains(fake.actionsString(), "start:whatsnewdock-self-update") {
		t.Errorf("helper was not started: %s", fake.actionsString())
	}
	// The running container must not be touched: the helper does that.
	if strings.Contains(fake.actionsString(), "stop:whatsnewdock") {
		t.Errorf("the app was stopped before the helper could take over: %s", fake.actionsString())
	}
}

func TestStartSelfUpdateRefusesSameImage(t *testing.T) {
	self := baseContainer("self1", "whatsnewdock", "ghcr.io/x/whatsnewdock:0.1.1", "sha256:self", true)
	fake := newFakeDaemon(self)
	err := startSelfUpdate(context.Background(), fake, "self1", "ghcr.io/x/whatsnewdock:0.1.1", "tcp://p:2375")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
}

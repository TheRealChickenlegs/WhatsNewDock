package dockerx

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
)

// These tests run against a real container runtime. They are skipped unless
// WND_PODMAN_TEST_HOST points at a Docker-compatible socket, e.g.
//
//	podman system service --time=0 unix:///tmp/wnd-podman.sock &
//	WND_PODMAN_TEST_HOST=unix:///tmp/wnd-podman.sock go test ./internal/dockerx/ -run Live -v
//
// They exist because the recreate flow is the one part of this package that
// cannot be proven with fakes alone: whether a real daemon accepts the
// configuration we hand back to ContainerCreate, and whether a stopped
// container really does come back under the same name.

const (
	liveImageOld = "docker.io/library/alpine:3.20"
	liveImageNew = "docker.io/library/alpine:3.21"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	host := os.Getenv("WND_PODMAN_TEST_HOST")
	if host == "" {
		t.Skip("set WND_PODMAN_TEST_HOST to run the live runtime tests")
	}
	c, err := New(config.DockerConfig{Host: host})
	if err != nil {
		t.Fatalf("connect to %s: %v", host, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func liveImageID(t *testing.T, c *Client, ref string) string {
	t.Helper()
	insp, err := c.cli.ImageInspect(context.Background(), ref)
	if err != nil {
		t.Fatalf("inspect image %s: %v", ref, err)
	}
	return insp.ID
}

func liveRemove(t *testing.T, c *Client, name string) {
	t.Helper()
	ctx := context.Background()
	found := findContainerByName(ctx, c.cli, name)
	if found == nil {
		return
	}
	if err := c.cli.ContainerRemove(ctx, found.ID, container.RemoveOptions{Force: true}); err != nil {
		t.Logf("cleanup %s: %v", name, err)
	}
}

// liveCreate starts a long-running container from ref, optionally labelled as
// systemd-managed.
func liveCreate(t *testing.T, c *Client, name, ref string, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	liveRemove(t, c, name)
	created, err := c.cli.ContainerCreate(ctx, &container.Config{
		Image:  ref,
		Cmd:    []string{"sleep", "600"},
		Labels: labels,
	}, &container.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := c.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	return created.ID
}

// TestLiveDirectRecreate is the end-to-end happy path: a plain container is
// recreated on a newer tag, comes back running, and leaves nothing behind.
func TestLiveDirectRecreate(t *testing.T) {
	c := liveClient(t)
	const name = "wnd-live-direct"
	liveCreate(t, c, name, liveImageOld, nil)
	t.Cleanup(func() { liveRemove(t, c, name) })

	wantImage := liveImageID(t, c, liveImageNew)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.RecreateContainer(ctx, findOrFail(t, c, name).ID, liveImageNew, nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	got := findOrFail(t, c, name)
	if got.State != "running" {
		t.Errorf("container state = %q, want running", got.State)
	}
	insp, err := c.cli.ContainerInspect(ctx, got.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if insp.Image != wantImage {
		t.Errorf("container image = %s, want %s (the freshly pulled tag)", insp.Image, wantImage)
	}
	if leftovers := liveFindLike(t, c, name+"-prev-"); len(leftovers) > 0 {
		t.Errorf("rollback backups were left behind: %v", leftovers)
	}
}

// TestLiveSystemdManagedRequiresRunning checks the fail-fast path against a real
// daemon: a stopped systemd-managed container must be refused, not stopped and
// left for a timeout.
func TestLiveSystemdManagedRequiresRunning(t *testing.T) {
	c := liveClient(t)
	const name = "wnd-live-quadlet-stopped"
	liveCreate(t, c, name, liveImageOld, map[string]string{"PODMAN_SYSTEMD_UNIT": name + ".service"})
	t.Cleanup(func() { liveRemove(t, c, name) })

	ctx2 := context.Background()
	id := findOrFail(t, c, name).ID
	timeout := 5
	if err := c.cli.ContainerStop(ctx2, id, container.StopOptions{Timeout: &timeout}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx2, 30*time.Second)
	defer cancel()
	err := c.RecreateContainer(ctx, id, liveImageNew, nil)
	if err == nil {
		t.Fatal("expected the update of a stopped systemd-managed container to be refused")
	}
	if !strings.Contains(err.Error(), "systemctl start "+name+".service") {
		t.Errorf("error should name the unit to start: %v", err)
	}
	if findOrFail(t, c, name).ID != id {
		t.Error("the container was disturbed by a refused update")
	}
}

// TestLiveSystemdManagedRespawn runs the Quadlet path against a real daemon with
// a stand-in for the systemd unit: when the container stops, the stand-in
// removes it and starts a fresh one under the same name from the newer tag,
// exactly as a Quadlet unit's ExecStop + Restart= policy does.
func TestLiveSystemdManagedRespawn(t *testing.T) {
	c := liveClient(t)
	const name = "wnd-live-quadlet"
	const unit = name + ".service"
	liveCreate(t, c, name, liveImageOld, map[string]string{"PODMAN_SYSTEMD_UNIT": unit})
	t.Cleanup(func() { liveRemove(t, c, name) })

	wantImage := liveImageID(t, c, liveImageNew)
	stop := make(chan struct{})
	done := make(chan struct{})
	go fakeSystemdUnit(c, name, unit, wantImage, stop, done)
	defer func() {
		close(stop)
		<-done
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.RecreateContainer(ctx, findOrFail(t, c, name).ID, liveImageNew, nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}

	got := findOrFail(t, c, name)
	if got.State != "running" {
		t.Errorf("container state = %q, want running", got.State)
	}
	insp, err := c.cli.ContainerInspect(ctx, got.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if insp.Image != wantImage {
		t.Errorf("container image = %s, want %s", insp.Image, wantImage)
	}
	if insp.Config.Labels["PODMAN_SYSTEMD_UNIT"] != unit {
		t.Errorf("the unit label was lost in the respawn: %v", insp.Config.Labels)
	}
}

// fakeSystemdUnit stands in for the unit's ExecStop + Restart= policy.
func fakeSystemdUnit(c *Client, name, unit, image string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ctx := context.Background()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		found := findContainerByName(ctx, c.cli, name)
		if found == nil || found.State == "running" {
			continue
		}
		// ExecStop removes the container, then the unit starts it again from
		// the unit definition (which now resolves to the pulled tag).
		_ = c.cli.ContainerRemove(ctx, found.ID, container.RemoveOptions{Force: true})
		created, err := c.cli.ContainerCreate(ctx, &container.Config{
			Image:  image,
			Cmd:    []string{"sleep", "600"},
			Labels: map[string]string{"PODMAN_SYSTEMD_UNIT": unit},
		}, &container.HostConfig{}, nil, nil, name)
		if err != nil {
			continue
		}
		_ = c.cli.ContainerStart(ctx, created.ID, container.StartOptions{})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func findOrFail(t *testing.T, c *Client, name string) *containerRef {
	t.Helper()
	found := findContainerByName(context.Background(), c.cli, name)
	if found == nil {
		t.Fatalf("no container named %q", name)
	}
	return found
}

func liveFindLike(t *testing.T, c *Client, prefix string) []string {
	t.Helper()
	all, err := c.cli.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		t.Fatalf("list containers: %v", err)
	}
	var out []string
	for _, s := range all {
		for _, n := range s.Names {
			n = strings.TrimPrefix(n, "/")
			if strings.HasPrefix(n, prefix) {
				out = append(out, fmt.Sprintf("%s (%s)", n, s.State))
			}
		}
	}
	return out
}

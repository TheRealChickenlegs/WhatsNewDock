package dockerx

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
)

// Live swarm tests run against a real Docker daemon. They are skipped unless
// WND_DOCKER_TEST_HOST points at a reachable socket, e.g.
//
//	sg docker -c "WND_DOCKER_TEST_HOST=unix:///var/run/docker.sock go test ./internal/dockerx/ -run LiveSwarm -v"
//
// A clean single-node swarm is the only way to prove the service-update path:
// the convergence rules, the forced roll on a floating tag, and the rollback
// when the new tasks never become healthy.
//
// These tests take over the daemon's swarm state, so they refuse to run on a
// daemon that is already part of a swarm and always leave afterwards.
const (
	liveSwarmImage = "docker.io/library/alpine:3.21"
)

func liveSwarmClient(t *testing.T) *Client {
	t.Helper()
	host := os.Getenv("WND_DOCKER_TEST_HOST")
	if host == "" {
		t.Skip("set WND_DOCKER_TEST_HOST to run the live swarm tests")
	}
	c, err := New(config.DockerConfig{Host: host})
	if err != nil {
		t.Fatalf("connect to %s: %v", host, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestLiveSwarm exercises the whole service-update path against a real daemon.
func TestLiveSwarm(t *testing.T) {
	c := liveSwarmClient(t)
	ctx := context.Background()

	info, err := c.cli.Info(ctx)
	if err != nil {
		t.Fatalf("docker info: %v", err)
	}
	if info.Swarm.LocalNodeState != swarm.LocalNodeStateInactive {
		t.Skipf("daemon is already part of a swarm (%s); refusing to touch it", info.Swarm.LocalNodeState)
	}

	if _, err := c.cli.SwarmInit(ctx, swarm.InitRequest{
		ListenAddr:    "0.0.0.0:2377",
		AdvertiseAddr: liveAdvertiseAddr(t),
	}); err != nil {
		t.Fatalf("swarm init: %v", err)
	}
	t.Cleanup(func() {
		leave, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := c.cli.SwarmLeave(leave, true); err != nil {
			t.Logf("swarm leave: %v", err)
		}
	})

	// The role only becomes manager once the swarm is up.
	status, err := c.SwarmStatus(ctx)
	if err != nil {
		t.Fatalf("swarm status: %v", err)
	}
	if status.Role != "manager" {
		t.Fatalf("role = %q, want manager on a single-node swarm", status.Role)
	}
	if !status.ServicesAvailable {
		t.Fatal("the services API should be available when talking to a daemon directly")
	}

	t.Run("updates a service onto a new tag", func(t *testing.T) {
		svc := liveCreateService(t, c, "wnd-live-update", liveSwarmImage, []string{"sleep", "600"}, 1)
		liveWaitForService(t, c, svc, 1)

		// alpine:3.21 -> 3.20 is still a real tag change, so the spec moves.
		if err := c.UpdateService(ctx, svc, "docker.io/library/alpine:3.20", nil); err != nil {
			t.Fatalf("update service: %v", err)
		}
		liveAssertService(t, c, svc, "docker.io/library/alpine:3.20", 1)
	})

	t.Run("forces a roll when the tag has not changed", func(t *testing.T) {
		svc := liveCreateService(t, c, "wnd-live-force", liveSwarmImage, []string{"sleep", "600"}, 1)
		liveWaitForService(t, c, svc, 1)

		// Same reference: without the force counter swarm would consider the
		// spec unchanged and roll nothing out.
		if err := c.UpdateService(ctx, svc, liveSwarmImage, nil); err != nil {
			t.Fatalf("forced update: %v", err)
		}
		liveAssertService(t, c, svc, liveSwarmImage, 1)
	})

	t.Run("rolls back when the new tasks never become healthy", func(t *testing.T) {
		fastConverge(t)
		svc := liveCreateService(t, c, "wnd-live-bad", liveSwarmImage, []string{"sleep", "600"}, 1)
		liveWaitForService(t, c, svc, 1)

		// Point the service at a command that exits immediately, so the task
		// never reaches a stable running state and the rollout cannot converge.
		if err := liveSetCommand(ctx, c, svc, []string{"sh", "-c", "exit 1"}); err != nil {
			t.Fatalf("set command: %v", err)
		}
		liveWaitForService(t, c, svc, 1)

		err := c.UpdateService(ctx, svc, liveSwarmImage, nil)
		if err == nil {
			t.Fatal("a rollout that never converges must not be reported as success")
		}
		// Either we rolled it back, or swarm did; both are honest failures.
		t.Logf("rollback reported: %v", err)
	})
}

// liveAdvertiseAddr finds the address the host would use to reach the network.
// A UDP "connection" sends nothing; it only asks the kernel which source
// address it would pick, which is the same choice `docker swarm init` makes.
func liveAdvertiseAddr(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("WND_SWARM_ADVERTISE_ADDR"); v != "" {
		return v
	}
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		t.Skipf("cannot determine an advertise address: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// liveCreateService creates a replicated service with a fixed command.
func liveCreateService(t *testing.T, c *Client, name, image string, cmd []string, replicas uint64) string {
	t.Helper()
	ctx := context.Background()
	resp, err := c.cli.ServiceCreate(ctx, swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: image, Command: cmd},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
	}, swarm.ServiceCreateOptions{})
	if err != nil {
		t.Fatalf("create service %s: %v", name, err)
	}
	t.Cleanup(func() {
		rm, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := c.cli.ServiceRemove(rm, resp.ID); err != nil {
			t.Logf("remove service %s: %v", name, err)
		}
	})
	return resp.ID
}

// liveSetCommand changes only the command, so the image stays put.
func liveSetCommand(ctx context.Context, c *Client, serviceID string, cmd []string) error {
	svc, _, err := c.cli.ServiceInspectWithRaw(ctx, serviceID, swarm.ServiceInspectOptions{})
	if err != nil {
		return err
	}
	spec := svc.Spec
	spec.TaskTemplate.ContainerSpec.Command = cmd
	spec.TaskTemplate.ForceUpdate++
	_, err = c.cli.ServiceUpdate(ctx, serviceID, svc.Meta.Version, spec, swarm.ServiceUpdateOptions{})
	return err
}

// liveServiceStatus reads a service through the list endpoint, which is the
// only one that reports task counts (ServiceInspect leaves ServiceStatus nil).
func liveServiceStatus(t *testing.T, c *Client, serviceID string) (image string, running, desired uint64, updateState string) {
	t.Helper()
	list, err := c.cli.ServiceList(context.Background(), swarm.ServiceListOptions{
		Filters: filters.NewArgs(filters.Arg("id", serviceID)),
		Status:  true,
	})
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	for _, svc := range list {
		if svc.ID != serviceID {
			continue
		}
		if svc.Spec.TaskTemplate.ContainerSpec != nil {
			image = svc.Spec.TaskTemplate.ContainerSpec.Image
		}
		if svc.ServiceStatus != nil {
			running, desired = svc.ServiceStatus.RunningTasks, svc.ServiceStatus.DesiredTasks
		}
		if svc.UpdateStatus != nil {
			updateState = string(svc.UpdateStatus.State)
		}
		return image, running, desired, updateState
	}
	t.Fatalf("service %s not found", serviceID)
	return
}

// liveWaitForService blocks until every desired task is running.
func liveWaitForService(t *testing.T, c *Client, serviceID string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, running, _, _ := liveServiceStatus(t, c, serviceID); running >= want {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	_, running, desired, state := liveServiceStatus(t, c, serviceID)
	t.Fatalf("service %s never reached %d running task(s) (running=%d desired=%d update=%q)",
		serviceID, want, running, desired, state)
}

func liveAssertService(t *testing.T, c *Client, serviceID, wantImage string, wantRunning uint64) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		img, running, _, _ := liveServiceStatus(t, c, serviceID)
		// The daemon pins the resolved digest onto the spec image and drops the
		// docker.io/library prefix, so compare the tag rather than the string.
		if liveImageTag(img) == liveImageTag(wantImage) && running >= wantRunning {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	img, running, _, state := liveServiceStatus(t, c, serviceID)
	t.Fatalf("service did not settle: image=%q running=%d update=%q, want tag %q running=%d",
		img, running, state, liveImageTag(wantImage), wantRunning)
}

// liveImageTag reduces an image reference to its tag, ignoring any digest and
// registry prefix the daemon has added.
func liveImageTag(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[i+1:]
	}
	return "latest"
}

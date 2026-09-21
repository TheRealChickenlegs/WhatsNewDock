package dockerx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/swarm"

	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// ---------------------------------------------------------------------------
// detection
// ---------------------------------------------------------------------------

func TestSwarmTaskFromLabels(t *testing.T) {
	labels := map[string]string{
		"com.docker.swarm.task.id":      "task1",
		"com.docker.swarm.task.name":    "web.1",
		"com.docker.swarm.node.id":      "node1",
		"com.docker.swarm.service.id":   "svc1",
		"com.docker.swarm.service.name": "web",
	}
	got := swarmTask(labels)
	if got.ServiceID != "svc1" || got.ServiceName != "web" || got.TaskID != "task1" || got.NodeID != "node1" {
		t.Fatalf("swarmTask = %+v", got)
	}
	if !IsSwarmTask(labels) {
		t.Error("IsSwarmTask should be true for a task container")
	}

	// A compose container carries none of these.
	compose := map[string]string{"com.docker.compose.project": "web", "com.docker.compose.service": "web"}
	if IsSwarmTask(compose) {
		t.Error("a compose container must not be treated as a swarm task")
	}
	if sw := swarmTask(compose); sw.TaskID != "" || sw.ServiceID != "" {
		t.Errorf("compose labels produced swarm identity: %+v", sw)
	}
}

func TestSwarmRoleFromInfo(t *testing.T) {
	cases := []struct {
		name string
		in   swarm.Info
		want store.SwarmRole
	}{
		{"inactive", swarm.Info{LocalNodeState: swarm.LocalNodeStateInactive}, store.SwarmNone},
		{"empty", swarm.Info{}, store.SwarmNone},
		{"manager", swarm.Info{LocalNodeState: swarm.LocalNodeStateActive, ControlAvailable: true}, store.SwarmManager},
		{"worker", swarm.Info{LocalNodeState: swarm.LocalNodeStateActive}, store.SwarmWorker},
		{"pending", swarm.Info{LocalNodeState: swarm.LocalNodeStatePending}, store.SwarmWorker},
	}
	for _, c := range cases {
		if got := swarmRoleFrom(c.in); got != c.want {
			t.Errorf("%s: role = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIsServiceAPIDenied(t *testing.T) {
	for _, msg := range []string{
		"Error response from daemon: 403 Forbidden",
		"permission denied while trying to connect",
		"services are disabled in the socket proxy",
		"not authorized",
	} {
		if !isServiceAPIDenied(errors.New(msg)) {
			t.Errorf("%q should read as a denial", msg)
		}
	}
	if isServiceAPIDenied(errors.New("context deadline exceeded")) {
		t.Error("a timeout is not a denial")
	}
	if isServiceAPIDenied(nil) {
		t.Error("nil is not a denial")
	}
}

// ---------------------------------------------------------------------------
// convergence
// ---------------------------------------------------------------------------

func TestClassifyServiceConvergence(t *testing.T) {
	cases := []struct {
		name     string
		state    serviceState
		deadline bool
		want     convergenceState
	}{
		{"still rolling out", serviceState{Running: 1, Desired: 3, UpdateState: "updating"}, false, convergePending},
		{"completed and running", serviceState{Running: 3, Desired: 3, UpdateState: "completed"}, false, convergeDone},
		{"completed but short", serviceState{Running: 1, Desired: 3, UpdateState: "completed"}, false, convergePending},
		// A rolling update keeps running == desired the whole time it replaces
		// tasks, and an absent status is a rollout that has not started yet.
		// Neither may be mistaken for success.
		{"rolling but fully running", serviceState{Running: 2, Desired: 2, UpdateState: "updating"}, false, convergePending},
		{"no status yet", serviceState{Running: 2, Desired: 2}, false, convergePending},
		{"no status at deadline", serviceState{Running: 2, Desired: 2}, true, convergeFailed},
		{"daemon rolled back", serviceState{Running: 3, Desired: 3, UpdateState: "rollback_completed"}, false, convergeRolledBack},
		{"deadline with crash loop", serviceState{Running: 0, Desired: 3, UpdateState: "updating"}, true, convergeFailed},
		{"deadline but running now", serviceState{Running: 3, Desired: 3, UpdateState: "completed"}, true, convergeDone},
	}
	for _, c := range cases {
		if got := classifyServiceConvergence(c.state, c.deadline); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

func TestRecreateDispatchSwarmTask(t *testing.T) {
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	c.labels = map[string]string{
		"com.docker.swarm.task.id":      "task1",
		"com.docker.swarm.service.id":   "svc1",
		"com.docker.swarm.service.name": "web",
	}
	fake := newFakeDaemon(c)
	svc := fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 1})
	svc.updateState = string(swarm.UpdateStateUpdating)

	if err := recreateContainer(context.Background(), fake, "old1", "nginx:1.26", nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	// The task container must not be touched at all.
	if a := fake.actionsString(); strings.Contains(a, "rename:") || strings.Contains(a, "create:") || strings.Contains(a, "stop:") {
		t.Errorf("a swarm task went through the direct container flow: %s", a)
	}
	if svc.image != "nginx:1.26" {
		t.Errorf("service image = %q, want nginx:1.26", svc.image)
	}
	if !strings.Contains(fake.actionsString(), "service-update:web->nginx:1.26") {
		t.Errorf("no service update recorded: %s", fake.actionsString())
	}
}

// ---------------------------------------------------------------------------
// the opt-in boundary
// ---------------------------------------------------------------------------

// TestUpdateServiceDeniedIsOptIn is the whole opt-in contract: with SERVICES=0
// the operator gets one actionable sentence, not a raw 403.
func TestUpdateServiceDeniedIsOptIn(t *testing.T) {
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	fake.failServiceAPI = errors.New("Error response from daemon: 403 Forbidden")

	err := updateService(context.Background(), fake, "svc1", "nginx:1.26", nil)
	if !errors.Is(err, ErrSwarmDisabled) {
		t.Fatalf("err = %v, want ErrSwarmDisabled", err)
	}
	if !strings.Contains(err.Error(), "SERVICES=1") {
		t.Errorf("message should name the fix: %v", err)
	}
}

func TestServicesAvailableProbe(t *testing.T) {
	open := newFakeDaemon(baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true))
	open.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 1})
	denied := newFakeDaemon(baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true))
	denied.failServiceAPI = errors.New("403 Forbidden")

	var cap swarmCapability
	tick := 0
	now := func() time.Time { tick++; return time.Unix(int64(tick), 0) }

	// Only a manager is ever probed, so a worker never pays for the call.
	if probeServices(context.Background(), denied, store.SwarmWorker, &cap, now) {
		t.Error("a worker must never report services as available")
	}
	if probeServices(context.Background(), denied, store.SwarmNone, &cap, now) {
		t.Error("a non-swarm host must never report services as available")
	}

	// A manager with the services API denied is the default, opt-in-off state.
	if probeServices(context.Background(), denied, store.SwarmManager, &cap, now) {
		t.Error("a denied proxies must not report services as available")
	}

	// A granted manager reports available.
	var cap2 swarmCapability
	if !probeServices(context.Background(), open, store.SwarmManager, &cap2, now) {
		t.Error("a granted services API should report available")
	}

	// The answer is cached: a second call inside the TTL does not re-probe.
	before := open.serviceListCalls
	if !probeServices(context.Background(), open, store.SwarmManager, &cap2, now) {
		t.Error("cached result should still be true")
	}
	if open.serviceListCalls != before {
		t.Errorf("probe was repeated inside the TTL (%d -> %d)", before, open.serviceListCalls)
	}
}

// ---------------------------------------------------------------------------
// rollout behaviour
// ---------------------------------------------------------------------------

func TestUpdateServiceForcesRollOnFloatingTag(t *testing.T) {
	// The spec already points at the tag we are asked to deploy, which is what
	// a floating tag looks like: swarm would consider the spec unchanged and do
	// nothing, so we must bump the force counter.
	c := baseContainer("old1", "web.1", "nginx:latest", "sha256:old", true)
	fake := newFakeDaemon(c)
	svc := fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:latest", desired: 1})

	if err := updateService(context.Background(), fake, "svc1", "nginx:latest", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(fake.serviceUpdCalls) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(fake.serviceUpdCalls))
	}
	if svc.spec.TaskTemplate.ForceUpdate != 1 {
		t.Errorf("ForceUpdate = %d, want 1", svc.spec.TaskTemplate.ForceUpdate)
	}
}

func TestUpdateServiceDoesNotForceOnTagChange(t *testing.T) {
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	svc := fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 1})

	if err := updateService(context.Background(), fake, "svc1", "nginx:1.26", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if svc.spec.TaskTemplate.ForceUpdate != 0 {
		t.Errorf("ForceUpdate = %d; a changed tag already triggers a rollout", svc.spec.TaskTemplate.ForceUpdate)
	}
}

func fastConverge(t *testing.T) {
	t.Helper()
	oldTimeout, oldPoll := serviceConvergeTimeout, serviceConvergePoll
	serviceConvergeTimeout, serviceConvergePoll = 120*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { serviceConvergeTimeout, serviceConvergePoll = oldTimeout, oldPoll })
}

// TestUpdateServiceRollsBackOnTimeout is the safety contract: a rollout that
// never settles is undone, never reported as success.
func TestUpdateServiceRollsBackOnTimeout(t *testing.T) {
	fastConverge(t)
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	svc := fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 3})
	// The new tasks never become healthy.
	svc.convergeAfter = 1 << 30

	err := updateService(context.Background(), fake, "svc1", "nginx:1.26", nil)
	if err == nil {
		t.Fatal("a rollout that never converges must be a failure")
	}
	if !strings.Contains(err.Error(), "previous version was restored") {
		t.Errorf("error should say it was rolled back: %v", err)
	}
	var sawRollback bool
	for _, call := range fake.serviceUpdCalls {
		if call.Rollback == "previous" {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Errorf("no rollback was issued: %s", fake.actionsString())
	}
}

// TestUpdateServiceDaemonRollbackIsFailure covers swarm deciding for itself
// that the new version is bad; we report it and do not roll back twice.
func TestUpdateServiceDaemonRollbackIsFailure(t *testing.T) {
	fastConverge(t)
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	svc := fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 1})
	svc.rollbackOnUpdate = true

	err := updateService(context.Background(), fake, "svc1", "nginx:1.26", nil)
	if err == nil {
		t.Fatal("a daemon rollback must be reported as a failure")
	}
	if !strings.Contains(err.Error(), "rolled it back") {
		t.Errorf("error should say swarm rolled back: %v", err)
	}
	for _, call := range fake.serviceUpdCalls {
		if call.Rollback == "previous" {
			t.Error("we issued a second rollback after swarm already undid the update")
		}
	}
}

func TestUpdateServiceUnknownService(t *testing.T) {
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	err := updateService(context.Background(), fake, "nope", "nginx:1.26", nil)
	if err == nil || !strings.Contains(err.Error(), "no such service") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateServiceUsesQueryRegistry(t *testing.T) {
	c := baseContainer("old1", "web.1", "nginx:1.25", "sha256:old", true)
	fake := newFakeDaemon(c)
	fake.addService(&fakeService{id: "svc1", name: "web", image: "nginx:1.25", desired: 1})

	if err := updateService(context.Background(), fake, "svc1", "nginx:1.26", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !fake.serviceUpdCalls[0].QueryRegistry {
		t.Error("a floating tag needs the registry re-queried for its digest")
	}
}

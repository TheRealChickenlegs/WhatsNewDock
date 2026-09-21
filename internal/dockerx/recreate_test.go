package dockerx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ---------------------------------------------------------------------------
// fake daemon
// ---------------------------------------------------------------------------

type fakeContainer struct {
	id      string
	name    string
	image   string
	ref     string
	running bool
	state   string // overrides the state derived from `running` when set
	labels  map[string]string
	removed bool
}

// fakeDaemon implements containerDaemon with just enough behaviour to exercise
// the recreate flow, including a simulated failure at each step.
type fakeDaemon struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	nextID     int
	actions    []string

	// createdCfg records the last container config passed to ContainerCreate,
	// so tests can assert what we asked the daemon to build.
	createdCfg  *container.Config
	createdHost *container.HostConfig

	failRenameAt string // container id whose rename fails
	failCreate   bool
	failStartAt  string // container id whose start fails
	pullErr      error
	onStop       func(d *fakeDaemon) // simulates the systemd unit reacting to a stop

	// Swarm service state.
	services map[string]*fakeService
	// pulledImageID is what ImageInspect reports after a pull, so a test can
	// model "the tag moved" or "nothing changed".
	pulledImageID string

	failServiceAPI   error // e.g. a socket proxy refusing /services
	failServiceUpd   error
	serviceUpdCalls  []swarm.ServiceUpdateOptions
	serviceListCalls int
}

// fakeService is enough of a swarm service to drive the rollout state machine.
type fakeService struct {
	id      string
	name    string
	image   string
	spec    swarm.ServiceSpec
	version uint64
	running uint64
	desired uint64
	// updateState is what the next inspection reports. Tests move it to model
	// a rollout completing, crash-looping or being rolled back.
	updateState string
	// convergeAfter is how many inspections report "updating" before the
	// service settles, so a test can model a rollout that takes a moment.
	convergeAfter int
	inspections   int
	// rollbackOnUpdate models swarm deciding for itself that the new version
	// is bad and undoing it.
	rollbackOnUpdate bool
}

func (s *fakeService) service() swarm.Service {
	spec := s.spec
	if spec.TaskTemplate.ContainerSpec == nil {
		spec.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{}
	}
	spec.TaskTemplate.ContainerSpec.Image = s.image
	svc := swarm.Service{
		ID:   s.id,
		Meta: swarm.Meta{Version: swarm.Version{Index: s.version}},
		Spec: spec,
	}
	svc.Spec.Annotations.Name = s.name
	svc.ServiceStatus = &swarm.ServiceStatus{RunningTasks: s.running, DesiredTasks: s.desired}
	if s.updateState != "" {
		svc.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateState(s.updateState)}
	}
	return svc
}

func newFakeDaemon(c *fakeContainer) *fakeDaemon {
	return &fakeDaemon{containers: map[string]*fakeContainer{c.id: c}, services: map[string]*fakeService{}}
}

// addService registers a swarm service the fake daemon will answer for.
func (d *fakeDaemon) addService(s *fakeService) *fakeService {
	d.services[s.id] = s
	return s
}

func (d *fakeDaemon) ServiceInspectWithRaw(_ context.Context, serviceID string, _ swarm.ServiceInspectOptions) (swarm.Service, []byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failServiceAPI != nil {
		return swarm.Service{}, nil, d.failServiceAPI
	}
	svc, ok := d.services[serviceID]
	if !ok {
		return swarm.Service{}, nil, errors.New("no such service: " + serviceID)
	}
	return svc.service(), nil, nil
}

func (d *fakeDaemon) ServiceUpdate(_ context.Context, serviceID string, version swarm.Version, spec swarm.ServiceSpec, options swarm.ServiceUpdateOptions) (swarm.ServiceUpdateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failServiceAPI != nil {
		return swarm.ServiceUpdateResponse{}, d.failServiceAPI
	}
	if d.failServiceUpd != nil {
		return swarm.ServiceUpdateResponse{}, d.failServiceUpd
	}
	svc, ok := d.services[serviceID]
	if !ok {
		return swarm.ServiceUpdateResponse{}, errors.New("no such service: " + serviceID)
	}
	d.serviceUpdCalls = append(d.serviceUpdCalls, options)
	if options.Rollback == "previous" {
		d.record("service-rollback:%s", svc.name)
		svc.updateState = string(swarm.UpdateStateRollbackCompleted)
		svc.running = svc.desired
		return swarm.ServiceUpdateResponse{}, nil
	}
	d.record("service-update:%s->%s", svc.name, spec.TaskTemplate.ContainerSpec.Image)
	svc.spec = spec
	svc.image = spec.TaskTemplate.ContainerSpec.Image
	svc.version = version.Index + 1
	svc.running = 0
	svc.inspections = 0
	svc.updateState = string(swarm.UpdateStateUpdating)
	if svc.rollbackOnUpdate {
		svc.updateState = string(swarm.UpdateStateRollbackCompleted)
		svc.running = svc.desired
	}
	return swarm.ServiceUpdateResponse{}, nil
}

func (d *fakeDaemon) ImageInspect(_ context.Context, imageID string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// The pulled reference resolves to whatever the fake was told it is.
	if d.pulledImageID == "" {
		return image.InspectResponse{}, errors.New("no such image: " + imageID)
	}
	return image.InspectResponse{ID: d.pulledImageID}, nil
}

func (d *fakeDaemon) ServiceList(_ context.Context, _ swarm.ServiceListOptions) ([]swarm.Service, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serviceListCalls++
	if d.failServiceAPI != nil {
		return nil, d.failServiceAPI
	}
	out := make([]swarm.Service, 0, len(d.services))
	for _, s := range d.services {
		// Advance the modelled rollout the way the daemon does: a rolling
		// update keeps the old task running, so only the state says when it is
		// finished.
		if s.updateState == string(swarm.UpdateStateUpdating) {
			if s.inspections >= s.convergeAfter {
				s.updateState = string(swarm.UpdateStateCompleted)
				s.running = s.desired
			}
			s.inspections++
		}
		out = append(out, s.service())
	}
	return out, nil
}

func (d *fakeDaemon) record(format string, args ...any) {
	d.actions = append(d.actions, fmt.Sprintf(format, args...))
}

// findByName must be called with the lock held.
func (d *fakeDaemon) findByName(name string) *fakeContainer {
	for _, c := range d.containers {
		if !c.removed && c.name == name {
			return c
		}
	}
	return nil
}

func (d *fakeDaemon) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.containers[id]
	if !ok || c.removed {
		return container.InspectResponse{}, errors.New("no such container: " + id)
	}
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:         c.id,
			Name:       "/" + c.name,
			Image:      c.image,
			State:      &container.State{Running: c.running, Status: container.ContainerState(c.stateOr())},
			HostConfig: &container.HostConfig{},
		},
		Config: &container.Config{Image: c.ref, Labels: c.labels},
	}, nil
}

func (c *fakeContainer) stateOr() string {
	if c.state != "" {
		return c.state
	}
	if c.running {
		return "running"
	}
	return "exited"
}

func (d *fakeDaemon) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	d.mu.Lock()
	c, ok := d.containers[id]
	if !ok || c.removed {
		d.mu.Unlock()
		return errors.New("no such container: " + id)
	}
	d.record("stop:%s", c.name)
	c.running = false
	c.state = "exited"
	hook := d.onStop
	d.mu.Unlock()
	if hook != nil {
		hook(d)
	}
	return nil
}

func (d *fakeDaemon) ContainerRename(_ context.Context, id, newName string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == d.failRenameAt {
		return errors.New("rename refused")
	}
	c, ok := d.containers[id]
	if !ok || c.removed {
		return errors.New("no such container: " + id)
	}
	d.record("rename:%s->%s", c.name, newName)
	c.name = newName
	return nil
}

func (d *fakeDaemon) ContainerCreate(_ context.Context, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.createdCfg = cfg
	d.createdHost = hostCfg
	if d.failCreate {
		return container.CreateResponse{}, errors.New("create refused")
	}
	d.nextID++
	id := fmt.Sprintf("new%d", d.nextID)
	d.containers[id] = &fakeContainer{id: id, name: name, image: "sha256:created", ref: cfg.Image}
	d.record("create:%s", name)
	return container.CreateResponse{ID: id}, nil
}

func (d *fakeDaemon) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == d.failStartAt {
		return errors.New("start refused")
	}
	c, ok := d.containers[id]
	if !ok || c.removed {
		return errors.New("no such container: " + id)
	}
	d.record("start:%s", c.name)
	c.running = true
	c.state = "running"
	return nil
}

func (d *fakeDaemon) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.containers[id]; ok {
		d.record("remove:%s", c.name)
		c.removed = true
	}
	return nil
}

func (d *fakeDaemon) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []container.Summary
	for _, c := range d.containers {
		if c.removed {
			continue
		}
		out = append(out, container.Summary{
			ID:    c.id,
			Names: []string{"/" + c.name},
			State: container.ContainerState(c.stateOr()),
		})
	}
	return out, nil
}

func (d *fakeDaemon) ImagePull(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
	if d.pullErr != nil {
		return nil, d.pullErr
	}
	return io.NopCloser(strings.NewReader("{\"status\":\"Pulling\"}\n")), nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func baseContainer(id, name, ref, imageID string, running bool) *fakeContainer {
	return &fakeContainer{id: id, name: name, ref: ref, image: imageID, running: running}
}

func quadletContainer(id, name, ref, imageID string, running bool) *fakeContainer {
	c := baseContainer(id, name, ref, imageID, running)
	c.labels = map[string]string{"PODMAN_SYSTEMD_UNIT": name + ".service"}
	return c
}

func mustState(t *testing.T, d *fakeDaemon, name string) *fakeContainer {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.findByName(name)
	if c == nil {
		t.Fatalf("no container named %q; have %s", name, d.dumpLocked())
	}
	return c
}

func (d *fakeDaemon) dumpLocked() string {
	var b strings.Builder
	for id, c := range d.containers {
		fmt.Fprintf(&b, "[%s %s running=%v removed=%v] ", id, c.name, c.running, c.removed)
	}
	return b.String()
}

func (d *fakeDaemon) actionsString() string { return strings.Join(d.actions, ",") }

func (d *fakeDaemon) aliveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.containers {
		if !c.removed {
			n++
		}
	}
	return n
}

// respawning mimics a Quadlet unit: the stop makes systemd remove the container
// and bring a fresh one back under the same name, on the given image.
func respawning(imageID, state string) func(*fakeDaemon) {
	return func(d *fakeDaemon) {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, c := range d.containers {
			if !c.removed && !c.running {
				c.removed = true
				d.nextID++
				id := fmt.Sprintf("new%d", d.nextID)
				d.containers[id] = &fakeContainer{
					id: id, name: c.name, image: imageID, ref: c.ref,
					running: state == "running", state: state,
				}
				return
			}
		}
	}
}

// fastRespawn shortens the respawn window so the timeout paths do not take 90
// seconds in tests.
func fastRespawn(t *testing.T) {
	t.Helper()
	oldTimeout, oldPoll := systemdRespawnTimeout, systemdRespawnPoll
	systemdRespawnTimeout, systemdRespawnPoll = 150*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { systemdRespawnTimeout, systemdRespawnPoll = oldTimeout, oldPoll })
}

// ---------------------------------------------------------------------------
// direct recreate
// ---------------------------------------------------------------------------

func TestRecreateDirectHappyPath(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(old)

	if err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	newC := mustState(t, d, "web")
	if newC.id == "old1" {
		t.Fatalf("the original container was not replaced: %s", d.actionsString())
	}
	if !newC.running {
		t.Errorf("replacement is not running: %s", d.actionsString())
	}
	if newC.ref != "nginx:1.26" {
		t.Errorf("replacement image ref = %q", newC.ref)
	}
	if !old.removed {
		t.Errorf("old container was not removed: %s", d.actionsString())
	}
	// The replacement must be created under the original name, without the
	// leading slash inspect reports.
	if !strings.Contains(d.actionsString(), "create:web") {
		t.Errorf("replacement not created under the original name: %s", d.actionsString())
	}
}

// TestRecreateDirectStoppedContainerStaysStopped is the mirror image of the
// recovery cases: an update of a container that was not running must not leave
// it running.
func TestRecreateDirectStoppedContainerStaysStopped(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", false)
	d := newFakeDaemon(old)
	d.failCreate = true

	if err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil); err == nil {
		t.Fatal("expected a create failure")
	}
	if mustState(t, d, "web").running {
		t.Errorf("a container that was stopped was started by the rollback: %s", d.actionsString())
	}
}

// TestRecreateDirectRestoresOnRenameFailure is the "failed to rename old
// container" case. Before the fix the container was left renamed-and-stopped
// and the user had to start it by hand.
func TestRecreateDirectRestoresOnRenameFailure(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(old)
	d.failRenameAt = "old1"

	err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil)
	if err == nil {
		t.Fatal("expected a rename failure")
	}
	if !strings.Contains(err.Error(), "rename old container") {
		t.Errorf("error = %v", err)
	}
	got := mustState(t, d, "web")
	if got.id != "old1" {
		t.Fatalf("original container lost its name: %s", d.actionsString())
	}
	if !got.running {
		t.Errorf("original container was left stopped after a rename failure: %s", d.actionsString())
	}
}

func TestRecreateDirectRestoresOnCreateFailure(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(old)
	d.failCreate = true

	if err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil); err == nil {
		t.Fatal("expected a create failure")
	}
	got := mustState(t, d, "web")
	if got.id != "old1" {
		t.Fatalf("original container did not get its name back: %s", d.actionsString())
	}
	if !got.running {
		t.Errorf("original container was left stopped after a create failure: %s", d.actionsString())
	}
}

func TestRecreateDirectRestoresOnStartFailure(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(old)
	d.failStartAt = "new1" // the id ContainerCreate will hand out

	err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil)
	if err == nil {
		t.Fatal("expected a start failure")
	}
	if !mustState(t, d, "web").running {
		t.Errorf("original container was left stopped after a start failure: %s", d.actionsString())
	}
	if !strings.Contains(d.actionsString(), "remove:web") {
		t.Errorf("failed replacement was not cleaned up: %s", d.actionsString())
	}
	if n := d.aliveCount(); n != 1 {
		t.Errorf("expected 1 remaining container, got %d: %s", n, d.actionsString())
	}
}

func TestRecreatePullFailureLeavesContainerAlone(t *testing.T) {
	old := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(old)
	d.pullErr = errors.New("registry unreachable")

	if err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil); err == nil {
		t.Fatal("expected a pull failure")
	}
	got := mustState(t, d, "web")
	if !got.running || got.id != "old1" {
		t.Errorf("a failed pull disturbed the running container: %s", d.actionsString())
	}
}

// ---------------------------------------------------------------------------
// systemd-managed (Podman Quadlet) recreate
// ---------------------------------------------------------------------------

func TestRecreateSystemdManagedSuccess(t *testing.T) {
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", true)
	d := newFakeDaemon(old)
	d.onStop = respawning("sha256:new", "running")

	if err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if !strings.Contains(d.actionsString(), "stop:immich") {
		t.Errorf("the unit was never triggered: %s", d.actionsString())
	}
	// The rename/create dance must not be attempted for a systemd-managed
	// container: that is exactly what produced "failed to rename old container".
	if strings.Contains(d.actionsString(), "rename:") || strings.Contains(d.actionsString(), "create:") {
		t.Errorf("systemd-managed container went through the direct flow: %s", d.actionsString())
	}
	got := mustState(t, d, "immich")
	if !got.running || got.id == "old1" {
		t.Errorf("no running replacement: %s", d.actionsString())
	}
}

func TestRecreateSystemdManagedRefusesWhenNotRunning(t *testing.T) {
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", false)
	d := newFakeDaemon(old)

	err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "systemctl start immich.service") {
		t.Errorf("error should tell the user how to recover: %v", err)
	}
	if strings.Contains(d.actionsString(), "stop:") {
		t.Errorf("a stopped unit was stopped anyway: %s", d.actionsString())
	}
}

func TestRecreateSystemdManagedTimeoutIsFailure(t *testing.T) {
	fastRespawn(t)
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", true)
	d := newFakeDaemon(old)
	// onStop stays nil: the unit has no Restart= policy and never comes back.

	err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil)
	if err == nil {
		t.Fatal("expected a failure when the unit never respawns")
	}
	if !strings.Contains(err.Error(), "Restart=") {
		t.Errorf("error should explain the requirement: %v", err)
	}
}

func TestRecreateSystemdManagedCrashLoopIsFailure(t *testing.T) {
	fastRespawn(t)
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", true)
	d := newFakeDaemon(old)
	// The unit respawns, but the new container never reaches running.
	d.onStop = respawning("sha256:new", "restarting")

	err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil)
	if err == nil {
		t.Fatal("a crash-looping replacement must not be reported as success")
	}
}

func TestRecreateSystemdManagedPinnedTagIsFailure(t *testing.T) {
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", true)
	d := newFakeDaemon(old)
	// The unit respawns successfully, but on the image it already had: its
	// Image= still points at the old tag, so nothing was actually updated.
	d.onStop = respawning("sha256:old", "running")

	err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil)
	if err == nil {
		t.Fatal("a respawn on the unchanged image must not be reported as success")
	}
	if !strings.Contains(err.Error(), "same image") {
		t.Errorf("error should explain that nothing changed: %v", err)
	}
}

func TestRecreateSystemdManagedIgnoresLookalikeNames(t *testing.T) {
	old := quadletContainer("old1", "immich", "ghcr.io/immich:1.0", "sha256:old", true)
	// A leftover rollback backup from an earlier failed direct update. The
	// daemon's name filter is regex-contains, so a naive lookup finds it too.
	backup := baseContainer("prev1", "immich-prev-123", "ghcr.io/immich:1.0", "sha256:old", true)
	d := newFakeDaemon(old)
	d.containers[backup.id] = backup
	d.onStop = respawning("sha256:new", "running")

	if err := recreateContainer(context.Background(), d, "old1", "ghcr.io/immich:1.1", nil); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	got := mustState(t, d, "immich")
	if got.id == backup.id {
		t.Fatal("the lookalike backup container was mistaken for the respawn")
	}
	if !strings.Contains(d.actionsString(), "stop:immich") {
		t.Errorf("the real container was not stopped: %s", d.actionsString())
	}
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

// TestSanitizeHostConfig covers the Podman round-trip quirk found by the live
// test: Podman reports MemorySwappiness=0 for a container that never set it, and
// crun then refuses to start a container that asks for it on cgroup v2.
func TestSanitizeHostConfig(t *testing.T) {
	zero := int64(0)
	sixty := int64(60)

	if sanitizeHostConfig(nil) != nil {
		t.Error("nil host config should stay nil")
	}
	original := &container.HostConfig{Resources: container.Resources{MemorySwappiness: &zero}, Privileged: true}
	got := sanitizeHostConfig(original)
	if got.MemorySwappiness != nil {
		t.Errorf("Podman's unset swappiness (%d) should be dropped", *got.MemorySwappiness)
	}
	if !got.Privileged {
		t.Error("other host config fields must be preserved")
	}
	if original.MemorySwappiness == nil {
		t.Error("the caller's host config must not be mutated")
	}
	// An explicit non-zero swappiness is left alone.
	explicit := &container.HostConfig{Resources: container.Resources{MemorySwappiness: &sixty}}
	if out := sanitizeHostConfig(explicit); out.MemorySwappiness == nil || *out.MemorySwappiness != 60 {
		t.Errorf("non-zero swappiness was altered: %+v", out.MemorySwappiness)
	}
}

func TestRecreateDispatchByLabel(t *testing.T) {
	plain := baseContainer("old1", "web", "nginx:1.25", "sha256:old", true)
	d := newFakeDaemon(plain)
	if err := recreateContainer(context.Background(), d, "old1", "nginx:1.26", nil); err != nil {
		t.Fatalf("plain container: %v", err)
	}
	if !strings.Contains(d.actionsString(), "rename:") {
		t.Errorf("a plain container should use the direct flow: %s", d.actionsString())
	}

	quadlet := quadletContainer("q1", "web2", "nginx:1.25", "sha256:old", true)
	d2 := newFakeDaemon(quadlet)
	d2.onStop = respawning("sha256:new", "running")
	if err := recreateContainer(context.Background(), d2, "q1", "nginx:1.26", nil); err != nil {
		t.Fatalf("quadlet container: %v", err)
	}
	if strings.Contains(d2.actionsString(), "rename:") {
		t.Errorf("a systemd-managed container must not use the direct flow: %s", d2.actionsString())
	}
}

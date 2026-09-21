package dockerx

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/swarm"

	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Swarm support is deliberately opt-in. WhatsNewDock only ever talks to the
// services API when the operator has granted it to the socket proxy
// (`SERVICES=1`); with the shipped default of `0` every call below comes back
// 403 and swarm hosts stay monitor-only. Nothing here widens that access by
// itself — the proxy setting is the switch.

const (
	// swarmCapabilityTTL is how long a probe result is trusted before it is
	// re-checked, so enabling the API in the proxy is picked up without an app
	// restart while polling stays cheap.
	swarmCapabilityTTL = 5 * time.Minute
	// serviceConvergenceTimeout is how long a rollout is given to settle.
	serviceConvergenceTimeout = 5 * time.Minute
	// serviceConvergencePoll is the interval between rollout checks.
	serviceConvergencePoll = 2 * time.Second
)

// The rollout window is a variable rather than a constant so tests can shrink
// it; the timeout paths otherwise take minutes to exercise.
var (
	serviceConvergeTimeout = serviceConvergenceTimeout
	serviceConvergePoll    = serviceConvergencePoll
)

// swarmIdentity is the swarm membership recorded on a task container.
type swarmIdentity struct {
	ServiceID   string
	ServiceName string
	TaskID      string
	NodeID      string
}

// swarmTask reads the system labels swarm puts on every task container. Moby
// applies these over the service's own labels, so their presence is an exact
// test for "this container is owned by the orchestrator".
func swarmTask(labels map[string]string) swarmIdentity {
	return swarmIdentity{
		ServiceID:   labels["com.docker.swarm.service.id"],
		ServiceName: labels["com.docker.swarm.service.name"],
		TaskID:      labels["com.docker.swarm.task.id"],
		NodeID:      labels["com.docker.swarm.node.id"],
	}
}

// IsSwarmTask reports whether a container belongs to a swarm service. Such a
// container must never be recreated directly: the orchestrator owns it.
func IsSwarmTask(labels map[string]string) bool {
	return labels["com.docker.swarm.task.id"] != ""
}

// swarmRoleFrom maps the daemon's swarm state onto our own vocabulary.
func swarmRoleFrom(info swarm.Info) store.SwarmRole {
	switch info.LocalNodeState {
	case swarm.LocalNodeStateActive:
		if info.ControlAvailable {
			return store.SwarmManager
		}
		return store.SwarmWorker
	case swarm.LocalNodeStatePending:
		return store.SwarmWorker
	default:
		return store.SwarmNone
	}
}

// swarmCapability memoises whether the services API is reachable, so a manager
// with the permission denied is probed once per TTL instead of on every poll.
type swarmCapability struct {
	mu      sync.Mutex
	checked time.Time
	ok      bool
}

// serviceLister is the one call the capability probe needs, so the probe can be
// exercised without a daemon.
type serviceLister interface {
	ServiceList(ctx context.Context, options swarm.ServiceListOptions) ([]swarm.Service, error)
}

// servicesAvailable reports whether the services API can be used. Only a
// manager can ever say yes, and the answer is cached for a short while so a
// denied proxy is probed once per TTL rather than on every poll.
func (c *Client) servicesAvailable(ctx context.Context, role store.SwarmRole) bool {
	return probeServices(ctx, c.cli, role, &c.swarmCap, time.Now)
}

func probeServices(ctx context.Context, d serviceLister, role store.SwarmRole, cap *swarmCapability, now func() time.Time) bool {
	if role != store.SwarmManager {
		return false
	}
	cap.mu.Lock()
	if !cap.checked.IsZero() && now().Sub(cap.checked) < swarmCapabilityTTL {
		ok := cap.ok
		cap.mu.Unlock()
		return ok
	}
	cap.mu.Unlock()

	_, err := d.ServiceList(ctx, swarm.ServiceListOptions{})
	ok := err == nil

	cap.mu.Lock()
	cap.ok = ok
	cap.checked = now()
	cap.mu.Unlock()
	return ok
}

// invalidateSwarmCapability forces the next probe to re-check, so a permission
// change is noticed as soon as we try to act on it.
func (c *Client) invalidateSwarmCapability() {
	c.swarmCap.mu.Lock()
	c.swarmCap.checked = time.Time{}
	c.swarmCap.mu.Unlock()
}

// SwarmStatus describes a manager's readiness for service updates.
type SwarmStatus struct {
	Role              store.SwarmRole
	ServicesAvailable bool
}

// Status reports the host's swarm role and whether the services API is usable.
func (c *Client) SwarmStatus(ctx context.Context) (SwarmStatus, error) {
	info, err := c.cli.Info(ctx)
	if err != nil {
		return SwarmStatus{}, fmt.Errorf("docker info: %w", err)
	}
	role := swarmRoleFrom(info.Swarm)
	return SwarmStatus{Role: role, ServicesAvailable: c.servicesAvailable(ctx, role)}, nil
}

// isServiceAPIDenied reports whether an error is the daemon or socket proxy
// refusing the services API, which is the expected state until the operator
// opts in.
func isServiceAPIDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "not authorized") ||
		strings.Contains(msg, "disabled")
}

// ---------------------------------------------------------------------------
// Rollout convergence
// ---------------------------------------------------------------------------

// serviceState is the part of a service inspection that decides whether a
// rollout has settled.
type serviceState struct {
	Running     uint64
	Desired     uint64
	UpdateState string
}

// convergenceState classifies a rollout.
type convergenceState int

const (
	// convergePending: keep polling.
	convergePending convergenceState = iota
	// convergeDone: every desired task is running on the new spec.
	convergeDone
	// convergeFailed: the rollout will not settle; undo it ourselves.
	convergeFailed
	// convergeRolledBack: the daemon already restored the previous spec.
	convergeRolledBack
)

// classifyServiceConvergence decides whether a rollout has finished.
//
// Only a rollout the daemon reports as *completed*, with every desired task
// running, is success. The task counts alone can never prove it: a rolling
// update keeps `running == desired` the whole time it is replacing tasks, so
// trusting them would report success while the old version is still serving.
// Nor can an absent update status be treated as settled — that is what a
// rollout looks like before the daemon has started it.
//
// A rollback the daemon performed on its own, or a deadline reached without
// converging, is a failure: a crash-looping task must never be reported as a
// successful update.
func classifyServiceConvergence(s serviceState, deadlineReached bool) convergenceState {
	switch swarm.UpdateState(s.UpdateState) {
	case swarm.UpdateStateCompleted:
		if s.Running >= s.Desired {
			return convergeDone
		}
		return convergePending
	case swarm.UpdateStateRollbackCompleted, swarm.UpdateStateRollbackPaused:
		return convergeRolledBack
	}
	if deadlineReached {
		return convergeFailed
	}
	return convergePending
}

// serviceStatus reads the fields that decide whether a rollout has settled.
//
// It has to use the list endpoint: ServiceInspect leaves ServiceStatus nil, so
// inspecting would report zero running tasks and zero desired ones, and every
// rollout would look converged the moment it was issued.
func serviceStatus(ctx context.Context, d containerDaemon, serviceID string) (serviceState, string, error) {
	list, err := d.ServiceList(ctx, swarm.ServiceListOptions{
		Filters: filters.NewArgs(filters.Arg("id", serviceID)),
		Status:  true,
	})
	if err != nil {
		return serviceState{}, "", err
	}
	for _, svc := range list {
		if svc.ID != serviceID {
			continue
		}
		image := ""
		if svc.Spec.TaskTemplate.ContainerSpec != nil {
			image = svc.Spec.TaskTemplate.ContainerSpec.Image
		}
		return serviceStateFrom(svc), image, nil
	}
	return serviceState{}, "", fmt.Errorf("service %s disappeared during the rollout", serviceID)
}

// serviceStateFrom reads the fields that matter for convergence.
func serviceStateFrom(svc swarm.Service) serviceState {
	st := serviceState{}
	if svc.ServiceStatus != nil {
		st.Running = svc.ServiceStatus.RunningTasks
		st.Desired = svc.ServiceStatus.DesiredTasks
	}
	if svc.UpdateStatus != nil {
		st.UpdateState = string(svc.UpdateStatus.State)
	}
	return st
}

// ---------------------------------------------------------------------------
// Service updates
// ---------------------------------------------------------------------------

// ErrSwarmDisabled is returned when the services API is not reachable, which is
// the expected state until an operator opts in. The message is written to be
// shown to a user verbatim.
var ErrSwarmDisabled = fmt.Errorf(
	"swarm service updates are opt-in: grant the services API to your socket proxy " +
		"(set SERVICES=1 alongside POST=1) and restart it, then try again")

// UpdateService pulls targetImage and rolls a swarm service onto it. The
// orchestrator owns the tasks, so this is the only safe way to update one.
func (c *Client) UpdateService(ctx context.Context, serviceID, targetImage string, progress ProgressFunc) error {
	return updateService(ctx, c.cli, serviceID, targetImage, progress)
}

func updateService(ctx context.Context, d containerDaemon, serviceID, targetImage string, progress ProgressFunc) error {
	svc, _, err := d.ServiceInspectWithRaw(ctx, serviceID, swarm.ServiceInspectOptions{})
	if err != nil {
		if isServiceAPIDenied(err) {
			return ErrSwarmDisabled
		}
		return fmt.Errorf("inspect service: %w", err)
	}
	if svc.Spec.TaskTemplate.ContainerSpec == nil {
		return fmt.Errorf("service %s has no container spec", serviceLabel(svc))
	}
	current := svc.Spec.TaskTemplate.ContainerSpec.Image

	// Pull first, so a bad reference fails before the service is touched.
	if err := pullImage(ctx, d, targetImage, progress); err != nil {
		return err
	}
	if progress != nil {
		progress("updating", -1)
	}

	spec := svc.Spec
	spec.TaskTemplate.ContainerSpec.Image = targetImage
	if current == targetImage {
		// A floating tag resolves to the same string, so swarm would consider
		// the spec unchanged and roll nothing out. Bump the force counter, which
		// is exactly what `docker service update --force` does.
		spec.TaskTemplate.ForceUpdate++
	}

	if _, err := d.ServiceUpdate(ctx, svc.ID, svc.Meta.Version, spec, swarm.ServiceUpdateOptions{
		QueryRegistry:    true,
		RegistryAuthFrom: swarm.RegistryAuthFromSpec,
	}); err != nil {
		if isServiceAPIDenied(err) {
			return ErrSwarmDisabled
		}
		return fmt.Errorf("update service: %w", err)
	}

	deadline := time.Now().Add(serviceConvergeTimeout)
	for {
		state, _, err := serviceStatus(ctx, d, svc.ID)
		if err == nil {
			switch classifyServiceConvergence(state, !time.Now().Before(deadline)) {
			case convergeDone:
				return nil
			case convergeRolledBack:
				return rolledBackByDaemon(svc, targetImage)
			case convergeFailed:
				// Re-inspect for the current version before undoing anything.
				cur, _, ierr := d.ServiceInspectWithRaw(ctx, svc.ID, swarm.ServiceInspectOptions{})
				if ierr != nil {
					return fmt.Errorf("service %s did not converge on %s and could not be re-read to roll it back: %w",
						serviceLabel(svc), targetImage, ierr)
				}
				return rollbackService(ctx, d, cur, targetImage)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(serviceConvergePoll):
		}
	}
}

// rolledBackByDaemon reports a rollout swarm undid on its own, so we do not
// issue a second rollback.
func rolledBackByDaemon(svc swarm.Service, targetImage string) error {
	return fmt.Errorf(
		"service %s did not stay running on %s, so swarm rolled it back to the previous version — "+
			"check `docker service ps %s`",
		serviceLabel(svc), targetImage, serviceLabel(svc))
}

// rollbackService restores the service's previous spec after a rollout failed to
// settle, so a broken image does not leave every replica crash-looping.
func rollbackService(ctx context.Context, d containerDaemon, svc swarm.Service, targetImage string) error {
	_, err := d.ServiceUpdate(ctx, svc.ID, svc.Meta.Version, svc.Spec, swarm.ServiceUpdateOptions{
		Rollback:         "previous",
		RegistryAuthFrom: swarm.RegistryAuthFromPreviousSpec,
	})
	if err != nil {
		return fmt.Errorf(
			"service %s did not converge on %s, and the rollback also failed (%v) — "+
				"check `docker service ps %s`",
			serviceLabel(svc), targetImage, err, serviceLabel(svc))
	}
	return fmt.Errorf(
		"service %s did not converge on %s within %s; the previous version was restored — "+
			"check `docker service ps %s` for why the new tasks did not stay running",
		serviceLabel(svc), targetImage, serviceConvergeTimeout, serviceLabel(svc))
}

// serviceLabel names a service for error messages, falling back to its id.
func serviceLabel(svc swarm.Service) string {
	if svc.Spec.Annotations.Name != "" {
		return svc.Spec.Annotations.Name
	}
	return svc.ID
}

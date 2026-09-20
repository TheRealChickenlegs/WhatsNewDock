package dockerx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// stopTimeoutSeconds is how long the runtime may take to stop a container
	// gracefully before it is killed.
	stopTimeoutSeconds = 30
	// restoreTimeout bounds the recovery work done after a failed update. It
	// deliberately does not use the caller's context, which may already be
	// cancelled by the time we need to put a container back.
	restoreTimeout = 30 * time.Second
)

// The respawn window is a variable rather than a constant so tests can shrink
// it; the timeout paths otherwise take a minute and a half to exercise.
var (
	// systemdRespawnTimeout is how long a Quadlet unit is given to bring its
	// container back. systemd honours RestartSec, and a heavy image can take a
	// while to reach running even though it is already pulled.
	systemdRespawnTimeout = 90 * time.Second
	// systemdRespawnPoll is the interval between respawn checks.
	systemdRespawnPoll = 500 * time.Millisecond
)

// containerDaemon is the subset of the Docker Engine API the recreate flow
// uses. Narrowing it to an interface keeps the recovery logic — the part that
// decides whether a user's workload ends up running — testable against a fake
// daemon instead of a live socket.
type containerDaemon interface {
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRename(ctx context.Context, containerID, newContainerName string) error
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
}

// RecreateContainer pulls a target image and recreates the container against it,
// preserving the original configuration.
func (c *Client) RecreateContainer(ctx context.Context, containerID, targetImage string, progress ProgressFunc) error {
	return recreateContainer(ctx, c.cli, containerID, targetImage, progress)
}

func recreateContainer(ctx context.Context, d containerDaemon, containerID, targetImage string, progress ProgressFunc) error {
	insp, err := d.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	// The image is pulled first either way, so a failed pull leaves the running
	// container completely untouched.
	if err := pullImage(ctx, d, targetImage, progress); err != nil {
		return err
	}

	// A container owned by a systemd unit (Podman Quadlet) cannot be recreated
	// the usual way: stopping it makes the unit tear it down, so renaming and
	// creating our own replacement both fail. Hand the recreate to systemd.
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	if unit := podmanSystemdUnit(labels); unit != "" {
		return recreateSystemdManaged(ctx, d, insp, targetImage, unit, progress)
	}
	return recreateDirect(ctx, d, insp, targetImage, progress)
}

// recreateDirect is the classic flow: stop, free the name, create a replacement
// from the old configuration, start it, then drop the old container.
//
// Every failure after the stop restores the original container — its name and
// its running state. A failed update must never leave a workload stopped, no
// matter what restart policy the container has.
func recreateDirect(ctx context.Context, d containerDaemon, insp container.InspectResponse, targetImage string, progress ProgressFunc) error {
	name := strings.TrimPrefix(insp.Name, "/")
	wasRunning := insp.State != nil && insp.State.Running

	if wasRunning {
		timeout := stopTimeoutSeconds
		if err := d.ContainerStop(ctx, insp.ID, container.StopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("stop container: %w", err)
		}
	}

	// From here on the original container is stopped, so anything that goes
	// wrong has to put it back exactly as it was.
	renamed := false
	restore := func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancel()
		if renamed {
			_ = d.ContainerRename(restoreCtx, insp.ID, name)
		}
		if wasRunning {
			_ = d.ContainerStart(restoreCtx, insp.ID, container.StartOptions{})
		}
	}

	// 1. Free the original name for the replacement. The timestamp is
	//    nanosecond-resolution so a retry can never collide with a leftover
	//    backup from an earlier attempt.
	backupName := fmt.Sprintf("%s-prev-%d", name, time.Now().UnixNano())
	if err := d.ContainerRename(ctx, insp.ID, backupName); err != nil {
		restore()
		return fmt.Errorf("rename old container: %w", err)
	}
	renamed = true

	// 2. Create the replacement from the original configuration.
	if progress != nil {
		progress("recreating", -1)
	}
	newConfig := *insp.Config
	newConfig.Image = targetImage
	created, err := d.ContainerCreate(ctx, &newConfig, sanitizeHostConfig(insp.HostConfig),
		buildNetworkingConfig(insp.NetworkSettings), nil, name)
	if err != nil {
		restore()
		return fmt.Errorf("create replacement container: %w", err)
	}

	// 3. Start it.
	if err := d.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Drop the replacement we could not start, then bring the original back.
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		_ = d.ContainerRemove(removeCtx, created.ID, container.RemoveOptions{Force: true})
		cancel()
		restore()
		return fmt.Errorf("start replacement container: %w", err)
	}

	// 4. Remove the old container now that the replacement is running.
	_ = d.ContainerRemove(ctx, insp.ID, container.RemoveOptions{Force: true})
	return nil
}

// recreateSystemdManaged updates a container that belongs to a systemd unit
// (Podman Quadlet) by handing the recreate to that unit.
//
// Stopping the container is the whole update: the unit's ExecStop removes it
// and its Restart= policy respawns a fresh one from the unit's
// `podman run --replace`, which resolves the tag that was just pulled. Renaming
// and creating the container ourselves cannot work here — by the time we could
// rename, the unit has already removed it (the "failed to rename old container"
// error), and our own container would collide with systemd's respawn.
func recreateSystemdManaged(ctx context.Context, d containerDaemon, insp container.InspectResponse, targetImage, unit string, progress ProgressFunc) error {
	name := strings.TrimPrefix(insp.Name, "/")

	// A stopped Quadlet container does not exist: the unit's teardown removed
	// it and the unit is inactive. Nothing in the Engine API can start a unit,
	// so stopping would be a no-op and we would wait out the timeout for
	// nothing. Fail fast with something the user can act on.
	if insp.State == nil || !insp.State.Running {
		return fmt.Errorf(
			"cannot update %s: it is managed by the systemd unit %s and is not running — "+
				"start it first (systemctl start %s) so it comes up on the newly pulled image",
			name, unit, unit)
	}

	// Remember which image the container is on now, so a respawn that changed
	// nothing is not reported as a successful update.
	imageBefore := insp.Image

	if progress != nil {
		progress("recreating", -1)
	}

	timeout := stopTimeoutSeconds
	if err := d.ContainerStop(ctx, insp.ID, container.StopOptions{Timeout: &timeout}); err != nil {
		// A Quadlet stop can race the unit's own teardown, in which case the
		// container is already gone. Not fatal — the respawn check below is what
		// decides whether the update worked.
		if !isGone(err) {
			return fmt.Errorf("stop container: %w", err)
		}
	}

	deadline := time.Now().Add(systemdRespawnTimeout)
	for {
		found := findContainerByName(ctx, d, name)
		outcome := decideRespawnOutcome(found, insp.ID, !time.Now().Before(deadline))
		if outcome.Done {
			if !outcome.OK {
				return fmt.Errorf(
					"systemd unit %s did not bring %s back to a running state after the update: "+
						"the image was pulled, but the unit needs a Restart= policy and the container "+
						"has to start cleanly on the new image — check `systemctl status %s`",
					unit, name, unit)
			}
			if err := verifyRespawnImage(ctx, d, outcome.ID, imageBefore, targetImage); err != nil {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(systemdRespawnPoll):
		}
	}
}

// verifyRespawnImage checks that the container systemd brought back is not
// still on the image it had before. A Quadlet unit pins its own Image=, so a
// unit that points at the old tag comes back unchanged, and without this check
// that would be reported as a successful update.
func verifyRespawnImage(ctx context.Context, d containerDaemon, newID, imageBefore, targetImage string) error {
	if imageBefore == "" {
		return nil
	}
	after, err := d.ContainerInspect(ctx, newID)
	if err != nil || after.Image == "" {
		// Best effort: the container is running, which is what we can assert.
		return nil
	}
	if after.Image == imageBefore {
		oldRef := targetImage
		if after.Config != nil && after.Config.Image != "" {
			oldRef = after.Config.Image
		}
		return fmt.Errorf(
			"the container came back running the same image it had before (%s): the unit's Image= "+
				"still resolves to the old image, so systemd's respawn did not pick up %s — edit the "+
				"unit and run `systemctl daemon-reload`, or set AutoUpdate=registry and use `podman auto-update`",
			oldRef, targetImage)
	}
	return nil
}

// findContainerByName returns the container whose name matches exactly, or nil.
func findContainerByName(ctx context.Context, d containerDaemon, name string) *containerRef {
	// The daemon's name filter is regex-contains, so it is only a prefilter;
	// the exact match below is what makes the result trustworthy.
	list, err := d.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return nil
	}
	for _, c := range list {
		if exactNameMatch(c.Names, name) {
			return &containerRef{ID: c.ID, State: string(c.State)}
		}
	}
	return nil
}

// sanitizeHostConfig removes values that are safe to read from an inspect but
// not safe to hand straight back to the daemon.
//
// Podman reports MemorySwappiness as 0 for a container that never set it, where
// Docker reports null. Passing that 0 back makes crun refuse to start the
// replacement on a cgroup v2 host ("cannot set memory swappiness with
// cgroupv2"), so Podman's "unset" is normalised to Docker's. Swappiness cannot
// be expressed at all on cgroup v2, and the field is deprecated in Docker, so
// the only thing lost is an explicit 0 on a cgroup v1 host.
func sanitizeHostConfig(hc *container.HostConfig) *container.HostConfig {
	if hc == nil {
		return nil
	}
	out := *hc
	if out.MemorySwappiness != nil && *out.MemorySwappiness == 0 {
		out.MemorySwappiness = nil
	}
	return &out
}

// isGone reports whether an error means the container no longer exists.
func isGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no container with name or id")
}

// pullMessage is one JSON progress message from the Docker ImagePull stream.
type pullMessage struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	ErrorDetail struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

// pullImage pulls an image and reports aggregate layer progress (and any pull
// error embedded in the stream) through the progress callback.
func pullImage(ctx context.Context, d containerDaemon, ref string, progress ProgressFunc) error {
	auth := base64.URLEncoding.EncodeToString([]byte(`{}`))
	stream, err := d.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: auth})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer stream.Close()

	layers := map[string][2]int64{}
	dec := json.NewDecoder(stream)
	for {
		var msg pullMessage
		if err := dec.Decode(&msg); err != nil {
			// io.EOF (or a stray non-JSON line) — the pull has completed.
			break
		}
		if msg.Error != "" {
			errMsg := msg.Error
			if msg.ErrorDetail.Message != "" {
				errMsg = msg.ErrorDetail.Message
			}
			return fmt.Errorf("pull image %s: %s", ref, errMsg)
		}
		if msg.ID == "" {
			continue
		}
		l := layers[msg.ID]
		if msg.ProgressDetail.Total > 0 {
			l[1] = msg.ProgressDetail.Total
		}
		if msg.ProgressDetail.Current > 0 {
			l[0] = msg.ProgressDetail.Current
		}
		layers[msg.ID] = l

		if progress != nil {
			var cur, tot int64
			for _, l := range layers {
				cur += l[0]
				tot += l[1]
			}
			pct := 0
			if tot > 0 {
				pct = int(cur * 100 / tot)
				if pct > 100 {
					pct = 100
				}
			}
			progress("pulling", pct)
		}
	}
	return nil
}

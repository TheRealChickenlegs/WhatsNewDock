package dockerx

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
)

// Self-update redeploys the WhatsNewDock container onto a newer image.
//
// A container cannot recreate itself: the stop/rename/create/start sequence
// kills the process that is driving it halfway through, and the app would be
// left down. So the work is handed to a short-lived helper container started
// from the image that is *already running* — code we know works — which
// recreates this container and then exits. The app dies as part of the swap,
// but the helper does not, so the sequence completes.
//
// This needs no permission the update feature does not already have: it creates
// and starts exactly one container, from an image already on the host.

// Environment variables the helper reads. They are set on the helper container,
// never on the app itself.
const (
	// EnvSelfUpdateContainer is the container the helper should replace.
	EnvSelfUpdateContainer = "WND_SELFUPDATE_CONTAINER"
	// EnvSelfUpdateImage is the image reference to redeploy onto.
	EnvSelfUpdateImage = "WND_SELFUPDATE_IMAGE"
	// SelfUpdateHelperLabel marks the helper so it is recognisable in `docker ps`.
	SelfUpdateHelperLabel = "com.whatsnewdock.role"
	// SelfUpdateHelperRole is the value of that label.
	SelfUpdateHelperRole = "self-update"
)

// SelfContainerID returns this process's container id, which Docker sets as the
// hostname unless it was overridden. It returns "" when we are not in a
// container we can identify, in which case self-update is not offered.
func SelfContainerID() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	h = strings.TrimSpace(h)
	// Docker hostnames are the 12-character short container id.
	if len(h) < 8 || len(h) > 64 {
		return ""
	}
	for _, r := range h {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return h
}

// HelperAccess is everything the helper needs to reach the Docker API the same
// way this container does. Getting this wrong is silent: the helper starts,
// fails to reach the daemon, and is removed before anyone can read its logs.
type HelperAccess struct {
	DockerHost string
	Network    *network.NetworkingConfig
	Binds      []string
	Mounts     []mount.Mount
	GroupAdd   []string
}

// helperAccess derives the helper's Docker access from our own inspect.
//
// A deployment that reaches a socket proxy over a network needs nothing but
// that network. One that mounts the socket directly needs the socket mount and
// the group that can read it — a distroless non-root process cannot open
// /var/run/docker.sock otherwise.
func helperAccess(insp container.InspectResponse, dockerHost string) HelperAccess {
	acc := HelperAccess{DockerHost: dockerHost}
	if insp.NetworkSettings != nil {
		acc.Network = buildNetworkingConfig(insp.NetworkSettings)
	}
	if insp.HostConfig == nil {
		return acc
	}
	if !strings.HasPrefix(dockerHost, "unix://") {
		return acc
	}
	socket := strings.TrimPrefix(dockerHost, "unix://")
	// Copy only the mount that carries the socket, so the helper never gets a
	// writable handle on the data volume.
	for _, b := range insp.HostConfig.Binds {
		if bindDestination(b) == socket {
			acc.Binds = append(acc.Binds, b)
		}
	}
	// Binds and Mounts describe the same attachment from different angles, and
	// passing both is rejected as a duplicate mount point. Prefer the bind form,
	// which keeps the original read-only flag; fall back to the resolved mount
	// when the socket was attached with --mount instead.
	if len(acc.Binds) == 0 {
		for _, m := range insp.Mounts {
			if m.Destination != socket {
				continue
			}
			acc.Mounts = append(acc.Mounts, mount.Mount{
				Type:     m.Type,
				Source:   m.Source,
				Target:   m.Destination,
				ReadOnly: !m.RW,
			})
		}
	}
	if len(acc.Binds) > 0 || len(acc.Mounts) > 0 {
		acc.GroupAdd = insp.HostConfig.GroupAdd
	}
	return acc
}

// bindDestination returns the container path of a "src:dst[:opts]" bind string.
func bindDestination(bind string) string {
	parts := strings.Split(bind, ":")
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[1]
	default:
		// src:dst:opts — the destination is still the second field.
		return parts[1]
	}
}

// SelfUpdateHelperConfig builds the helper's container config. It is a pure
// function so what we would create can be asserted without a daemon.
//
// selfImage must be the image *id* this container is running, not its tag: by
// the time the helper starts, the tag may already point somewhere else. The
// code doing the swap should be the code already proven to run here.
func SelfUpdateHelperConfig(selfImage, selfID, targetImage string, access HelperAccess) (*container.Config, *container.HostConfig) {
	cfg := &container.Config{
		Image: selfImage,
		Env: []string{
			"WND_MODE=" + string(config.ModeSelfUpdate),
			EnvSelfUpdateContainer + "=" + selfID,
			EnvSelfUpdateImage + "=" + targetImage,
			"WND_DOCKER_HOST=" + access.DockerHost,
		},
		Labels: map[string]string{SelfUpdateHelperLabel: SelfUpdateHelperRole},
	}
	hostCfg := &container.HostConfig{AutoRemove: true}
	// The helper must reach the same Docker endpoint we do.
	if access.Network != nil && len(access.Network.EndpointsConfig) > 0 {
		for name := range access.Network.EndpointsConfig {
			hostCfg.NetworkMode = container.NetworkMode(name)
			break
		}
	}
	hostCfg.Binds = access.Binds
	hostCfg.Mounts = access.Mounts
	hostCfg.GroupAdd = access.GroupAdd
	return cfg, hostCfg
}

// selfUpdateHelperName is the helper's container name. Only one is ever needed,
// so a leftover from a previous attempt is cleared before creating a new one.
const selfUpdateHelperName = "whatsnewdock-self-update"

// ErrAlreadyCurrent means the target reference already resolves to the image
// this container is running: there is nothing to deploy. It is a normal outcome,
// not a failure — an orchestrated update sends every agent the same target and
// the ones that are already current say so.
var ErrAlreadyCurrent = fmt.Errorf("already running the newest build")

// StartSelfUpdate launches the helper that will replace this container.
//
// It returns as soon as the helper is running; the caller should tell the user
// the app is about to restart, because it will be stopped moments later.
func (c *Client) StartSelfUpdate(ctx context.Context, selfID, targetImage, dockerHost string) error {
	return startSelfUpdate(ctx, c.cli, selfID, targetImage, dockerHost)
}

func startSelfUpdate(ctx context.Context, d containerDaemon, selfID, targetImage, dockerHost string) error {
	insp, err := d.ContainerInspect(ctx, selfID)
	if err != nil {
		return fmt.Errorf("inspect own container: %w", err)
	}
	if insp.Config == nil || insp.Config.Image == "" {
		return fmt.Errorf("cannot determine the image this container runs from")
	}
	// Pull first: nothing should be stopped until the new image is on disk.
	// A moving tag is redeployed by pulling it again, which is also how we can
	// tell whether anything actually changed.
	if err := pullImage(ctx, d, targetImage, nil); err != nil {
		return err
	}
	if insp.Config.Image == targetImage {
		if img, err := d.ImageInspect(ctx, targetImage); err == nil && img.ID == insp.Image {
			return fmt.Errorf("%w of %s", ErrAlreadyCurrent, targetImage)
		}
	}

	// The helper is pinned to the image *id* rather than the reference: the pull
	// above may have re-pointed the reference at the new image, and then the
	// helper would run code that has never executed here — or, for a moving tag
	// pointing at an older release, code that does not know how to redeploy at
	// all. The point of the helper is that it runs the code already running.
	cfg, hostCfg := SelfUpdateHelperConfig(insp.Image, selfID, targetImage,
		helperAccess(insp, dockerHost))

	// A helper left over from an interrupted attempt would block the name.
	if stale := findContainerByName(ctx, d, selfUpdateHelperName); stale != nil {
		_ = d.ContainerRemove(ctx, stale.ID, container.RemoveOptions{Force: true})
	}

	created, err := d.ContainerCreate(ctx, cfg, hostCfg, nil, nil, selfUpdateHelperName)
	if err != nil {
		return fmt.Errorf("create self-update helper: %w", err)
	}
	if err := d.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = d.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("start self-update helper: %w", err)
	}
	return nil
}

// RunSelfUpdateHelper is the entry point for the helper container: it replaces
// the given container with the target image and returns. The container is
// created with AutoRemove, so it disappears when this exits.
func (c *Client) RunSelfUpdateHelper(ctx context.Context, selfID, targetImage string) error {
	if selfID == "" || targetImage == "" {
		return fmt.Errorf("self-update helper needs both a container id and a target image")
	}
	return c.RecreateContainer(ctx, selfID, targetImage, nil)
}

// SelfImage reports the image reference this process runs from and the digest
// that reference resolved to. The digest is taken from the image's repo digest
// — the value the registry served — so a locally built image, which has none,
// is distinguishable from one pulled from a registry.
func (c *Client) SelfImage(ctx context.Context, containerID string) (ref string, digest string, err error) {
	insp, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", "", err
	}
	if insp.Config == nil || insp.Config.Image == "" {
		return "", "", fmt.Errorf("container %s has no image reference", containerID)
	}
	ref = insp.Config.Image
	// The image the container was created from, which may be an id.
	img, err := c.cli.ImageInspect(ctx, insp.Image)
	if err == nil && len(img.RepoDigests) > 0 {
		if i := strings.IndexByte(img.RepoDigests[0], '@'); i >= 0 {
			return ref, img.RepoDigests[0][i+1:], nil
		}
	}
	// No repo digest means it was never pulled from a registry; the image id is
	// all we have, and callers treat an id as "not comparable".
	return ref, "", nil
}

// ContainerImage returns the image reference a container was created from.
func (c *Client) ContainerImage(ctx context.Context, containerID string) (string, error) {
	insp, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", err
	}
	if insp.Config == nil || insp.Config.Image == "" {
		return "", fmt.Errorf("container %s has no image reference", containerID)
	}
	return insp.Config.Image, nil
}

// imageRefDigest returns the digest a reference currently resolves to.
func (c *Client) imageRefDigest(ctx context.Context, ref string) (string, error) {
	img, err := c.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", err
	}
	return img.ID, nil
}

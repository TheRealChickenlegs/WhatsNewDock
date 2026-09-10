// Package dockerx wraps the Docker Engine API client and provides the
// high-level operations WhatsNewDock needs: host info, container snapshots
// and the safe container-recreate update flow. It never shells out to the
// docker CLI — every operation goes through the Engine API.
package dockerx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Client is a Docker Engine API client wrapper.
type Client struct {
	cli *client.Client
}

// New creates a Docker client from configuration.
func New(cfg config.DockerConfig) (*Client, error) {
	opts := []client.Opt{
		client.WithHost(cfg.Host),
		client.WithAPIVersionNegotiation(),
	}
	if cfg.APIVersion != "" {
		opts = append(opts, client.WithVersion(cfg.APIVersion))
	}
	if cfg.TLS {
		opts = append(opts, client.WithTLSClientConfig(cfg.TLSCA, cfg.TLSCert, cfg.TLSKey))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Client{cli: cli}, nil
}

// Close releases the underlying client.
func (c *Client) Close() error { return c.cli.Close() }

// Ping verifies connectivity to the Docker daemon.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.cli.Ping(ctx)
	return err
}

// Info returns host-level information about the Docker daemon.
func (c *Client) Info(ctx context.Context) (store.Server, error) {
	s := store.Server{}
	info, err := c.cli.Info(ctx)
	if err != nil {
		return s, fmt.Errorf("docker info: %w", err)
	}
	s.Name = info.Name
	if s.Name == "" {
		s.Name = "docker-host"
	}
	s.DockerVersion = info.ServerVersion
	s.OS = info.OSType
	s.Arch = info.Architecture
	s.CPUs = info.NCPU
	s.MemoryBytes = info.MemTotal
	s.Labels = info.Labels
	return s, nil
}

// Snapshot describes the complete state of a Docker host.
type Snapshot struct {
	Server     store.Server
	Stacks     []*store.Stack
	Containers []*store.Container
}

// Snapshot collects server info, stacks and containers in one call.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	snap := &Snapshot{Server: info}
	stackSet := map[string]store.StackKind{}

	for _, ctr := range containers {
		name := strings.TrimPrefix(ctr.Names[0], "/")
		insp, err := c.cli.ContainerInspect(ctx, ctr.ID)
		if err != nil {
			// Container may have vanished mid-poll; skip it.
			continue
		}

		c := store.Container{
			ServerID:       "", // assigned by the store during snapshot replace
			DockerID:       ctr.ID,
			Name:           name,
			Image:          ctr.Image,
			ImageDigest:    strings.TrimPrefix(ctr.ImageID, "sha256:"),
			State:          ctr.State,
			Status:         ctr.Status,
			Running:        ctr.State == "running",
			RestartPolicy:  string(insp.HostConfig.RestartPolicy.Name),
			ComposeService: ctr.Labels["com.docker.compose.service"],
			CreatedAt:      time.Unix(ctr.Created, 0).UTC(),
			Labels:         ctr.Labels,
			Ports:          formatPorts(ctr.Ports),
		}
		if c.ImageDigest != "" {
			c.ImageDigest = "sha256:" + c.ImageDigest
		}
		if t, err := time.Parse(time.RFC3339Nano, insp.State.StartedAt); err == nil {
			c.StartedAt = t.UTC()
		}

		// Resolve image components.
		if ref, err := reference.Parse(ctr.Image); err == nil {
			c.ImageName = ref.Name
			c.ImageTag = ref.Tag
			c.Registry = ref.Registry
			c.Repository = ref.Repository
		} else {
			c.ImageName = ctr.Image
			c.ImageTag = "latest"
		}

		// Determine stack membership.
		if ns := ctr.Labels["com.docker.stack.namespace"]; ns != "" {
			c.StackName = ns
			stackSet[ns] = store.StackSwarm
		} else if proj := ctr.Labels["com.docker.compose.project"]; proj != "" {
			c.StackName = proj
			stackSet[proj] = store.StackCompose
		}

		snap.Containers = append(snap.Containers, &c)
	}

	// Build the unique stack list.
	names := make([]string, 0, len(stackSet))
	for n := range stackSet {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		snap.Stacks = append(snap.Stacks, &store.Stack{Name: n, Kind: stackSet[n]})
	}

	return snap, nil
}

func formatPorts(ports []container.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if p.PublicPort == 0 {
			out = append(out, fmt.Sprintf("%d/%s", p.PrivatePort, p.Type))
			continue
		}
		ip := p.IP
		if ip == "" || ip == "::" {
			ip = ""
		} else {
			ip += ":"
		}
		out = append(out, fmt.Sprintf("%s%d->%d/%s", ip, p.PublicPort, p.PrivatePort, p.Type))
	}
	sort.Strings(out)
	return out
}

// ProgressFunc reports update progress. stage is "pulling" (percent 0-100) or
// "recreating" (percent -1, indeterminate).
type ProgressFunc func(stage string, percent int)

// RecreateContainer pulls a target image and recreates the container against
// it, preserving the original configuration and keeping the old container as
// a stopped, renamed rollback backup. This mirrors docker-compose semantics
// using only the Engine API (no shell commands).
func (c *Client) RecreateContainer(ctx context.Context, containerID, targetImage string, progress ProgressFunc) error {
	insp, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	// 1. Pull the target image.
	if err := c.pullImage(ctx, targetImage, progress); err != nil {
		return err
	}

	// 2. Build new container config from the old one, swapping the image.
	newConfig := *insp.Config
	newConfig.Image = targetImage

	// 3. Stop the old container.
	timeout := 30
	if err := c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stop container: %w", err)
	}

	// 4. Rename old container as a rollback backup.
	backupName := fmt.Sprintf("%s-prev-%d", strings.TrimPrefix(insp.Name, "/"), time.Now().Unix())
	if err := c.cli.ContainerRename(ctx, containerID, backupName); err != nil {
		return fmt.Errorf("rename old container: %w", err)
	}

	// 5. Create the replacement.
	if progress != nil {
		progress("recreating", -1)
	}
	netConfig := buildNetworkingConfig(insp.NetworkSettings)
	created, err := c.cli.ContainerCreate(ctx, &newConfig, insp.HostConfig, netConfig, nil, insp.Name)
	if err != nil {
		// Roll back the rename so the old container keeps its name.
		_ = c.cli.ContainerRename(ctx, containerID, insp.Name)
		return fmt.Errorf("create replacement container: %w", err)
	}

	// 6. Start it.
	if err := c.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Roll back: drop the failed replacement and restore the old container.
		_ = c.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		_ = c.cli.ContainerRename(ctx, containerID, insp.Name)
		if insp.State.Running {
			_ = c.cli.ContainerStart(ctx, containerID, container.StartOptions{})
		}
		return fmt.Errorf("start replacement container: %w", err)
	}

	return nil
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
func (c *Client) pullImage(ctx context.Context, ref string, progress ProgressFunc) error {
	auth := base64.URLEncoding.EncodeToString([]byte(`{}`))
	stream, err := c.cli.ImagePull(ctx, ref, image.PullOptions{RegistryAuth: auth})
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

// buildNetworkingConfig reconstructs network endpoint config from an inspect.
func buildNetworkingConfig(settings *types.NetworkSettings) *network.NetworkingConfig {
	if settings == nil || len(settings.Networks) == 0 {
		return nil
	}
	cfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	for name, ep := range settings.Networks {
		cfg.EndpointsConfig[name] = &network.EndpointSettings{
			IPAMConfig:          ep.IPAMConfig,
			Links:               ep.Links,
			Aliases:             ep.Aliases,
			NetworkID:           ep.NetworkID,
			EndpointID:          ep.EndpointID,
			Gateway:             ep.Gateway,
			IPAddress:           ep.IPAddress,
			IPPrefixLen:         ep.IPPrefixLen,
			IPv6Gateway:         ep.IPv6Gateway,
			GlobalIPv6Address:   ep.GlobalIPv6Address,
			GlobalIPv6PrefixLen: ep.GlobalIPv6PrefixLen,
			MacAddress:          ep.MacAddress,
		}
	}
	return cfg
}

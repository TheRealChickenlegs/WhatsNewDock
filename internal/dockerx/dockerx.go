// Package dockerx wraps the Docker Engine API client and provides the
// high-level operations WhatsNewDock needs: host info, container snapshots
// and the safe container-recreate update flow. It never shells out to the
// docker CLI — every operation goes through the Engine API.
package dockerx

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Client is a Docker Engine API client wrapper.
type Client struct {
	cli *client.Client
	// swarmCap memoises the services-API capability probe (see swarm.go).
	swarmCap swarmCapability
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
	s.SwarmRole = swarmRoleFrom(info.Swarm)
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
	// Only a manager can use the services API, and only when the socket proxy
	// has been granted it; the answer is cached, so this is not a per-poll cost.
	snap.Server.SwarmServices = c.servicesAvailable(ctx, snap.Server.SwarmRole)
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
		if name, kind := containerStack(ctr.Labels); name != "" {
			c.StackName = name
			stackSet[name] = kind
		}

		// Podman records the owning systemd unit on containers it manages
		// through Quadlet. Recreating such a container by hand would leave the
		// unit out of sync with reality, so mark it managed and refuse
		// one-click updates for it. Containers without this label — every
		// Docker container, and Podman containers started by hand — are
		// unaffected.
		if unit := podmanSystemdUnit(ctr.Labels); unit != "" {
			c.SystemdUnit = unit
			c.Managed = true
		}

		// A swarm task is owned by its service, so it is managed too: recreating
		// it directly would race the orchestrator and produce a container
		// wearing another task's labels.
		if sw := swarmTask(ctr.Labels); sw.TaskID != "" {
			c.SwarmServiceID = sw.ServiceID
			c.SwarmServiceName = sw.ServiceName
			c.SwarmTaskID = sw.TaskID
			c.SwarmNodeID = sw.NodeID
			c.Managed = true
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

// containerStack resolves which group a container belongs to. Docker's swarm
// and compose labels keep priority; Podman's Kubernetes pod label is only a
// fallback, so grouping on Docker hosts is completely unchanged.
func containerStack(labels map[string]string) (string, store.StackKind) {
	if ns := labels["com.docker.stack.namespace"]; ns != "" {
		return ns, store.StackSwarm
	}
	if proj := labels["com.docker.compose.project"]; proj != "" {
		return proj, store.StackCompose
	}
	// Podman sets the pod name for containers created by `podman kube play`.
	if pod := labels["io.kubernetes.pod.name"]; pod != "" {
		return pod, store.StackPod
	}
	return "", ""
}

// podmanSystemdUnit returns the systemd unit that owns a Podman container, or
// "" when the container is not systemd-managed. Podman sets this label for
// containers generated from Quadlet units; no Docker container ever carries it.
func podmanSystemdUnit(labels map[string]string) string {
	return labels["PODMAN_SYSTEMD_UNIT"]
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

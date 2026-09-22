// Package agent implements the agent mode: a lightweight process that
// snapshots a Docker host and reports it to a central server, and executes
// update commands queued by that server.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/protocol"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Agent is the agent-mode runtime.
type Agent struct {
	cfg        config.AgentConfig
	docker     *dockerx.Client
	http       *http.Client
	version    string
	dockerHost string
	// selfImage/selfDigest describe this agent's own container, resolved once.
	selfImage  string
	selfDigest string
}

// New builds an agent. dockerHost is the endpoint this agent reaches Docker
// through; it is handed to the self-update helper so the helper can reach the
// same place.
func New(cfg config.AgentConfig, docker *dockerx.Client, version, dockerHost string) *Agent {
	tr := &http.Transport{}
	if cfg.TLSSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit opt-in for self-signed/development servers
	}
	return &Agent{
		cfg:        cfg,
		docker:     docker,
		version:    version,
		dockerHost: dockerHost,
		http:       &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
}

// Run starts the report and command-poll loops until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	reportEvery := a.cfg.Interval
	if reportEvery <= 0 {
		reportEvery = 30 * time.Second
	}
	cmdEvery := 10 * time.Second
	if reportEvery < cmdEvery {
		cmdEvery = reportEvery
	}

	a.reportOnce(ctx)
	reportTicker := time.NewTicker(reportEvery)
	cmdTicker := time.NewTicker(cmdEvery)
	defer reportTicker.Stop()
	defer cmdTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-reportTicker.C:
			a.reportOnce(ctx)
		case <-cmdTicker.C:
			a.pollCommands(ctx)
		}
	}
}

func (a *Agent) reportOnce(ctx context.Context) {
	snap, err := a.docker.Snapshot(ctx)
	if err != nil {
		slog.Error("docker snapshot failed", "err", err)
		return
	}
	rep := protocol.Report{
		AgentName:  a.cfg.Name,
		Version:    a.version,
		Info:       snap.Server,
		Stacks:     derefStacks(snap.Stacks),
		Containers: derefContainers(snap.Containers),
		SelfImage:  a.selfImage,
		SelfDigest: a.selfDigest,
	}
	if rep.SelfImage == "" {
		// Resolved once: the container this process runs in does not change.
		rep.SelfImage, rep.SelfDigest = a.resolveSelf(ctx)
	}
	if err := a.postJSON(ctx, "/api/agent/v1/report", rep, nil); err != nil {
		logReportFailure("report failed", err)
	} else {
		slog.Debug("reported", "containers", len(rep.Containers))
	}
}

func (a *Agent) pollCommands(ctx context.Context) {
	var cmds []protocol.Command
	if err := a.getJSON(ctx, "/api/agent/v1/commands", &cmds); err != nil {
		logReportFailure("command poll failed", err)
		return
	}
	for _, cmd := range cmds {
		if err := a.execute(ctx, cmd); err != nil {
			slog.Error("command execution failed", "id", cmd.ID, "err", err)
		}
	}
}

func (a *Agent) execute(ctx context.Context, cmd protocol.Command) error {
	var res protocol.CommandResult
	switch cmd.Kind {
	case "update":
		if err := a.docker.RecreateContainer(ctx, cmd.ContainerID, cmd.TargetImage, nil); err != nil {
			res = protocol.CommandResult{Status: "failed", Message: err.Error()}
		} else {
			res = protocol.CommandResult{Status: "done", Message: "container recreated with " + cmd.TargetImage}
		}
	case "self_update":
		// Redeploy this agent onto a newer image. The work is done by a helper
		// container, because replacing ourselves would kill the process that is
		// posting this result. The server treats the agent reporting back on the
		// new image as the real success signal, so a lost reply is harmless.
		res = a.selfUpdate(ctx, cmd.TargetImage)
	default:
		res = protocol.CommandResult{Status: "failed", Message: "unknown command kind " + cmd.Kind}
	}
	body, _ := json.Marshal(res)
	return a.postJSON(ctx, "/api/agent/v1/commands/"+cmd.ID+"/result", nil, body)
}

// selfImage and selfDigest describe the container this agent runs in. They are
// resolved once and cached.
func (a *Agent) resolveSelf(ctx context.Context) (image, digest string) {
	id := dockerx.SelfContainerID()
	if id == "" {
		return "", ""
	}
	image, digest, err := a.docker.SelfImage(ctx, id)
	if err != nil {
		slog.Debug("cannot describe own container", "err", err)
		return "", ""
	}
	a.selfImage, a.selfDigest = image, digest
	slog.Info("agent runs from", "image", image, "digest", digest)
	return image, digest
}

// selfUpdate redeploys this agent onto target. It never runs when the agent
// cannot identify its own container, which is also what stops an agent in an
// unusual runtime from replacing the wrong thing.
func (a *Agent) selfUpdate(ctx context.Context, target string) protocol.CommandResult {
	if target == "" {
		return protocol.CommandResult{Status: "failed", Message: "no target image was given"}
	}
	selfID := dockerx.SelfContainerID()
	if selfID == "" {
		return protocol.CommandResult{Status: "failed",
			Message: "this agent cannot identify its own container, so it cannot update itself"}
	}
	// Make sure the server knows what we are moving from.
	a.resolveSelf(ctx)
	slog.Info("updating the agent", "target", target)
	if err := a.docker.StartSelfUpdate(ctx, selfID, target, a.dockerHost); err != nil {
		if errors.Is(err, dockerx.ErrAlreadyCurrent) {
			return protocol.CommandResult{Status: "done", Message: "already running " + target}
		}
		return protocol.CommandResult{Status: "failed", Message: err.Error()}
	}
	return protocol.CommandResult{Status: "done", Message: "redeploying onto " + target}
}

func (a *Agent) postJSON(ctx context.Context, path string, payload any, raw []byte) error {
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	} else if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return serverError(resp)
	}
	return nil
}

func (a *Agent) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url(path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return serverError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

// ServerError is a non-2xx reply from the server.
type ServerError struct {
	Status  int
	Message string
}

func (e *ServerError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("server returned %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("server returned %d", e.Status)
}

// Unavailable reports whether the reply means the server is not reachable right
// now — a restart, a deploy, or a proxy with no healthy upstream. It is worth
// retrying and not worth an error line.
func (e *ServerError) Unavailable() bool {
	switch e.Status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// serverError reads the server's own error message when it sent JSON. Bodies
// that are not JSON — a proxy's HTML error page, say — are dropped rather than
// pasted into the log.
func serverError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	err := &ServerError{Status: resp.StatusCode}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		err.Message = strings.TrimSpace(payload.Error)
	}
	return err
}

// transient reports whether an error is the server being briefly unavailable,
// which the agent retries on its own and should not shout about.
func transient(err error) bool {
	var se *ServerError
	if errors.As(err, &se) {
		return se.Unavailable()
	}
	// A dial failure or a dropped connection is the server restarting.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF)
}

// logReportFailure keeps a restart quiet and a real fault loud.
func logReportFailure(what string, err error) {
	if transient(err) {
		slog.Warn(what+": server unavailable, will retry", "err", err)
		return
	}
	slog.Error(what, "err", err)
}

func (a *Agent) url(path string) string {
	return strings.TrimSuffix(a.cfg.ServerURL, "/") + path
}

// --- helpers for converting pointer slices to value slices ----------------

func derefStacks(in []*store.Stack) []store.Stack {
	out := make([]store.Stack, 0, len(in))
	for _, s := range in {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

func derefContainers(in []*store.Container) []store.Container {
	out := make([]store.Container, 0, len(in))
	for _, c := range in {
		if c != nil {
			out = append(out, *c)
		}
	}
	return out
}

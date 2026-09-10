// Package agent implements the agent mode: a lightweight process that
// snapshots a Docker host and reports it to a central server, and executes
// update commands queued by that server.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/protocol"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Agent is the agent-mode runtime.
type Agent struct {
	cfg     config.AgentConfig
	docker  *dockerx.Client
	http    *http.Client
	version string
}

// New builds an agent.
func New(cfg config.AgentConfig, docker *dockerx.Client, version string) *Agent {
	tr := &http.Transport{}
	if cfg.TLSSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- explicit opt-in for self-signed/development servers
	}
	return &Agent{
		cfg:     cfg,
		docker:  docker,
		version: version,
		http:    &http.Client{Transport: tr, Timeout: 30 * time.Second},
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
	}
	if err := a.postJSON(ctx, "/api/agent/v1/report", rep, nil); err != nil {
		slog.Error("report failed", "err", err)
	} else {
		slog.Debug("reported", "containers", len(rep.Containers))
	}
}

func (a *Agent) pollCommands(ctx context.Context) {
	var cmds []protocol.Command
	if err := a.getJSON(ctx, "/api/agent/v1/commands", &cmds); err != nil {
		slog.Error("command poll failed", "err", err)
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
	default:
		res = protocol.CommandResult{Status: "failed", Message: "unknown command kind " + cmd.Kind}
	}
	body, _ := json.Marshal(res)
	return a.postJSON(ctx, "/api/agent/v1/commands/"+cmd.ID+"/result", nil, body)
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
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

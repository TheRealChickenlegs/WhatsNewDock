package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

// Updating the deployment is a sequence, not a single action: every agent is
// moved first, and only once they are all back on the new build is this
// controller replaced. The other order would leave agents talking to a server
// they have not caught up with, and the controller is the process that goes down
// last, so it is the one that can report what happened.

// agentUpdateStatus is one agent's place in the sequence.
type agentUpdateStatus string

const (
	agentQueued  agentUpdateStatus = "queued"
	agentUpdated agentUpdateStatus = "updated"
	agentFailed  agentUpdateStatus = "failed"
	agentSkipped agentUpdateStatus = "skipped"
)

// agentUpdate is one agent's progress through a self-update run.
type agentUpdate struct {
	ServerID string            `json:"server_id"`
	Name     string            `json:"name"`
	From     string            `json:"from,omitempty"`
	To       string            `json:"to,omitempty"`
	Status   agentUpdateStatus `json:"status"`
	Message  string            `json:"message,omitempty"`

	commandID  string
	fromDigest string
}

// selfUpdateRun is the live state of an orchestrated self-update.
type selfUpdateRun struct {
	ID        string         `json:"id"`
	StartedAt time.Time      `json:"started_at"`
	Phase     string         `json:"phase"` // agents | controller | done | failed
	Kind      string         `json:"kind,omitempty"`
	Target    string         `json:"target,omitempty"`
	Message   string         `json:"message,omitempty"`
	Error     string         `json:"error,omitempty"`
	Agents    []*agentUpdate `json:"agents"`
	Finished  bool           `json:"finished"`
}

func (r *selfUpdateRun) counts() (done, failed, pending int) {
	for _, a := range r.Agents {
		switch a.Status {
		case agentUpdated, agentSkipped:
			done++
		case agentFailed:
			failed++
		default:
			pending++
		}
	}
	return
}

// selfUpdateTracker holds the current run so the UI can follow it.
type selfUpdateTracker struct {
	mu  sync.Mutex
	run *selfUpdateRun
}

func (t *selfUpdateTracker) set(run *selfUpdateRun) {
	t.mu.Lock()
	t.run = run
	t.mu.Unlock()
}

func (t *selfUpdateTracker) get() *selfUpdateRun {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.run == nil {
		return nil
	}
	// A shallow copy, deep enough for encoding while the run continues.
	out := *t.run
	out.Agents = make([]*agentUpdate, len(t.run.Agents))
	for i, a := range t.run.Agents {
		cp := *a
		out.Agents[i] = &cp
	}
	return &out
}

// updateAgent applies a change to one agent's row under the tracker lock.
func (t *selfUpdateTracker) updateAgent(serverID string, fn func(*agentUpdate)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.run == nil {
		return
	}
	for _, a := range t.run.Agents {
		if a.ServerID == serverID {
			fn(a)
			return
		}
	}
}

func (t *selfUpdateTracker) setPhase(phase, message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.run == nil {
		return
	}
	t.run.Phase = phase
	if message != "" {
		t.run.Message = message
	}
	if phase == "done" || phase == "failed" {
		t.run.Finished = true
	}
}

func (t *selfUpdateTracker) fail(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.run == nil {
		return
	}
	t.run.Phase = "failed"
	t.run.Error = fmt.Sprintf(format, args...)
	t.run.Finished = true
}

// agentUpdateTarget is the image an agent should be moved to. A rebuilt tag is
// re-pulled under the agent's own reference; a new release moves the agent to
// that release tag, keeping whatever registry and repository it uses.
func agentUpdateTarget(agentImage string, res selfupdate.Result) string {
	if res.Kind == selfupdate.KindBuild {
		if agentImage != "" {
			return agentImage
		}
		return res.Target
	}
	if agentImage != "" {
		if tag := selfupdate.TagOf(res.Target); tag != "" {
			return selfupdate.Retag(agentImage, tag)
		}
	}
	return res.Target
}

// startSelfUpdateRun plans the update and returns its initial state. The plan is
// built before this returns so the caller can hand the UI a run that already
// lists the agents, and only the waiting happens in the background.
func (s *Server) startSelfUpdateRun(res selfupdate.Result, force bool) *selfUpdateRun {
	run := &selfUpdateRun{
		ID:        newServerID(),
		StartedAt: time.Now().UTC(),
		Phase:     "agents",
		Kind:      string(res.Kind),
		Target:    res.Target,
		Message:   "Updating agents…",
	}
	s.planAgents(run, res)
	s.selfRun.set(run)
	go s.orchestrateSelfUpdate(run, res, force)
	return run
}

// planAgents queues a self-update on every reachable agent and records what it
// was told to do. Unreachable agents are recorded as skipped rather than
// silently ignored, because they are the ones the operator has to deal with.
func (s *Server) planAgents(run *selfUpdateRun, res selfupdate.Result) {
	servers, err := s.st.ListServers()
	if err != nil {
		slog.Error("self-update: cannot list servers", "err", err)
		return
	}
	for _, srv := range servers {
		if srv.Kind != store.ServerAgent {
			continue
		}
		entry := &agentUpdate{
			ServerID:   srv.ID,
			Name:       srv.Name,
			From:       srv.AgentImage,
			Status:     agentQueued,
			fromDigest: srv.AgentDigest,
		}
		if !effectiveOnline(srv) {
			entry.Status = agentSkipped
			entry.Message = "offline; update it by hand"
			run.Agents = append(run.Agents, entry)
			continue
		}
		// Every reachable agent is told to move. One already on the target
		// answers "already running …", which is a normal outcome rather than a
		// failure, so the server does not have to guess which agents are behind.
		entry.To = agentUpdateTarget(srv.AgentImage, res)
		cmd := &store.Command{ServerID: srv.ID, Kind: "self_update", TargetImage: entry.To}
		if err := s.st.CreateCommand(cmd); err != nil {
			entry.Status = agentFailed
			entry.Message = err.Error()
			run.Agents = append(run.Agents, entry)
			continue
		}
		entry.commandID = cmd.ID
		run.Agents = append(run.Agents, entry)
	}
}

// orchestrateSelfUpdate moves every agent onto the target, waits for them to
// report back, and only then replaces this controller.
func (s *Server) orchestrateSelfUpdate(run *selfUpdateRun, res selfupdate.Result, force bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	queued := 0
	for _, a := range run.Agents {
		if a.Status == agentQueued {
			queued++
		}
	}

	if queued == 0 {
		slog.Info("self-update: no agents to update, moving to the controller")
	} else {
		_ = s.st.AddEvent(s.eventNow("self_update_agents", "system", "", "",
			fmt.Sprintf("%d agent(s) told to update to %s", queued, res.Target)))
		s.waitForAgents(ctx, run)
	}

	done, failed, _ := run.counts()
	if failed > 0 && !force {
		s.selfRun.fail("%d agent(s) did not update; the controller was left alone. "+
			"Fix or remove them, then try again", failed)
		_ = s.st.AddEvent(s.eventNow("self_update_failed", "system", "", "",
			fmt.Sprintf("agents not updated (%d ok, %d failed)", done, failed)))
		return
	}

	s.selfRun.setPhase("controller", "Restarting WhatsNewDock…")
	selfID := selfUpdateSelfID()
	if selfID == "" {
		s.selfRun.fail("this instance cannot identify its own container")
		return
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer startCancel()
	if err := s.docker.StartSelfUpdate(startCtx, selfID, res.Target, s.cfg.Docker.Host); err != nil {
		s.selfRun.fail("could not start the controller update: %v", err)
		_ = s.st.AddEvent(s.eventNow("self_update_failed", "system", "", "", err.Error()))
		return
	}
	_ = s.st.AddEvent(s.eventNow("self_update_started", "system", "", "",
		fmt.Sprintf("%s -> %s (%d agents updated)", res.Current, res.Target, done)))
	s.selfRun.setPhase("controller", "WhatsNewDock is restarting…")
	// From here the process is replaced underneath us; the UI watches for the
	// new version to answer rather than waiting for this run to finish.
}

// waitForAgents blocks until every queued agent reports back or the deadline
// passes. An agent that reconnects on a new digest counts as done even if its
// command reply was lost when its process was replaced.
func (s *Server) waitForAgents(ctx context.Context, run *selfUpdateRun) {
	deadline := time.Now().Add(agentUpdateTimeout)
	for {
		_, _, pending := run.counts()
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			for _, a := range run.Agents {
				if a.Status == agentQueued {
					_ = a
					s.selfRun.updateAgent(a.ServerID, func(au *agentUpdate) {
						au.Status = agentFailed
						au.Message = "did not report back in time"
					})
				}
			}
			return
		}
		time.Sleep(agentUpdatePoll)

		servers, err := s.st.ListServers()
		if err != nil {
			continue
		}
		byID := map[string]store.Server{}
		for _, srv := range servers {
			byID[srv.ID] = srv
		}
		for _, a := range run.Agents {
			if a.Status != agentQueued {
				continue
			}
			// The agent's own report is the strongest signal.
			if srv, ok := byID[a.ServerID]; ok && srv.AgentDigest != "" && srv.AgentDigest != a.fromDigest {
				s.selfRun.updateAgent(a.ServerID, func(au *agentUpdate) {
					au.Status = agentUpdated
					au.Message = "reconnected on " + srv.AgentVersion
				})
				continue
			}
			cmd, err := s.st.GetCommand(a.commandID)
			if err != nil || cmd == nil {
				continue
			}
			switch cmd.Status {
			case "done":
				s.selfRun.updateAgent(a.ServerID, func(au *agentUpdate) {
					au.Status = agentUpdated
					au.Message = cmd.Result
				})
			case "failed":
				s.selfRun.updateAgent(a.ServerID, func(au *agentUpdate) {
					au.Status = agentFailed
					au.Message = cmd.Result
				})
			}
		}
	}
}

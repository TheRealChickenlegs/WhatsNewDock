package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
)

func TestAgentUpdateTarget(t *testing.T) {
	build := selfupdate.Result{Kind: selfupdate.KindBuild, Target: "ghcr.io/x/whatsnewdock:latest"}
	release := selfupdate.Result{Kind: selfupdate.KindRelease, Target: "ghcr.io/x/whatsnewdock:v0.1.2"}

	// A rebuilt tag stays on whatever reference the agent already uses.
	if got := agentUpdateTarget("ghcr.io/mirror/wnd:latest", build); got != "ghcr.io/mirror/wnd:latest" {
		t.Errorf("build: got %q", got)
	}
	// A release moves the agent to that release, keeping its own registry.
	if got := agentUpdateTarget("ghcr.io/mirror/wnd:0.1.1", release); got != "ghcr.io/mirror/wnd:v0.1.2" {
		t.Errorf("release: got %q", got)
	}
	// An agent that has not reported an image falls back to the server's target.
	if got := agentUpdateTarget("", build); got != build.Target {
		t.Errorf("unknown image: got %q", got)
	}
	if got := agentUpdateTarget("", release); got != release.Target {
		t.Errorf("unknown image: got %q", got)
	}
}

func TestSelfupdateTagOf(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/x/wnd:latest":       "latest",
		"ghcr.io/x/wnd:v0.1.2":       "v0.1.2",
		"localhost:5000/x/wnd:0.1.1": "0.1.1",
		"wnd":                        "",
		"localhost:5000/wnd":         "",
		"wnd@sha256:abc":             "",
	} {
		if got := selfupdate.TagOf(in); got != want {
			t.Errorf("TagOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// seedAgent adds an agent that has reported in, which is the only way one
// becomes reachable: a freshly created row has never been heard from.
func seedAgent(t *testing.T, s *Server, image, digest, version string) store.Server {
	t.Helper()
	srv := &store.Server{Name: "edge", Kind: store.ServerAgent, AgentTokenHash: "hash-" + image}
	if err := s.st.CreateServer(srv); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	report := &store.Server{
		ID: srv.ID, Name: "edge", Kind: store.ServerAgent, Status: "online",
		LastSeen: time.Now().UTC(), AgentImage: image, AgentDigest: digest, AgentVersion: version,
	}
	if err := s.st.ReplaceServerSnapshot(report, nil, nil); err != nil {
		t.Fatalf("agent report: %v", err)
	}
	got, err := s.st.GetServer(srv.ID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	return *got
}

// TestSelfUpdateRunUpdatesAgentsFirst is the ordering guarantee: while an agent
// is still being updated the controller must not be touched, and the run must
// say so.
func TestSelfUpdateRunUpdatesAgentsFirst(t *testing.T) {
	shortPolls(t)
	s := newAgentOnlyTestServer(t)
	s.docker = testDockerClient(t)
	agent := seedAgent(t, s, "ghcr.io/x/wnd:0.1.1", "sha256:old", "v0.1.1")

	run := s.startSelfUpdateRun(selfupdate.Result{
		Kind: selfupdate.KindRelease, Target: "ghcr.io/x/wnd:v0.1.2",
	}, false)

	if len(run.Agents) != 1 {
		t.Fatalf("expected the agent to be queued, got %+v", run.Agents)
	}
	if run.Agents[0].To != "ghcr.io/x/wnd:v0.1.2" {
		t.Errorf("agent target = %q", run.Agents[0].To)
	}
	// Give it a moment: the controller must still be untouched.
	time.Sleep(150 * time.Millisecond)
	if got := s.selfRun.get(); got.Phase != "agents" || got.Finished {
		t.Fatalf("controller was not held back: phase=%q finished=%v err=%q", got.Phase, got.Finished, got.Error)
	}

	// The agent reports back, as it would after redeploying itself.
	cmds, err := s.st.ListPendingCommands(agent.ID)
	if err != nil || len(cmds) != 1 {
		t.Fatalf("pending commands = %d, %v", len(cmds), err)
	}
	if cmds[0].Kind != "self_update" || cmds[0].TargetImage != "ghcr.io/x/wnd:v0.1.2" {
		t.Errorf("unexpected command: %+v", cmds[0])
	}
	if err := s.st.UpdateCommandResult(cmds[0].ID, "done", "redeploying onto ghcr.io/x/wnd:v0.1.2"); err != nil {
		t.Fatalf("complete command: %v", err)
	}

	// Only now may the controller phase begin. (It fails here because the test
	// client points at a socket that does not exist — the point is that it is
	// attempted at all, and only after the agent finished.)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := s.selfRun.get()
		if got.Phase != "agents" {
			if got.Agents[0].Status != agentUpdated {
				t.Errorf("agent status = %q, want updated", got.Agents[0].Status)
			}
			// Reaching the controller phase at all proves the order: it only
			// happens once every agent has finished. In this test it fails
			// there, because the test process is not a container.
			if got.Phase != "failed" && got.Phase != "controller" {
				t.Errorf("phase = %q, want the controller step", got.Phase)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never moved past the agent phase")
}

// TestSelfUpdateRunSkipsOfflineAgents: an unreachable agent cannot be updated,
// so it is reported rather than blocking the run.
func TestSelfUpdateRunSkipsOfflineAgents(t *testing.T) {
	shortPolls(t)
	s := newAgentOnlyTestServer(t)
	s.docker = testDockerClient(t)

	offline := &store.Server{Name: "edge", Kind: store.ServerAgent, Status: "offline",
		LastSeen: time.Now().UTC().Add(-time.Hour)}
	if err := s.st.CreateServer(offline); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	run := s.startSelfUpdateRun(selfupdate.Result{
		Kind: selfupdate.KindRelease, Target: "ghcr.io/x/wnd:v0.1.2",
	}, false)
	if len(run.Agents) != 1 || run.Agents[0].Status != agentSkipped {
		t.Fatalf("offline agent = %+v", run.Agents)
	}
	if !strings.Contains(run.Agents[0].Message, "offline") {
		t.Errorf("message = %q", run.Agents[0].Message)
	}
	cmds, _ := s.st.ListPendingCommands(offline.ID)
	if len(cmds) != 0 {
		t.Errorf("an offline agent was given work: %+v", cmds)
	}
}

func shortPolls(t *testing.T) {
	t.Helper()
	oldTimeout, oldPoll := agentUpdateTimeout, agentUpdatePoll
	agentUpdateTimeout, agentUpdatePoll = 2*time.Second, 20*time.Millisecond
	t.Cleanup(func() { agentUpdateTimeout, agentUpdatePoll = oldTimeout, oldPoll })
}

// TestAgentBuildState covers the badge the Servers page shows: an agent reads as
// current only when it is on what this deployment should be running.
func TestAgentBuildState(t *testing.T) {
	s := newAgentOnlyTestServer(t)
	s.version = "v0.1.2"

	agent := func(version string) store.Server {
		return store.Server{Kind: store.ServerAgent, AgentVersion: version}
	}

	if got := s.agentBuildState(agent("v0.1.2")); got != "current" {
		t.Errorf("same version = %q", got)
	}
	if got := s.agentBuildState(agent("v0.1.1")); got != "behind" {
		t.Errorf("older version = %q", got)
	}
	// A main build ahead of the release still counts as current.
	if got := s.agentBuildState(agent("v0.1.2-3-gabc1234")); got != "current" {
		t.Errorf("describe build = %q", got)
	}
	// An agent that has never reported cannot be classified.
	if got := s.agentBuildState(agent("")); got != "unknown" {
		t.Errorf("no version = %q", got)
	}
	// Non-agent servers have no agent build state at all.
	if got := s.agentBuildState(store.Server{Kind: store.ServerDirect}); got != "" {
		t.Errorf("direct endpoint = %q", got)
	}

	// While a release is waiting, the comparison follows what the deployment is
	// moving to — not the controller it is moving away from. Injected, so this
	// does not depend on the network.
	want := "v0.9.9"
	s.selfUpdate = selfupdate.New(
		func(context.Context) selfupdate.Current { return selfupdate.Current{Version: s.version} },
		func(context.Context) (*selfupdate.Release, error) {
			return &selfupdate.Release{Tag: want}, nil
		}, nil)
	if res := s.selfUpdate.Check(context.Background()); res.Latest != want {
		t.Fatalf("check did not record the release: %+v", res)
	}
	if got := s.agentBuildState(agent("v0.1.2")); got != "behind" {
		t.Errorf("an agent on the old controller = %q, want behind", got)
	}
	if got := s.agentBuildState(agent(want)); got != "current" {
		t.Errorf("an agent already on the release = %q, want current", got)
	}
}

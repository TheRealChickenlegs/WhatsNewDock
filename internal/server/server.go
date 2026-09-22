// Package server implements the central server: the HTTP API, the web UI,
// authentication, the embedded local Docker monitor, and the update-check
// scheduler.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/changelog"
	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/reference"
	"github.com/whatsnewdock/whatsnewdock/internal/selfupdate"
	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
	"github.com/whatsnewdock/whatsnewdock/internal/updater"
)

const (
	localSnapshotInterval = 30 * time.Second
	snapshotTimeout       = 60 * time.Second
	offlineMarkInterval   = 60 * time.Second
	offlineThreshold      = 2 * time.Minute
	// checkRunTimeout bounds one full update check, scheduled or on demand.
	checkRunTimeout = 20 * time.Minute
)

// Server is the central WhatsNewDock server runtime.
type Server struct {
	cfg            *config.Config
	st             *store.Store
	auth           *auth.Manager
	docker         *dockerx.Client // nil when no local Docker access
	endpoints      *endpointPool   // Docker clients for agentless direct hosts
	ch             *changelog.Client
	updater        *updater.Updater
	version        string
	localID        string
	handler        http.Handler
	trustedProxies []*net.IPNet
	loginLimiter   *loginLimiter
	jobs           *jobTracker
	// selfUpdate checks whether a newer WhatsNewDock release exists. It never
	// touches this deployment; applying an update is a separate user action.
	selfUpdate *selfupdate.Checker
	// selfRun is the live agent-then-controller update run, if any.
	selfRun *selfUpdateTracker
	// checks records the state of the last update check so the UI can wait for
	// an on-demand check to finish instead of reading stale results.
	checks *checkRunTracker
}

// checkRunTracker records the state of the most recent update check.
type checkRunTracker struct {
	mu         sync.Mutex
	running    bool
	trigger    string
	startedAt  time.Time
	finishedAt time.Time
	err        string
}

// begin marks a check as running. It reports false when one is already in
// flight, so a second request joins the running check instead of starting a
// duplicate.
func (c *checkRunTracker) begin(trigger string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return false
	}
	c.running = true
	c.trigger = trigger
	c.startedAt = time.Now().UTC()
	c.err = ""
	return true
}

func (c *checkRunTracker) end(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = false
	c.finishedAt = time.Now().UTC()
	if err != nil {
		c.err = err.Error()
	}
}

func (c *checkRunTracker) status() (running bool, trigger string, startedAt, finishedAt time.Time, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running, c.trigger, c.startedAt, c.finishedAt, c.err
}

// selfUpdateSelfID is the container this process runs in.
func selfUpdateSelfID() string { return dockerx.SelfContainerID() }

// How long agents are given to come back, and how often we look. Variables so
// tests can shrink them; the defaults are deliberate.
var (
	agentUpdateTimeout = 3 * time.Minute
	agentUpdatePoll    = 3 * time.Second
)

// selfUpdateImage is the image the running container was created from, used to
// redeploy onto a newer tag. Empty when we cannot tell.
func (s *Server) selfUpdateImage(ctx context.Context) string {
	if s.cfg.SelfUpdate.Image != "" {
		return s.cfg.SelfUpdate.Image
	}
	ref, _, err := s.currentImage(ctx)
	if err != nil {
		return ""
	}
	return ref
}

// currentImage resolves the image reference and digest this process runs from.
func (s *Server) currentImage(ctx context.Context) (ref, digest string, err error) {
	id := dockerx.SelfContainerID()
	if id == "" || s.docker == nil {
		return "", "", fmt.Errorf("this instance cannot identify its own container")
	}
	return s.docker.SelfImage(ctx, id)
}

// currentBuild describes the running build for the update check.
func (s *Server) currentBuild(ctx context.Context) selfupdate.Current {
	cur := selfupdate.Current{Version: s.version}
	ref, digest, err := s.currentImage(ctx)
	if err != nil {
		slog.Debug("cannot describe own image", "err", err)
		return cur
	}
	if s.cfg.SelfUpdate.Image != "" {
		ref = s.cfg.SelfUpdate.Image
	}
	cur.Image, cur.Digest = ref, digest
	return cur
}

// imageDigestMoved reports whether the registry now serves a different manifest
// for the image reference we are running. This is what detects a rebuilt moving
// tag such as ":latest", where the version string never changes.
func (s *Server) imageDigestMoved(ctx context.Context, imageRef, currentDigest string) (bool, string, error) {
	ref, err := reference.Parse(imageRef)
	if err != nil {
		return false, "", err
	}
	upToDate, err := s.ch.ManifestUpToDate(ctx, ref.Registry, ref.Repository, ref.Tag, currentDigest)
	if err != nil {
		return false, "", err
	}
	if upToDate {
		return false, currentDigest, nil
	}
	return true, "", nil
}

// New builds a server, opening storage and wiring dependencies.
func New(cfg *config.Config, version string) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "whatsnewdock.db"))
	if err != nil {
		return nil, fmt.Errorf(
			"open database in %s: %w — the data directory must be writable by the "+
				"container user (uid=%d gid=%d). For a bind mount, run `chown -R %d:%d %s`, "+
				"use a named volume, or set `user:` in docker-compose",
			cfg.DataDir, err, os.Getuid(), os.Getgid(), os.Getuid(), os.Getgid(), cfg.DataDir)
	}

	authMgr, err := auth.New(&cfg.Auth, st, cfg.BaseURL)
	if err != nil {
		_ = st.Close()
		return nil, err
	}

	// Docker client for the local host (best-effort: the server still serves
	// the UI and remote agents when the socket is absent).
	var docker *dockerx.Client
	if cfg.Docker.Host != "" {
		if dc, err := dockerx.New(cfg.Docker); err == nil {
			docker = dc
		} else {
			slog.Warn("local docker client unavailable; continuing without local monitoring", "err", err)
		}
	}

	ch := changelog.New(cfg.Updates)
	up := updater.New(st, ch, &cfg.Updates)

	s := &Server{
		cfg:            cfg,
		st:             st,
		auth:           authMgr,
		docker:         docker,
		endpoints:      newEndpointPool(),
		ch:             ch,
		updater:        up,
		version:        version,
		trustedProxies: parseTrustedCIDRs(cfg.TrustedProxies),
		loginLimiter:   newLoginLimiter(),
		jobs:           newJobTracker(),
		selfRun:        &selfUpdateTracker{},
		checks:         &checkRunTracker{},
	}
	s.selfUpdate = selfupdate.New(s.currentBuild, func(ctx context.Context) (*selfupdate.Release, error) {
		rel, err := ch.LatestRelease(ctx, cfg.SelfUpdate.Repo)
		if err != nil || rel == nil {
			return nil, err
		}
		return &selfupdate.Release{
			Tag: rel.Tag, URL: rel.URL, Name: rel.Title, PublishedAt: rel.PublishedAt,
		}, nil
	}, s.imageDigestMoved)

	if err := s.bootstrap(); err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := s.loadRuntimeSettings(); err != nil {
		slog.Warn("failed to load runtime settings", "err", err)
	}
	if err := s.loadAuthSettings(); err != nil {
		slog.Warn("failed to load auth settings", "err", err)
	}
	s.handler = s.routes()
	return s, nil
}

// bootstrap creates the local server row and the initial admin account.
func (s *Server) bootstrap() error {
	if s.docker != nil {
		if err := s.ensureLocalServer(); err != nil {
			return err
		}
	}
	return s.ensureInitialAdmin()
}

func (s *Server) ensureLocalServer() error {
	servers, err := s.st.ListServers()
	if err != nil {
		return err
	}
	for _, srv := range servers {
		if srv.IsLocal {
			s.localID = srv.ID
			return nil
		}
	}
	srv := &store.Server{
		ID:       newServerID(),
		Name:     "local",
		IsLocal:  true,
		Status:   "online",
		LastSeen: time.Now().UTC(),
	}
	if err := s.st.CreateServer(srv); err != nil {
		return err
	}
	s.localID = srv.ID
	return nil
}

func (s *Server) ensureInitialAdmin() error {
	n, err := s.st.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	username := s.cfg.InitialAdminUser
	if username == "" {
		username = "admin"
	}
	password := s.cfg.InitialAdminPassword
	if password == "" {
		password = randomPassword(16)
		slog.Warn("no admin account found; created initial admin",
			"username", username, "password", password,
			"note", "change this password or set WND_INITIAL_ADMIN_PASSWORD on first run")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	u := &store.User{Username: username, Role: auth.RoleAdmin, PasswordHash: hash}
	if err := s.st.CreateUser(u); err != nil {
		return err
	}
	slog.Info("initial admin account created", "username", username)
	return nil
}

// Handler returns the composed HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// Close releases resources.
func (s *Server) Close() error {
	if s.docker != nil {
		_ = s.docker.Close()
	}
	s.endpoints.closeAll()
	return s.st.Close()
}

// Run starts the background loops and the HTTP server until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Mark remote servers offline on startup; agents will report in and direct
	// endpoints will be polled by the snapshot loop below.
	_ = s.st.MarkAllOffline()

	var wg sync.WaitGroup

	// The snapshot loop always runs: it covers the local host when a Docker
	// socket is available and any configured direct endpoints. With neither,
	// it is a no-op and agents keep working exactly as before.
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.snapshotLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.updaterLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.offlineMarkerLoop(ctx)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.selfUpdateLoop(ctx)
	}()

	srv := &http.Server{
		Addr:              s.cfg.Address(),
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", s.cfg.Address(), "mode", string(s.cfg.Mode))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		wg.Wait()
		return nil
	case err := <-errCh:
		wg.Wait()
		return err
	}
}

func (s *Server) snapshotLoop(ctx context.Context) {
	t := time.NewTicker(localSnapshotInterval)
	defer t.Stop()
	s.snapshotAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.snapshotAll(ctx)
		}
	}
}

// snapshotAll refreshes every locally-reachable host. A failing endpoint never
// prevents the others from being refreshed.
func (s *Server) snapshotAll(ctx context.Context) {
	if s.docker != nil {
		s.snapshotLocal(ctx)
	}
	servers, err := s.st.ListServersByKind(store.ServerDirect)
	if err != nil {
		slog.Error("list direct endpoints failed", "err", err)
		return
	}
	for _, srv := range servers {
		s.snapshotDirect(ctx, srv)
	}
}

func (s *Server) snapshotLocal(ctx context.Context) {
	ctx2, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	snap, err := s.docker.Snapshot(ctx2)
	if err != nil {
		slog.Error("local snapshot failed", "err", err)
		return
	}
	snap.Server.ID = s.localID
	snap.Server.IsLocal = true
	snap.Server.Kind = store.ServerLocal
	snap.Server.Status = "online"
	snap.Server.LastSeen = time.Now().UTC()
	// Keep an operator-chosen name; otherwise fall back to the daemon's
	// hostname and finally to "local".
	if cur, err := s.st.GetServer(s.localID); err == nil && cur.NameCustom {
		snap.Server.Name = cur.Name
		snap.Server.NameCustom = true
	}
	if snap.Server.Name == "" {
		snap.Server.Name = "local"
	}
	if err := s.st.ReplaceServerSnapshot(&snap.Server, snap.Stacks, snap.Containers); err != nil {
		slog.Error("persist local snapshot failed", "err", err)
	}
}

// snapshotDirect polls one agentless endpoint over the Docker Engine API.
func (s *Server) snapshotDirect(ctx context.Context, srv store.Server) {
	dc, err := s.endpoints.get(srv)
	if err != nil {
		slog.Warn("direct endpoint unavailable", "server", srv.Name, "err", err)
		return
	}
	ctx2, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	snap, err := dc.Snapshot(ctx2)
	if err != nil {
		slog.Warn("direct snapshot failed", "server", srv.Name, "err", err)
		return
	}
	snap.Server.ID = srv.ID
	snap.Server.Name = srv.Name
	snap.Server.NameCustom = srv.NameCustom
	snap.Server.IsLocal = false
	snap.Server.Kind = store.ServerDirect
	snap.Server.Status = "online"
	snap.Server.LastSeen = time.Now().UTC()
	if err := s.st.ReplaceServerSnapshot(&snap.Server, snap.Stacks, snap.Containers); err != nil {
		slog.Error("persist direct snapshot failed", "server", srv.Name, "err", err)
	}
}

func (s *Server) updaterLoop(ctx context.Context) {
	// First check shortly after startup.
	first := time.NewTimer(30 * time.Second)
	defer first.Stop()
	interval := s.cfg.Updates.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	run := func() { _ = s.runUpdateCheck(ctx, "scheduled") }

	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			run()
		case <-t.C:
			run()
		}
	}
}

func (s *Server) offlineMarkerLoop(ctx context.Context) {
	t := time.NewTicker(offlineMarkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.markOfflineServers()
		}
	}
}

func (s *Server) markOfflineServers() {
	servers, err := s.st.ListServers()
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, srv := range servers {
		if srv.IsLocal {
			continue
		}
		if srv.Status == "online" && now.Sub(srv.LastSeen) > offlineThreshold {
			_ = s.st.MarkServerOffline(srv.ID)
		}
	}
}

// selfUpdateLoop checks for a newer WhatsNewDock release. The interval is read
// every cycle so a change in Settings takes effect without a restart.
func (s *Server) selfUpdateLoop(ctx context.Context) {
	// A short first delay keeps startup quiet and lets the UI come up first.
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		case <-time.After(s.selfUpdateInterval()):
		}
		if !s.cfg.SelfUpdate.Enabled {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res := s.selfUpdate.Check(checkCtx)
		cancel()
		if res.Error != "" {
			slog.Debug("self-update check failed", "err", res.Error)
			continue
		}
		if res.UpdateAvailable {
			slog.Info("a newer WhatsNewDock release is available",
				"current", res.Current, "latest", res.Latest, "url", res.URL)
		}
	}
}

// selfUpdateInterval is the configured check interval, floored at an hour so a
// typo cannot hammer the GitHub API.
func (s *Server) selfUpdateInterval() time.Duration {
	d := s.cfg.SelfUpdate.Interval
	if d < time.Hour {
		return time.Hour
	}
	return d
}

// runUpdateCheck performs one full update check and records its state.
//
// The caller supplies the parent context. Work started from an HTTP handler
// must not inherit the request context: net/http cancels it as soon as the
// handler returns, which would abort the check mid-flight and leave the UI
// showing nothing.
func (s *Server) runUpdateCheck(parent context.Context, trigger string) error {
	if !s.checks.begin(trigger) {
		slog.Info("update check already running", "trigger", trigger)
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, checkRunTimeout)
	defer cancel()

	slog.Info("starting update check", "trigger", trigger)
	start := time.Now()
	err := s.updater.CheckAll(ctx)
	s.checks.end(err)
	if err != nil {
		slog.Error("update check failed", "trigger", trigger, "err", err)
		return err
	}
	slog.Info("update check complete", "trigger", trigger, "duration", time.Since(start).String())
	return nil
}

func newServerID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomPassword(n int) string {
	const chars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

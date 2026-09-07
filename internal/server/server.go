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
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whatsnewdock/whatsnewdock/internal/changelog"
	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/server/auth"
	"github.com/whatsnewdock/whatsnewdock/internal/store"
	"github.com/whatsnewdock/whatsnewdock/internal/updater"
)

const (
	localSnapshotInterval = 30 * time.Second
	offlineMarkInterval   = 60 * time.Second
	offlineThreshold      = 2 * time.Minute
)

// Server is the central WhatsNewDock server runtime.
type Server struct {
	cfg     *config.Config
	st      *store.Store
	auth    *auth.Manager
	docker  *dockerx.Client // nil when no local Docker access
	ch      *changelog.Client
	updater *updater.Updater
	version string
	localID string
	handler http.Handler
}

// New builds a server, opening storage and wiring dependencies.
func New(cfg *config.Config, version string) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "whatsnewdock.db"))
	if err != nil {
		return nil, err
	}

	authMgr, err := auth.New(cfg.Auth, st, cfg.BaseURL)
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
		cfg:     cfg,
		st:      st,
		auth:    authMgr,
		docker:  docker,
		ch:      ch,
		updater: up,
		version: version,
	}

	if err := s.bootstrap(); err != nil {
		_ = st.Close()
		return nil, err
	}
	if err := s.loadRuntimeSettings(); err != nil {
		slog.Warn("failed to load runtime settings", "err", err)
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
	return s.st.Close()
}

// Run starts the background loops and the HTTP server until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Mark remote servers offline on startup; agents will report in.
	_ = s.st.MarkAllOffline()

	var wg sync.WaitGroup

	if s.docker != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.localSnapshotLoop(ctx)
		}()
	}

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

func (s *Server) localSnapshotLoop(ctx context.Context) {
	t := time.NewTicker(localSnapshotInterval)
	defer t.Stop()
	s.snapshotLocal(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.snapshotLocal(ctx)
		}
	}
}

func (s *Server) snapshotLocal(ctx context.Context) {
	snap, err := s.docker.Snapshot(ctx)
	if err != nil {
		slog.Error("local snapshot failed", "err", err)
		return
	}
	snap.Server.ID = s.localID
	snap.Server.IsLocal = true
	snap.Server.Status = "online"
	snap.Server.LastSeen = time.Now().UTC()
	if snap.Server.Name == "" {
		snap.Server.Name = "local"
	}
	if err := s.st.ReplaceServerSnapshot(&snap.Server, snap.Stacks, snap.Containers); err != nil {
		slog.Error("persist local snapshot failed", "err", err)
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

	run := func() {
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		slog.Info("starting update check")
		start := time.Now()
		if err := s.updater.CheckAll(ctx2); err != nil {
			slog.Error("update check failed", "err", err)
			return
		}
		slog.Info("update check complete", "duration", time.Since(start).String())
	}

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

// triggerCheck runs an update check on demand (used by the API).
func (s *Server) triggerCheck(ctx context.Context) error {
	return s.updater.CheckAll(ctx)
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

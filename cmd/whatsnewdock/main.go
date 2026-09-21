// Command whatsnewdock is the entrypoint for the WhatsNewDock server and
// agent. It runs in "server" mode by default and in "agent" mode when
// WND_MODE=agent.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/whatsnewdock/whatsnewdock/internal/agent"
	"github.com/whatsnewdock/whatsnewdock/internal/config"
	"github.com/whatsnewdock/whatsnewdock/internal/dockerx"
	"github.com/whatsnewdock/whatsnewdock/internal/server"
)

// version is injected at build time via -ldflags.
var version = "dev"

func main() {
	flags := config.ParseFlags(os.Args[1:])
	if flags.Version {
		fmt.Println("whatsnewdock", version)
		return
	}
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration error:", err)
		os.Exit(1)
	}
	setupLogging(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The self-update helper is a one-shot process: it replaces the container it
	// was started for and exits. It must never fall through to serving traffic.
	if cfg.Mode == config.ModeSelfUpdate {
		if err := runSelfUpdateHelper(ctx, cfg); err != nil {
			slog.Error("self-update helper failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if cfg.Mode == config.ModeAgent {
		if err := runAgent(ctx, cfg); err != nil {
			slog.Error("agent exited", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := runServer(ctx, cfg); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func runServer(ctx context.Context, cfg *config.Config) error {
	srv, err := server.New(cfg, version)
	if err != nil {
		return err
	}
	defer srv.Close()
	return srv.Run(ctx)
}

// runSelfUpdateHelper redeploys the container named in the environment onto the
// requested image. It runs from the image that is already on the host, so the
// code performing the swap is the code already proven to work there.
func runSelfUpdateHelper(ctx context.Context, cfg *config.Config) error {
	selfID := os.Getenv(dockerx.EnvSelfUpdateContainer)
	target := os.Getenv(dockerx.EnvSelfUpdateImage)
	if selfID == "" || target == "" {
		return fmt.Errorf("helper needs %s and %s", dockerx.EnvSelfUpdateContainer, dockerx.EnvSelfUpdateImage)
	}
	docker, err := dockerx.New(cfg.Docker)
	if err != nil {
		return err
	}
	defer docker.Close()
	slog.Info("redeploying container", "container", selfID, "image", target)
	if err := docker.RunSelfUpdateHelper(ctx, selfID, target); err != nil {
		return err
	}
	slog.Info("redeploy complete", "container", selfID, "image", target)
	return nil
}

func runAgent(ctx context.Context, cfg *config.Config) error {
	docker, err := dockerx.New(cfg.Docker)
	if err != nil {
		return err
	}
	defer docker.Close()
	a := agent.New(cfg.Agent, docker, version)
	return a.Run(ctx)
}

func setupLogging(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(h))
}

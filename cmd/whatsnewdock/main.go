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

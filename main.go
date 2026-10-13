package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"perps/internal/app"
	"perps/internal/config"
	"perps/internal/rpc"
	"perps/internal/server"
	"perps/internal/sources"
	"perps/internal/store"
)

const (
	healthURL     = "http://127.0.0.1:8080/healthz"
	healthTimeout = 2 * time.Second
)

var errUsage = errors.New("usage: app [serve|healthcheck|migrate]")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	slog.SetDefault(logger())
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("perps-engine stopped", "error", err)
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(ctx)
	case "healthcheck":
		return healthcheck(ctx, healthURL)
	case "migrate":
		return migrate(ctx)
	}
	return fmt.Errorf("%w, got %q", errUsage, cmd)
}

// postgresDSN and postgresSchema stay empty in production, so the libpq PG variables decide
var (
	postgresDSN    string
	postgresSchema string
	apiAddr        = server.Addr
	debugAddr      = server.DebugAddr
)

const migrateTimeout = 30 * time.Second

func openStore(ctx context.Context) (*store.Store, error) {
	st, err := store.Connect(ctx, postgresDSN, postgresSchema)
	if err != nil {
		return nil, err
	}
	mctx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()
	if err := st.Migrate(mctx); err != nil {
		st.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return st, nil
}

func serve(ctx context.Context) error {
	cfg, err := config.Load(config.Dir())
	if err != nil {
		return err
	}
	slog.Info("perps-engine started", "chain", cfg.Chain, "exchange", cfg.Exchange,
		"refresh_interval", cfg.RefreshInterval, "hypersync_key_set", cfg.HyperSyncKey.Set())
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	chain, err := newChain(cfg)
	if err != nil {
		return fmt.Errorf("rpc client: %w", err)
	}
	deps := app.Deps{Config: cfg, Chain: chain, Store: st, Logger: slog.Default()}
	if cfg.LeaderboardURL != "" {
		board, err := sources.NewREST(cfg.LeaderboardURL)
		if err != nil {
			return fmt.Errorf("leaderboard client: %w", err)
		}
		deps.Board = board
	}
	if cfg.HyperSyncKey.Set() {
		deps.Hyper = sources.NewHyperSync(cfg.HyperSyncURL, cfg.HyperSyncKey)
	}
	a, err := app.New(deps)
	if err != nil {
		return err
	}

	runCtx, stopRunner := context.WithCancel(ctx)
	defer stopRunner()
	var loop sync.WaitGroup
	loop.Go(func() {
		if err := a.Runner.Run(runCtx); err != nil {
			slog.Error("refresh runner stopped", "error", err)
		}
	})
	err = server.Run(ctx, server.NewServer(apiAddr, a.API), server.Debug(debugAddr, a.Metrics))
	stopRunner()
	loop.Wait()
	return err
}

func newChain(cfg config.Config) (*rpc.Client, error) {
	return rpc.New(cfg.RPCURL, rpc.WithUpstreamFilter(cfg.RPCUpstreamFilter), rpc.WithOnCredits(func(method, header string, paid bool) {
		if paid {
			slog.Warn("paid rpc call", "method", method, "credits", header)
		}
	}))
}

func migrate(ctx context.Context) error {
	if _, err := config.Load(config.Dir()); err != nil {
		return err
	}
	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	st.Close()
	slog.Info("migrations applied")
	return nil
}

func healthcheck(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %s", resp.Status)
	}
	return nil
}

func logger() *slog.Logger {
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL")))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"perps/internal/config"
	"perps/internal/perpl"
	"perps/internal/refresh"
	"perps/internal/server"
)

// Store is everything the runner writes and the routes read
type Store interface {
	refresh.Store
	Reader
}

type Deps struct {
	Config config.Config
	Chain  refresh.Chain
	Store  Store
	// Hyper and Board may be nil, which turns the backfill and the leaderboard poll off
	Hyper refresh.Scanner
	Board refresh.Board
	// MarketData may be nil, which turns the candle and funding history off
	MarketData MarketData
	Logger     *slog.Logger
}

// staleAfter is how old a snapshot may get before the API calls it stale, a few missed refreshes
func staleAfter(c config.Config) time.Duration { return 4 * c.RefreshInterval }

// leaderboardMaxAge is how old a leaderboard copy may get, a few missed polls
func leaderboardMaxAge(c config.Config) time.Duration { return 3 * c.LeaderboardInterval }

// boardGap is the pause between the four leaderboard requests of one poll
const boardGap = time.Second

// App holds the runner to start and the two handlers to serve
type App struct {
	Runner  *refresh.Runner
	API     http.Handler
	Metrics http.Handler
}

func New(d Deps) (*App, error) {
	exchange, err := perpl.ParseAddress(d.Config.Exchange)
	if err != nil {
		return nil, fmt.Errorf("exchange address: %w", err)
	}
	// the runner owns its own registry because the server metrics need the runner first
	reg := prometheus.NewRegistry()
	runner, err := refresh.New(refresh.Options{
		Config: d.Config, Chain: d.Chain, Store: d.Store, HyperSync: d.Hyper, Board: d.Board, BoardGap: boardGap,
		Registerer: reg, Logger: d.Logger,
	})
	if err != nil {
		return nil, err
	}
	src := NewSource(runner, d.Store, exchange, WithLeaderboardMaxAge(leaderboardMaxAge(d.Config), time.Now), WithMarketData(d.MarketData))
	m := server.NewMetrics(src)
	return &App{
		Runner:  runner,
		API:     server.New(src, m, server.Options{StaleAfter: staleAfter(d.Config)}),
		Metrics: promhttp.HandlerFor(prometheus.Gatherers{m.Registry, reg}, promhttp.HandlerOpts{}),
	}, nil
}

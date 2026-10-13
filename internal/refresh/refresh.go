// Package refresh keeps the in-memory Perpl snapshot fresh and feeds Postgres
package refresh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"perps/internal/config"
	"perps/internal/perpl"
	"perps/internal/rpc"
	"perps/internal/sources"
	"perps/internal/store"
)

const (
	// the node may reorg the last blocks so every read is pinned a little behind the head
	pinDepth       = 2
	fallbackWindow = 9000
	pruneEvery     = time.Hour
	// a day more than the 168 hours the funding route can ask for
	marketRetention = 8 * 24 * time.Hour
	// the history route asks for at most 720 hours, one day more is kept
	oiRetention = 31 * 24 * time.Hour
	// the 30 s snapshots are thinned to one open interest point per bucket
	oiBucket        = 5 * time.Minute
	dbTimeout       = 10 * time.Second
	accountCacheTTL = 5 * time.Minute
	accountCacheMax = 10000
	maxTopLookups   = 400
	maxPositionPage = 1000
)

var (
	ErrNotReady        = errors.New("no snapshot yet")
	ErrAccountNotFound = errors.New("perpl account not found")
	ErrUnknownMarket   = errors.New("unknown market")
)

// Chain is the part of the rpc client the runner uses
type Chain interface {
	BlockNumber(ctx context.Context) (uint64, error)
	BlockByNumber(ctx context.Context, number uint64) (rpc.Block, error)
	Call(ctx context.Context, to perpl.Address, data []byte, block uint64) ([]byte, error)
	StorageAt(ctx context.Context, addr perpl.Address, slot perpl.Hash, block uint64) (perpl.Hash, error)
	Logs(ctx context.Context, addr perpl.Address, topic0 perpl.Hash, from, to uint64) ([]rpc.Log, error)
	TransactionByHash(ctx context.Context, hash perpl.Hash) (rpc.Tx, error)
	PaidCalls() int64
}

// Store is the part of the Postgres store the runner uses
type Store interface {
	InsertMarketSnapshots(ctx context.Context, rows []store.MarketSnapshot) error
	InsertPositionSnapshots(ctx context.Context, rows []store.PositionSnapshot) error
	PrunePositionSnapshots(ctx context.Context, before time.Time) (int64, error)
	PruneMarketSnapshots(ctx context.Context, before time.Time) (int64, error)
	InsertMarketOI(ctx context.Context, rows []store.OIPoint) error
	PruneMarketOI(ctx context.Context, before time.Time) (int64, error)
	UpsertLiquidations(ctx context.Context, rows []store.Liquidation) error
	CountLiquidations(ctx context.Context) (store.LiquidationTotals, error)
	UpsertAccounts(ctx context.Context, rows []store.Account) error
	AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error)
	UnresolvedLiquidationAccounts(ctx context.Context, limit int) ([]uint64, error)
	Cursor(ctx context.Context, name string) (block uint64, found bool, err error)
	SetCursor(ctx context.Context, name string, block uint64) error
}

// Scanner is the HyperSync client
type Scanner interface {
	Scan(ctx context.Context, q sources.Query, fn func(sources.Page) error) (uint64, error)
}

// Board is the public leaderboard client
type Board interface {
	Board(ctx context.Context, period sources.Period, by sources.By) (sources.Leaderboard, error)
}

var (
	_ Chain   = (*rpc.Client)(nil)
	_ Store   = (*store.Store)(nil)
	_ Scanner = (*sources.HyperSync)(nil)
	_ Board   = (*sources.REST)(nil)
)

type Options struct {
	Config config.Config
	Chain  Chain
	Store  Store
	// HyperSync is used for the backfill only when the config also holds a key
	HyperSync Scanner
	// Board is optional and a nil value turns the leaderboard poll off
	Board Board
	// BoardGap is the pause between two leaderboard requests
	BoardGap   time.Duration
	Registerer prometheus.Registerer
	Logger     *slog.Logger
	Now        func() time.Time
}

type Runner struct {
	cfg          config.Config
	exchange     perpl.Address
	expectedImpl *perpl.Address
	chain        Chain
	store        Store
	hyper        Scanner
	board        Board
	boardGap     time.Duration
	log          *slog.Logger
	now          func() time.Time
	sem          chan struct{}
	lookupSem    chan struct{}
	dbTimeout    time.Duration
	batchLimit   int
	m            *metrics

	latest     atomic.Pointer[Snapshot]
	leaders    atomic.Pointer[LeaderboardData]
	accountsCh chan *Snapshot

	// the refresh loop is the only user of these
	slow         *slowData
	baselineImpl *perpl.Address
	lastPrune    time.Time
	lastOI       time.Time
	lastPaid     int64

	refreshes     atomic.Uint64
	refreshErrors atomic.Uint64
	lastErrorAtNs atomic.Int64
	decimalsMu    sync.Mutex
	decimals      map[uint64]scales
	knownMu       sync.Mutex
	known         map[uint64]struct{}
	// missing holds ids that getAccountById reverted on, so each is asked once
	missing        map[uint64]struct{}
	accountMu      sync.Mutex
	accountCache   map[perpl.Address]accountEntry
	flights        map[perpl.Address]*flight
	historyMu      sync.Mutex
	history        historyState
	tailChunk      uint64
	backfillActive bool
}

func New(o Options) (*Runner, error) {
	if err := validate(o); err != nil {
		return nil, err
	}
	exchange, err := perpl.ParseAddress(o.Config.Exchange)
	if err != nil {
		return nil, fmt.Errorf("exchange address: %w", err)
	}
	r := &Runner{
		cfg:            o.Config,
		exchange:       exchange,
		chain:          o.Chain,
		store:          o.Store,
		hyper:          o.HyperSync,
		board:          o.Board,
		boardGap:       o.BoardGap,
		log:            o.Logger,
		now:            o.Now,
		sem:            make(chan struct{}, o.Config.MaxInflightCalls),
		lookupSem:      make(chan struct{}, lookupSlots),
		dbTimeout:      dbTimeout,
		batchLimit:     maxMulticallCalls,
		accountsCh:     make(chan *Snapshot, 1),
		decimals:       make(map[uint64]scales),
		known:          make(map[uint64]struct{}),
		missing:        make(map[uint64]struct{}),
		accountCache:   make(map[perpl.Address]accountEntry),
		flights:        make(map[perpl.Address]*flight),
		tailChunk:      5 * rpc.MaxLogSpan,
		backfillActive: o.HyperSync != nil && o.Config.HyperSyncKey.Set(),
	}
	if o.Config.ExpectedImplementation != "" {
		impl, err := perpl.ParseAddress(o.Config.ExpectedImplementation)
		if err != nil {
			return nil, fmt.Errorf("expected implementation: %w", err)
		}
		r.expectedImpl = &impl
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.m, err = newMetrics(o.Registerer, r); err != nil {
		return nil, err
	}
	return r, nil
}

func validate(o Options) error {
	c := o.Config
	switch {
	case o.Chain == nil:
		return errors.New("refresh: chain client is required")
	case o.Store == nil:
		return errors.New("refresh: store is required")
	case c.MaxInflightCalls < 1:
		return errors.New("refresh: max_inflight_calls must be at least 1")
	case c.PositionPageSize < 1:
		return errors.New("refresh: position_page_size must be at least 1")
	case c.RefreshInterval <= 0:
		return errors.New("refresh: refresh_interval must be positive")
	case c.PositionSnapshotEvery < 1:
		return errors.New("refresh: position_snapshot_every must be at least 1")
	case len(c.Shocks) == 0:
		return errors.New("refresh: at least one shock is required")
	}
	return nil
}

/* Public reads */

func (r *Runner) Latest() *Snapshot { return r.latest.Load() }

func (r *Runner) Ready() bool { return r.latest.Load() != nil }

/* Run */

// Run blocks until ctx ends and then returns nil once every loop has stopped
func (r *Runner) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	start := func(fn func(context.Context)) { wg.Go(func() { fn(ctx) }) }
	start(r.runAccounts)
	start(r.runHistory)
	if r.board != nil {
		start(r.runLeaderboard)
	}

	r.refresh(ctx)
	r.pruneIfDue(ctx)
	tick := time.NewTicker(r.cfg.RefreshInterval)
	defer tick.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-tick.C:
			r.refresh(ctx)
			r.pruneIfDue(ctx)
		}
	}
	wg.Wait()
	return nil
}

// wait returns false when ctx ended first
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (r *Runner) countError(stage string, err error) {
	r.m.errors.WithLabelValues(stage).Inc()
	r.lastErrorAtNs.Store(r.now().UnixNano())
	r.log.Warn("refresh error", "stage", stage, "err", err)
}

/* Limited calls */

func (r *Runner) acquire(ctx context.Context) (func(), error) {
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func limited[T any](ctx context.Context, r *Runner, fn func() (T, error)) (T, error) {
	var zero T
	release, err := r.acquire(ctx)
	if err != nil {
		return zero, err
	}
	defer release()
	return fn()
}

func (r *Runner) call(ctx context.Context, data []byte, block uint64) ([]byte, error) {
	return limited(ctx, r, func() ([]byte, error) { return r.chain.Call(ctx, r.exchange, data, block) })
}

func (r *Runner) blockNumber(ctx context.Context) (uint64, error) {
	return limited(ctx, r, func() (uint64, error) { return r.chain.BlockNumber(ctx) })
}

func (r *Runner) blockByNumber(ctx context.Context, n uint64) (rpc.Block, error) {
	return limited(ctx, r, func() (rpc.Block, error) { return r.chain.BlockByNumber(ctx, n) })
}

/* Groups */

// group runs functions concurrently and cancels the rest on the first error
type group struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

func newGroup(parent context.Context) *group {
	ctx, cancel := context.WithCancel(parent)
	return &group{ctx: ctx, cancel: cancel}
}

func (g *group) Go(fn func(ctx context.Context) error) {
	g.wg.Go(func() {
		if err := fn(g.ctx); err != nil {
			g.once.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	})
}

func (g *group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}

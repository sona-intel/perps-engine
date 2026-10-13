// Package app joins the refresh runner and the Postgres store into the source the HTTP server reads
package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"perps/internal/perpl"
	"perps/internal/refresh"
	"perps/internal/server"
	"perps/internal/sources"
	"perps/internal/store"
)

// Runner is the part of the refresh runner the source reads
type Runner interface {
	Latest() *refresh.Snapshot
	Ready() bool
	Leaderboard() (refresh.LeaderboardData, bool)
	ResolveAccount(ctx context.Context, addr perpl.Address) (perpl.Account, error)
	Status() refresh.Status
}

// Reader is the part of the Postgres store the HTTP routes read
type Reader interface {
	AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error)
	Liquidations(ctx context.Context, f store.LiquidationFilter) (store.LiquidationPage, error)
	LiquidationsDaily(ctx context.Context, perpID *uint64, since time.Time) ([]store.DailyLiquidations, error)
	FundingSeries(ctx context.Context, perpID uint64, since time.Time, step time.Duration) ([]store.FundingPoint, error)
	KeeperCounts(ctx context.Context, limit int) ([]store.KeeperCount, error)
	CountLiquidations(ctx context.Context) (store.LiquidationTotals, error)
}

type converted struct {
	from *refresh.Snapshot
	to   *server.Snapshot
}

// Source implements server.Source
type Source struct {
	Reader
	runner   Runner
	exchange perpl.Address
	cache    atomic.Pointer[converted]

	boardMaxAge time.Duration
	now         func() time.Time
}

// SourceOption tunes a Source
type SourceOption func(*Source)

// WithLeaderboardMaxAge drops a leaderboard copy that is older than limit, so the volume fields fall back to the unavailable code
func WithLeaderboardMaxAge(limit time.Duration, now func() time.Time) SourceOption {
	return func(s *Source) { s.boardMaxAge, s.now = limit, now }
}

func NewSource(r Runner, rd Reader, exchange perpl.Address, opts ...SourceOption) *Source {
	s := &Source{Reader: rd, runner: r, exchange: exchange, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Snapshot converts each refresh once, since handlers call it on every request
func (s *Source) Snapshot() *server.Snapshot {
	snap := s.runner.Latest()
	if snap == nil {
		return nil
	}
	if c := s.cache.Load(); c != nil && c.from == snap {
		return c.to
	}
	c := &converted{from: snap, to: &server.Snapshot{
		Block:    snap.Block,
		Time:     snap.Time,
		TakenAt:  snap.TakenAt,
		Exchange: snap.Exchange,
		Books:    snap.Books(),
	}}
	s.cache.Store(c)
	return c.to
}

func (s *Source) Ready() bool { return s.runner.Ready() }

func (s *Source) Runtime() server.Runtime {
	st := s.runner.Status()
	rt := server.Runtime{Exchange: s.exchange, Backfill: st.Backfill, PaidRPCCalls: st.PaidRPCCalls}
	if snap := s.runner.Latest(); snap != nil {
		rt.Implementation = snap.Implementation
		rt.ImplementationChanged = snap.ImplementationChanged
	}
	return rt
}

func (s *Source) Leaderboard(period sources.Period) (sources.Leaderboard, bool) {
	data, ok := s.runner.Leaderboard()
	if !ok {
		return sources.Leaderboard{}, false
	}
	var lb *sources.Leaderboard
	var at time.Time
	switch period {
	case sources.PeriodDay:
		lb, at = data.Day, data.DayAt
	case sources.PeriodAll:
		lb, at = data.All, data.AllAt
	}
	if lb == nil || (s.boardMaxAge > 0 && s.now().Sub(at) > s.boardMaxAge) {
		return sources.Leaderboard{}, false
	}
	return *lb, true
}

func (s *Source) Account(ctx context.Context, addr perpl.Address) (perpl.Account, error) {
	acct, err := s.runner.ResolveAccount(ctx, addr)
	if errors.Is(err, refresh.ErrAccountNotFound) {
		return perpl.Account{}, fmt.Errorf("%w: %w", server.ErrAccountNotFound, err)
	}
	return acct, err
}

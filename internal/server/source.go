package server

import (
	"context"
	"errors"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/sources"
	"perps/internal/store"
)

const (
	BackfillDisabled = "disabled"
	BackfillPending  = "pending"
	BackfillRunning  = "running"
	BackfillRetrying = "retrying"
	BackfillComplete = "complete"
)

var ErrAccountNotFound = errors.New("server: no Perpl account")

// Snapshot is read by many handlers at once, so the refresh loop must never change one after it publishes it
type Snapshot struct {
	Block uint64
	Time  time.Time
	// TakenAt is when the refresh finished, the zero time when unknown
	TakenAt  time.Time
	Exchange perpl.ExchangeInfo
	Books    []risk.MarketBook
}

type Runtime struct {
	Exchange              perpl.Address
	Implementation        perpl.Address
	ImplementationChanged bool
	Backfill              string
	PaidRPCCalls          int64
}

// Source must be safe for concurrent use
type Source interface {
	// Snapshot is nil before the first refresh has finished
	Snapshot() *Snapshot
	Runtime() Runtime
	Ready() bool
	// Leaderboard reports false when none was fetched yet or the last fetch failed
	Leaderboard(period sources.Period) (sources.Leaderboard, bool)
	// Account returns ErrAccountNotFound, wrapped or not, when the exchange has no account for the address
	Account(ctx context.Context, addr perpl.Address) (perpl.Account, error)
	AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error)
	Liquidations(ctx context.Context, f store.LiquidationFilter) (store.LiquidationPage, error)
	LiquidationsDaily(ctx context.Context, perpID *uint64, since time.Time) ([]store.DailyLiquidations, error)
	FundingSeries(ctx context.Context, perpID uint64, since time.Time, step time.Duration) ([]store.FundingPoint, error)
	KeeperCounts(ctx context.Context, limit int) ([]store.KeeperCount, error)
	CountLiquidations(ctx context.Context) (store.LiquidationTotals, error)
}

// priced drops the perp ids that exist but were never launched, because they have no mark price
func (s *Snapshot) priced() []risk.MarketBook {
	out := make([]risk.MarketBook, 0, len(s.Books))
	for _, b := range s.Books {
		if isPriced(b) {
			out = append(out, b)
		}
	}
	return out
}

func isPriced(b risk.MarketBook) bool { return b.Market.MarkPNS != nil && b.Market.MarkPNS.Sign() > 0 }

func (s *Snapshot) positions() int {
	n := 0
	for _, b := range s.priced() {
		n += len(b.Positions)
	}
	return n
}

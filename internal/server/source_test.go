package server

import (
	"context"
	"time"

	"perps/internal/perpl"
	"perps/internal/store"
)

// storeReads is the part of Source that the Postgres store answers on its own
type storeReads interface {
	AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error)
	Liquidations(ctx context.Context, f store.LiquidationFilter) (store.LiquidationPage, error)
	LiquidationsDaily(ctx context.Context, perpID *uint64, since time.Time) ([]store.DailyLiquidations, error)
	FundingSeries(ctx context.Context, perpID uint64, since time.Time, step time.Duration) ([]store.FundingPoint, error)
	KeeperCounts(ctx context.Context, limit int) ([]store.KeeperCount, error)
	CountLiquidations(ctx context.Context) (store.LiquidationTotals, error)
}

var (
	_ storeReads = (*store.Store)(nil)
	_ Source     = (*fakeSource)(nil)
)

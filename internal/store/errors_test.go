package store_test

import (
	"math"
	"os"
	"testing"
	"time"

	"perps/internal/store"
	"perps/internal/testkit"
)

func TestEveryMethodReturnsAnErrorOnceThePoolIsClosed(t *testing.T) {
	d := migrated(t)
	s, err := store.Connect(t.Context(), os.Getenv(testkit.EnvDSN), d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	perp := uint64(1)
	calls := map[string]func() error{
		"Migrate":               func() error { return s.Migrate(ctx) },
		"InsertMarketSnapshots": func() error { return s.InsertMarketSnapshots(ctx, []store.MarketSnapshot{testkit.Market(1, 1)}) },
		"InsertPositionSnapshot": func() error {
			return s.InsertPositionSnapshots(ctx, []store.PositionSnapshot{testkit.Position(1, 1, 1)})
		},
		"PrunePositionSnapshots": func() error { _, err := s.PrunePositionSnapshots(ctx, time.Now()); return err },
		"PruneMarketSnapshots":   func() error { _, err := s.PruneMarketSnapshots(ctx, time.Now()); return err },
		"FundingSeries":          func() error { _, err := s.FundingSeries(ctx, 1, time.Now(), 0); return err },
		"UpsertLiquidations":     func() error { return s.UpsertLiquidations(ctx, []store.Liquidation{testkit.Liquidation(1, 1, 1)}) },
		"Liquidations":           func() error { _, err := s.Liquidations(ctx, store.LiquidationFilter{Limit: 1}); return err },
		"LiquidationsDaily":      func() error { _, err := s.LiquidationsDaily(ctx, &perp, time.Now()); return err },
		"KeeperCounts":           func() error { _, err := s.KeeperCounts(ctx, 0); return err },
		"CountLiquidations":      func() error { _, err := s.CountLiquidations(ctx); return err },
		"UpsertAccounts":         func() error { return s.UpsertAccounts(ctx, []store.Account{{ID: 1, Address: testkit.Addr(1)}}) },
		"AccountAddresses":       func() error { _, err := s.AccountAddresses(ctx, []uint64{1}); return err },
		"Cursor":                 func() error { _, _, err := s.Cursor(ctx, store.CursorTail); return err },
		"SetCursor":              func() error { return s.SetCursor(ctx, store.CursorTail, 1) },
		"Ping":                   func() error { return s.Ping(ctx) },
	}
	s.Close()
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s succeeded on a closed pool", name)
		}
	}
}

func TestIdsBeyondInt64AreRejectedBeforeAnyQuery(t *testing.T) {
	d := migrated(t)
	s := d.Store
	ctx := t.Context()
	huge := uint64(math.MaxInt64) + 1
	perp := huge
	calls := map[string]func() error{
		"FundingSeries": func() error { _, err := s.FundingSeries(ctx, huge, time.Now(), 0); return err },
		"Liquidations perp": func() error {
			_, err := s.Liquidations(ctx, store.LiquidationFilter{Limit: 1, PerpID: &perp})
			return err
		},
		"Liquidations block": func() error {
			_, err := s.Liquidations(ctx, store.LiquidationFilter{Limit: 1, After: &store.LiquidationCursor{Block: huge}})
			return err
		},
		"Liquidations log index": func() error {
			_, err := s.Liquidations(ctx, store.LiquidationFilter{Limit: 1, After: &store.LiquidationCursor{LogIndex: huge}})
			return err
		},
		"LiquidationsDaily": func() error { _, err := s.LiquidationsDaily(ctx, &perp, time.Now()); return err },
		"UpsertAccounts":    func() error { return s.UpsertAccounts(ctx, []store.Account{{ID: huge}}) },
		"AccountAddresses":  func() error { _, err := s.AccountAddresses(ctx, []uint64{huge}); return err },
		"InsertPositionSnapshots": func() error {
			return s.InsertPositionSnapshots(ctx, []store.PositionSnapshot{testkit.Position(huge, 1, 1)})
		},
		"market positions count": func() error {
			m := testkit.Market(1, 1)
			m.Positions = -1
			return s.InsertMarketSnapshots(ctx, []store.MarketSnapshot{m})
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s accepted an id beyond int64", name)
		}
	}
}

func TestMigrateRollsBackWhenAFileFails(t *testing.T) {
	d := testkit.New(t)
	if _, err := d.Store.Pool.Exec(t.Context(), `create table market_snapshots (x int)`); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Migrate(t.Context()); err == nil {
		t.Fatal("Migrate succeeded over a conflicting table")
	}
	var n int
	if err := d.Store.Pool.QueryRow(t.Context(), `select count(*) from information_schema.tables where table_schema = $1 and table_name in ('liquidations', 'schema_migrations')`, d.Schema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a failed migration left %d tables behind", n)
	}
}

package store_test

import (
	"math"
	"testing"
	"time"

	"perps/internal/store"
	"perps/internal/testkit"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func count(t *testing.T, d *testkit.DB, table string) int {
	t.Helper()
	var n int
	if err := d.Store.Pool.QueryRow(t.Context(), `select count(*) from `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInsertMarketSnapshotsStoresEveryColumn(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	row := store.MarketSnapshot{
		PerpID: 20, Block: 112777356, Time: t0, Mark: "2534.51", Oracle: "2534.3", OracleTime: t0.Add(-2 * time.Second),
		LongOI: "1800.123", ShortOI: "1800.123", LongNotional: "4562341.77", ShortNotional: "4562341.77", Positions: 84,
		Insurance: "13021.500000", FundingRatePct100k: -12, SCRM10: new("4.25"), SCRP10: nil,
	}
	if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{row}); err != nil {
		t.Fatal(err)
	}
	var (
		perp, block, positions                             int64
		ts, oracleTS                                       time.Time
		mark, oracle, longOI, shortOI, longN, shortN, insu string
		rate                                               int16
		m10, p10                                           *string
	)
	err := d.Store.Pool.QueryRow(t.Context(), `select perp_id, block, ts, mark::text, oracle::text, oracle_ts, long_oi::text, short_oi::text,
		long_notional::text, short_notional::text, positions, insurance::text, funding_rate_pct100k, scr_m10::text, scr_p10::text from market_snapshots`).
		Scan(&perp, &block, &ts, &mark, &oracle, &oracleTS, &longOI, &shortOI, &longN, &shortN, &positions, &insu, &rate, &m10, &p10)
	if err != nil {
		t.Fatal(err)
	}
	if perp != 20 || block != 112777356 || !ts.Equal(t0) || !oracleTS.Equal(t0.Add(-2*time.Second)) || mark != "2534.51" || oracle != "2534.3" ||
		longOI != "1800.123" || shortOI != "1800.123" || longN != "4562341.77" || shortN != "4562341.77" || positions != 84 ||
		insu != "13021.500000" || rate != -12 || m10 == nil || *m10 != "4.25" || p10 != nil {
		t.Fatalf("stored row: perp %d block %d ts %v oracle_ts %v mark %s oracle %s oi %s/%s notional %s/%s positions %d insurance %s rate %d scr %v/%v",
			perp, block, ts, oracleTS, mark, oracle, longOI, shortOI, longN, shortN, positions, insu, rate, m10, p10)
	}
}

func TestInsertMarketSnapshotsIsIdempotentPerPerpAndBlock(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	batch := []store.MarketSnapshot{testkit.Market(1, 100), testkit.Market(10, 100), testkit.Market(20, 100)}
	for range 3 {
		if err := d.Store.InsertMarketSnapshots(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{testkit.Market(1, 100), testkit.Market(1, 101)}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, "market_snapshots"); n != 4 {
		t.Fatalf("%d rows, want 4", n)
	}
}

func TestInsertMarketSnapshotsKeepsTheFirstWriteOnAConflict(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	first := testkit.Market(1, 100)
	again := first
	again.Mark = "1"
	for _, r := range []store.MarketSnapshot{first, again} {
		if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{r}); err != nil {
			t.Fatal(err)
		}
	}
	var mark string
	if err := d.Store.Pool.QueryRow(t.Context(), `select mark::text from market_snapshots`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	if mark != first.Mark {
		t.Fatalf("mark %s, want the first write %s", mark, first.Mark)
	}
}

func TestInsertMarketSnapshotsNoRowsIsANoOp(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	if err := d.Store.InsertMarketSnapshots(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, "market_snapshots"); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

func TestInsertMarketSnapshotsRejectsIdsBeyondInt64(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	bad := testkit.Market(1, 100)
	bad.Block = math.MaxUint64
	if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{testkit.Market(1, 99), bad}); err == nil {
		t.Fatal("a block above int64 was accepted")
	}
	if n := count(t, d, "market_snapshots"); n != 0 {
		t.Fatalf("a failed batch left %d rows", n)
	}
}

func TestInsertMarketSnapshotsRejectsANonNumber(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	bad := testkit.Market(1, 100)
	bad.Mark = "abc"
	if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{bad}); err == nil {
		t.Fatal("a non-numeric mark was accepted")
	}
}

func TestInsertPositionSnapshotsRoundTrip(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	row := store.PositionSnapshot{
		Block: 112777356, Time: t0, PerpID: 1, AccountID: 4848, Side: store.SideShort, Size: "3.25", Entry: "82912.1", Deposit: "9000.5",
		PremiumPnl: "-12.25", DeltaPnl: "140.1", LiqPrice: "90123.45", BkPrice: "0",
	}
	if err := d.Store.InsertPositionSnapshots(t.Context(), []store.PositionSnapshot{row}); err != nil {
		t.Fatal(err)
	}
	var (
		block, perp, acct                                       int64
		ts                                                      time.Time
		side, size, entry, deposit, premium, delta, liq, bankrp string
	)
	err := d.Store.Pool.QueryRow(t.Context(), `select block, ts, perp_id, account_id, side, size::text, entry::text, deposit::text, premium_pnl::text,
		delta_pnl::text, liq_price::text, bk_price::text from position_snapshots`).Scan(&block, &ts, &perp, &acct, &side, &size, &entry, &deposit, &premium, &delta, &liq, &bankrp)
	if err != nil {
		t.Fatal(err)
	}
	if block != 112777356 || !ts.Equal(t0) || perp != 1 || acct != 4848 || side != "short" || size != "3.25" || entry != "82912.1" ||
		deposit != "9000.5" || premium != "-12.25" || delta != "140.1" || liq != "90123.45" || bankrp != "0" {
		t.Fatalf("stored row: %d %v %d %d %s %s %s %s %s %s %s %s", block, ts, perp, acct, side, size, entry, deposit, premium, delta, liq, bankrp)
	}
}

func TestInsertPositionSnapshotsIsIdempotentPerBlockPerpAccount(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	batch := []store.PositionSnapshot{testkit.Position(100, 1, 1), testkit.Position(100, 1, 2), testkit.Position(100, 10, 1)}
	for range 3 {
		if err := d.Store.InsertPositionSnapshots(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, d, "position_snapshots"); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

func TestInsertPositionSnapshotsRejectsABadSideAndKeepsNothing(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	bad := testkit.Position(100, 1, 2)
	bad.Side = "up"
	if err := d.Store.InsertPositionSnapshots(t.Context(), []store.PositionSnapshot{testkit.Position(100, 1, 1), bad}); err == nil {
		t.Fatal("side up was accepted")
	}
	if n := count(t, d, "position_snapshots"); n != 0 {
		t.Fatalf("a failed batch left %d rows", n)
	}
}

func TestInsertPositionSnapshotsHandlesAFullSnapshot(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	const n = 2000
	rows := make([]store.PositionSnapshot, n)
	for i := range rows {
		rows[i] = testkit.Position(500, uint64(i%17+1), uint64(i))
	}
	if err := d.Store.InsertPositionSnapshots(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if got := count(t, d, "position_snapshots"); got != n {
		t.Fatalf("%d rows, want %d", got, n)
	}
}

func TestPrunePositionSnapshotsDeletesOnlyOlderRows(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	var rows []store.PositionSnapshot
	for i := range 6 {
		r := testkit.Position(uint64(100+i), 1, 7)
		r.Time = t0.Add(time.Duration(i) * time.Hour)
		rows = append(rows, r)
	}
	if err := d.Store.InsertPositionSnapshots(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	cutoff := t0.Add(3 * time.Hour)
	deleted, err := d.Store.PrunePositionSnapshots(t.Context(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Fatalf("deleted %d, want 3", deleted)
	}
	var oldest time.Time
	if err := d.Store.Pool.QueryRow(t.Context(), `select min(ts) from position_snapshots`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if !oldest.Equal(cutoff) {
		t.Fatalf("oldest kept row %v, want the cutoff %v itself", oldest, cutoff)
	}
	if again, err := d.Store.PrunePositionSnapshots(t.Context(), cutoff); err != nil || again != 0 {
		t.Fatalf("second prune deleted %d, err %v", again, err)
	}
}

func TestPruneMarketSnapshotsDeletesOnlyOlderRows(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	var rows []store.MarketSnapshot
	for i := range 6 {
		r := testkit.Market(uint64(i%2+1), uint64(100+i))
		r.Time = t0.Add(time.Duration(i) * time.Hour)
		rows = append(rows, r)
	}
	if err := d.Store.InsertMarketSnapshots(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	cutoff := t0.Add(3 * time.Hour)
	deleted, err := d.Store.PruneMarketSnapshots(t.Context(), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 || count(t, d, "market_snapshots") != 3 {
		t.Fatalf("deleted %d, %d rows left", deleted, count(t, d, "market_snapshots"))
	}
	var oldest time.Time
	if err := d.Store.Pool.QueryRow(t.Context(), `select min(ts) from market_snapshots`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if !oldest.Equal(cutoff) {
		t.Fatalf("oldest kept row %v, want the cutoff %v itself", oldest, cutoff)
	}
	if again, err := d.Store.PruneMarketSnapshots(t.Context(), cutoff); err != nil || again != 0 {
		t.Fatalf("second prune deleted %d, err %v", again, err)
	}
	if got := count(t, d, "position_snapshots"); got != 0 {
		t.Fatalf("position snapshots touched, %d rows", got)
	}
}

func fundingFixture(t *testing.T) *testkit.DB {
	t.Helper()
	d := testkit.New(t)
	d.Migrate(t)
	var rows []store.MarketSnapshot
	for i := range 10 {
		r := testkit.Market(20, uint64(1000+i))
		r.Time = t0.Add(time.Duration(i) * 30 * time.Second)
		r.Mark = "2500." + string(rune('0'+i))
		r.Oracle = "2499.5"
		r.FundingRatePct100k = int16(i - 5)
		rows = append(rows, r)
		other := r
		other.PerpID = 1
		other.Mark = "99999"
		rows = append(rows, other)
	}
	if err := d.Store.InsertMarketSnapshots(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFundingSeriesIsOneMarketAscendingWithSignedRates(t *testing.T) {
	d := fundingFixture(t)
	got, err := d.Store.FundingSeries(t.Context(), 20, t0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("%d points, want 10", len(got))
	}
	for i, p := range got {
		wantMark := "2500." + string(rune('0'+i))
		if i == 0 {
			wantMark = "2500"
		}
		if p.Block != uint64(1000+i) || !p.Time.Equal(t0.Add(time.Duration(i)*30*time.Second)) || p.Mark != wantMark || p.Oracle != "2499.5" || p.FundingRatePct100k != int16(i-5) {
			t.Fatalf("point %d = %+v", i, p)
		}
	}
}

func TestFundingSeriesHonoursSince(t *testing.T) {
	d := fundingFixture(t)
	got, err := d.Store.FundingSeries(t.Context(), 20, t0.Add(4*30*time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got[0].Block != 1004 {
		t.Fatalf("%d points from block %d, want 6 from 1004", len(got), got[0].Block)
	}
}

func TestFundingSeriesDownsamplesToTheLastRowOfEachStep(t *testing.T) {
	d := fundingFixture(t)
	got, err := d.Store.FundingSeries(t.Context(), 20, t0, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []uint64
	for _, p := range got {
		blocks = append(blocks, p.Block)
	}
	if len(blocks) != 3 || blocks[0] != 1003 || blocks[1] != 1007 || blocks[2] != 1009 {
		t.Fatalf("blocks %v, want [1003 1007 1009]", blocks)
	}
}

func TestFundingSeriesUnknownMarketIsEmptyNotNil(t *testing.T) {
	d := fundingFixture(t)
	got, err := d.Store.FundingSeries(t.Context(), 999, t0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}

func TestFundingSeriesRejectsANegativeStep(t *testing.T) {
	d := fundingFixture(t)
	if _, err := d.Store.FundingSeries(t.Context(), 20, t0, -time.Second); err == nil {
		t.Fatal("a negative step was accepted")
	}
}

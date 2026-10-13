package store_test

import (
	"math"
	"slices"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/store"
	"perps/internal/testkit"
)

func liq(block, logIndex, perp uint64) store.Liquidation {
	return testkit.Liquidation(block, logIndex, perp)
}

func migrated(t *testing.T) *testkit.DB {
	t.Helper()
	d := testkit.New(t)
	d.Migrate(t)
	return d
}

func blocks(items []store.Liquidation) [][2]uint64 {
	out := make([][2]uint64, len(items))
	for i, it := range items {
		out[i] = [2]uint64{it.Block, it.LogIndex}
	}
	return out
}

func TestUpsertLiquidationsRoundTripsEveryField(t *testing.T) {
	d := migrated(t)
	in := store.Liquidation{
		Block: 102493417, LogIndex: 237, Tx: testkit.Hash(1), Time: time.Date(2026, 8, 7, 4, 52, 23, 0, time.UTC), PerpID: 50, AccountID: 1551,
		Side: store.SideLong, Mark: "1135.03", LiqPrice: "1107.41", LiqLot: "44.8112", PosLot: "12.2935", Notional: "49624.81", OnOrderBook: true,
		Keeper: testkit.Addr(2),
	}
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{in}); err != nil {
		t.Fatal(err)
	}
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Next != nil {
		t.Fatalf("page = %+v", page)
	}
	got := page.Items[0]
	if got.Block != in.Block || got.LogIndex != in.LogIndex || got.Tx != in.Tx || !got.Time.Equal(in.Time) || got.PerpID != 50 || got.AccountID != 1551 ||
		got.Side != store.SideLong || got.Mark != "1135.03" || got.LiqPrice != "1107.41" || got.LiqLot != "44.8112" || got.PosLot != "12.2935" ||
		got.Notional != "49624.81" || !got.OnOrderBook || got.Keeper != in.Keeper {
		t.Fatalf("got %+v\nwant %+v", got, in)
	}
}

func TestUpsertLiquidationsReturnsCanonicalDecimals(t *testing.T) {
	d := migrated(t)
	in := liq(10, 1, 1)
	in.Mark, in.LiqPrice, in.LiqLot, in.PosLot, in.Notional = "100.500000", "99.000", "0.00000", "10.0", "1234.50"
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{in}); err != nil {
		t.Fatal(err)
	}
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	got := page.Items[0]
	if got.Mark != "100.5" || got.LiqPrice != "99" || got.LiqLot != "0" || got.PosLot != "10" || got.Notional != "1234.5" {
		t.Fatalf("decimals %q %q %q %q %q", got.Mark, got.LiqPrice, got.LiqLot, got.PosLot, got.Notional)
	}
}

func TestUpsertLiquidationsIsIdempotent(t *testing.T) {
	d := migrated(t)
	batch := []store.Liquidation{liq(10, 1, 1), liq(10, 2, 1), liq(11, 1, 20)}
	for range 3 {
		if err := d.Store.UpsertLiquidations(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, d, "liquidations"); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

func TestUpsertLiquidationsOverwritesTheRowOnConflict(t *testing.T) {
	d := migrated(t)
	first := liq(10, 1, 1)
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{first}); err != nil {
		t.Fatal(err)
	}
	fixed := first
	fixed.Keeper = testkit.Addr(99)
	fixed.Notional = "1"
	fixed.Time = first.Time.Add(time.Second)
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{fixed}); err != nil {
		t.Fatal(err)
	}
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Keeper != fixed.Keeper || page.Items[0].Notional != "1" || !page.Items[0].Time.Equal(fixed.Time) {
		t.Fatalf("page = %+v", page.Items)
	}
}

func TestUpsertLiquidationsAcceptsTheSameKeyTwiceInOneBatch(t *testing.T) {
	d := migrated(t)
	a, b := liq(10, 1, 1), liq(10, 1, 1)
	b.Notional = "777"
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{a, b, liq(10, 2, 1)}); err != nil {
		t.Fatal(err)
	}
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[1].Notional != "777" {
		t.Fatalf("items %+v, want the later duplicate to win", page.Items)
	}
}

func TestUpsertLiquidationsNoRowsIsANoOp(t *testing.T) {
	d := migrated(t)
	if err := d.Store.UpsertLiquidations(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, "liquidations"); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

func TestUpsertLiquidationsRejectsABadBatchAtomically(t *testing.T) {
	d := migrated(t)
	bad := liq(10, 2, 1)
	bad.LogIndex = math.MaxUint64
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{liq(10, 1, 1), bad}); err == nil {
		t.Fatal("a log index above int32 was accepted")
	}
	bad = liq(10, 3, 1)
	bad.Side = "sideways"
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{liq(10, 1, 1), bad}); err == nil {
		t.Fatal("a bad side was accepted")
	}
	if n := count(t, d, "liquidations"); n != 0 {
		t.Fatalf("failed batches left %d rows", n)
	}
}

func seedLiquidations(t *testing.T) *testkit.DB {
	t.Helper()
	d := migrated(t)
	rows := []store.Liquidation{
		liq(100, 1, 1), liq(100, 5, 20), liq(101, 2, 1), liq(103, 1, 1), liq(103, 2, 20), liq(103, 9, 1), liq(110, 4, 20),
	}
	if err := d.Store.UpsertLiquidations(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestLiquidationsAreNewestFirst(t *testing.T) {
	d := seedLiquidations(t)
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]uint64{{110, 4}, {103, 9}, {103, 2}, {103, 1}, {101, 2}, {100, 5}, {100, 1}}
	if got := blocks(page.Items); !slices.Equal(got, want) || page.Next != nil {
		t.Fatalf("order %v next %v, want %v and no next", got, page.Next, want)
	}
}

func TestLiquidationsPageThroughEveryRowOnce(t *testing.T) {
	d := seedLiquidations(t)
	var all [][2]uint64
	var after *store.LiquidationCursor
	pages := 0
	for {
		page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 3, After: after})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		all = append(all, blocks(page.Items)...)
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	want := [][2]uint64{{110, 4}, {103, 9}, {103, 2}, {103, 1}, {101, 2}, {100, 5}, {100, 1}}
	if !slices.Equal(all, want) || pages != 3 {
		t.Fatalf("paged %v in %d pages, want %v in 3", all, pages, want)
	}
}

func TestLiquidationsHaveNoNextWhenTheLastPageIsExactlyFull(t *testing.T) {
	d := seedLiquidations(t)
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 7 || page.Next != nil {
		t.Fatalf("%d items, next %v", len(page.Items), page.Next)
	}
	page, err = d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 6 || page.Next == nil || *page.Next != (store.LiquidationCursor{Block: 100, LogIndex: 5}) {
		t.Fatalf("%d items, next %v, want the cursor of the last item returned", len(page.Items), page.Next)
	}
}

func TestLiquidationsMarketFilter(t *testing.T) {
	d := seedLiquidations(t)
	perp := uint64(20)
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{PerpID: &perp, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := blocks(page.Items); !slices.Equal(got, [][2]uint64{{110, 4}, {103, 2}}) || page.Next == nil {
		t.Fatalf("items %v next %v", got, page.Next)
	}
	page, err = d.Store.Liquidations(t.Context(), store.LiquidationFilter{PerpID: &perp, Limit: 2, After: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	if got := blocks(page.Items); !slices.Equal(got, [][2]uint64{{100, 5}}) || page.Next != nil {
		t.Fatalf("second page %v next %v", got, page.Next)
	}
}

func TestLiquidationsUnknownMarketIsAnEmptyPage(t *testing.T) {
	d := seedLiquidations(t)
	perp := uint64(777)
	page, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{PerpID: &perp, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || len(page.Items) != 0 || page.Next != nil {
		t.Fatalf("page = %#v", page)
	}
}

func TestLiquidationsRejectAnOutOfRangeLimit(t *testing.T) {
	d := seedLiquidations(t)
	for _, limit := range []int{-1, 0, store.MaxLiquidationLimit + 1} {
		if _, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: limit}); err == nil {
			t.Errorf("limit %d was accepted", limit)
		}
	}
	if _, err := d.Store.Liquidations(t.Context(), store.LiquidationFilter{Limit: store.MaxLiquidationLimit}); err != nil {
		t.Errorf("limit %d: %v", store.MaxLiquidationLimit, err)
	}
}

func TestLiquidationCursorTextRoundTrip(t *testing.T) {
	c := store.LiquidationCursor{Block: 112777356, LogIndex: 451}
	got, err := store.ParseLiquidationCursor(c.String())
	if err != nil || got != c {
		t.Fatalf("round trip gave %+v, %v from %q", got, err, c.String())
	}
}

func TestParseLiquidationCursorRejectsMalformedText(t *testing.T) {
	for _, s := range []string{"", "12", "12.", ".5", "a.b", "12.-1", "-1.2", "1.2.3", "1,2", " 1.2", "99999999999999999999.1", "1.4294967296"} {
		if c, err := store.ParseLiquidationCursor(s); err == nil {
			t.Errorf("%q parsed as %+v", s, c)
		}
	}
}

func TestLiquidationsDailyGroupsByUTCDay(t *testing.T) {
	d := migrated(t)
	at := func(block, idx, perp uint64, ts time.Time, notional string) store.Liquidation {
		r := liq(block, idx, perp)
		r.Time, r.Notional = ts, notional
		return r
	}
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	rows := []store.Liquidation{
		at(1, 1, 1, day.Add(-time.Second), "7"),
		at(2, 1, 1, day, "100.50"),
		at(3, 1, 20, day.Add(23*time.Hour+59*time.Minute+59*time.Second), "200.25"),
		at(4, 1, 1, day.Add(24*time.Hour), "5"),
		at(5, 1, 1, day.Add(72*time.Hour), "9"),
	}
	if err := d.Store.UpsertLiquidations(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	got, err := d.Store.LiquidationsDaily(t.Context(), nil, day)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.DailyLiquidations{
		{Day: day, Count: 2, NotionalUSD: "300.75"},
		{Day: day.Add(24 * time.Hour), Count: 1, NotionalUSD: "5"},
		{Day: day.Add(72 * time.Hour), Count: 1, NotionalUSD: "9"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("daily\n got %+v\nwant %+v", got, want)
	}
}

func TestLiquidationsDailyStartsAtTheUTCDayOfSince(t *testing.T) {
	d := migrated(t)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	early := liq(1, 1, 1)
	early.Time = day.Add(time.Hour)
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{early}); err != nil {
		t.Fatal(err)
	}
	local := time.FixedZone("UTC+3", 3*3600)
	got, err := d.Store.LiquidationsDaily(t.Context(), nil, day.Add(20*time.Hour).In(local))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Day.Equal(day) {
		t.Fatalf("got %+v, want the whole UTC day of since", got)
	}
}

func TestLiquidationsDailyMarketFilterAndEmpty(t *testing.T) {
	d := seedLiquidations(t)
	perp := uint64(20)
	got, err := d.Store.LiquidationsDaily(t.Context(), &perp, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, g := range got {
		total += g.Count
	}
	if total != 3 {
		t.Fatalf("market 20 has %d liquidations in %+v, want 3", total, got)
	}
	none := uint64(555)
	got, err = d.Store.LiquidationsDaily(t.Context(), &none, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want an empty non-nil slice", got)
	}
}

func withKeepers(t *testing.T, d *testkit.DB, keepers ...perpl.Address) {
	t.Helper()
	rows := make([]store.Liquidation, len(keepers))
	for i, k := range keepers {
		rows[i] = liq(uint64(10+i), 1, 1)
		rows[i].Keeper = k
	}
	if err := d.Store.UpsertLiquidations(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
}

func TestKeeperCountsRankByCount(t *testing.T) {
	d := migrated(t)
	k1, k2, k3 := testkit.Addr(1), testkit.Addr(2), testkit.Addr(3)
	withKeepers(t, d, k2, k3, k1, k1, k2, k3, k1, k2, k1)
	got, err := d.Store.KeeperCounts(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.KeeperCount{{Keeper: k1, Liquidations: 4}, {Keeper: k2, Liquidations: 3}, {Keeper: k3, Liquidations: 2}}
	if !slices.Equal(got, want) {
		t.Fatalf("keepers %+v, want %+v", got, want)
	}
	top, err := d.Store.KeeperCounts(t.Context(), 2)
	if err != nil || !slices.Equal(top, want[:2]) {
		t.Fatalf("limit 2 gave %+v, %v", top, err)
	}
}

func TestKeeperCountsBreakTiesByAddress(t *testing.T) {
	d := migrated(t)
	var keepers []perpl.Address
	for i := range 8 {
		keepers = append(keepers, perpl.Address{byte(0xf0 - 7*i)})
	}
	withKeepers(t, d, keepers...)
	got, err := d.Store.KeeperCounts(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]store.KeeperCount, len(keepers))
	for i, k := range slices.Backward(keepers) {
		want[len(keepers)-1-i] = store.KeeperCount{Keeper: k, Liquidations: 1}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("keepers %+v, want ascending addresses %+v", got, want)
	}
}

func TestKeeperCountsOfAnEmptyTable(t *testing.T) {
	d := migrated(t)
	got, err := d.Store.KeeperCounts(t.Context(), 0)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestCountLiquidations(t *testing.T) {
	d := migrated(t)
	empty, err := d.Store.CountLiquidations(t.Context())
	if err != nil || empty != (store.LiquidationTotals{}) {
		t.Fatalf("empty table gave %+v, %v", empty, err)
	}
	d = seedLiquidations(t)
	got, err := d.Store.CountLiquidations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != (store.LiquidationTotals{Count: 7, FirstBlock: 100, LastBlock: 110}) {
		t.Fatalf("totals %+v", got)
	}
}

package refresh

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"perps/internal/risk"
	"perps/internal/sources"
	"perps/internal/store"
)

func hexExpected(t *testing.T, all []liqExpected, block, index uint64) liqExpected {
	t.Helper()
	for _, e := range all {
		if decUint(t, e.BlockNumber) == block && decUint(t, e.LogIndex) == index {
			return e
		}
	}
	t.Fatalf("no expected row for %d.%d", block, index)
	return liqExpected{}
}

// checkRow compares a stored liquidation with the recorded expectation and the decimals of its market
func checkRow(t *testing.T, row store.Liquidation, e liqExpected, priceDec, lotDec uint8) {
	t.Helper()
	if row.Tx != mustHash(t, e.TransactionHash) {
		t.Fatalf("%d.%d tx %s", row.Block, row.LogIndex, row.Tx)
	}
	if row.Keeper != mustAddress(t, e.TransactionFrom) {
		t.Fatalf("%d.%d keeper %s, want %s", row.Block, row.LogIndex, row.Keeper, e.TransactionFrom)
	}
	if row.Time.Unix() != int64(decUint(t, e.BlockTimestamp)) {
		t.Fatalf("%d.%d time %v", row.Block, row.LogIndex, row.Time)
	}
	if row.PerpID != decUint(t, e.Event.PerpID) || row.AccountID != decUint(t, e.Event.PosAccountID) {
		t.Fatalf("%d.%d ids %d %d", row.Block, row.LogIndex, row.PerpID, row.AccountID)
	}
	wantSide := store.SideLong
	if e.Event.PositionType == "1" {
		wantSide = store.SideShort
	}
	if row.Side != wantSide || row.OnOrderBook != e.Event.OnOrderBook {
		t.Fatalf("%d.%d side %s book %v", row.Block, row.LogIndex, row.Side, row.OnOrderBook)
	}
	scale := func(v string, dec uint8) *big.Rat {
		return new(big.Rat).SetFrac(decBig(t, v), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil))
	}
	mark := scale(e.Event.MarkPricePNS, priceDec)
	if row.Mark != risk.FormatRat(mark, 18) || row.LiqPrice != risk.FormatRat(scale(e.Event.LiqPricePNS, priceDec), 18) {
		t.Fatalf("%d.%d prices %s %s", row.Block, row.LogIndex, row.Mark, row.LiqPrice)
	}
	lot := scale(e.Event.LiqLotLNS, lotDec)
	if row.LiqLot != risk.FormatRat(lot, 18) || row.PosLot != risk.FormatRat(scale(e.Event.PosLotLNS, lotDec), 18) {
		t.Fatalf("%d.%d lots %s %s", row.Block, row.LogIndex, row.LiqLot, row.PosLot)
	}
	if want := risk.FormatRat(new(big.Rat).Mul(lot, mark), 18); row.Notional != want {
		t.Fatalf("%d.%d notional %s, want %s", row.Block, row.LogIndex, row.Notional, want)
	}
}

func marketDecimals(t *testing.T, g *rig, perp uint64) (uint8, uint8) {
	t.Helper()
	dec, err := g.r.decimalsFor(context.Background(), perp)
	if err != nil {
		t.Fatal(err)
	}
	return dec.price, dec.lot
}

func TestBackfillThenTailEndToEnd(t *testing.T) {
	hs, requests := hyperSyncServer(t)
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	expectedTail := g.chain.loadTailFixtures(true)
	hsExpected := hyperSyncExpected(t)
	ctx := context.Background()

	// the first look at the chain is at block 109168002 so the backfill ends where the tail begins
	g.chain.setHead(109168002)
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if tail, ok := g.store.cursor(store.CursorTail); !ok || tail != 109168001 {
		t.Fatalf("tail cursor %d %v", tail, ok)
	}
	if st := g.r.Status(); st.Backfill != BackfillRunning || st.BackfillTarget != 109168001 {
		t.Fatalf("status after init %+v", st)
	}

	if err := g.r.backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatalf("%d hypersync requests", requests.Load())
	}
	if bf, ok := g.store.cursor(store.CursorBackfill); !ok || bf != 109168001 {
		t.Fatalf("backfill cursor %d %v", bf, ok)
	}
	rows := g.store.liquidations()
	if len(rows) != len(hsExpected) {
		t.Fatalf("%d rows after the backfill, want %d", len(rows), len(hsExpected))
	}
	for _, row := range rows {
		pd, ld := marketDecimals(t, g, row.PerpID)
		checkRow(t, row, hexExpected(t, hsExpected, row.Block, row.LogIndex), pd, ld)
	}
	if st := g.r.Status(); st.Backfill != BackfillComplete || st.LiquidationsIndexed != 10 {
		t.Fatalf("status after the backfill %+v", st)
	}

	// the tail picks up everything after the backfill range
	g.chain.setHead(112777358)
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 112777357 {
		t.Fatalf("tail cursor %d", tail)
	}
	rows = g.store.liquidations()
	if len(rows) != 12 {
		t.Fatalf("%d rows after the tail, want 12", len(rows))
	}
	for _, blockIndex := range [][2]uint64{{109168594, 43}, {111612151, 34}} {
		var found bool
		for _, row := range rows {
			if row.Block == blockIndex[0] && row.LogIndex == blockIndex[1] {
				pd, ld := marketDecimals(t, g, row.PerpID)
				checkRow(t, row, hexExpected(t, expectedTail, row.Block, row.LogIndex), pd, ld)
				found = true
			}
		}
		if !found {
			t.Fatalf("tail row %v missing", blockIndex)
		}
	}
	st := g.r.Status()
	if st.LiquidationsIndexed != 12 || st.FirstLiquidationBlock != 102493417 || st.LastLiquidationBlock != 111612151 || st.TailBlock != 112777357 {
		t.Fatalf("status after the tail %+v", st)
	}
	if got := metricValue(t, g.reg, "perps_liquidations_indexed", nil); got != 12 {
		t.Fatalf("liquidations metric %v", got)
	}

	// a repeat of the same range changes nothing
	g.chain.setHead(112777359)
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(g.store.liquidations()) != 12 {
		t.Fatal("a repeat tail added rows")
	}
}

func hyperSyncExpected(t *testing.T) []liqExpected {
	t.Helper()
	var rows []liqExpected
	readJSON(t, testdata("hypersync", "expected.json"), &rows)
	return rows
}

func TestTailPagesAndCachesLookups(t *testing.T) {
	g := newRig(t)
	g.chain.loadTailFixtures(false)
	g.r.tailChunk = 20000
	ctx := context.Background()
	g.chain.setHead(90121002)
	g.store.cursors[store.CursorTail] = 90076000
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if st := g.r.Status(); st.Backfill != BackfillDisabled {
		t.Fatalf("backfill %q without a key", st.Backfill)
	}
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}

	want := [][2]uint64{{90076000, 90095999}, {90096000, 90115999}, {90116000, 90121000}}
	if len(g.chain.logWindows) != len(want) {
		t.Fatalf("log windows %v", g.chain.logWindows)
	}
	for i, w := range want {
		if g.chain.logWindows[i] != w {
			t.Fatalf("window %d is %v, want %v", i, g.chain.logWindows[i], w)
		}
	}
	rows := g.store.liquidations()
	if len(rows) != 4 {
		t.Fatalf("%d rows, want 4 (three in 90076937 and one in 90120252)", len(rows))
	}
	// the timestamps come from BlockByNumber because the logs carry none, once per block
	if n := g.chain.blockLookups[90076937]; n != 1 {
		t.Fatalf("block 90076937 looked up %d times", n)
	}
	expected := mustLoadExpected(t)
	for _, row := range rows {
		pd, ld := marketDecimals(t, g, row.PerpID)
		checkRow(t, row, hexExpected(t, expected, row.Block, row.LogIndex), pd, ld)
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 90121001 {
		t.Fatalf("tail cursor %d", tail)
	}
}

func mustLoadExpected(t *testing.T) []liqExpected {
	t.Helper()
	var rows []liqExpected
	readJSON(t, testdata("liquidations", "expected.json"), &rows)
	return rows
}

func TestTailStartsNineThousandBlocksBackWithoutAKey(t *testing.T) {
	g := newRig(t)
	g.chain.setHead(112777358)
	if err := g.r.initHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tail, ok := g.store.cursor(store.CursorTail); !ok || tail != 112777358-9000 {
		t.Fatalf("tail cursor %d %v", tail, ok)
	}
	if _, ok := g.store.cursor(store.CursorBackfill); ok {
		t.Fatal("backfill cursor written without a key")
	}
	if g.r.Status().Backfill != BackfillDisabled {
		t.Fatal("backfill not reported as disabled")
	}
}

func TestTailKeepsAnExistingCursor(t *testing.T) {
	g := newRig(t)
	g.store.cursors[store.CursorTail] = 5000
	g.chain.setHead(112777358)
	if err := g.r.initHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 5000 {
		t.Fatalf("tail cursor moved to %d", tail)
	}
}

func TestTailWithNothingNewDoesNothing(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	g.chain.setHead(112777358)
	g.store.cursors[store.CursorTail] = 112777357
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(g.chain.logWindows) != 0 {
		t.Fatalf("log windows %v", g.chain.logWindows)
	}
}

func TestTailKeepsTheCursorWhenAStepFails(t *testing.T) {
	g := newRig(t)
	g.chain.loadTailFixtures(false)
	ctx := context.Background()
	g.chain.setHead(90121002)
	g.store.cursors[store.CursorTail] = 90076000
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	g.store.failOn("liquidations", errBoom)
	if err := g.r.tailOnce(ctx); err == nil {
		t.Fatal("tail succeeded with a failing store")
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 90076000 {
		t.Fatalf("cursor moved to %d past unwritten rows", tail)
	}
	g.store.failOn("liquidations", nil)
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(g.store.liquidations()) != 4 {
		t.Fatalf("%d rows after the retry", len(g.store.liquidations()))
	}
}

func TestTailFailsWhenTheKeeperLookupFails(t *testing.T) {
	g := newRig(t)
	g.chain.loadTailFixtures(true)
	ctx := context.Background()
	g.chain.setHead(90121002)
	g.store.cursors[store.CursorTail] = 90076000
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	g.chain.mu.Lock()
	clear(g.chain.txs)
	g.chain.mu.Unlock()
	if err := g.r.tailOnce(ctx); err == nil {
		t.Fatal("tail succeeded without transactions")
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 90076000 {
		t.Fatalf("cursor %d", tail)
	}
}

func TestTailSkipsMalformedLogs(t *testing.T) {
	g := newRig(t)
	g.chain.loadTailFixtures(true)
	ctx := context.Background()
	g.chain.setHead(90121002)
	g.store.cursors[store.CursorTail] = 90076000
	g.chain.mu.Lock()
	g.chain.logs[len(g.chain.logs)-1].Data = []byte{1, 2, 3}
	g.chain.mu.Unlock()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "decode"}); got != 1 {
		t.Fatalf("decode errors %v", got)
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 90121001 {
		t.Fatalf("cursor %d", tail)
	}
}

func TestBackfillResumesFromItsCursorAfterAFailure(t *testing.T) {
	var starts []uint64
	attempts := 0
	hs := scanFunc(func(ctx context.Context, q sources.Query, fn func(sources.Page) error) (uint64, error) {
		starts = append(starts, q.From)
		attempts++
		if attempts == 1 {
			if err := fn(sources.Page{NextBlock: 102600000}); err != nil {
				return q.From, err
			}
			return 102600000, errBoom
		}
		if err := fn(sources.Page{NextBlock: q.To}); err != nil {
			return q.From, err
		}
		return q.To, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	ctx := context.Background()
	g.chain.setHead(109168002)
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.backfill(ctx); err == nil {
		t.Fatal("backfill hid the failure")
	}
	if bf, _ := g.store.cursor(store.CursorBackfill); bf != 102600000 {
		t.Fatalf("cursor %d after the failure", bf)
	}
	if st := g.r.Status(); st.Backfill != BackfillRetrying || st.BackfillBlock != 102600000 {
		t.Fatalf("status %+v", st)
	}
	if err := g.r.backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if starts[0] != 102493417 || starts[1] != 102600000 {
		t.Fatalf("scan starts %v", starts)
	}
	if bf, _ := g.store.cursor(store.CursorBackfill); bf != 109168001 {
		t.Fatalf("cursor %d", bf)
	}
	if g.r.Status().Backfill != BackfillComplete {
		t.Fatal("backfill not complete")
	}
}

func TestBackfillStaysBehindWhenTheArchiveStopsEarly(t *testing.T) {
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		return q.From + 10, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.chain.setHead(109168002)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.backfill(ctx); err == nil {
		t.Fatal("an incomplete scan was treated as done")
	}
	if g.r.Status().Backfill != BackfillRetrying {
		t.Fatalf("status %+v", g.r.Status())
	}
}

func TestBackfillCoversTheRangeBeforeAnExistingTail(t *testing.T) {
	var got sources.Query
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		got = q
		return q.To, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.store.cursors[store.CursorTail] = 111000000
	g.chain.setHead(112777358)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if got.From != 102493417 || got.To != 111000000 || got.Address.String() != exchangeText {
		t.Fatalf("query %+v", got)
	}
}

func TestBackfillIsSkippedWhenAlreadyComplete(t *testing.T) {
	called := false
	hs := scanFunc(func(context.Context, sources.Query, func(sources.Page) error) (uint64, error) {
		called = true
		return 0, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.store.cursors[store.CursorTail] = 111000000
	g.store.cursors[store.CursorBackfill] = 111000000
	g.chain.setHead(112777358)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if g.r.Status().Backfill != BackfillComplete {
		t.Fatalf("status %+v", g.r.Status())
	}
	if err := g.r.backfill(ctx); err != nil || called {
		t.Fatalf("scan called %v err %v", called, err)
	}
}

func TestBackfillNeedsAKeyEvenWithAClient(t *testing.T) {
	hs := scanFunc(func(context.Context, sources.Query, func(sources.Page) error) (uint64, error) {
		t.Error("scan without a key")
		return 0, errors.New("unreachable")
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs; o.Config.HyperSyncKey = "" })
	g.chain.setHead(112777358)
	if err := g.r.initHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.r.Status().Backfill != BackfillDisabled {
		t.Fatalf("status %+v", g.r.Status())
	}
}

func TestInitHistoryReportsAHeadFailure(t *testing.T) {
	g := newRig(t)
	g.chain.headErr = errBoom
	if err := g.r.initHistory(context.Background()); err == nil {
		t.Fatal("init succeeded without a head")
	}
}

func TestDecimalsAreFetchedOnDemandAndCached(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	dec, err := g.r.decimalsFor(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	before := g.chain.selectors[selector(t, "getPerpetualInfoV2")]
	again, err := g.r.decimalsFor(ctx, 10)
	if err != nil || again != dec {
		t.Fatal("cache returned a different value")
	}
	if g.chain.selectors[selector(t, "getPerpetualInfoV2")] != before {
		t.Fatal("decimals were not cached")
	}
	if _, err := g.r.decimalsFor(ctx, 424242); err == nil {
		t.Fatal("unknown perp resolved")
	}
}

func TestBackfillStaysCompleteAfterARestartOnceTheTailMovedOn(t *testing.T) {
	scans := 0
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		scans++
		return q.To, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.chain.setHead(109168002)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok := g.store.cursor(store.CursorBackfillTarget); !ok || got != 109168001 {
		t.Fatalf("backfill target cursor %d %v", got, ok)
	}
	if err := g.r.backfill(ctx); err != nil || scans != 1 {
		t.Fatalf("first backfill err %v scans %d", err, scans)
	}

	// the tail has covered 332k blocks since, which is what a restart finds
	g.store.cursors[store.CursorTail] = 109500000
	g.chain.setHead(109500003)
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if st := g.r.Status(); st.Backfill != BackfillComplete || st.BackfillBlock != 109168001 || st.BackfillTarget != 109168001 {
		t.Fatalf("status after the restart %+v", st)
	}
	if err := g.r.backfill(ctx); err != nil || scans != 1 {
		t.Fatalf("restart scanned again, err %v scans %d", err, scans)
	}
}

func TestBackfillResumesToTheSavedTargetAfterARestart(t *testing.T) {
	var got sources.Query
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		got = q
		return q.To, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.store.cursors[store.CursorTail] = 111500000
	g.store.cursors[store.CursorBackfill] = 105000000
	g.store.cursors[store.CursorBackfillTarget] = 111000000
	g.chain.setHead(112000000)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if err := g.r.backfill(ctx); err != nil {
		t.Fatal(err)
	}
	if got.From != 105000000 || got.To != 111000000 {
		t.Fatalf("query %d-%d, the tail already covers everything after the saved target", got.From, got.To)
	}
}

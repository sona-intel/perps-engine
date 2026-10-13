package refresh

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/store"
)

type derivedPositions struct {
	PerpID         uint64 `json:"perp_id"`
	Symbol         string `json:"symbol"`
	PositionsTotal int    `json:"positions_total"`
	Positions      []struct {
		AccountID string `json:"account_id"`
		Entry     string `json:"entry"`
		LiqPrice  string `json:"liq_price"`
		Bankrupt  string `json:"bankruptcy_price"`
	} `json:"positions"`
}

func loadDerived(t *testing.T, perp uint64) (derivedPositions, bool) {
	t.Helper()
	var d derivedPositions
	path := testdata("derived", fmt.Sprintf("positions.perp%d.json", perp))
	if _, err := lookup(path); err != nil {
		return d, false
	}
	readJSON(t, path, &d)
	return d, true
}

func TestRefreshBuildsSnapshotFromFixtures(t *testing.T) {
	g := newRig(t)
	s := g.refresh()

	if s.Block != pinnedBlock || s.Time.Unix() != pinnedTime {
		t.Fatalf("snapshot at %d %v", s.Block, s.Time)
	}
	ids := fixturePerpIDs(t)
	if len(s.Markets) != len(ids) {
		t.Fatalf("%d markets, want %d", len(s.Markets), len(ids))
	}
	total := 0
	for i, id := range ids {
		mv := s.Markets[i]
		if mv.Market.PerpID != id {
			t.Fatalf("market %d is perp %d, want %d", i, mv.Market.PerpID, id)
		}
		got, ok := s.Market(id)
		if !ok || got != mv {
			t.Fatalf("lookup of perp %d", id)
		}
		want := 0
		if d, ok := loadDerived(t, id); ok {
			want = d.PositionsTotal
			if len(mv.Book) != want || len(mv.Raw) != want {
				t.Fatalf("perp %d has %d assessed and %d raw, want %d", id, len(mv.Book), len(mv.Raw), want)
			}
			for j, p := range d.Positions {
				a := mv.Book[j]
				if strconv.FormatUint(a.AccountID, 10) != p.AccountID {
					t.Fatalf("perp %d position %d is account %d, want %s", id, j, a.AccountID, p.AccountID)
				}
				if got := risk.FormatRat(a.LiqPrice, 18); got != p.LiqPrice {
					t.Fatalf("perp %d account %s liq %s, want %s", id, p.AccountID, got, p.LiqPrice)
				}
				if got := risk.FormatRat(a.BankruptcyPrice, 18); got != p.Bankrupt {
					t.Fatalf("perp %d account %s bankruptcy %s, want %s", id, p.AccountID, got, p.Bankrupt)
				}
			}
		} else if len(mv.Book) != 0 {
			t.Fatalf("perp %d has %d positions without a fixture", id, len(mv.Book))
		}
		if mv.Metrics.Positions != want {
			t.Fatalf("perp %d metrics positions %d, want %d", id, mv.Metrics.Positions, want)
		}
		if len(mv.Stress) != len(g.cfg.Shocks) {
			t.Fatalf("perp %d has %d stress results", id, len(mv.Stress))
		}
		for k, st := range mv.Stress {
			if st.Shock != g.cfg.Shocks[k] {
				t.Fatalf("perp %d stress %d at shock %v", id, k, st.Shock)
			}
		}
		total += want
	}
	if total != 665 || s.Positions != 665 {
		t.Fatalf("positions %d and %d, want 665", total, s.Positions)
	}
	btc, _ := s.Find("btc")
	byID, _ := s.Find("1")
	if btc == nil || btc != byID || btc.Market.Symbol != "BTC" || len(btc.Book) != 246 {
		t.Fatalf("find BTC %v", btc)
	}
	if _, ok := s.Find("nope"); ok {
		t.Fatal("unknown symbol found")
	}
	if s.Exchange.CollateralDecimals != 6 || s.Exchange.FundingInterval != 8571 {
		t.Fatalf("exchange %+v", s.Exchange)
	}
	if btc.Market.MaintMarginFracHdths != 2500 || btc.Market.Liquidation.LiqInsAmtPer100K == 0 {
		t.Fatalf("slow fields missing %+v", btc.Market)
	}
	if s.Implementation != g.chain.impl || s.ImplementationChanged {
		t.Fatalf("implementation %s changed %v", s.Implementation, s.ImplementationChanged)
	}
}

func TestSnapshotIndexesAndDerivedMetrics(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	btc, _ := s.Find("BTC")

	acct := s.Account(4848)
	if len(acct) == 0 {
		t.Fatal("top BTC account has no positions")
	}
	for _, a := range acct {
		if a.AccountID != 4848 {
			t.Fatalf("account index returned %d", a.AccountID)
		}
	}
	if got := s.Account(999999999); len(got) != 0 {
		t.Fatalf("unknown account has %d positions", len(got))
	}
	if len(s.Books()) != len(s.Markets) {
		t.Fatalf("%d books", len(s.Books()))
	}

	m := btc.Metrics
	if m.Mark != 82893.4 {
		t.Fatalf("mark %v", m.Mark)
	}
	if m.LongNotional <= 0 || m.ShortNotional <= 0 || m.Positions != 246 {
		t.Fatalf("notionals %v %v positions %d", m.LongNotional, m.ShortNotional, m.Positions)
	}
	long, short := 0.0, 0.0
	for _, a := range btc.Book {
		f, _ := a.Notional.Float64()
		if a.Long {
			long += f
		} else {
			short += f
		}
	}
	if diff := long - m.LongNotional; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("long notional %v, want %v", m.LongNotional, long)
	}
	if diff := short - m.ShortNotional; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("short notional %v, want %v", m.ShortNotional, short)
	}
	wantGap := (m.Mark - m.Oracle) / m.Oracle * 10000
	if diff := m.OracleGapBps - wantGap; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("oracle gap %v, want %v", m.OracleGapBps, wantGap)
	}
	if m.OracleAgeSec != int64(pinnedTime)-int64(btc.Market.OracleTimestampSec) && m.OracleAgeSec != 0 {
		t.Fatalf("oracle age %d", m.OracleAgeSec)
	}

	// stress and the map recompute from the same book
	want, err := risk.Stress(btc.Market, s.Exchange, btc.Book, -0.1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Stress(1, -0.1)
	if err != nil {
		t.Fatal(err)
	}
	if got.GapBadDebt != want.GapBadDebt || got.Liquidating != want.Liquidating {
		t.Fatalf("stress %+v, want %+v", got, want)
	}
	other, err := s.Stress(1, 0.07)
	if err != nil || other.Shock != 0.07 {
		t.Fatalf("stress at an unconfigured shock %+v %v", other, err)
	}
	if _, err := s.Stress(1, 0.9); err == nil {
		t.Fatal("shock above the bound accepted")
	}
	if _, err := s.Stress(424242, -0.1); err == nil {
		t.Fatal("unknown market accepted")
	}
	lm, err := s.LiquidationMap(1, 25, 20)
	if err != nil || len(lm.Buckets) == 0 {
		t.Fatalf("liquidation map %v", err)
	}
	if _, err := s.LiquidationMap(1, 1, 20); err == nil {
		t.Fatal("bucket below the bound accepted")
	}
	if wantM10 := btc.Stress[slices.Index(g.cfg.Shocks, -0.1)].SCR; (btc.Metrics.SCRM10 == nil) != (wantM10 == nil) {
		t.Fatalf("scr at -10%% %v want %v", btc.Metrics.SCRM10, wantM10)
	}
}

func TestRefreshPinsEveryCallToOneBlock(t *testing.T) {
	g := newRig(t)
	g.refresh()
	if len(g.chain.callBlocks) != 1 || g.chain.callBlocks[pinnedBlock] == 0 {
		t.Fatalf("calls by block %v", g.chain.callBlocks)
	}
	g.chain.setHead(pinnedBlock + 3)
	g.refresh()
	if len(g.chain.callBlocks) != 2 || g.chain.callBlocks[pinnedBlock+1] == 0 {
		t.Fatalf("calls by block %v", g.chain.callBlocks)
	}
	if g.r.Latest().Block != pinnedBlock+1 {
		t.Fatalf("latest at %d", g.r.Latest().Block)
	}
}

func TestSlowReadsOnlyWhenDue(t *testing.T) {
	g := newRig(t)
	g.refresh()
	g.chain.setHead(pinnedBlock + 3)
	g.refresh()

	ids := len(fixturePerpIDs(t))
	sel := func(name string) string { return selector(t, name) }
	if n := g.chain.selectors[sel("getExchangeInfo")]; n != 1 {
		t.Fatalf("exchange info read %d times in two refreshes", n)
	}
	if n := g.chain.selectors[sel("getMarginFractions")]; n != ids {
		t.Fatalf("margin fractions read %d times, want %d", n, ids)
	}
	if n := g.chain.selectors[sel("getLiquidationInfo")]; n != ids {
		t.Fatalf("liquidation info read %d times, want %d", n, ids)
	}
	if n := g.chain.selectors[sel("getPerpetualInfoV2")]; n != 2*ids {
		t.Fatalf("perp info read %d times, want %d", n, 2*ids)
	}

	g.advance(11 * time.Minute)
	g.chain.setHead(pinnedBlock + 4)
	g.refresh()
	if n := g.chain.selectors[sel("getExchangeInfo")]; n != 2 {
		t.Fatalf("exchange info read %d times after the slow interval", n)
	}
}

func TestRefreshSkipsAnUnchangedBlock(t *testing.T) {
	g := newRig(t)
	first := g.refresh()
	calls := len(g.chain.callBlocks)
	if err := g.r.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g.r.Latest() != first || len(g.chain.callBlocks) != calls {
		t.Fatal("a second refresh at the same block rebuilt the snapshot")
	}
}

func TestRefreshHonoursMaxInflight(t *testing.T) {
	g := newRig(t, func(o *Options) { o.Config.MaxInflightCalls = 3 })
	g.chain.delay = 2 * time.Millisecond
	g.refresh()
	if g.chain.maxInflight.Load() > 3 || g.chain.maxInflight.Load() < 2 {
		t.Fatalf("max in flight %d with a limit of 3", g.chain.maxInflight.Load())
	}
}

func TestFailedRefreshKeepsThePreviousSnapshot(t *testing.T) {
	g := newRig(t)
	first := g.refresh()

	g.chain.setHead(pinnedBlock + 3)
	g.chain.fail = func(sel string, data []byte) error {
		if sel == selector(t, "getPositionsV2") {
			return errBoom
		}
		return nil
	}
	if err := g.r.refreshOnce(context.Background()); err == nil {
		t.Fatal("refresh succeeded with failing position pages")
	}
	if g.r.Latest() != first {
		t.Fatal("a failed refresh replaced the snapshot")
	}

	before := g.r.Status().RefreshErrors
	g.r.refresh(context.Background())
	if g.r.Latest() != first {
		t.Fatal("refresh wrapper replaced the snapshot")
	}
	st := g.r.Status()
	if st.RefreshErrors != before+1 || st.LastErrorAt.IsZero() {
		t.Fatalf("status after a failure %+v", st)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "refresh"}); got != 1 {
		t.Fatalf("error metric %v", got)
	}

	g.chain.fail = nil
	g.refresh()
	if g.r.Latest() == first || g.r.Latest().Block != pinnedBlock+1 {
		t.Fatal("snapshot did not recover")
	}
}

func TestSlowReadFailureKeepsCachedValues(t *testing.T) {
	g := newRig(t)
	g.refresh()
	g.advance(11 * time.Minute)
	g.chain.setHead(pinnedBlock + 3)
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getExchangeInfo") {
			return errBoom
		}
		return nil
	}
	s := g.refresh()
	if s.Exchange.CollateralDecimals != 6 {
		t.Fatalf("exchange info lost %+v", s.Exchange)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "slow"}); got != 1 {
		t.Fatalf("slow error metric %v", got)
	}
}

func TestFirstRefreshFailsWithoutSlowData(t *testing.T) {
	g := newRig(t)
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getExchangeInfo") {
			return errBoom
		}
		return nil
	}
	if err := g.r.refreshOnce(context.Background()); err == nil {
		t.Fatal("first refresh succeeded without exchange info")
	}
	if g.r.Ready() || g.r.Latest() != nil {
		t.Fatal("runner is ready without a snapshot")
	}
}

func TestImplementationChange(t *testing.T) {
	other := "0x00000000000000000000000000000000000000aa"
	g := newRig(t, func(o *Options) { o.Config.ExpectedImplementation = other })
	s := g.refresh()
	if !s.ImplementationChanged || g.r.Status().ImplementationChanged != true {
		t.Fatal("a mismatch with expected_implementation was not flagged")
	}

	m := newRig(t, func(o *Options) { o.Config.ExpectedImplementation = "0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a" })
	if m.refresh().ImplementationChanged {
		t.Fatal("the expected implementation was flagged")
	}

	// without an expected value the first address seen is the baseline
	b := newRig(t)
	b.refresh()
	var upgraded perpl.Address
	copy(upgraded[:], []byte("upgraded-implementation"))
	b.chain.mu.Lock()
	b.chain.impl = upgraded
	b.chain.mu.Unlock()
	b.chain.setHead(pinnedBlock + 3)
	if s := b.refresh(); !s.ImplementationChanged || s.Implementation != upgraded {
		t.Fatalf("an upgrade after the first snapshot was missed %v", s.Implementation)
	}
}

func TestPersistsMarketsEveryRefreshAndPositionsEveryNth(t *testing.T) {
	g := newRig(t) // PositionSnapshotEvery is 2
	s1 := g.refresh()
	g.chain.setHead(pinnedBlock + 3)
	g.refresh()
	g.chain.setHead(pinnedBlock + 4)
	g.refresh()

	rows := g.store.marketRows()
	priced := 0
	for _, mv := range s1.Markets {
		if mv.Priced {
			priced++
		}
	}
	if priced == 0 || len(rows) != 3*priced {
		t.Fatalf("%d market rows for %d priced markets over 3 refreshes", len(rows), priced)
	}
	blocks := map[uint64]int{}
	for _, r := range rows {
		blocks[r.Block]++
	}
	if len(blocks) != 3 {
		t.Fatalf("rows by block %v", blocks)
	}

	// refreshes 1 and 3 write positions, refresh 2 does not
	pos := g.store.positionRows()
	byBlock := map[uint64]int{}
	for _, p := range pos {
		byBlock[p.Block]++
	}
	if byBlock[pinnedBlock] != 665 || byBlock[pinnedBlock+1] != 0 || byBlock[pinnedBlock+2] != 665 {
		t.Fatalf("position rows by block %v", byBlock)
	}
}

func TestStoredRowsCarryDecimalStrings(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	btc, _ := s.Find("BTC")

	var row store.MarketSnapshot
	for _, r := range g.store.marketRows() {
		if r.PerpID == 1 {
			row = r
		}
	}
	if row.Mark != "82893.4" || row.Block != pinnedBlock || row.Time.Unix() != pinnedTime {
		t.Fatalf("btc row %+v", row)
	}
	if row.Positions != 246 || row.Insurance != "182983.039136" {
		t.Fatalf("btc row %+v", row)
	}
	if row.LongOI == "" || row.ShortOI == "" || row.LongNotional == "" || row.Oracle == "" {
		t.Fatalf("btc row has empty fields %+v", row)
	}
	if row.OracleTime.Unix() != int64(btc.Market.OracleTimestampSec) {
		t.Fatalf("oracle time %v", row.OracleTime)
	}
	if (row.SCRM10 == nil) != (btc.Metrics.SCRM10 == nil) {
		t.Fatalf("scr mismatch %v %v", row.SCRM10, btc.Metrics.SCRM10)
	}

	d, _ := loadDerived(t, 1)
	var pos store.PositionSnapshot
	for _, p := range g.store.positionRows() {
		if p.PerpID == 1 && strconv.FormatUint(p.AccountID, 10) == d.Positions[0].AccountID {
			pos = p
		}
	}
	if pos.LiqPrice != d.Positions[0].LiqPrice || pos.Entry != d.Positions[0].Entry || pos.BkPrice != d.Positions[0].Bankrupt {
		t.Fatalf("position row %+v, want %+v", pos, d.Positions[0])
	}
	if pos.Side != store.SideLong || pos.Size != "0.00179" || pos.Deposit != "41.612829" || pos.PremiumPnl != "6.224546" || pos.DeltaPnl != "23.540648" {
		t.Fatalf("position row %+v", pos)
	}
}

func TestPersistenceFailureDoesNotBlockTheSnapshot(t *testing.T) {
	g := newRig(t)
	g.store.failOn("markets", errBoom)
	g.refresh()
	if !g.r.Ready() {
		t.Fatal("snapshot not served after a store failure")
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "persist"}); got != 1 {
		t.Fatalf("persist error metric %v", got)
	}
}

func TestPrunesSnapshotsHourly(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	g.r.pruneIfDue(ctx)
	if len(g.store.pruned) != 1 || !g.store.pruned[0].Equal(epoch.Add(-168*time.Hour)) {
		t.Fatalf("first prune %v", g.store.pruned)
	}
	if len(g.store.prunedMkt) != 1 || !g.store.prunedMkt[0].Equal(epoch.Add(-8*24*time.Hour)) {
		t.Fatalf("first market prune %v", g.store.prunedMkt)
	}
	g.advance(30 * time.Minute)
	g.r.pruneIfDue(ctx)
	if len(g.store.pruned) != 1 || len(g.store.prunedMkt) != 1 {
		t.Fatal("pruned twice within the hour")
	}
	g.advance(31 * time.Minute)
	g.r.pruneIfDue(ctx)
	if len(g.store.pruned) != 2 || len(g.store.prunedMkt) != 2 {
		t.Fatalf("no prune after an hour %v %v", g.store.pruned, g.store.prunedMkt)
	}
	g.store.failOn("prune", errBoom)
	g.advance(2 * time.Hour)
	g.r.pruneIfDue(ctx)
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "prune"}); got != 1 {
		t.Fatalf("prune error metric %v", got)
	}
	if len(g.store.prunedMkt) != 3 {
		t.Fatal("the market prune must run when the position prune fails")
	}
	g.store.failOn("prune", nil)
	g.store.failOn("prune_markets", errBoom)
	g.advance(2 * time.Hour)
	g.r.pruneIfDue(ctx)
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "prune"}); got != 2 {
		t.Fatalf("prune error metric %v", got)
	}
}

func TestPaidRPCCallsAreCountedAndExposed(t *testing.T) {
	g := newRig(t)
	g.chain.mu.Lock()
	g.chain.paid = 3
	g.chain.mu.Unlock()
	s := g.refresh()
	if s.PaidRPCCalls != 3 || g.r.Status().PaidRPCCalls != 3 {
		t.Fatalf("paid calls %d", s.PaidRPCCalls)
	}
	if got := metricValue(t, g.reg, "perps_paid_rpc_calls_total", nil); got != 3 {
		t.Fatalf("paid metric %v", got)
	}
}

func TestMetricsAfterRefresh(t *testing.T) {
	g := newRig(t)
	g.refresh()
	g.advance(7 * time.Second)
	if got := metricValue(t, g.reg, "perps_positions", nil); got != 665 {
		t.Fatalf("positions gauge %v", got)
	}
	if got := metricValue(t, g.reg, "perps_snapshot_age_seconds", nil); got < 6.9 || got > 7.1 {
		t.Fatalf("snapshot age %v", got)
	}
	if got := histogramCount(t, g.reg, "perps_refresh_duration_seconds"); got != 0 {
		t.Fatalf("refreshOnce must not observe the duration, got %d", got)
	}
	g.chain.setHead(pinnedBlock + 3)
	g.r.refresh(context.Background())
	if got := histogramCount(t, g.reg, "perps_refresh_duration_seconds"); got != 1 {
		t.Fatalf("duration observations %d", got)
	}
}

func TestStatusReport(t *testing.T) {
	g := newRig(t)
	if st := g.r.Status(); st.Ready || st.Block != 0 || st.Exchange != exchangeText {
		t.Fatalf("status before the first snapshot %+v", st)
	}
	g.refresh()
	g.advance(5 * time.Second)
	st := g.r.Status()
	if !st.Ready || st.Block != pinnedBlock || st.Markets != 17 || st.Positions != 665 {
		t.Fatalf("status %+v", st)
	}
	if st.SnapshotAge < 5*time.Second || st.SnapshotAge > 6*time.Second {
		t.Fatalf("snapshot age %v", st.SnapshotAge)
	}
	if st.Implementation != g.chain.impl.String() || st.Refreshes != 1 {
		t.Fatalf("status %+v", st)
	}
	if st.Backfill != BackfillPending {
		t.Fatalf("backfill state %q before history starts", st.Backfill)
	}
}

func TestAssessmentFailureFailsTheRefresh(t *testing.T) {
	g := newRig(t, func(o *Options) { o.Config.Shocks = []float64{0.9} })
	if err := g.r.refreshOnce(context.Background()); err == nil {
		t.Fatal("an unusable shock did not fail the refresh")
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	good := func() Options {
		return Options{Config: testConfig(), Chain: newFakeChain(t), Store: newFakeStore(), Logger: quietLogger()}
	}
	cases := map[string]func(*Options){
		"no chain":        func(o *Options) { o.Chain = nil },
		"no store":        func(o *Options) { o.Store = nil },
		"bad exchange":    func(o *Options) { o.Config.Exchange = "0x12" },
		"bad impl":        func(o *Options) { o.Config.ExpectedImplementation = "nope" },
		"zero inflight":   func(o *Options) { o.Config.MaxInflightCalls = 0 },
		"zero page":       func(o *Options) { o.Config.PositionPageSize = 0 },
		"zero interval":   func(o *Options) { o.Config.RefreshInterval = 0 },
		"zero snap every": func(o *Options) { o.Config.PositionSnapshotEvery = 0 },
		"no shocks":       func(o *Options) { o.Config.Shocks = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := good()
			mutate(&o)
			if _, err := New(o); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := New(good()); err != nil {
		t.Fatal(err)
	}
}

func TestNewReturnsMetricRegistrationErrors(t *testing.T) {
	g := newRig(t)
	o := Options{Config: testConfig(), Chain: newFakeChain(t), Store: newFakeStore(), Registerer: g.reg}
	if _, err := New(o); err == nil {
		t.Fatal("registering twice on one registry succeeded")
	}
}

func TestAStalledDatabaseDoesNotFreezeTheRefreshLoop(t *testing.T) {
	g := newRig(t)
	g.r.dbTimeout = 50 * time.Millisecond
	g.store.stall.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.refresh()
		g.r.pruneIfDue(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh loop hung on the database")
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "persist"}); got != 1 {
		t.Fatalf("persist errors %v", got)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "prune"}); got != 2 {
		t.Fatalf("prune errors %v", got)
	}
}

func TestLastRefreshTimestampMetric(t *testing.T) {
	g := newRig(t)
	if got := metricValue(t, g.reg, "perps_last_refresh_success_timestamp_seconds", nil); got != 0 {
		t.Fatalf("before the first snapshot %v", got)
	}
	g.refresh()
	if got := metricValue(t, g.reg, "perps_last_refresh_success_timestamp_seconds", nil); got != float64(epoch.Unix()) {
		t.Fatalf("after a refresh %v, want %d", got, epoch.Unix())
	}
}

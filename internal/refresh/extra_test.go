package refresh

import (
	"context"
	"math/big"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/rpc"
	"perps/internal/sources"
	"perps/internal/store"
)

func TestSCRAtTenPercentIsComputedWhenNotConfigured(t *testing.T) {
	g := newRig(t, func(o *Options) { o.Config.Shocks = []float64{-0.2, 0.2} })
	s := g.refresh()
	btc, _ := s.Find("BTC")
	want, err := s.Stress(1, -0.1)
	if err != nil {
		t.Fatal(err)
	}
	if (btc.Metrics.SCRM10 == nil) != (want.SCR == nil) {
		t.Fatalf("scr at -10%% %v want %v", btc.Metrics.SCRM10, want.SCR)
	}
	if want.SCR != nil && *btc.Metrics.SCRM10 != *want.SCR {
		t.Fatalf("scr at -10%% %v want %v", *btc.Metrics.SCRM10, *want.SCR)
	}
}

func TestUnpricedMarketWithPositionsIsAnError(t *testing.T) {
	g := newRig(t)
	m := perpl.Market{PerpID: 5, MarkPNS: big.NewInt(0)}
	if _, err := g.r.buildView(m, []perpl.Position{{PerpID: 5}}, perpl.ExchangeInfo{}, epoch); err == nil {
		t.Fatal("positions without a mark price accepted")
	}
	mv, err := g.r.buildView(m, nil, perpl.ExchangeInfo{}, epoch)
	if err != nil || mv.Priced || len(mv.Stress) != 0 {
		t.Fatalf("empty unpriced market %+v %v", mv, err)
	}
	snap := newSnapshot([]*MarketView{mv}, nil)
	if _, err := snap.Stress(5, 0.1); err == nil {
		t.Fatal("stress on an unpriced market")
	}
	if _, err := snap.LiquidationMap(77, 25, 20); err == nil {
		t.Fatal("map for an unknown market")
	}
	if rows := marketRows(snap); len(rows) != 0 {
		t.Fatalf("%d rows for an unpriced market", len(rows))
	}
}

func TestAccountCacheStaysBounded(t *testing.T) {
	g := newRig(t)
	for i := range accountCacheMax + 50 {
		var a perpl.Address
		a[0], a[1], a[2] = byte(i), byte(i>>8), 1
		g.r.rememberAccount(a, accountEntry{at: epoch})
	}
	if n := len(g.r.accountCache); n > accountCacheMax {
		t.Fatalf("%d cached accounts", n)
	}
	old := accountEntry{at: epoch.Add(-time.Hour)}
	g.r.accountCache[perpl.Address{9}] = old
	g.r.rememberAccount(perpl.Address{8}, accountEntry{at: epoch})
	if _, ok := g.r.accountCache[perpl.Address{9}]; ok && len(g.r.accountCache) >= accountCacheMax {
		t.Fatal("expired entry kept at the limit")
	}
}

func TestRunBackfillRetriesUntilDone(t *testing.T) {
	attempts := 0
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		attempts++
		if attempts < 3 {
			return q.From, errBoom
		}
		return q.To, nil
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs; o.Config.RefreshInterval = time.Millisecond })
	g.chain.setHead(109168002)
	ctx := context.Background()
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}
	g.r.runBackfill(ctx)
	if attempts != 3 || g.r.Status().Backfill != BackfillComplete {
		t.Fatalf("attempts %d status %+v", attempts, g.r.Status())
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "backfill"}); got != 2 {
		t.Fatalf("backfill errors %v", got)
	}
}

func TestRunBackfillStopsWhenCancelled(t *testing.T) {
	hs := scanFunc(func(_ context.Context, q sources.Query, _ func(sources.Page) error) (uint64, error) {
		return q.From, errBoom
	})
	g := newRig(t, func(o *Options) { o.HyperSync = hs })
	g.chain.setHead(109168002)
	if err := g.r.initHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.r.runBackfill(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("backfill kept running after cancel")
	}
}

func TestUnknownMarketLogsAreSkippedButOutagesFailTheStep(t *testing.T) {
	g := newRig(t)
	g.chain.loadTailFixtures(true)
	ctx := context.Background()
	g.chain.setHead(90121002)
	g.store.cursors[store.CursorTail] = 90076000
	if err := g.r.initHistory(ctx); err != nil {
		t.Fatal(err)
	}

	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getPerpetualInfoV2") {
			return &rpc.Error{Code: rpc.CodeRevert, Message: "execution reverted"}
		}
		return nil
	}
	if err := g.r.tailOnce(ctx); err != nil {
		t.Fatalf("a revert for an unknown market failed the step: %v", err)
	}
	if n := len(g.store.liquidations()); n != 0 {
		t.Fatalf("%d rows for unknown markets", n)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "decode"}); got != 4 {
		t.Fatalf("decode errors %v", got)
	}

	g.store.cursors[store.CursorTail] = 90076000
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getPerpetualInfoV2") {
			return errBoom
		}
		return nil
	}
	if err := g.r.tailOnce(ctx); err == nil {
		t.Fatal("an rpc outage did not fail the step")
	}
	if tail, _ := g.store.cursor(store.CursorTail); tail != 90076000 {
		t.Fatalf("cursor moved to %d", tail)
	}
}

func TestPositionPersistenceFailureIsCounted(t *testing.T) {
	g := newRig(t)
	g.store.failOn("positions", errBoom)
	g.refresh()
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "persist"}); got != 1 {
		t.Fatalf("persist errors %v", got)
	}
}

func TestPinnedBlockPrefersTheSnapshot(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	b, err := g.r.pinnedBlock(ctx)
	if err != nil || b != pinnedBlock {
		t.Fatalf("pinned %d %v", b, err)
	}
	g.refresh()
	g.chain.setHead(1)
	if b, err := g.r.pinnedBlock(ctx); err != nil || b != pinnedBlock {
		t.Fatalf("pinned %d %v", b, err)
	}
	g2 := newRig(t)
	g2.chain.setHead(1)
	if _, err := g2.r.pinnedBlock(ctx); err == nil {
		t.Fatal("a head below the pin depth was accepted")
	}
}

func TestRefreshRejectsAShallowHead(t *testing.T) {
	g := newRig(t)
	g.chain.setHead(1)
	if err := g.r.refreshOnce(context.Background()); err == nil {
		t.Fatal("refresh at head 1 succeeded")
	}
}

func TestSlowDataMustCoverNewMarkets(t *testing.T) {
	g := newRig(t)
	g.refresh()
	if g.r.slow.covers(append(fixturePerpIDs(t), 999)) {
		t.Fatal("slow data claims to cover an unknown market")
	}
}

func TestCmpUint(t *testing.T) {
	if cmpUint(1, 2) != -1 || cmpUint(2, 1) != 1 || cmpUint(3, 3) != 0 {
		t.Fatal("cmpUint")
	}
}

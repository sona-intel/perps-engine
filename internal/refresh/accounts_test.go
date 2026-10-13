package refresh

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/sources"
	"perps/internal/store"
)

const (
	btcTopID   = 4848
	btcTopAddr = "0x7e842a5d75249257036d6bde1887874131e77ad1"
	noAccount  = "0x000000000000000000000000000000000000dEaD"
)

func TestResolveTopAccountsStoresTheOnesItFinds(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	ctx := context.Background()

	var mu sync.Mutex
	asked := map[string]int{}
	g.chain.fail = func(sel string, data []byte) error {
		if sel == selGetAccountByID {
			mu.Lock()
			asked[string(data)]++
			mu.Unlock()
		}
		return nil
	}
	g.r.resolveTop(ctx, s)

	if got := g.store.accounts[btcTopID]; got.String() != btcTopAddr {
		t.Fatalf("account %d stored as %s", btcTopID, got)
	}
	if len(g.store.accounts) > 3 {
		t.Fatalf("%d accounts stored, only 3 are recorded in the fixtures", len(g.store.accounts))
	}

	// an account that resolved is not asked for again
	g.r.resolveTop(ctx, s)
	if n := asked[string(perpl.EncodeGetAccountByID(btcTopID))]; n != 1 {
		t.Fatalf("account %d requested %d times", btcTopID, n)
	}
}

func TestResolveTopAccountsOnlyAsksForTopAccounts(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	g.r.resolveTop(context.Background(), s)

	want := map[uint64]bool{}
	conc := risk.Concentration(s.Books())
	for _, a := range conc.Overall.TopAccounts {
		want[a.AccountID] = true
	}
	for _, m := range conc.Markets {
		for _, a := range m.TopAccounts {
			want[a.AccountID] = true
		}
	}
	if got := g.chain.selectors[selGetAccountByID]; got != len(want) {
		t.Fatalf("%d getAccountById calls for %d top accounts", got, len(want))
	}
}

func TestResolveTopAccountsUsesTheStoreFirst(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	conc := risk.Concentration(s.Books())
	for _, a := range conc.Overall.TopAccounts {
		g.store.accounts[a.AccountID] = mustAddress(t, btcTopAddr)
	}
	for _, m := range conc.Markets {
		for _, a := range m.TopAccounts {
			g.store.accounts[a.AccountID] = mustAddress(t, btcTopAddr)
		}
	}
	g.r.resolveTop(context.Background(), s)
	if got := g.chain.selectors[selGetAccountByID]; got != 0 {
		t.Fatalf("%d chain calls for accounts the store knows", got)
	}
}

func TestResolveTopAccountsCountsRealFailures(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selGetAccountByID {
			return errBoom
		}
		return nil
	}
	g.r.resolveTop(context.Background(), s)
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "accounts"}); got == 0 {
		t.Fatal("failures were not counted")
	}
	if len(g.store.accounts) != 0 {
		t.Fatal("accounts stored despite failures")
	}
}

func TestResolveAccountByAddress(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("resolve before the first snapshot %v", err)
	}
	g.refresh()

	acct, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr))
	if err != nil {
		t.Fatal(err)
	}
	if acct.AccountID != btcTopID || acct.BalanceCNS.Cmp(big.NewInt(50008795)) != 0 {
		t.Fatalf("account %+v", acct)
	}
	if g.store.accounts[btcTopID].String() != btcTopAddr {
		t.Fatal("resolved account was not stored")
	}

	byAddr := g.chain.selectors[selector(t, "getAccountByAddr")]
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); err != nil {
		t.Fatal(err)
	}
	if g.chain.selectors[selector(t, "getAccountByAddr")] != byAddr {
		t.Fatal("second lookup within five minutes hit the chain")
	}
	g.advance(4*time.Minute + 59*time.Second)
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); err != nil {
		t.Fatal(err)
	}
	if g.chain.selectors[selector(t, "getAccountByAddr")] != byAddr {
		t.Fatal("cache expired early")
	}
	g.advance(2 * time.Second)
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); err != nil {
		t.Fatal(err)
	}
	if g.chain.selectors[selector(t, "getAccountByAddr")] != byAddr+1 {
		t.Fatal("cache did not expire after five minutes")
	}
}

func TestResolveAccountNotFoundIsCachedToo(t *testing.T) {
	g := newRig(t)
	g.refresh()
	ctx := context.Background()
	addr := mustAddress(t, noAccount)
	if _, err := g.r.ResolveAccount(ctx, addr); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err %v", err)
	}
	calls := g.chain.selectors[selector(t, "getAccountByAddr")]
	if _, err := g.r.ResolveAccount(ctx, addr); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err %v", err)
	}
	if g.chain.selectors[selector(t, "getAccountByAddr")] != calls {
		t.Fatal("a missing account was looked up twice")
	}
	if len(g.store.accounts) != 0 {
		t.Fatal("a missing account was stored")
	}
}

func TestResolveAccountDoesNotCacheTransientErrors(t *testing.T) {
	g := newRig(t)
	g.refresh()
	ctx := context.Background()
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getAccountByAddr") {
			return errBoom
		}
		return nil
	}
	_, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr))
	if err == nil || errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err %v", err)
	}
	g.chain.fail = nil
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); err != nil {
		t.Fatalf("retry failed %v", err)
	}
}

func TestResolveAccountReadsAtTheSnapshotBlock(t *testing.T) {
	g := newRig(t)
	g.refresh()
	before := g.chain.callBlocks[pinnedBlock]
	if _, err := g.r.ResolveAccount(context.Background(), mustAddress(t, btcTopAddr)); err != nil {
		t.Fatal(err)
	}
	if g.chain.callBlocks[pinnedBlock] != before+1 {
		t.Fatalf("calls by block %v", g.chain.callBlocks)
	}
}

func TestLeaderboardKeepsTheLastGoodCopy(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if _, ok := g.r.Leaderboard(); ok {
		t.Fatal("leaderboard available before a poll")
	}
	day := sources.Leaderboard{At: sources.Stamp{Block: 7}, Rows: []sources.LeaderboardRow{{Rank: 1, Address: perpl.Address{1}, VolumeCNS: big.NewInt(5), PnlCNS: big.NewInt(1)}}}
	all := sources.Leaderboard{At: sources.Stamp{Block: 8}, Rows: []sources.LeaderboardRow{{Rank: 1, Address: perpl.Address{2}, VolumeCNS: big.NewInt(9), PnlCNS: big.NewInt(2)}}}
	g.board.rows[sources.PeriodDay] = day
	g.board.rows[sources.PeriodAll] = all

	g.r.pollLeaderboard(ctx)
	data, ok := g.r.Leaderboard()
	if !ok || data.Day == nil || data.All == nil || data.Day.At.Block != 7 || data.All.At.Block != 8 {
		t.Fatalf("leaderboard %+v ok %v", data, ok)
	}
	if !data.DayAt.Equal(epoch) || !data.AllAt.Equal(epoch) {
		t.Fatalf("fetched at %v %v", data.DayAt, data.AllAt)
	}

	// a failing period keeps its old copy while the other one moves on
	g.advance(time.Minute)
	g.board.setFail(sources.PeriodDay, errBoom)
	g.board.rows[sources.PeriodAll] = sources.Leaderboard{At: sources.Stamp{Block: 9}}
	g.r.pollLeaderboard(ctx)
	data, ok = g.r.Leaderboard()
	if !ok || data.Day.At.Block != 7 || data.All.At.Block != 9 {
		t.Fatalf("leaderboard %+v", data)
	}
	if !data.DayAt.Equal(epoch) || !data.AllAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("fetched at %v %v", data.DayAt, data.AllAt)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "leaderboard"}); got != 1 {
		t.Fatalf("leaderboard errors %v", got)
	}
	if !g.r.Status().Leaderboard {
		t.Fatal("status reports no leaderboard")
	}

	g.board.setFail(sources.PeriodAll, errBoom)
	g.r.pollLeaderboard(ctx)
	if data, _ := g.r.Leaderboard(); data.Day.At.Block != 7 || data.All.At.Block != 9 {
		t.Fatalf("leaderboard lost after total failure %+v", data)
	}
}

func TestLeaderboardUnavailableWhenNeverFetched(t *testing.T) {
	g := newRig(t)
	for _, p := range []sources.Period{sources.PeriodDay, sources.PeriodAll} {
		g.board.setFail(p, errBoom)
		g.board.setFailBy(p, sources.ByPnl, errBoom)
	}
	g.r.pollLeaderboard(context.Background())
	if _, ok := g.r.Leaderboard(); ok {
		t.Fatal("leaderboard available after only failures")
	}
	if g.r.Status().Leaderboard {
		t.Fatal("status reports a leaderboard")
	}
}

func TestPollingWithoutABoardIsANoop(t *testing.T) {
	g := newRig(t, func(o *Options) { o.Board = nil })
	g.r.pollLeaderboard(context.Background())
	if _, ok := g.r.Leaderboard(); ok {
		t.Fatal("leaderboard from nowhere")
	}
}

func TestConcurrentLookupsOfOneAddressShareOneCall(t *testing.T) {
	g := newRig(t)
	g.refresh()
	release := make(chan struct{})
	byAddr := selector(t, "getAccountByAddr")
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == byAddr {
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := g.r.ResolveAccount(context.Background(), mustAddress(t, btcTopAddr)); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := g.chain.selectors[byAddr]; n != 1 {
		t.Fatalf("%d getAccountByAddr calls for one address", n)
	}
}

func TestLookupsDoNotWaitForTheRefreshSlots(t *testing.T) {
	g := newRig(t)
	g.refresh()
	for range cap(g.r.sem) {
		g.r.sem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := g.r.ResolveAccount(ctx, mustAddress(t, btcTopAddr)); err != nil {
		t.Fatalf("lookup starved by a busy refresh: %v", err)
	}
}

func TestLookupsOfManyAddressesStayWithinTheirOwnSlots(t *testing.T) {
	g := newRig(t)
	g.refresh()
	g.chain.delay = 10 * time.Millisecond
	g.chain.maxInflight.Store(0)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			addr := mustAddress(t, btcTopAddr)
			addr[19] = byte(i + 1)
			_, _ = g.r.ResolveAccount(context.Background(), addr)
		})
	}
	wg.Wait()
	if got := g.chain.maxInflight.Load(); got > lookupSlots {
		t.Fatalf("%d lookups in flight, the limit is %d", got, lookupSlots)
	}
}

func TestResolveTopAccountsAlsoResolvesLiquidatedAccounts(t *testing.T) {
	g := newRig(t)
	s := g.refresh()
	const gone = 77
	g.store.liqs[[2]uint64{1, 0}] = store.Liquidation{Block: 1, AccountID: gone}
	g.store.liqs[[2]uint64{2, 0}] = store.Liquidation{Block: 2, AccountID: btcTopID}
	g.store.accounts[btcTopID] = mustAddress(t, btcTopAddr)

	var mu sync.Mutex
	asked := 0
	g.chain.fail = func(sel string, data []byte) error {
		if sel == selGetAccountByID && string(data) == string(perpl.EncodeGetAccountByID(gone)) {
			mu.Lock()
			asked++
			mu.Unlock()
		}
		return nil
	}
	g.r.resolveTop(context.Background(), s)
	g.r.resolveTop(context.Background(), s)
	if asked != 1 {
		t.Fatalf("account %d requested %d times, want once even though the exchange has none", gone, asked)
	}
	if _, ok := g.store.accounts[gone]; ok {
		t.Fatal("an account the exchange does not know was stored")
	}
}

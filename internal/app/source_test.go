package app

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/refresh"
	"perps/internal/risk"
	"perps/internal/server"
	"perps/internal/sources"
	"perps/internal/store"
)

type fakeRunner struct {
	latest  *refresh.Snapshot
	ready   bool
	board   refresh.LeaderboardData
	boardOK bool
	status  refresh.Status
	acct    perpl.Account
	acctErr error
	asked   perpl.Address
}

func (f *fakeRunner) Latest() *refresh.Snapshot { return f.latest }
func (f *fakeRunner) Ready() bool               { return f.ready }
func (f *fakeRunner) Leaderboard() (refresh.LeaderboardData, bool) {
	return f.board, f.boardOK
}
func (f *fakeRunner) Status() refresh.Status { return f.status }
func (f *fakeRunner) ResolveAccount(_ context.Context, a perpl.Address) (perpl.Account, error) {
	f.asked = a
	return f.acct, f.acctErr
}

type fakeReader struct{ Reader }

func (fakeReader) CountLiquidations(context.Context) (store.LiquidationTotals, error) {
	return store.LiquidationTotals{Count: 7, FirstBlock: 3, LastBlock: 9}, nil
}

func market(perp uint64, positions int) *refresh.MarketView {
	mv := &refresh.MarketView{Market: perpl.Market{PerpID: perp, Symbol: fmt.Sprintf("M%d", perp)}}
	mv.Book = make([]risk.Assessed, positions)
	for i := range mv.Book {
		mv.Book[i] = risk.Assessed{PerpID: perp, AccountID: uint64(i + 1)}
	}
	return mv
}

func snapshot() *refresh.Snapshot {
	return &refresh.Snapshot{
		Block:          42,
		Time:           time.Unix(1_700_000_000, 0).UTC(),
		Exchange:       perpl.ExchangeInfo{CollateralDecimals: 6, FundingInterval: 8571},
		Implementation: perpl.Address{0xaa},
		Markets:        []*refresh.MarketView{market(1, 2), market(10, 0)},
	}
}

var exchange = perpl.Address{0x34}

func TestSourceHasNoSnapshotBeforeTheRunnerPublishes(t *testing.T) {
	s := NewSource(&fakeRunner{}, fakeReader{}, exchange)
	if s.Snapshot() != nil || s.Ready() {
		t.Fatal("a snapshot appeared from nowhere")
	}
}

func TestSnapshotMapsMarketsInOrder(t *testing.T) {
	snap := snapshot()
	s := NewSource(&fakeRunner{latest: snap, ready: true}, fakeReader{}, exchange)
	got := s.Snapshot()
	if got == nil || !s.Ready() {
		t.Fatal("no snapshot")
	}
	if got.Block != 42 || !got.Time.Equal(snap.Time) || got.Exchange.FundingInterval != 8571 || got.Exchange.CollateralDecimals != 6 {
		t.Fatalf("header %+v", got)
	}
	if len(got.Books) != 2 || got.Books[0].Market.PerpID != 1 || got.Books[1].Market.PerpID != 10 {
		t.Fatalf("books %+v", got.Books)
	}
	if len(got.Books[0].Positions) != 2 || len(got.Books[1].Positions) != 0 {
		t.Fatalf("positions %+v", got.Books)
	}
}

func TestSnapshotIsBuiltOncePerRefresh(t *testing.T) {
	f := &fakeRunner{latest: snapshot(), ready: true}
	s := NewSource(f, fakeReader{}, exchange)
	first := s.Snapshot()
	if s.Snapshot() != first {
		t.Fatal("the same refresh was converted twice")
	}
	f.latest = snapshot()
	next := s.Snapshot()
	if next == first {
		t.Fatal("a new refresh was ignored")
	}
}

func TestRuntimeCombinesStatusAndSnapshot(t *testing.T) {
	f := &fakeRunner{latest: snapshot(), status: refresh.Status{
		Backfill: refresh.BackfillRunning, PaidRPCCalls: 3,
	}}
	f.latest.ImplementationChanged = true
	rt := NewSource(f, fakeReader{}, exchange).Runtime()
	want := server.Runtime{
		Exchange: exchange, Implementation: perpl.Address{0xaa}, ImplementationChanged: true,
		Backfill: server.BackfillRunning, PaidRPCCalls: 3,
	}
	if rt != want {
		t.Fatalf("got %+v want %+v", rt, want)
	}
}

func TestRuntimeBeforeTheFirstSnapshot(t *testing.T) {
	rt := NewSource(&fakeRunner{status: refresh.Status{Backfill: refresh.BackfillPending}}, fakeReader{}, exchange).Runtime()
	if rt.Exchange != exchange || !rt.Implementation.IsZero() || rt.ImplementationChanged || rt.Backfill != server.BackfillPending {
		t.Fatalf("got %+v", rt)
	}
}

func TestBackfillStatesMatchBetweenPackages(t *testing.T) {
	pairs := [][2]string{
		{refresh.BackfillDisabled, server.BackfillDisabled},
		{refresh.BackfillPending, server.BackfillPending},
		{refresh.BackfillRunning, server.BackfillRunning},
		{refresh.BackfillRetrying, server.BackfillRetrying},
		{refresh.BackfillComplete, server.BackfillComplete},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Fatalf("%q != %q", p[0], p[1])
		}
	}
}

func TestLeaderboardPicksThePeriod(t *testing.T) {
	day := &sources.Leaderboard{Rows: []sources.LeaderboardRow{{Rank: 1, PnlCNS: big.NewInt(1), VolumeCNS: big.NewInt(2)}}}
	all := &sources.Leaderboard{Rows: []sources.LeaderboardRow{{Rank: 9}}}
	cases := []struct {
		name    string
		data    refresh.LeaderboardData
		ok      bool
		period  sources.Period
		wantOK  bool
		wantTop int
	}{
		{"day", refresh.LeaderboardData{Day: day, All: all}, true, sources.PeriodDay, true, 1},
		{"all", refresh.LeaderboardData{Day: day, All: all}, true, sources.PeriodAll, true, 9},
		{"day never fetched", refresh.LeaderboardData{All: all}, true, sources.PeriodDay, false, 0},
		{"all never fetched", refresh.LeaderboardData{Day: day}, true, sources.PeriodAll, false, 0},
		{"nothing fetched", refresh.LeaderboardData{}, false, sources.PeriodDay, false, 0},
		{"unknown period", refresh.LeaderboardData{Day: day, All: all}, true, sources.Period("week"), false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewSource(&fakeRunner{board: c.data, boardOK: c.ok}, fakeReader{}, exchange)
			lb, ok := s.Leaderboard(c.period)
			if ok != c.wantOK {
				t.Fatalf("ok %v", ok)
			}
			if ok && lb.Rows[0].Rank != c.wantTop {
				t.Fatalf("rank %d", lb.Rows[0].Rank)
			}
		})
	}
}

func TestAccountMapsNotFoundToTheServerError(t *testing.T) {
	addr := perpl.Address{0x01}
	f := &fakeRunner{acctErr: fmt.Errorf("wrapped: %w", refresh.ErrAccountNotFound)}
	s := NewSource(f, fakeReader{}, exchange)
	_, err := s.Account(t.Context(), addr)
	if !errors.Is(err, server.ErrAccountNotFound) {
		t.Fatalf("got %v", err)
	}
	if f.asked != addr {
		t.Fatal("address not forwarded")
	}
}

func TestAccountPassesOtherErrorsAndResults(t *testing.T) {
	boom := errors.New("boom")
	_, err := NewSource(&fakeRunner{acctErr: boom}, fakeReader{}, exchange).Account(t.Context(), perpl.Address{})
	if !errors.Is(err, boom) || errors.Is(err, server.ErrAccountNotFound) {
		t.Fatalf("got %v", err)
	}
	_, err = NewSource(&fakeRunner{acctErr: refresh.ErrNotReady}, fakeReader{}, exchange).Account(t.Context(), perpl.Address{})
	if !errors.Is(err, refresh.ErrNotReady) {
		t.Fatalf("got %v", err)
	}
	acct, err := NewSource(&fakeRunner{acct: perpl.Account{AccountID: 5}}, fakeReader{}, exchange).Account(t.Context(), perpl.Address{})
	if err != nil || acct.AccountID != 5 {
		t.Fatalf("got %+v %v", acct, err)
	}
}

func TestStoreReadsPassThrough(t *testing.T) {
	got, err := NewSource(&fakeRunner{}, fakeReader{}, exchange).CountLiquidations(t.Context())
	if err != nil || got.Count != 7 || got.LastBlock != 9 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestSourceContracts(t *testing.T) {
	var _ server.Source = (*Source)(nil)
	var _ Runner = (*refresh.Runner)(nil)
	var _ Reader = (*store.Store)(nil)
}

func TestLeaderboardCopyOlderThanTheLimitIsUnavailable(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	day := &sources.Leaderboard{Rows: []sources.LeaderboardRow{{Rank: 1}}}
	all := &sources.Leaderboard{Rows: []sources.LeaderboardRow{{Rank: 9}}}
	data := refresh.LeaderboardData{Day: day, All: all, DayAt: now.Add(-31 * time.Minute), AllAt: now.Add(-29 * time.Minute)}
	s := NewSource(&fakeRunner{board: data, boardOK: true}, fakeReader{}, exchange,
		WithLeaderboardMaxAge(30*time.Minute, func() time.Time { return now }))
	if _, ok := s.Leaderboard(sources.PeriodDay); ok {
		t.Fatal("a 31 minute old copy was served")
	}
	if _, ok := s.Leaderboard(sources.PeriodAll); !ok {
		t.Fatal("a 29 minute old copy was dropped")
	}
	unlimited := NewSource(&fakeRunner{board: data, boardOK: true}, fakeReader{}, exchange)
	if _, ok := unlimited.Leaderboard(sources.PeriodDay); !ok {
		t.Fatal("no limit set but the copy was dropped")
	}
}

func TestSnapshotCarriesTheTimeItWasTaken(t *testing.T) {
	snap := snapshot()
	snap.TakenAt = time.Unix(1_700_000_005, 0).UTC()
	got := NewSource(&fakeRunner{latest: snap, ready: true}, fakeReader{}, exchange).Snapshot()
	if !got.TakenAt.Equal(snap.TakenAt) {
		t.Fatalf("TakenAt %v", got.TakenAt)
	}
}

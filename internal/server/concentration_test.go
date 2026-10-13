package server

import (
	"net/http"
	"slices"
	"testing"

	"perps/internal/sources"
	"perps/internal/store"
	"perps/internal/testkit"
)

func TestConcentrationPerMarketAndOverall(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/concentration")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if n := len(r.at(t, "markets").([]any)); n != 2 {
		t.Fatalf("%d markets", n)
	}
	near(t, r.at(t, "markets", 0, "perp_id"), 7)
	if r.at(t, "markets", 0, "symbol") != "TST" || r.at(t, "markets", 0, "long_notional_usd") != "1500" ||
		r.at(t, "markets", 0, "short_notional_usd") != "1500" || r.at(t, "markets", 0, "total_notional_usd") != "3000" {
		t.Fatal(r.at(t, "markets", 0))
	}
	near(t, r.at(t, "markets", 0, "positions"), 4)
	near(t, r.at(t, "markets", 0, "top1"), 1000.0/3000)
	near(t, r.at(t, "markets", 0, "top5"), 1)
	near(t, r.at(t, "markets", 0, "top10"), 1)
	near(t, r.at(t, "markets", 0, "top_accounts", 0, "account_id"), 101)
	near(t, r.at(t, "markets", 0, "top_accounts", 1, "account_id"), 102)
	near(t, r.at(t, "markets", 0, "top_accounts", 0, "share"), 1000.0/3000)
	if r.at(t, "markets", 0, "top_accounts", 0, "notional_usd") != "1000" || r.at(t, "markets", 0, "top_accounts", 0, "address") != testkit.Addr(101).String() {
		t.Fatal(r.at(t, "markets", 0, "top_accounts", 0))
	}
	near(t, r.at(t, "markets", 0, "top_accounts", 0, "positions"), 1)
	near(t, r.at(t, "markets", 1, "top1"), 0.5)

	near(t, r.at(t, "overall", "accounts"), 5)
	if r.at(t, "overall", "total_notional_usd") != "3400" {
		t.Fatal(r.at(t, "overall"))
	}
	near(t, r.at(t, "overall", "top1"), 1200.0/3400)
	near(t, r.at(t, "overall", "top5"), 1)
	near(t, r.at(t, "overall", "top_accounts", 0, "account_id"), 101)
	near(t, r.at(t, "overall", "top_accounts", 0, "positions"), 2)
	if r.at(t, "overall", "top_accounts", 0, "notional_usd") != "1200" {
		t.Fatal(r.at(t, "overall", "top_accounts", 0))
	}
}

func TestConcentrationJoinsAddressesByAccountID(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/concentration")
	if want := []uint64{100, 101, 102, 103, 104}; !slices.Equal(f.idsAsked, want) {
		t.Fatalf("asked for %v, want %v", f.idsAsked, want)
	}
	for i := range 4 {
		if r.at(t, "markets", 0, "top_accounts", i, "account_id").(float64) == 104 {
			isNull(t, r.at(t, "markets", 0, "top_accounts", i, "address"))
			return
		}
	}
	t.Fatal("account 104 missing")
}

func TestConcentrationVolumeShares(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/concentration")
	near(t, r.at(t, "volume", "day", "rows"), 3)
	if r.at(t, "volume", "day", "total_volume_usd") != "1500" || r.at(t, "volume", "day", "top10_volume_usd") != "1500" {
		t.Fatal(r.at(t, "volume", "day"))
	}
	near(t, r.at(t, "volume", "day", "top1"), 1000.0/1500)
	near(t, r.at(t, "volume", "day", "top5"), 1)
	near(t, r.at(t, "volume", "day", "top10"), 1)
	if r.at(t, "volume", "all", "total_volume_usd") != "100000" {
		t.Fatal(r.at(t, "volume", "all"))
	}
	near(t, r.at(t, "volume", "all", "top1"), 0.5)
	for _, c := range r.coverage(t) {
		if c == "perpl_leaderboard_unavailable" {
			t.Fatal("leaderboard reported unavailable")
		}
	}
}

func TestConcentrationWithoutLeaderboards(t *testing.T) {
	for _, gone := range [][]sources.Period{{sources.PeriodDay}, {sources.PeriodAll}, {sources.PeriodDay, sources.PeriodAll}} {
		f := newFake(t)
		for _, p := range gone {
			delete(f.boards, p)
		}
		r := fetch(t, testHandler(f), base+"/concentration")
		if r.Code != http.StatusOK {
			t.Fatal(r.Code, r.Body)
		}
		for _, p := range []sources.Period{sources.PeriodDay, sources.PeriodAll} {
			_, missing := f.boards[p]
			if got := r.at(t, "volume", string(p)); (got == nil) != !missing {
				t.Errorf("%v gone, %s is %v", gone, p, got)
			}
		}
		if !slices.Contains(r.coverage(t), "perpl_leaderboard_unavailable") {
			t.Errorf("%v gone, coverage %v", gone, r.coverage(t))
		}
	}
}

func TestConcentrationEmptyLeaderboardHasNoShares(t *testing.T) {
	f := newFake(t)
	f.boards[sources.PeriodDay] = sources.Leaderboard{}
	r := fetch(t, testHandler(f), base+"/concentration")
	near(t, r.at(t, "volume", "day", "rows"), 0)
	near(t, r.at(t, "volume", "day", "top1"), 0)
	if r.at(t, "volume", "day", "total_volume_usd") != "0" {
		t.Fatal(r.at(t, "volume", "day"))
	}
}

func TestConcentrationKeepers(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/concentration")
	if f.keeperLimit != 10 {
		t.Fatalf("asked for %d keepers", f.keeperLimit)
	}
	near(t, r.at(t, "keepers", "total"), 40)
	near(t, r.at(t, "keepers", "first_block"), 54_773_100)
	near(t, r.at(t, "keepers", "last_block"), 112_777_000)
	if r.at(t, "keepers", "items", 0, "address") != testkit.Addr(1).String() {
		t.Fatal(r.at(t, "keepers"))
	}
	near(t, r.at(t, "keepers", "items", 0, "liquidations"), 30)
	near(t, r.at(t, "keepers", "items", 0, "share"), 0.75)
	near(t, r.at(t, "keepers", "items", 1, "share"), 0.25)
}

func TestConcentrationWithoutLiquidations(t *testing.T) {
	f := newFake(t)
	f.keepers, f.totals = nil, store.LiquidationTotals{}
	r := fetch(t, testHandler(f), base+"/concentration")
	near(t, r.at(t, "keepers", "total"), 0)
	near(t, r.at(t, "keepers", "first_block"), 0)
	if n := len(r.at(t, "keepers", "items").([]any)); n != 0 {
		t.Fatal(n)
	}
}

func TestConcentrationSharesOfMoreKeepersThanListed(t *testing.T) {
	f := newFake(t)
	f.totals.Count = 100
	r := fetch(t, testHandler(f), base+"/concentration")
	near(t, r.at(t, "keepers", "items", 0, "share"), 0.3)
}

func TestHistoryPartialCoverage(t *testing.T) {
	for state, want := range map[string]bool{BackfillComplete: false, BackfillRunning: true, BackfillDisabled: true} {
		f := newFake(t)
		f.rt.Backfill = state
		for _, path := range []string{base + "/concentration", base + "/liquidations", base + "/liquidations/daily"} {
			got := false
			for _, c := range fetch(t, testHandler(f), path).coverage(t) {
				got = got || c == "perpl_history_partial"
			}
			if got != want {
				t.Errorf("%s with backfill %s: partial coverage %v", path, state, got)
			}
		}
	}
}

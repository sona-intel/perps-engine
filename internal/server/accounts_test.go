package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"perps/internal/problem"
	"perps/internal/sources"
	"perps/internal/testkit"
)

func TestWalletCard(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/accounts/"+accountAddr.String())
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	near(t, r.at(t, "account_id"), 101)
	if r.at(t, "address") != accountAddr.String() || r.at(t, "balance_usd") != "25" || r.at(t, "locked_usd") != "5" ||
		r.at(t, "frozen") != false || r.at(t, "total_notional_usd") != "1200" || r.at(t, "total_deposit_usd") != "120" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "min_distance"), 0.05)
	if n := len(r.at(t, "positions").([]any)); n != 2 {
		t.Fatalf("%d positions", n)
	}
	want := map[string]any{
		"market": "TST", "side": "long", "size": "10", "entry_price": "100", "mark_price": "100", "deposit_usd": "100", "premium_pnl_usd": "0",
		"notional_usd": "1000", "maintenance_margin_usd": "50", "liq_price": "95", "bankruptcy_price": "90",
	}
	for k, v := range want {
		if r.at(t, "positions", 0, k) != v {
			t.Errorf("positions[0].%s = %v, want %v", k, r.at(t, "positions", 0, k), v)
		}
	}
	near(t, r.at(t, "positions", 0, "perp_id"), 7)
	near(t, r.at(t, "positions", 0, "leverage"), 10)
	near(t, r.at(t, "positions", 0, "distance"), 0.05)
	near(t, r.at(t, "positions", 0, "oi_share"), 1000.0/1500)
	if r.at(t, "positions", 1, "market") != "ALT" || r.at(t, "positions", 1, "liq_price") != "47" || r.at(t, "positions", 1, "mark_price") != "50" {
		t.Fatal(r.at(t, "positions", 1))
	}
	near(t, r.at(t, "positions", 1, "distance"), 0.06)
	near(t, r.at(t, "positions", 1, "oi_share"), 1)
	if got := r.coverage(t); !slices.Equal(got, []string{"perpl_snapshot_only"}) {
		t.Fatal(got)
	}
}

func TestWalletStanding(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/accounts/"+accountAddr.String())
	near(t, r.at(t, "standing", "day", "rank"), 2)
	if r.at(t, "standing", "day", "pnl_usd") != "-5" || r.at(t, "standing", "day", "volume_usd") != "400" {
		t.Fatal(r.at(t, "standing", "day"))
	}
	near(t, r.at(t, "standing", "all", "rank"), 17)
	if r.at(t, "standing", "all", "volume_usd") != "20000" {
		t.Fatal(r.at(t, "standing", "all"))
	}
}

func TestWalletStandingIsNullWhenUnlistedOrUnavailable(t *testing.T) {
	f := newFake(t)
	f.boards[sources.PeriodDay] = sources.Leaderboard{Rows: []sources.LeaderboardRow{row(1, testkit.Addr(500), 1, 1)}}
	delete(f.boards, sources.PeriodAll)
	r := fetch(t, testHandler(f), base+"/accounts/"+accountAddr.String())
	isNull(t, r.at(t, "standing", "day"))
	isNull(t, r.at(t, "standing", "all"))
	if got := r.coverage(t); !slices.Equal(got, []string{"perpl_snapshot_only", "perpl_leaderboard_unavailable"}) {
		t.Fatal(got)
	}
	f.boards[sources.PeriodAll] = sources.Leaderboard{}
	if got := fetch(t, testHandler(f), base+"/accounts/"+accountAddr.String()).coverage(t); !slices.Equal(got, []string{"perpl_snapshot_only"}) {
		t.Fatalf("both boards read, coverage %v", got)
	}
}

func TestWalletAddressInAnyCase(t *testing.T) {
	h := testHandler(newFake(t))
	for _, a := range []string{accountAddr.String(), "0x" + strings.ToUpper(accountAddr.String()[2:])} {
		if r := fetch(t, h, base+"/accounts/"+a); r.Code != http.StatusOK || r.at(t, "address") != accountAddr.String() {
			t.Errorf("%s: %d %s", a, r.Code, r.Body)
		}
	}
}

func TestWalletWithoutPositions(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/accounts/"+testkit.Addr(555).String())
	if r.Code != http.StatusOK || r.at(t, "frozen") != true || len(r.at(t, "positions").([]any)) != 0 {
		t.Fatal(r.Code, r.Body)
	}
	isNull(t, r.at(t, "min_distance"))
	if r.at(t, "total_notional_usd") != "0" || r.at(t, "balance_usd") != "1" {
		t.Fatal(r.at(t))
	}
}

func TestWalletErrors(t *testing.T) {
	f := newFake(t)
	h := testHandler(f)
	r := fetch(t, h, base+"/accounts/"+unknownAddr.String())
	if r.Code != http.StatusNotFound || r.code(t) != problem.CodeNotFound {
		t.Errorf("unknown: %d %s", r.Code, r.Body)
	}
	for _, a := range []string{"0x12", "nothex", "7e842a5d75249257036d6bde1887874131e77ad1", "0xzz842a5d75249257036d6bde1887874131e77ad1", "eip155:143:" + accountAddr.String()} {
		if r := fetch(t, h, base+"/accounts/"+a); r.Code != http.StatusBadRequest || r.code(t) != problem.CodeInvalidID {
			t.Errorf("%s: %d %s", a, r.Code, r.Body)
		}
	}
	f.accountErr = errors.New("rpc: connection refused")
	if r := fetch(t, h, base+"/accounts/"+accountAddr.String()); r.Code != http.StatusServiceUnavailable || r.code(t) != problem.CodeUpstream {
		t.Errorf("rpc down: %d %s", r.Code, r.Body)
	}
	f.accountErr = context.DeadlineExceeded
	if r := fetch(t, h, base+"/accounts/"+accountAddr.String()); r.Code != http.StatusGatewayTimeout {
		t.Errorf("slow rpc: %d %s", r.Code, r.Body)
	}
}

func TestWalletNotFoundDoesNotEchoTheResolverError(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/accounts/"+unknownAddr.String())
	if strings.Contains(r.Body.String(), "resolve") {
		t.Fatal(r.Body)
	}
}

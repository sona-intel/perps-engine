package server

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"perps/internal/problem"
	"perps/internal/store"
	"perps/internal/testkit"
)

func TestLiquidationFeed(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/liquidations")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if f.filter.Limit != 50 || f.filter.PerpID != nil || f.filter.After != nil {
		t.Fatalf("filter %+v", f.filter)
	}
	items := r.at(t, "items").([]any)
	if len(items) != 5 {
		t.Fatalf("%d items", len(items))
	}
	first := map[string]any{
		"time": "2026-10-12T12:00:00Z", "tx": testkit.Hash(112_000_000_000).String(), "market": "TST", "side": "long",
		"mark": "2534.51", "liq_price": "2501.2", "size": "1.5", "remaining_size": "0", "notional_usd": "3751.8",
		"keeper": testkit.Addr(0).String(), "on_order_book": true,
	}
	for k, v := range first {
		if k == "time" {
			continue
		}
		if r.at(t, "items", 0, k) != v {
			t.Errorf("items[0].%s = %v, want %v", k, r.at(t, "items", 0, k), v)
		}
	}
	near(t, r.at(t, "items", 0, "block"), 112_000_000)
	near(t, r.at(t, "items", 0, "log_index"), 0)
	near(t, r.at(t, "items", 0, "perp_id"), 7)
	near(t, r.at(t, "items", 0, "account_id"), 1000)
	if r.at(t, "items", 1, "side") != "short" {
		t.Fatal(r.at(t, "items", 1))
	}
	absent(t, r, "next_cursor")
}

func TestLiquidationFeedNamesMarketsFromAnySnapshotMarket(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/liquidations")
	if r.at(t, "items", 2, "market") != "99" {
		t.Errorf("unknown perp: %v", r.at(t, "items", 2))
	}
	if r.at(t, "items", 3, "market") != "OLD" {
		t.Errorf("never launched perp: %v", r.at(t, "items", 3))
	}
}

func TestLiquidationFeedKeeperIsNullWhenUnknown(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/liquidations")
	isNull(t, r.at(t, "items", 4, "keeper"))
}

func TestLiquidationFeedTimeIsRFC3339(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/liquidations")
	got, err := time.Parse(time.RFC3339, r.at(t, "items", 0, "time").(string))
	if err != nil || !got.Equal(testkit.Epoch.Add(112_000_000*time.Second)) {
		t.Fatal(got, err)
	}
}

func TestLiquidationFeedPaging(t *testing.T) {
	f := newFake(t)
	h := testHandler(f)
	r := fetch(t, h, base+"/liquidations?limit=2")
	if f.filter.Limit != 2 || len(r.at(t, "items").([]any)) != 2 {
		t.Fatal(f.filter, r.Body)
	}
	cursor, _ := r.at(t, "next_cursor").(string)
	if cursor != "111999990.1" {
		t.Fatalf("cursor %q", cursor)
	}
	r = fetch(t, h, base+"/liquidations?limit=2&cursor="+cursor)
	if f.filter.After == nil || f.filter.After.Block != 111_999_990 || f.filter.After.LogIndex != 1 {
		t.Fatalf("filter %+v", f.filter)
	}
	near(t, r.at(t, "items", 0, "block"), 111_999_980)
	r = fetch(t, h, base+"/liquidations?limit=2&cursor="+r.at(t, "next_cursor").(string))
	if n := len(r.at(t, "items").([]any)); n != 1 {
		t.Fatalf("%d items on the last page", n)
	}
	absent(t, r, "next_cursor")
}

func TestLiquidationFeedFiltersByMarket(t *testing.T) {
	f := newFake(t)
	for _, m := range []string{"tst", "7"} {
		r := fetch(t, testHandler(f), base+"/liquidations?market="+m)
		if f.filter.PerpID == nil || *f.filter.PerpID != 7 {
			t.Fatalf("filter %+v", f.filter)
		}
		if n := len(r.at(t, "items").([]any)); n != 3 {
			t.Fatalf("market %s: %d items", m, n)
		}
	}
	if r := fetch(t, testHandler(f), base+"/liquidations?market=OLD"); r.Code != http.StatusOK || *f.filter.PerpID != 30 {
		t.Fatal(r.Code, r.Body)
	}
}

func TestLiquidationFeedParameterErrors(t *testing.T) {
	h := testHandler(newFake(t))
	for q, want := range map[string]string{
		"limit=0": problem.CodeInvalidQuery, "limit=201": problem.CodeInvalidQuery, "limit=x": problem.CodeInvalidQuery,
		"cursor=zzz": problem.CodeInvalidQuery, "market=bad%21": problem.CodeInvalidQuery, "market=DOGE": problem.CodeNotFound,
		"market=999": problem.CodeNotFound,
	} {
		r := fetch(t, h, base+"/liquidations?"+q)
		if r.code(t) != want {
			t.Errorf("%s: %d %s", q, r.Code, r.Body)
		}
	}
	if r := fetch(t, h, base+"/liquidations?limit=200"); r.Code != http.StatusOK {
		t.Errorf("limit 200: %d", r.Code)
	}
}

func TestDailyIsZeroFilledAndAscending(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/liquidations/daily")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	days := r.at(t, "items").([]any)
	if len(days) != 90 {
		t.Fatalf("%d days", len(days))
	}
	if r.at(t, "items", 0, "day") != "2026-07-15" || r.at(t, "items", 89, "day") != "2026-10-12" {
		t.Fatalf("%v .. %v", r.at(t, "items", 0, "day"), r.at(t, "items", 89, "day"))
	}
	if !f.dailySince.Equal(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)) || f.dailyPerp != nil {
		t.Fatalf("asked since %v perp %v", f.dailySince, f.dailyPerp)
	}
	for i := range 90 {
		day, _ := time.Parse(time.DateOnly, r.at(t, "items", i, "day").(string))
		if want := time.Date(2026, 7, 15+i, 0, 0, 0, 0, time.UTC); !day.Equal(want) {
			t.Fatalf("day %d is %s", i, day)
		}
	}
	near(t, r.at(t, "items", 87, "count"), 2)
	if r.at(t, "items", 87, "notional_usd") != "1500.5" || r.at(t, "items", 88, "notional_usd") != "0" {
		t.Fatal(r.at(t, "items", 87), r.at(t, "items", 88))
	}
	near(t, r.at(t, "items", 88, "count"), 0)
	near(t, r.at(t, "items", 89, "count"), 1)
	if r.at(t, "items", 89, "notional_usd") != "300" {
		t.Fatal(r.at(t, "items", 89))
	}
}

func TestDailyWindowAndMarket(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/liquidations/daily?days=3&market=tst")
	if len(r.at(t, "items").([]any)) != 3 || r.at(t, "items", 0, "day") != "2026-10-10" {
		t.Fatal(r.Body)
	}
	if f.dailyPerp == nil || *f.dailyPerp != 7 || !f.dailySince.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("asked %v %v", f.dailyPerp, f.dailySince)
	}
	r = fetch(t, testHandler(f), base+"/liquidations/daily?days=1")
	if len(r.at(t, "items").([]any)) != 1 || r.at(t, "items", 0, "day") != "2026-10-12" {
		t.Fatal(r.Body)
	}
	r = fetch(t, testHandler(f), base+"/liquidations/daily?days=400")
	if len(r.at(t, "items").([]any)) != 400 {
		t.Fatal(len(r.at(t, "items").([]any)))
	}
}

func TestDailyIgnoresRowsOutsideTheWindow(t *testing.T) {
	f := newFake(t)
	f.daily = append([]store.DailyLiquidations{{Day: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Count: 9, NotionalUSD: "9"}}, f.daily...)
	r := fetch(t, testHandler(f), base+"/liquidations/daily?days=3")
	if len(r.at(t, "items").([]any)) != 3 {
		t.Fatal(r.Body)
	}
	near(t, r.at(t, "items", 0, "count"), 2)
}

func TestDailyParameterErrors(t *testing.T) {
	h := testHandler(newFake(t))
	for _, q := range []string{"days=0", "days=401", "days=x", "days=-1", "market=DOGE", "market=bad%21"} {
		if r := fetch(t, h, base+"/liquidations/daily?"+q); r.Code < 400 || r.Code >= 500 {
			t.Errorf("%s: %d", q, r.Code)
		}
	}
}

func TestFundingSeries(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/funding?market=tst")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if f.fundingPerp != 7 || f.fundingStep != 2*time.Minute || !f.fundingSince.Equal(snapTime.Add(time.Second).Add(-24*time.Hour)) {
		t.Fatalf("asked %d %v %v", f.fundingPerp, f.fundingSince, f.fundingStep)
	}
	near(t, r.at(t, "perp_id"), 7)
	near(t, r.at(t, "hours"), 24)
	near(t, r.at(t, "step_s"), 120)
	if r.at(t, "symbol") != "TST" {
		t.Fatal(r.at(t))
	}
	if n := len(r.at(t, "items").([]any)); n != 3 {
		t.Fatalf("%d points", n)
	}
	near(t, r.at(t, "items", 0, "block"), 100)
	if r.at(t, "items", 0, "mark") != "100" || r.at(t, "items", 0, "oracle") != "100.5" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "gap_bps"), (100-100.5)/100.5*10000)
	near(t, r.at(t, "items", 0, "funding_rate"), 0.00002)
	near(t, r.at(t, "items", 1, "funding_rate"), -0.00004)
	near(t, r.at(t, "items", 1, "gap_bps"), 100.0)
	isNull(t, r.at(t, "items", 2, "gap_bps"))
	near(t, r.at(t, "items", 2, "funding_rate"), 0)
	if r.at(t, "items", 0, "time") != "2026-10-12T11:00:17Z" {
		t.Fatal(r.at(t, "items", 0, "time"))
	}
	if got := r.coverage(t); len(got) != 1 || got[0] != "perpl_funding_from_snapshots" {
		t.Fatal(got)
	}
}

func TestFundingStepGrowsWithTheWindow(t *testing.T) {
	cases := map[int]time.Duration{1: 30 * time.Second, 6: 30 * time.Second, 24: 2 * time.Minute, 100: 510 * time.Second, 168: 14 * time.Minute}
	for hours, want := range cases {
		f := newFake(t)
		r := fetch(t, testHandler(f), base+"/funding?market=ALT&hours="+strconv.Itoa(hours))
		if r.Code != http.StatusOK || f.fundingStep != want {
			t.Errorf("%d hours: step %v, want %v", hours, f.fundingStep, want)
		}
		near(t, r.at(t, "step_s"), want.Seconds())
	}
}

func TestFundingNeedsAMarketAndSaneHours(t *testing.T) {
	h := testHandler(newFake(t))
	for q, want := range map[string]string{
		"": problem.CodeInvalidQuery, "hours=24": problem.CodeInvalidQuery, "market=TST&hours=0": problem.CodeInvalidQuery,
		"market=TST&hours=169": problem.CodeInvalidQuery, "market=TST&hours=x": problem.CodeInvalidQuery, "market=DOGE": problem.CodeNotFound,
	} {
		r := fetch(t, h, base+"/funding?"+q)
		if r.code(t) != want {
			t.Errorf("%q: %d %s", q, r.Code, r.Body)
		}
	}
	if r := fetch(t, h, base+"/funding?market=OLD"); r.Code != http.StatusOK {
		t.Errorf("never launched market: %d", r.Code)
	}
}

func TestLiquidationFeedResolvesAccountAddress(t *testing.T) {
	f := newFake(t)
	r := fetch(t, testHandler(f), base+"/liquidations")
	if got := r.at(t, "items", 0, "account_address"); got != testkit.Addr(1000).String() {
		t.Errorf("items[0].account_address = %v", got)
	}
	isNull(t, r.at(t, "items", 1, "account_address"))
	if len(f.idsAsked) != 2 {
		t.Errorf("asked for %v, want each account once", f.idsAsked)
	}
}

func TestLiquidationFeedFailsWhenAccountReadFails(t *testing.T) {
	f := newFake(t)
	f.errFor = map[string]error{"AccountAddresses": errors.New("dial tcp: connection refused")}
	if r := fetch(t, testHandler(f), base+"/liquidations"); r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code, r.Body)
	}
}

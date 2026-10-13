package server

import (
	"errors"
	"net/http"
	"testing"

	"perps/internal/problem"
	"perps/internal/testkit"
)

func TestMarketsListsLiveMarketsByPerpID(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if n := len(r.at(t, "items").([]any)); n != 2 {
		t.Fatalf("%d markets, the never launched one must stay out", n)
	}
	if r.at(t, "items", 0, "symbol") != "TST" || r.at(t, "items", 1, "symbol") != "ALT" {
		t.Fatal(r.body)
	}
	if r.at(t, "items", 0, "name") != "Test Perp" || r.at(t, "items", 0, "mark") != "100" || r.at(t, "items", 0, "oracle") != "100.5" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "perp_id"), 7)
	near(t, r.at(t, "items", 0, "status"), 4)
	near(t, r.at(t, "items", 0, "oracle_gap_bps"), (100-100.5)/100.5*10000)
	near(t, r.at(t, "items", 0, "oracle_age_s"), 20)
	if r.at(t, "items", 0, "long_oi", "size") != "15" || r.at(t, "items", 0, "long_oi", "usd") != "1500" || r.at(t, "items", 0, "short_oi", "size") != "15" || r.at(t, "items", 0, "short_oi", "usd") != "1500" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "positions"), 4)
	if r.at(t, "items", 0, "insurance_fund_usd") != "25" {
		t.Fatal(r.at(t, "items", 0, "insurance_fund_usd"))
	}
	near(t, r.at(t, "items", 0, "funding_rate"), 0.00002)
	near(t, r.at(t, "items", 0, "max_leverage"), 10)
	near(t, r.at(t, "items", 0, "maint_margin_fraction"), 0.05)
}

func TestMarketsShockCoverRatioAtTenPercent(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets")
	near(t, r.at(t, "items", 0, "scr_m10"), 2.5)
	near(t, r.at(t, "items", 0, "scr_p10"), 1.25)
	if r.at(t, "items", 0, "no_bad_debt_m10") != false || r.at(t, "items", 0, "no_bad_debt_p10") != false {
		t.Fatal(r.at(t, "items", 0))
	}
	isNull(t, r.at(t, "items", 1, "scr_m10"))
	isNull(t, r.at(t, "items", 1, "scr_p10"))
	if r.at(t, "items", 1, "no_bad_debt_m10") != true || r.at(t, "items", 1, "no_bad_debt_p10") != true {
		t.Fatal(r.at(t, "items", 1))
	}
	near(t, r.at(t, "items", 1, "oracle_gap_bps"), 0)
	near(t, r.at(t, "items", 1, "funding_rate"), -0.00004)
	near(t, r.at(t, "items", 1, "max_leverage"), 15)
	near(t, r.at(t, "items", 1, "maint_margin_fraction"), 0.04)
}

func TestMarketsCarryCoverageCodes(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets")
	got := r.coverage(t)
	if len(got) != 2 || got[0] != "perpl_snapshot_only" || got[1] != "perpl_bad_debt_is_floor" {
		t.Fatal(got)
	}
}

func TestMarketsWithAnEmptyBookStillAnswer(t *testing.T) {
	f := newFake(t)
	f.snap.Books[1].Positions = nil
	r := fetch(t, testHandler(f), base+"/markets")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	near(t, r.at(t, "items", 1, "positions"), 0)
	if r.at(t, "items", 1, "no_bad_debt_m10") != true {
		t.Fatal(r.at(t, "items", 1))
	}
}

func TestOracleGapIsNullWithoutAnOracle(t *testing.T) {
	f := newFake(t)
	f.snap.Books[0].Market.OraclePNS.SetInt64(0)
	r := fetch(t, testHandler(f), base+"/markets")
	isNull(t, r.at(t, "items", 0, "oracle_gap_bps"))
	if r.at(t, "items", 0, "oracle") != "0" {
		t.Fatal(r.at(t, "items", 0, "oracle"))
	}
}

func TestOracleAgeNeverGoesNegative(t *testing.T) {
	f := newFake(t)
	f.snap.Books[0].Market.OracleTimestampSec = uint64(snapTime.Unix()) + 30
	near(t, fetch(t, testHandler(f), base+"/markets").at(t, "items", 0, "oracle_age_s"), 0)
}

func TestMarketDetail(t *testing.T) {
	h := testHandler(newFake(t))
	for _, id := range []string{"TST", "tst", "7", "Tst"} {
		r := fetch(t, h, base+"/markets/"+id)
		if r.Code != http.StatusOK || r.at(t, "symbol") != "TST" {
			t.Fatalf("%s: %d %s", id, r.Code, r.Body)
		}
	}
	r := fetch(t, h, base+"/markets/TST")
	near(t, r.at(t, "initial_margin_fraction"), 0.1)
	near(t, r.at(t, "max_leverage"), 10)
	near(t, r.at(t, "maint_margin_fraction"), 0.05)
	near(t, r.at(t, "funding_interval"), 8571)
	near(t, r.at(t, "funding_rate_clamp"), 0.0001)
	near(t, r.at(t, "ref_price_max_age_s"), 60)
	near(t, r.at(t, "liquidation", "insurance"), 0.1)
	near(t, r.at(t, "liquidation", "user"), 0.8)
	near(t, r.at(t, "liquidation", "protocol"), 0.1)
	near(t, r.at(t, "liquidation", "btl", "price_threshold"), 0.95)
	for _, k := range []string{"insurance", "user", "buyer", "protocol"} {
		near(t, r.at(t, "liquidation", "btl", k), 0.25)
	}
	if r.at(t, "liquidation", "btl", "restrict_buyers") != true {
		t.Fatal(r.at(t, "liquidation"))
	}
	near(t, r.at(t, "scr_m10"), 2.5)
	near(t, r.at(t, "scr_p10"), 1.25)
	if r.at(t, "mark") != "100" {
		t.Fatal(r.at(t))
	}
}

func TestMarketErrors(t *testing.T) {
	h := testHandler(newFake(t))
	cases := map[string]struct {
		status int
		code   string
	}{
		"DOGE": {http.StatusNotFound, problem.CodeNotFound},
		"999":  {http.StatusNotFound, problem.CodeNotFound},
		"OLD":  {http.StatusNotFound, problem.CodeNotFound},
		"30":   {http.StatusNotFound, problem.CodeNotFound},
		"b%21": {http.StatusBadRequest, problem.CodeInvalidID},
	}
	for id, want := range cases {
		for _, suffix := range []string{"", "/liquidation-map", "/stress"} {
			r := fetch(t, h, base+"/markets/"+id+suffix)
			if r.Code != want.status || r.code(t) != want.code {
				t.Errorf("%s%s: %d %s", id, suffix, r.Code, r.Body)
			}
		}
	}
}

func TestLiquidationMapDefaults(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/TST/liquidation-map")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	near(t, r.at(t, "perp_id"), 7)
	if r.at(t, "symbol") != "TST" || r.at(t, "mark") != "100" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "bucket_bps"), 25)
	near(t, r.at(t, "range_pct"), 20)
	buckets := r.at(t, "buckets").([]any)
	if len(buckets) != 160 {
		t.Fatalf("%d buckets", len(buckets))
	}
	byFrom := map[string]map[string]any{}
	for _, b := range buckets {
		m := b.(map[string]any)
		byFrom[m["price_from"].(string)] = m
	}
	for from, want := range map[string]struct {
		to, long, short string
		la, sa          float64
	}{
		"95":  {"95.25", "1000", "0", 1, 0},
		"97":  {"97.25", "500", "0", 1, 0},
		"101": {"101.25", "0", "500", 0, 1},
		"105": {"105.25", "0", "1000", 0, 1},
		"80":  {"80.25", "0", "0", 0, 0},
	} {
		b := byFrom[from]
		if b == nil {
			t.Errorf("no bucket from %s", from)
			continue
		}
		if b["price_to"] != want.to || b["long_notional_usd"] != want.long || b["short_notional_usd"] != want.short ||
			b["long_accounts"] != want.la || b["short_accounts"] != want.sa {
			t.Errorf("bucket %s: %v", from, b)
		}
	}
	if r.at(t, "beyond_range", "long_notional_usd") != "0" || r.at(t, "beyond_range", "short_notional_usd") != "0" {
		t.Fatal(r.at(t, "beyond_range"))
	}
	near(t, r.at(t, "beyond_range", "long_positions"), 0)
	near(t, r.at(t, "beyond_range", "short_positions"), 0)
	if n := len(r.at(t, "cumulative", "long").([]any)); n != 20 {
		t.Fatalf("%d long points", n)
	}
	for _, c := range []struct {
		side string
		idx  int
		not  string
		pos  float64
	}{
		{"long", 0, "0", 0},
		{"long", 2, "500", 1},
		{"long", 4, "1500", 2},
		{"long", 19, "1500", 2},
		{"short", 0, "500", 1},
		{"short", 2, "500", 1},
		{"short", 4, "1500", 2},
		{"short", 19, "1500", 2},
	} {
		if r.at(t, "cumulative", c.side, c.idx, "notional_usd") != c.not {
			t.Errorf("%s %d: %v", c.side, c.idx, r.at(t, "cumulative", c.side, c.idx))
		}
		near(t, r.at(t, "cumulative", c.side, c.idx, "positions"), c.pos)
		near(t, r.at(t, "cumulative", c.side, c.idx, "move_pct"), float64(c.idx+1))
	}
}

func TestLiquidationMapCustomRangeCountsPositionsBeyondIt(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/7/liquidation-map?bucket_bps=25&range_pct=3")
	near(t, r.at(t, "range_pct"), 3)
	if n := len(r.at(t, "buckets").([]any)); n != 24 {
		t.Fatalf("%d buckets", n)
	}
	if r.at(t, "beyond_range", "long_notional_usd") != "1000" || r.at(t, "beyond_range", "short_notional_usd") != "1000" {
		t.Fatal(r.at(t, "beyond_range"))
	}
	near(t, r.at(t, "beyond_range", "long_positions"), 1)
	near(t, r.at(t, "beyond_range", "short_positions"), 1)
	if n := len(r.at(t, "cumulative", "short").([]any)); n != 3 {
		t.Fatalf("%d points", n)
	}
	r = fetch(t, testHandler(newFake(t)), base+"/markets/ALT/liquidation-map?bucket_bps=500&range_pct=10")
	if n := len(r.at(t, "buckets").([]any)); n != 4 || r.at(t, "mark") != "50" {
		t.Fatal(n, r.at(t, "mark"))
	}
}

func TestLiquidationMapParameterBounds(t *testing.T) {
	h := testHandler(newFake(t))
	for _, q := range []string{"bucket_bps=4", "bucket_bps=501", "bucket_bps=abc", "bucket_bps=0", "range_pct=0", "range_pct=51", "range_pct=-1", "range_pct=2.5", "bucket_bps=25&range_pct=x"} {
		r := fetch(t, h, base+"/markets/TST/liquidation-map?"+q)
		if r.Code != http.StatusBadRequest || r.code(t) != problem.CodeInvalidQuery {
			t.Errorf("%s: %d %s", q, r.Code, r.Body)
		}
	}
	for _, q := range []string{"bucket_bps=5", "bucket_bps=500", "range_pct=1", "range_pct=50"} {
		if r := fetch(t, h, base+"/markets/TST/liquidation-map?"+q); r.Code != http.StatusOK {
			t.Errorf("%s: %d %s", q, r.Code, r.Body)
		}
	}
}

func TestLiquidationMapCoverage(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/TST/liquidation-map")
	if got := r.coverage(t); len(got) != 1 || got[0] != "perpl_snapshot_only" {
		t.Fatal(got)
	}
}

func TestStressDefaultShockIsMinusTenPercent(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/TST/stress")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	near(t, r.at(t, "shock"), -0.1)
	near(t, r.at(t, "perp_id"), 7)
	if r.at(t, "symbol") != "TST" || r.at(t, "p0") != "100" || r.at(t, "p1") != "90" {
		t.Fatal(r.at(t))
	}
	if r.at(t, "liquidating", "notional_usd") != "1350" {
		t.Fatal(r.at(t, "liquidating"))
	}
	near(t, r.at(t, "liquidating", "positions"), 2)
	near(t, r.at(t, "liquidating", "accounts"), 2)
	if r.at(t, "gap_bad_debt_usd") != "10" || r.at(t, "insurance_fund_usd") != "25" || r.at(t, "uncovered_usd") != "0" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "scr"), 2.5)
	if r.at(t, "no_bad_debt") != false {
		t.Fatal(r.at(t))
	}
	top := r.at(t, "top_positions").([]any)
	if len(top) != 2 {
		t.Fatal(top)
	}
	want := map[string]any{
		"side": "long", "size": "10", "notional_usd": "1000", "liq_price": "95", "bankruptcy_price": "90", "bad_debt_usd": "0",
	}
	for k, v := range want {
		if r.at(t, "top_positions", 0, k) != v {
			t.Errorf("top[0].%s = %v", k, r.at(t, "top_positions", 0, k))
		}
	}
	near(t, r.at(t, "top_positions", 0, "account_id"), 101)
	near(t, r.at(t, "top_positions", 1, "account_id"), 103)
	if got := r.at(t, "top_positions", 0, "account_address"); got != accountAddr.String() {
		t.Errorf("top[0].account_address = %v", got)
	}
	if got := r.at(t, "top_positions", 1, "account_address"); got != testkit.Addr(103).String() {
		t.Errorf("top[1].account_address = %v", got)
	}
	if r.at(t, "top_positions", 1, "bad_debt_usd") != "10" {
		t.Fatal(r.at(t, "top_positions", 1))
	}
	if got := r.coverage(t); len(got) != 2 || got[0] != "perpl_snapshot_only" || got[1] != "perpl_bad_debt_is_floor" {
		t.Fatal(got)
	}
}

func TestStressDeeperDropLeavesUncoveredLoss(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/tst/stress?shock=-0.15")
	if r.at(t, "p1") != "85" || r.at(t, "gap_bad_debt_usd") != "85" || r.at(t, "uncovered_usd") != "60" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "scr"), 25.0/85.0)
	near(t, r.at(t, "liquidating", "positions"), 2)
	if r.at(t, "liquidating", "notional_usd") != "1275" {
		t.Fatal(r.at(t, "liquidating"))
	}
}

func TestStressUpMoveHitsShorts(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/TST/stress?shock=0.15")
	if r.at(t, "p1") != "115" || r.at(t, "gap_bad_debt_usd") != "95" || r.at(t, "uncovered_usd") != "70" || r.at(t, "liquidating", "notional_usd") != "1725" {
		t.Fatal(r.at(t))
	}
	if r.at(t, "top_positions", 0, "side") != "short" {
		t.Fatal(r.at(t, "top_positions"))
	}
}

func TestStressWithoutBadDebtHasNullCover(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/markets/ALT/stress?shock=-0.1")
	isNull(t, r.at(t, "scr"))
	if r.at(t, "no_bad_debt") != true || r.at(t, "gap_bad_debt_usd") != "0" || r.at(t, "uncovered_usd") != "0" {
		t.Fatal(r.at(t))
	}
	r = fetch(t, testHandler(newFake(t)), base+"/markets/ALT/stress?shock=0.01")
	near(t, r.at(t, "liquidating", "positions"), 0)
	if n := len(r.at(t, "top_positions").([]any)); n != 0 {
		t.Fatal(n)
	}
}

func TestStressShockBounds(t *testing.T) {
	h := testHandler(newFake(t))
	for _, route := range []string{"/markets/TST/stress", "/stress"} {
		for _, q := range []string{"shock=0", "shock=0.51", "shock=-0.51", "shock=abc", "shock=NaN", "shock=1e-1", "shock=-20"} {
			r := fetch(t, h, base+route+"?"+q)
			if r.Code != http.StatusBadRequest || r.code(t) != problem.CodeInvalidQuery {
				t.Errorf("%s?%s: %d %s", route, q, r.Code, r.Body)
			}
		}
		for _, q := range []string{"shock=0.5", "shock=-0.5", "shock=0.001"} {
			if r := fetch(t, h, base+route+"?"+q); r.Code != http.StatusOK {
				t.Errorf("%s?%s: %d %s", route, q, r.Code, r.Body)
			}
		}
	}
}

func TestStressAllSummarisesEveryLiveMarket(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/stress?shock=-0.15")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if n := len(r.at(t, "items").([]any)); n != 2 {
		t.Fatalf("%d markets", n)
	}
	if r.at(t, "items", 0, "symbol") != "TST" || r.at(t, "items", 1, "symbol") != "ALT" {
		t.Fatal(r.at(t, "items"))
	}
	if r.at(t, "items", 0, "gap_bad_debt_usd") != "85" || r.at(t, "items", 1, "p1") != "42.5" {
		t.Fatal(r.at(t, "items"))
	}
	near(t, r.at(t, "items", 0, "shock"), -0.15)
	def := fetch(t, testHandler(newFake(t)), base+"/stress")
	near(t, def.at(t, "items", 0, "shock"), -0.1)
}

func TestStressTopPositionAddressIsNullWhenUnknown(t *testing.T) {
	f := newFake(t)
	delete(f.addrs, 103)
	r := fetch(t, testHandler(f), base+"/markets/TST/stress?shock=-0.15")
	isNull(t, r.at(t, "top_positions", 1, "account_address"))
	if all := fetch(t, testHandler(f), base+"/stress?shock=-0.15"); all.Code != http.StatusOK {
		t.Fatal(all.Code)
	}
}

func TestStressFailsWhenAccountReadFails(t *testing.T) {
	for _, path := range []string{"/markets/TST/stress", "/stress"} {
		f := newFake(t)
		f.errFor = map[string]error{"AccountAddresses": errors.New("dial tcp: connection refused")}
		if r := fetch(t, testHandler(f), base+path); r.Code != http.StatusServiceUnavailable {
			t.Fatal(path, r.Code, r.Body)
		}
	}
}

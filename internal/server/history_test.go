package server

import (
	"errors"
	"math/big"
	"net/http"
	"slices"
	"testing"
	"time"
)

func historyURL(market, query string) string {
	return base + "/markets/" + market + "/history?" + query
}

func TestHistoryVolumeServesTheRecordedCandles(t *testing.T) {
	f := fixtureSource(t)
	r := fetch(t, testHandler(f), historyURL("BTC", "metric=volume&hours=24"))
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if r.at(t, "kind") != "perpl_market_history" || r.at(t, "chain") != "eip155:143" || r.at(t, "market") != "BTC" || r.at(t, "metric") != "volume" {
		t.Fatal(r.body)
	}
	near(t, r.at(t, "step_s"), 3600)
	if n := len(r.at(t, "items").([]any)); n != 24 {
		t.Fatalf("%d items", n)
	}
	// the first recorded candle: t 1791831600000, v 338723232260 at six decimals, n 905
	if r.at(t, "items", 0, "t") != "2026-10-12T19:00:00Z" || r.at(t, "items", 0, "volume_usd") != "338723.23226" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "trades"), 905)
	if f.histMarket != 1 || f.histHours != 24 {
		t.Fatalf("asked market %d for %d hours", f.histMarket, f.histHours)
	}
	if got := r.coverage(t); len(got) != 0 {
		t.Fatalf("coverage %v", got)
	}
	if r.Header().Get("Sona-Block") == "" {
		t.Fatalf("headers %v", r.Header())
	}
}

func TestHistoryHoursDefaultToAWeek(t *testing.T) {
	f := fixtureSource(t)
	if r := fetch(t, testHandler(f), historyURL("btc", "metric=volume")); r.Code != http.StatusOK || f.histHours != 168 {
		t.Fatal(r.Code, f.histHours)
	}
	for _, hours := range []int{24, 168, 720} {
		if r := fetch(t, testHandler(f), historyURL("1", "metric=funding&hours="+itoa(uint64(hours)))); r.Code != http.StatusOK || f.histHours != hours {
			t.Fatal(r.Code, hours, f.histHours)
		}
	}
}

func TestHistoryFundingServesTheRecordedEvents(t *testing.T) {
	r := fetch(t, testHandler(fixtureSource(t)), historyURL("BTC", "metric=funding&hours=24"))
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if r.at(t, "metric") != "funding" || len(r.at(t, "items").([]any)) != 33 {
		t.Fatal(r.body)
	}
	// the first recorded event: t 1791832988000, rate -10 millionths
	if r.at(t, "items", 0, "t") != "2026-10-12T19:23:08Z" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "rate"), -0.00001)
	if _, has := r.at(t, "items", 0).(map[string]any)["rate_8h_pct"]; has {
		t.Fatal("rate_8h_pct is not defined by Perpl")
	}
	near(t, r.at(t, "step_s"), 2588)
}

func TestHistoryOpenInterestAsksForAThinnedWindow(t *testing.T) {
	cases := map[int]time.Duration{24: 5 * time.Minute, 168: 30 * time.Minute, 720: 2 * time.Hour}
	for hours, step := range cases {
		f := fixtureSource(t)
		r := fetch(t, testHandler(f), historyURL("BTC", "metric=oi&hours="+itoa(uint64(hours))))
		if r.Code != http.StatusOK {
			t.Fatal(r.Code, r.Body)
		}
		near(t, r.at(t, "step_s"), step.Seconds())
		wantSince := snapTime.Add(time.Second).Add(-time.Duration(hours) * time.Hour)
		if f.oiMarket != 1 || f.oiStep != step || !f.oiSince.Equal(wantSince) {
			t.Fatalf("asked market %d since %v step %v", f.oiMarket, f.oiSince, f.oiStep)
		}
		if len(r.at(t, "items").([]any)) != 3 || r.at(t, "items", 2, "t") != "2026-10-12T14:20:00Z" {
			t.Fatal(r.at(t, "items"))
		}
		btc, err := resolveMarket("BTC", f.snap.Books)
		if err != nil {
			t.Fatal(err)
		}
		near(t, r.at(t, "items", 0, "positions"), float64(len(btc.Positions)))
		if got := r.coverage(t); !slices.Equal(got, []string{"perpl_oi_from_snapshots"}) {
			t.Fatalf("coverage %v", got)
		}
	}
}

func TestHistoryOpenInterestOfAStaleSnapshotSaysSo(t *testing.T) {
	f := fixtureSource(t)
	f.snap.TakenAt = snapTime
	r := fetch(t, staleHandler(f, snapTime.Add(time.Hour), time.Minute), historyURL("BTC", "metric=oi"))
	if got := r.coverage(t); !slices.Equal(got, []string{"perpl_oi_from_snapshots", "perpl_snapshot_stale"}) {
		t.Fatalf("coverage %v", got)
	}
}

func TestHistoryEmptyWindowsAreEmptyArrays(t *testing.T) {
	f := fixtureSource(t)
	f.oi, f.candles, f.events = nil, nil, nil
	for _, metric := range []string{"oi", "volume", "funding"} {
		r := fetch(t, testHandler(f), historyURL("BTC", "metric="+metric))
		if r.Code != http.StatusOK || len(r.at(t, "items").([]any)) != 0 {
			t.Fatal(metric, r.Code, r.body)
		}
	}
	near(t, fetch(t, testHandler(f), historyURL("BTC", "metric=funding")).at(t, "step_s"), 0)
}

func TestHistoryRejectsBadRequests(t *testing.T) {
	h := testHandler(fixtureSource(t))
	for name, path := range map[string]string{
		"no metric":       historyURL("BTC", "hours=24"),
		"unknown metric":  historyURL("BTC", "metric=price"),
		"hours 48":        historyURL("BTC", "metric=oi&hours=48"),
		"hours 0":         historyURL("BTC", "metric=oi&hours=0"),
		"hours not a num": historyURL("BTC", "metric=oi&hours=abc"),
		"unknown param":   historyURL("BTC", "metric=oi&step=1"),
		"repeated metric": historyURL("BTC", "metric=oi&metric=volume"),
	} {
		if r := fetch(t, h, path); r.Code != http.StatusBadRequest || r.code(t) != "invalid_query" {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if r := fetch(t, h, historyURL("BT%21", "metric=oi")); r.Code != http.StatusBadRequest || r.code(t) != "invalid_id" {
		t.Errorf("bad market: %d %s", r.Code, r.Body)
	}
	if r := fetch(t, h, historyURL("DOGE", "metric=oi")); r.Code != http.StatusNotFound || r.code(t) != "not_found" {
		t.Errorf("unknown market: %d %s", r.Code, r.Body)
	}
}

func TestHistoryUpstreamFailuresAre503(t *testing.T) {
	for _, metric := range []string{"volume", "funding", "oi"} {
		f := fixtureSource(t)
		f.err = errors.New("perpl rest: upstream answered HTTP 429")
		r := fetch(t, testHandler(f), historyURL("BTC", "metric="+metric))
		if r.Code != http.StatusServiceUnavailable || r.code(t) != "upstream_unavailable" {
			t.Errorf("%s: %d %s", metric, r.Code, r.Body)
		}
	}
	f := newFake(t)
	f.snap = nil
	if r := fetch(t, testHandler(f), historyURL("BTC", "metric=oi")); r.Code != http.StatusServiceUnavailable {
		t.Errorf("no snapshot: %d", r.Code)
	}
}

func shareOf(row, total int64) float64 {
	f, _ := new(big.Rat).SetFrac64(row, total).Float64()
	return f
}

func TestLeaderboardDefaultsToTheAllTimePnlBoard(t *testing.T) {
	r := fetch(t, testHandler(fixtureSource(t)), base+"/leaderboard")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if r.at(t, "kind") != "perpl_leaderboard" || r.at(t, "by") != "pnl" || r.at(t, "period") != "all" || r.at(t, "chain") != "eip155:143" {
		t.Fatal(r.body)
	}
	if len(r.at(t, "items").([]any)) != 25 {
		t.Fatalf("%d items", len(r.at(t, "items").([]any)))
	}
	// the recorded all-time PnL leader: p 181234390058, v 168783097290863 at six decimals
	if r.at(t, "items", 0, "address") != "0xc8d79f44912a9f55c6faf819283efcea9661d1dc" || r.at(t, "items", 0, "pnl_usd") != "181234.390058" ||
		r.at(t, "items", 0, "volume_usd") != "168783097.290863" {
		t.Fatal(r.at(t, "items", 0))
	}
	near(t, r.at(t, "items", 0, "rank"), 1)
	if r.at(t, "as_of") != "2026-10-13T18:35:09Z" {
		t.Fatal(r.at(t, "as_of"))
	}
	if got := r.coverage(t); !slices.Equal(got, []string{"perpl_leaderboard_figures"}) {
		t.Fatalf("coverage %v", got)
	}
}

func TestLeaderboardSharesAreOverTheWholeBoard(t *testing.T) {
	f := fixtureSource(t)
	lb := f.pnlBoard["all"]
	total := lb.TotalVolumeCNS().Int64()
	r := fetch(t, testHandler(f), base+"/leaderboard?by=pnl&period=all&limit=1")
	near(t, r.at(t, "items", 0, "volume_share"), shareOf(lb.Rows[0].VolumeCNS.Int64(), total))
	if len(r.at(t, "items").([]any)) != 1 {
		t.Fatal(r.body)
	}
}

func TestLeaderboardByVolumeAndDay(t *testing.T) {
	f := fixtureSource(t)
	r := fetch(t, testHandler(f), base+"/leaderboard?by=volume&period=day&limit=100")
	if r.Code != http.StatusOK || r.at(t, "by") != "volume" || r.at(t, "period") != "day" {
		t.Fatal(r.Code, r.Body)
	}
	near(t, r.at(t, "items", 0, "volume_share"), shareOf(f.boards["day"].Rows[0].VolumeCNS.Int64(), f.boards["day"].TotalVolumeCNS().Int64()))
	r = fetch(t, testHandler(f), base+"/leaderboard?by=pnl&period=day&limit=3")
	if r.Code != http.StatusOK || r.at(t, "by") != "pnl" || r.at(t, "period") != "day" || len(r.at(t, "items").([]any)) != 3 {
		t.Fatal(r.Code, r.Body)
	}
	// the recorded day PnL leader: p 1038561027
	if r.at(t, "items", 0, "pnl_usd") != "1038.561027" {
		t.Fatal(r.at(t, "items", 0))
	}
}

func TestLeaderboardLimitsTheRowsToTheBoard(t *testing.T) {
	f := fixtureSource(t)
	rows := f.pnlBoard["day"].Rows
	r := fetch(t, testHandler(f), base+"/leaderboard?by=pnl&period=day&limit=100")
	if n := len(r.at(t, "items").([]any)); n != len(rows) {
		t.Fatalf("%d items for %d rows", n, len(rows))
	}
}

func TestLeaderboardRejectsBadRequests(t *testing.T) {
	h := testHandler(fixtureSource(t))
	for name, path := range map[string]string{
		"by":         base + "/leaderboard?by=rank",
		"period":     base + "/leaderboard?period=week",
		"limit 0":    base + "/leaderboard?limit=0",
		"limit 101":  base + "/leaderboard?limit=101",
		"unknown":    base + "/leaderboard?market=BTC",
		"repeated":   base + "/leaderboard?by=pnl&by=volume",
		"empty by":   base + "/leaderboard?by=",
		"limit text": base + "/leaderboard?limit=ten",
	} {
		if r := fetch(t, h, path); r.Code != http.StatusBadRequest || r.code(t) != "invalid_query" {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
}

func TestLeaderboardWithoutABoardIsUnavailable(t *testing.T) {
	f := fixtureSource(t)
	clear(f.pnlBoard)
	r := fetch(t, testHandler(f), base+"/leaderboard?by=pnl&period=all")
	if r.Code != http.StatusServiceUnavailable || r.code(t) != "upstream_unavailable" || r.Header().Get("Retry-After") != "5" {
		t.Fatal(r.Code, r.Body, r.Header())
	}
	f = newFake(t)
	f.snap = nil
	if r := fetch(t, testHandler(f), base+"/leaderboard"); r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code)
	}
}

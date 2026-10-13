package server

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"perps/internal/sources"
	"perps/internal/store"
)

// recordedAt is the moment the candle and funding samples under testdata/rest were taken
var recordedAt = time.UnixMilli(1791916815784)

// recordedREST replays the samples of testdata/rest through the real clients
func recordedREST(t testing.TB) (*sources.REST, *sources.MarketData) {
	t.Helper()
	files := map[string]string{
		"/trading/leaderboard/all/pnl": "leaderboard_all_pnl.json", "/trading/leaderboard/day/pnl": "leaderboard_day_pnl.json",
		"/market-data/1/candles/3600/": "candles_btc_1h.json", "/market-data/1/funding/": "funding_btc.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, name := range files {
			if strings.HasPrefix(r.URL.Path, "/api/v1"+prefix) {
				raw, err := os.ReadFile(filepath.Join(testdata, "rest", name))
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(raw)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	rest, err := sources.NewREST(srv.URL + "/api/v1/trading/leaderboard")
	if err != nil {
		t.Fatal(err)
	}
	md, err := sources.NewMarketData(srv.URL+"/api/v1/market-data", sources.WithMarketDataClock(
		func() time.Time { return recordedAt }, func(context.Context, time.Duration) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	return rest, md
}

// recordedHistory fills the fake with the recorded boards, candles and funding events, and with open interest points taken from the snapshot
func recordedHistory(t testing.TB, f *fakeSource) {
	t.Helper()
	rest, md := recordedREST(t)
	f.pnlBoard = map[sources.Period]sources.Leaderboard{}
	for _, p := range []sources.Period{sources.PeriodAll, sources.PeriodDay} {
		lb, err := rest.Board(context.Background(), p, sources.ByPnl)
		if err != nil {
			t.Fatal(err)
		}
		f.pnlBoard[p] = lb
	}
	var err error
	if f.candles, err = md.Candles(context.Background(), 1, 24); err != nil {
		t.Fatal(err)
	}
	if f.events, err = md.Funding(context.Background(), 1, 24); err != nil {
		t.Fatal(err)
	}
	btc, err := resolveMarket("BTC", f.snap.Books)
	if err != nil {
		t.Fatal(err)
	}
	long := new(big.Rat)
	for _, a := range btc.Positions {
		if a.Long {
			long.Add(long, a.Notional)
		}
	}
	for i := range 3 {
		f.oi = append(f.oi, store.OIPoint{
			Market: 1, Time: f.snap.Time.Truncate(5 * time.Minute).Add(time.Duration(i-2) * 5 * time.Minute),
			OpenInterestUSD: usdRat(long), Positions: len(btc.Positions),
		})
	}
}

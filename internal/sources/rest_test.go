package sources_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/sources"
)

const leaderboardPath = "/api/v1/trading/leaderboard"

func newREST(t *testing.T, h http.Handler, opts ...sources.RESTOption) *sources.REST {
	t.Helper()
	srv := serve(t, h)
	c, err := sources.NewREST(srv.URL+leaderboardPath, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func bigOf(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return n
}

func TestLeaderboardDayDecodesTheRecordedSample(t *testing.T) {
	var path string
	c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		replay(fixture(t, "rest/leaderboard_day_vol.json"))(w, r)
	}))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodDay)
	if err != nil {
		t.Fatal(err)
	}
	if path != leaderboardPath+"/day/vol" {
		t.Fatalf("requested %q", path)
	}
	if len(lb.Rows) != 50 || lb.At.Block != 112775687 || !lb.At.Time.Equal(time.UnixMilli(1791814413000)) {
		t.Fatalf("%d rows, at %+v", len(lb.Rows), lb.At)
	}
	want := sources.LeaderboardRow{
		Rank: 1, Address: mustAddr(t, "0x0cae9940b85079ef776946bc48ec2b1b1d625812"), PnlCNS: bigOf(t, "-943059763"), VolumeCNS: bigOf(t, "8716023344081"),
	}
	got := lb.Rows[0]
	if got.Rank != want.Rank || got.Address != want.Address || got.PnlCNS.Cmp(want.PnlCNS) != 0 || got.VolumeCNS.Cmp(want.VolumeCNS) != 0 {
		t.Fatalf("row 0 = %+v, want %+v", got, want)
	}
	for i, r := range lb.Rows {
		if r.Rank != i+1 {
			t.Fatalf("row %d has rank %d", i, r.Rank)
		}
	}
}

func TestLeaderboardAllUsesItsOwnPath(t *testing.T) {
	var path string
	c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		replay(fixture(t, "rest/leaderboard_all_vol.json"))(w, r)
	}))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodAll)
	if err != nil {
		t.Fatal(err)
	}
	if path != leaderboardPath+"/all/vol" || lb.At.Block != 112775692 || lb.Rows[0].VolumeCNS.Cmp(bigOf(t, "828269239974034")) != 0 {
		t.Fatalf("path %q, at %+v, first row %+v", path, lb.At, lb.Rows[0])
	}
}

func TestLeaderboardTotalVolumeSumsEveryRow(t *testing.T) {
	raw := fixture(t, "rest/leaderboard_day_vol.json")
	var generic struct {
		D []map[string]any `json:"d"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	want := new(big.Int)
	for _, row := range generic.D {
		want.Add(want, bigOf(t, row["v"].(string)))
	}
	c := newREST(t, replay(raw))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodDay)
	if err != nil {
		t.Fatal(err)
	}
	if got := lb.TotalVolumeCNS(); got.Cmp(want) != 0 || got.Sign() <= 0 {
		t.Fatalf("total %v, want %v", got, want)
	}
	if (sources.Leaderboard{}).TotalVolumeCNS().Sign() != 0 {
		t.Fatal("an empty leaderboard must total zero")
	}
}

func TestLeaderboardKeepsValuesBeyondInt64Exact(t *testing.T) {
	body := `{"at":{"b":5,"t":1000},"d":[{"i":1,"a":"0x0000000000000000000000000000000000000001","p":"-123456789012345678901234567890","v":123456789012345678901234567890}]}`
	c := newREST(t, replay([]byte(body)))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodDay)
	if err != nil {
		t.Fatal(err)
	}
	if lb.Rows[0].PnlCNS.String() != "-123456789012345678901234567890" || lb.Rows[0].VolumeCNS.String() != "123456789012345678901234567890" {
		t.Fatalf("row = %+v", lb.Rows[0])
	}
}

func TestLeaderboardAcceptsEmptyRows(t *testing.T) {
	c := newREST(t, replay([]byte(`{"at":{"b":5,"t":1000},"d":[]}`)))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodDay)
	if err != nil || len(lb.Rows) != 0 {
		t.Fatalf("got %+v, %v", lb, err)
	}
}

func TestLeaderboardRejectsMalformedBodies(t *testing.T) {
	const addr = "0x0000000000000000000000000000000000000001"
	cases := map[string]string{
		"not json":         `<html>blocked</html>`,
		"empty body":       ``,
		"no rows key":      `{"at":{"b":5,"t":1000}}`,
		"null rows":        `{"at":{"b":5,"t":1000},"d":null}`,
		"short address":    `{"d":[{"i":1,"a":"0x12","p":"1","v":"2"}]}`,
		"no 0x":            `{"d":[{"i":1,"a":"0000000000000000000000000000000000000001","p":"1","v":"2"}]}`,
		"missing volume":   `{"d":[{"i":1,"a":"` + addr + `","p":"1"}]}`,
		"missing pnl":      `{"d":[{"i":1,"a":"` + addr + `","v":"1"}]}`,
		"volume not int":   `{"d":[{"i":1,"a":"` + addr + `","p":"1","v":"1.5"}]}`,
		"volume text":      `{"d":[{"i":1,"a":"` + addr + `","p":"1","v":"lots"}]}`,
		"volume exponent":  `{"d":[{"i":1,"a":"` + addr + `","p":"1","v":1e3}]}`,
		"volume null":      `{"d":[{"i":1,"a":"` + addr + `","p":"1","v":null}]}`,
		"rank wrong type":  `{"d":[{"i":"first","a":"` + addr + `","p":"1","v":"1"}]}`,
		"rows wrong shape": `{"d":{"i":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := newREST(t, replay([]byte(body)))
			if lb, err := c.Leaderboard(t.Context(), sources.PeriodDay); err == nil {
				t.Fatalf("accepted %q as %+v", body, lb)
			}
		})
	}
}

func TestLeaderboardIgnoresFieldsItDoesNotKnow(t *testing.T) {
	body := `{"at":{"b":5,"t":1000,"x":1},"extra":true,"d":[{"i":1,"a":"0x0000000000000000000000000000000000000001","p":"1","r":-5,"v":"2","s":"-9","new":[1]}]}`
	c := newREST(t, replay([]byte(body)))
	lb, err := c.Leaderboard(t.Context(), sources.PeriodDay)
	if err != nil || len(lb.Rows) != 1 {
		t.Fatalf("got %+v, %v", lb, err)
	}
}

func TestLeaderboardRejectsAnUnknownPeriodWithoutARequest(t *testing.T) {
	hits := 0
	c := newREST(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	for _, p := range []sources.Period{"", "week", "day/../all", "all?x=1"} {
		if _, err := c.Leaderboard(t.Context(), p); err == nil {
			t.Errorf("period %q was accepted", p)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests were sent", hits)
	}
}

func TestTickerDecodesTheRecordedSample(t *testing.T) {
	var path string
	c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		replay(fixture(t, "rest/ticker.json"))(w, r)
	}))
	tk, err := c.Ticker(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/market-data/ticker" {
		t.Fatalf("requested %q", path)
	}
	if tk.Block != 112775696 || len(tk.Markets) != 11 {
		t.Fatalf("block %d, %d markets", tk.Block, len(tk.Markets))
	}
	btc, ok := tk.Markets[1]
	if !ok {
		t.Fatal("no BTC (perp 1) in the ticker")
	}
	checks := map[string]struct {
		got  *big.Int
		want string
	}{
		"oracle": {btc.OraclePNS, "828863"}, "mark": {btc.MarkPNS, "829121"}, "last": {btc.LastPNS, "828726"}, "mid": {btc.MidPNS, "828683"},
		"bid": {btc.BidPNS, "828571"}, "ask": {btc.AskPNS, "828795"}, "prev": {btc.PrevPNS, "830774"}, "oi": {btc.OpenInterestLNS, "827372"},
		"day volume lots": {btc.DayVolumeLNS, "25126201"}, "day volume cns": {btc.DayVolumeCNS, "20986278965037"},
	}
	for name, c := range checks {
		if c.got == nil || c.got.String() != c.want {
			t.Errorf("%s = %v, want %s", name, c.got, c.want)
		}
	}
	if btc.At.Block != 112775696 || !btc.At.Time.Equal(time.UnixMilli(1791814416000)) {
		t.Fatalf("btc at %+v", btc.At)
	}
	for _, id := range []uint64{10, 20, 31, 40, 50, 60, 70, 90, 100, 110} {
		if tk.Markets[id].MarkPNS == nil {
			t.Errorf("perp %d has no mark", id)
		}
	}
}

func TestTickerKeepsOptionalFieldsNilWhenAbsent(t *testing.T) {
	body := `{"sn":9,"d":{"7":{"at":{"b":9,"t":2000},"orl":10,"mrk":11}}}`
	c := newREST(t, replay([]byte(body)))
	tk, err := c.Ticker(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := tk.Markets[7]
	if m.OraclePNS.Int64() != 10 || m.MarkPNS.Int64() != 11 || m.BidPNS != nil || m.AskPNS != nil || m.DayVolumeCNS != nil {
		t.Fatalf("market = %+v", m)
	}
}

func TestTickerRejectsMalformedBodies(t *testing.T) {
	cases := map[string]string{
		"not json":      `nope`,
		"no markets":    `{"sn":9}`,
		"bad market id": `{"sn":9,"d":{"btc":{"at":{"b":9,"t":2000},"orl":10,"mrk":11}}}`,
		"negative id":   `{"sn":9,"d":{"-1":{"at":{"b":9,"t":2000},"orl":10,"mrk":11}}}`,
		"no mark":       `{"sn":9,"d":{"1":{"at":{"b":9,"t":2000},"orl":10}}}`,
		"no oracle":     `{"sn":9,"d":{"1":{"at":{"b":9,"t":2000},"mrk":10}}}`,
		"mark text":     `{"sn":9,"d":{"1":{"at":{"b":9,"t":2000},"orl":10,"mrk":"high"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := newREST(t, replay([]byte(body)))
			if tk, err := c.Ticker(t.Context()); err == nil {
				t.Fatalf("accepted %q as %+v", body, tk)
			}
		})
	}
}

func TestTickerURLIsTheSiblingOfTheLeaderboardURL(t *testing.T) {
	var path string
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		replay([]byte(`{"sn":1,"d":{}}`))(w, r)
	}))
	for _, base := range []string{srv.URL + leaderboardPath, srv.URL + leaderboardPath + "/"} {
		c, err := sources.NewREST(base)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Ticker(t.Context()); err != nil {
			t.Fatal(err)
		}
		if path != "/api/v1/market-data/ticker" {
			t.Fatalf("base %q requested %q", base, path)
		}
	}
}

func TestTickerURLCanBeSetAndIsRequiredWhenNotDerivable(t *testing.T) {
	var path string
	srv := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		replay([]byte(`{"sn":1,"d":{}}`))(w, r)
	}))
	c, err := sources.NewREST(srv.URL+"/custom/board", sources.WithTickerURL(srv.URL+"/custom/ticker"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ticker(t.Context()); err != nil || path != "/custom/ticker" {
		t.Fatalf("path %q, err %v", path, err)
	}
	path = ""
	c, err = sources.NewREST(srv.URL + "/custom/board")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ticker(t.Context()); err == nil || path != "" {
		t.Fatalf("ticker without a URL gave err %v and hit %q", err, path)
	}
}

func TestNewRESTRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "ftp://example.com/x", "://nope", "example.com/x", "https://user:pw@example.com/x", "https:///x"} {
		if _, err := sources.NewREST(u); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	if _, err := sources.NewREST("https://example.com/x", sources.WithTickerURL("ftp://example.com/t")); err == nil {
		t.Error("a bad ticker URL was accepted")
	}
}

func TestRESTSendsAUserAgentAndNoCredentials(t *testing.T) {
	var header http.Header
	c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		replay([]byte(`{"d":[]}`))(w, r)
	}))
	if _, err := c.Leaderboard(t.Context(), sources.PeriodDay); err != nil {
		t.Fatal(err)
	}
	if ua := header.Get("User-Agent"); ua == "" || strings.HasPrefix(ua, "Go-http-client") {
		t.Fatalf("User-Agent %q", ua)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if header.Get(h) != "" {
			t.Errorf("header %s was sent", h)
		}
	}
}

func TestRESTStatusErrorsCarryTheStatusAndRetryAfter(t *testing.T) {
	cases := []struct {
		status int
		after  string
		want   time.Duration
	}{{http.StatusNotFound, "", 0}, {http.StatusTooManyRequests, "7", 7 * time.Second}, {http.StatusInternalServerError, "", 0}, {http.StatusServiceUnavailable, "garbage", 0}}
	for _, tc := range cases {
		c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if tc.after != "" {
				w.Header().Set("Retry-After", tc.after)
			}
			http.Error(w, "secret-looking body", tc.status)
		}))
		_, err := c.Leaderboard(t.Context(), sources.PeriodDay)
		var se *sources.StatusError
		if !errors.As(err, &se) || se.Status != tc.status || se.RetryAfter != tc.want {
			t.Errorf("status %d gave %v", tc.status, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-looking") {
			t.Errorf("status %d error echoes the body: %v", tc.status, err)
		}
	}
}

func TestRESTRejectsABodyBeyondTheSizeLimit(t *testing.T) {
	huge := []byte(`{"d":[],"pad":"` + strings.Repeat("a", 17<<20) + `"}`)
	c := newREST(t, replay(huge))
	if _, err := c.Leaderboard(t.Context(), sources.PeriodDay); err == nil {
		t.Fatal("a 17 MiB body was accepted")
	}
}

func TestRESTStopsWhenTheContextIsCancelled(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	c := newREST(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Leaderboard(ctx, sources.PeriodDay)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func mustAddr(t *testing.T, s string) perpl.Address {
	t.Helper()
	a, err := perpl.ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLeaderboardDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	sink := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		replay(fixture(t, "rest/leaderboard_day_vol.json"))(w, r)
	}))
	c := newREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+r.URL.Path, http.StatusFound)
	}))
	if _, err := c.Leaderboard(t.Context(), sources.PeriodDay); err == nil {
		t.Fatal("a redirect was followed")
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect target was called")
	}
}

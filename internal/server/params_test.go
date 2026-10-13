package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/problem"
	"perps/internal/risk"
	"perps/internal/store"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var he *httpError
	if !errors.As(err, &he) {
		t.Fatalf("error %v is no httpError", err)
	}
	return he.code
}

func query(raw string) url.Values {
	q, err := url.ParseQuery(raw)
	if err != nil {
		panic(err)
	}
	return q
}

func TestCheckChain(t *testing.T) {
	if err := checkChain("eip155:143"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"eip155:1", "eip155:0143", "eip155:144", "solana:5eykt4UsFv8P8NJdTREpY1vzqKqZKvdp"} {
		if got := codeOf(t, checkChain(s)); got != problem.CodeUnsupportedChain {
			t.Errorf("%q gives %s", s, got)
		}
	}
	// anything that is no CAIP-2 id is a malformed identifier and not an unsupported chain
	for _, s := range []string{"EIP155:143", "foo", "abc", "", "eip155:143 ", "eip155:143/x", "eip155:", ":143", "eip155:143:1", "a:1", "eip155:" + strings.Repeat("1", 33)} {
		if got := codeOf(t, checkChain(s)); got != problem.CodeInvalidID {
			t.Errorf("%q gives %s", s, got)
		}
	}
}

func TestParseQuery(t *testing.T) {
	get := func(raw string) *http.Request {
		return httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x?"+raw, nil)
	}
	if q, err := parseQuery(get("a=1&b=2"), "a", "b"); err != nil || q.Get("a") != "1" || q.Get("b") != "2" {
		t.Fatal(q, err)
	}
	if q, err := parseQuery(get(""), "a"); err != nil || len(q) != 0 {
		t.Fatal(q, err)
	}
	for _, raw := range []string{"c=1", "a=1&a=2", "a=", "a", "a=%zz", "a=1&c=2"} {
		_, err := parseQuery(get(raw), "a", "b")
		if got := codeOf(t, err); got != problem.CodeInvalidQuery {
			t.Errorf("%q gives %s", raw, got)
		}
	}
}

func TestParseQueryNamesUnknownParameterWithoutEchoingAllOfIt(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x?"+strings.Repeat("z", 200)+"=1", nil)
	_, err := parseQuery(req)
	var he *httpError
	if !errors.As(err, &he) || len(he.detail) > 100 {
		t.Fatalf("detail %q", he.detail)
	}
}

func TestParseShock(t *testing.T) {
	good := map[string]float64{
		"": -0.1, "shock=-0.2": -0.2, "shock=0.05": 0.05, "shock=%2B0.5": 0.5, "shock=-0.5": -0.5, "shock=0.0001": 0.0001,
		"shock=-.25": -0.25, "shock=0.15": 0.15,
	}
	for raw, want := range good {
		got, err := parseShock(query(raw))
		if err != nil || got != want {
			t.Errorf("%q gives %v, %v", raw, got, err)
		}
	}
	bad := []string{
		"shock=0", "shock=0.0", "shock=-0", "shock=0.51", "shock=-0.51", "shock=1", "shock=-20", "shock=abc", "shock=NaN", "shock=Inf",
		"shock=-Inf", "shock=1e-1", "shock=0x1p-2", "shock=.", "shock=-", "shock=--0.1", "shock=0.1.2", "shock=0,1", "shock=0.1%25",
		"shock=" + strings.Repeat("0", 40) + "1", "shock=0." + strings.Repeat("0", 30) + "1",
	}
	for _, raw := range bad {
		_, err := parseShock(query(raw))
		if err == nil || codeOf(t, err) != problem.CodeInvalidQuery {
			t.Errorf("%q gives %v", raw, err)
		}
	}
}

func TestIntParam(t *testing.T) {
	good := map[string]int{"": 25, "n=5": 5, "n=500": 500, "n=25": 25, "n=0100": 100}
	for raw, want := range good {
		got, err := intParam(query(raw), "n", 25, 5, 500)
		if err != nil || got != want {
			t.Errorf("%q gives %d, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"n=4", "n=501", "n=0", "n=-5", "n=+5", "n=2.5", "n=abc", "n=1e2", "n=0x10", "n=99999999999999999999", "n=5 ", "n=%205"} {
		_, err := intParam(query(raw), "n", 25, 5, 500)
		if err == nil || codeOf(t, err) != problem.CodeInvalidQuery {
			t.Errorf("%q gives %v", raw, err)
		}
	}
}

func TestIntParamDetailNamesTheBounds(t *testing.T) {
	_, err := intParam(query("bucket_bps=9999"), "bucket_bps", 25, 5, 500)
	var he *httpError
	if !errors.As(err, &he) || !strings.Contains(he.detail, "bucket_bps") || !strings.Contains(he.detail, "5") || !strings.Contains(he.detail, "500") {
		t.Fatalf("detail %v", err)
	}
}

func TestParseCursor(t *testing.T) {
	got, err := parseCursor(query("cursor=123.4"))
	if err != nil || got == nil || *got != (store.LiquidationCursor{Block: 123, LogIndex: 4}) {
		t.Fatal(got, err)
	}
	if got, err := parseCursor(query("")); err != nil || got != nil {
		t.Fatal(got, err)
	}
	for _, raw := range []string{"cursor=abc", "cursor=1", "cursor=1.2.3", "cursor=.1", "cursor=1.", "cursor=-1.2", "cursor=1.-2", "cursor=1e3.1", "cursor=99999999999999999999.1"} {
		_, err := parseCursor(query(raw))
		if err == nil || codeOf(t, err) != problem.CodeInvalidQuery {
			t.Errorf("%q gives %v", raw, err)
		}
	}
}

func TestParseAddress(t *testing.T) {
	want, _ := perpl.ParseAddress("0x7e842a5d75249257036d6bde1887874131e77ad1")
	for _, s := range []string{"0x7e842a5d75249257036d6bde1887874131e77ad1", "0x7E842A5D75249257036D6BDE1887874131E77AD1", "0x7e842a5D75249257036d6bde1887874131e77aD1"} {
		got, err := parseAddress(s)
		if err != nil || got != want {
			t.Errorf("%s gives %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "7e842a5d75249257036d6bde1887874131e77ad1", "0x7e842a5d75249257036d6bde1887874131e77ad", "0x7e842a5d75249257036d6bde1887874131e77ad11", "0xzz842a5d75249257036d6bde1887874131e77ad1", "0X7e842a5d75249257036d6bde1887874131e77ad1", "eip155:143:0x7e842a5d75249257036d6bde1887874131e77ad1"} {
		_, err := parseAddress(s)
		if err == nil || codeOf(t, err) != problem.CodeInvalidID {
			t.Errorf("%q gives %v", s, err)
		}
	}
}

func marketBooks(symbols ...string) []risk.MarketBook {
	ids := map[string]uint64{"BTC": 1, "MON": 10, "ETH": 20, "SOL_v2": 31}
	var out []risk.MarketBook
	for _, s := range symbols {
		out = append(out, risk.MarketBook{Market: perpl.Market{PerpID: ids[s], Symbol: s}})
	}
	return out
}

func TestResolveMarket(t *testing.T) {
	books := marketBooks("BTC", "MON", "ETH", "SOL_v2")
	good := map[string]uint64{"BTC": 1, "btc": 1, "Btc": 1, "1": 1, "01": 1, "MON": 10, "10": 10, "SOL_v2": 31, "sol_v2": 31, "SOL_V2": 31, "31": 31, "eth": 20}
	for s, id := range good {
		b, err := resolveMarket(s, books)
		if err != nil || b.Market.PerpID != id {
			t.Errorf("%q gives %d, %v", s, b.Market.PerpID, err)
		}
	}
	for _, s := range []string{"DOGE", "2", "999", "SOL", "99999999999999999999999"} {
		if _, err := resolveMarket(s, books); err == nil || codeOf(t, err) != problem.CodeNotFound {
			t.Errorf("%q gives %v", s, err)
		}
	}
	for _, s := range []string{"", "BTC Perp", "BT!", "a/b", strings.Repeat("A", 33), "bt\x00c", "é"} {
		if _, err := resolveMarket(s, books); err == nil || codeOf(t, err) != problem.CodeInvalidID {
			t.Errorf("%q gives %v", s, err)
		}
	}
}

func TestResolveMarketOptionalQueryValue(t *testing.T) {
	books := marketBooks("BTC", "MON")
	if b, err := marketFilter(query(""), books); err != nil || b != nil {
		t.Fatal(b, err)
	}
	b, err := marketFilter(query("market=mon"), books)
	if err != nil || b == nil || b.Market.PerpID != 10 {
		t.Fatal(b, err)
	}
	if _, err := marketFilter(query("market=zzz"), books); err == nil || codeOf(t, err) != problem.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := marketFilter(query("market=b%21"), books); err == nil || codeOf(t, err) != problem.CodeInvalidQuery {
		t.Fatal(err)
	}
}

func TestDeadline(t *testing.T) {
	cases := map[string]time.Duration{
		"": 5 * time.Second, "abc": 5 * time.Second, "1.5": 5 * time.Second, "400": 400 * time.Millisecond, "0": 0,
		"-5": -5 * time.Millisecond, "99999999999": time.Hour, "-99999999999": -time.Hour, "10000": 10 * time.Second,
	}
	for raw, want := range cases {
		h := http.Header{}
		if raw != "" {
			h.Set("Sona-Deadline-Ms", raw)
		}
		if got := deadline(h); got != want {
			t.Errorf("%q gives %v, want %v", raw, got, want)
		}
	}
}

func TestHTTPErrorText(t *testing.T) {
	e := &httpError{code: "x", detail: "y"}
	if e.Error() != "x: y" {
		t.Fatal(e.Error())
	}
}

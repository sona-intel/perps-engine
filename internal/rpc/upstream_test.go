package rpc

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"perps/internal/perpl"
)

const (
	wantHeader = "X-ERPC-Use-Upstream"
	paidFilter = "!drpc-*"
)

var (
	zeroHash64 = "0x" + strings.Repeat("0", 64)
	zeroAddr40 = "0x" + strings.Repeat("0", 40)
)

// headerNode answers every method with a valid result and records the headers of each request
func headerNode(t *testing.T, fail func(n int) bool) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []http.Header
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, hr *http.Request) {
		mu.Lock()
		seen = append(seen, hr.Header.Clone())
		n := len(seen)
		mu.Unlock()
		if fail != nil && fail(n) {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(hr.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply(w, req.ID, resultFor(req), nil)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(seen)
	}
}

func resultFor(r request) any {
	switch r.Method {
	case "eth_call":
		return "0x"
	case "eth_blockNumber":
		return "0x5"
	case "eth_getBlockByNumber":
		return map[string]string{"number": "0x5", "hash": zeroHash64, "timestamp": "0x1"}
	case "eth_getStorageAt":
		return zeroHash64
	case "eth_getTransactionByHash":
		return map[string]string{"hash": zeroHash64, "from": zeroAddr40}
	case "eth_getLogs":
		return []any{}
	}
	return nil
}

func TestUpstreamFilterOnEveryMethod(t *testing.T) {
	srv, headers := headerNode(t, nil)
	c := newClient(t, srv.URL, WithUpstreamFilter(paidFilter))
	ctx := context.Background()
	var addr perpl.Address
	var hash perpl.Hash
	steps := map[string]func() error{
		"eth_call":                 func() error { _, err := c.Call(ctx, addr, []byte{0x82, 0xad, 0xa0, 0x81}, 7); return err },
		"eth_blockNumber":          func() error { _, err := c.BlockNumber(ctx); return err },
		"eth_getBlockByNumber":     func() error { _, err := c.BlockByNumber(ctx, 5); return err },
		"eth_getStorageAt":         func() error { _, err := c.StorageAt(ctx, addr, hash, 5); return err },
		"eth_getTransactionByHash": func() error { _, err := c.TransactionByHash(ctx, hash); return err },
		"eth_getLogs":              func() error { _, err := c.Logs(ctx, addr, hash, 0, 4); return err },
	}
	for method, step := range steps {
		before := len(headers())
		if err := step(); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		got := headers()
		if len(got) != before+1 {
			t.Fatalf("%s sent %d requests, want 1", method, len(got)-before)
		}
		if v := got[before].Values(wantHeader); len(v) != 1 || v[0] != paidFilter {
			t.Errorf("%s carried %q, want one %q", method, v, paidFilter)
		}
	}
}

func TestUpstreamFilterOnEveryRetryAttempt(t *testing.T) {
	srv, headers := headerNode(t, func(n int) bool { return n < 3 })
	c := newClient(t, srv.URL, WithUpstreamFilter(paidFilter))
	if _, err := c.BlockNumber(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := headers()
	if len(got) != 3 {
		t.Fatalf("%d attempts, want 3", len(got))
	}
	for i, h := range got {
		if h.Get(wantHeader) != paidFilter {
			t.Errorf("attempt %d carried %q", i, h.Get(wantHeader))
		}
	}
}

func TestUpstreamFilterOnEveryLogWindow(t *testing.T) {
	srv, log := node(t, func(r request) (any, *Error) {
		f := r.param(t, 0).(map[string]any)
		from, to := hexNumberOf(t, f["fromBlock"].(string)), hexNumberOf(t, f["toBlock"].(string))
		if to-from+1 > 250 {
			return nil, &Error{Code: CodeRangeTooLarge, Message: "query exceeds max results"}
		}
		return []any{}, nil
	})
	c := newClient(t, srv.URL, WithUpstreamFilter(paidFilter))
	if _, err := c.Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 0, 2599); err != nil {
		t.Fatal(err)
	}
	if len(log.headers) < 10 {
		t.Fatalf("only %d windows, the range should have been split and halved", len(log.headers))
	}
	for i, h := range log.headers {
		if h.Get(wantHeader) != paidFilter {
			t.Errorf("window %d carried %q", i, h.Get(wantHeader))
		}
	}
}

func TestNoUpstreamHeaderWithoutAFilter(t *testing.T) {
	for name, opts := range map[string][]Option{
		"no option":    nil,
		"empty filter": {WithUpstreamFilter("")},
	} {
		t.Run(name, func(t *testing.T) {
			srv, log := node(t, func(r request) (any, *Error) { return "0x5", nil })
			c := newClient(t, srv.URL, opts...)
			if _, err := c.BlockNumber(context.Background()); err != nil {
				t.Fatal(err)
			}
			if v := log.headers[0].Values(wantHeader); len(v) != 0 {
				t.Fatalf("header sent as %q", v)
			}
		})
	}
}

func TestUpstreamFilterIsSentVerbatim(t *testing.T) {
	const pattern = "monad-*|!drpc-?"
	srv, log := node(t, func(r request) (any, *Error) { return "0x5", nil })
	c := newClient(t, srv.URL, WithUpstreamFilter(pattern))
	if _, err := c.BlockNumber(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.headers[0].Get(wantHeader); got != pattern {
		t.Fatalf("sent %q, want %q", got, pattern)
	}
}

func TestUpstreamFilterValidation(t *testing.T) {
	good := []string{"", "!drpc-*", "drpc", "a|b", strings.Repeat("a", MaxUpstreamFilter)}
	for _, v := range good {
		if !ValidUpstreamFilter(v) {
			t.Errorf("%q rejected", v)
		}
		if _, err := New("http://router.invalid", WithUpstreamFilter(v)); err != nil {
			t.Errorf("option rejected %q: %v", v, err)
		}
	}
	bad := map[string]string{
		"carriage return":  "!drpc-*\r",
		"line feed":        "!drpc-*\n",
		"header injection": "!drpc-*\r\nX-ERPC-Use-Upstream: drpc",
		"tab":              "a\tb",
		"space":            "a b",
		"leading space":    " !drpc-*",
		"trailing space":   "!drpc-* ",
		"nul":              "a\x00b",
		"delete":           "a\x7fb",
		"non ascii":        "déjà",
		"too long":         strings.Repeat("a", MaxUpstreamFilter+1),
	}
	for name, v := range bad {
		if ValidUpstreamFilter(v) {
			t.Errorf("%s accepted by ValidUpstreamFilter", name)
		}
		if _, err := New("http://router.invalid", WithUpstreamFilter(v)); err == nil {
			t.Errorf("%s accepted by the option", name)
		}
	}
}

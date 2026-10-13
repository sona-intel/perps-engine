package rpc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"perps/internal/perpl"
)

const exchange = "0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"

type callFixture struct {
	Function string `json:"function"`
	Block    uint64 `json:"block"`
	Request  struct {
		To   string `json:"to"`
		Data string `json:"data"`
	} `json:"request"`
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

func ethCallNames(t *testing.T) []string {
	t.Helper()
	var m struct {
		Calls []struct {
			Name     string `json:"name"`
			Function string `json:"function"`
		} `json:"calls"`
	}
	readJSON(t, fixturePath("manifest.json"), &m)
	var names []string
	for _, c := range m.Calls {
		if strings.HasPrefix(c.Function, "get") {
			names = append(names, c.Name)
		}
	}
	if len(names) < 100 {
		t.Fatalf("only %d call fixtures", len(names))
	}
	return names
}

/* Fixture replay through the real wire format */

func TestCallReplaysEveryFixture(t *testing.T) {
	fixtures := map[string]callFixture{}
	for _, name := range ethCallNames(t) {
		var fx callFixture
		readJSON(t, fixturePath("calls", name+".json"), &fx)
		fixtures[name] = fx
	}
	byData := map[string]callFixture{}
	for _, fx := range fixtures {
		byData[strings.ToLower(fx.Request.Data)+"@"+hexBlock(fx.Block)] = fx
	}

	srv, _ := node(t, func(r request) (any, *Error) {
		if r.Method != "eth_call" {
			t.Errorf("method %s", r.Method)
		}
		call := r.param(t, 0).(map[string]any)
		if got := call["to"].(string); got != strings.ToLower(exchange) {
			t.Errorf("to %s", got)
		}
		key := call["data"].(string) + "@" + r.param(t, 1).(string)
		fx, ok := byData[key]
		if !ok {
			t.Errorf("unknown call %s", key)
			return nil, &Error{Code: -32000, Message: "unknown"}
		}
		if fx.Error != nil {
			return nil, &Error{Code: fx.Error.Code, Message: fx.Error.Message, Data: fx.Error.Data}
		}
		return fx.Result, nil
	})
	c := newClient(t, srv.URL)
	to := mustAddress(t, exchange)

	for name, fx := range fixtures {
		t.Run(name, func(t *testing.T) {
			got, err := c.Call(context.Background(), to, mustHex(t, fx.Request.Data), fx.Block)
			if fx.Error != nil {
				var rpcErr *Error
				if !errors.As(err, &rpcErr) || rpcErr.Code != fx.Error.Code || rpcErr.Data != fx.Error.Data {
					t.Fatalf("err %v", err)
				}
				if !IsRevert(err) {
					t.Fatal("revert not recognised")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if "0x"+hexOf(got) != fx.Result {
				t.Fatalf("result differs for %s", name)
			}
		})
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&15])
	}
	return string(out)
}

func hexBlock(n uint64) string { return quantity(n) }

func TestCallDecodesWithPerpl(t *testing.T) {
	var fx callFixture
	readJSON(t, fixturePath("calls", "getPerpetualInfoV2.perp1.json"), &fx)
	srv, _ := node(t, func(r request) (any, *Error) { return fx.Result, nil })
	got, err := newClient(t, srv.URL).Call(context.Background(), mustAddress(t, exchange), perpl.EncodeGetPerpetualInfoV2(1), fx.Block)
	if err != nil {
		t.Fatal(err)
	}
	m, err := perpl.DecodeMarket(got)
	if err != nil || m.Symbol != "BTC" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestCallRequestShape(t *testing.T) {
	srv, log := node(t, func(r request) (any, *Error) { return "0x", nil })
	c := newClient(t, srv.URL)
	got, err := c.Call(context.Background(), mustAddress(t, exchange), []byte{0xde, 0xad}, 255)
	if err != nil || len(got) != 0 {
		t.Fatalf("%x %v", got, err)
	}
	req := log.at(0)
	if req.JSONRPC != "2.0" || req.Method != "eth_call" || len(req.Params) != 2 {
		t.Fatalf("%+v", req)
	}
	call := req.param(t, 0).(map[string]any)
	if call["data"] != "0xdead" || call["to"] != strings.ToLower(exchange) || req.param(t, 1) != "0xff" {
		t.Fatalf("%v %v", call, req.Params[1])
	}
	h := log.headers[0]
	if h.Get("Content-Type") != "application/json" || h.Get("User-Agent") == "" {
		t.Fatalf("headers %v", h)
	}
}

func TestIDsAreUniquePerCall(t *testing.T) {
	srv, log := node(t, func(r request) (any, *Error) { return "0x1", nil })
	c := newClient(t, srv.URL)
	for range 3 {
		if _, err := c.BlockNumber(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for i := range 3 {
		seen[string(log.at(i).ID)] = true
	}
	if len(seen) != 3 {
		t.Fatalf("ids %v", seen)
	}
}

/* Simple methods */

func TestBlockNumber(t *testing.T) {
	srv, log := node(t, func(r request) (any, *Error) { return "0x6b8d891", nil })
	n, err := newClient(t, srv.URL).BlockNumber(context.Background())
	if err != nil || n != 112777361 {
		t.Fatalf("%d %v", n, err)
	}
	if log.at(0).Method != "eth_blockNumber" || len(log.at(0).Params) != 0 {
		t.Fatalf("%+v", log.at(0))
	}
}

func TestQuantityParsing(t *testing.T) {
	for _, bad := range []string{"", "0x", "6b8d891", "0xzz", "0x10000000000000000", `"0x1"`} {
		srv, _ := node(t, func(r request) (any, *Error) { return bad, nil })
		if n, err := newClient(t, srv.URL).BlockNumber(context.Background()); err == nil {
			t.Errorf("%q gave %d", bad, n)
		}
	}
	srv, _ := node(t, func(r request) (any, *Error) { return "0xffffffffffffffff", nil })
	if n, err := newClient(t, srv.URL).BlockNumber(context.Background()); err != nil || n != 1<<64-1 {
		t.Fatalf("%d %v", n, err)
	}
	if got := quantity(0); got != "0x0" {
		t.Fatalf("quantity(0) = %s", got)
	}
}

func TestBlockByNumber(t *testing.T) {
	var fx struct {
		Result map[string]any `json:"result"`
	}
	readJSON(t, fixturePath("calls", "eth_getBlockByNumber.pinned.json"), &fx)
	var chain struct {
		Block     uint64 `json:"pinned_block"`
		Hash      string `json:"pinned_block_hash"`
		Timestamp int64  `json:"pinned_block_timestamp"`
	}
	readJSON(t, fixturePath("chain.json"), &chain)

	srv, log := node(t, func(r request) (any, *Error) { return fx.Result, nil })
	b, err := newClient(t, srv.URL).BlockByNumber(context.Background(), chain.Block)
	if err != nil {
		t.Fatal(err)
	}
	if b.Number != chain.Block || b.Hash.String() != chain.Hash || b.Time.Unix() != chain.Timestamp || b.Time.Location() != time.UTC {
		t.Fatalf("%+v", b)
	}
	req := log.at(0)
	if req.Method != "eth_getBlockByNumber" || req.param(t, 0) != "0x6b8d88c" || req.param(t, 1) != false {
		t.Fatalf("%+v", req)
	}
}

func TestBlockByNumberNotFoundAndMismatch(t *testing.T) {
	srv, _ := node(t, func(r request) (any, *Error) { return nil, nil })
	if _, err := newClient(t, srv.URL).BlockByNumber(context.Background(), 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("null block gave %v", err)
	}
	srv, _ = node(t, func(r request) (any, *Error) {
		return map[string]any{"number": "0x6", "hash": strings.Repeat("0x00", 1), "timestamp": "0x1"}, nil
	})
	if _, err := newClient(t, srv.URL).BlockByNumber(context.Background(), 5); err == nil {
		t.Fatal("malformed block accepted")
	}
	srv, _ = node(t, func(r request) (any, *Error) {
		return map[string]any{"number": "0x6", "hash": "0x" + strings.Repeat("11", 32), "timestamp": "0x1"}, nil
	})
	if _, err := newClient(t, srv.URL).BlockByNumber(context.Background(), 5); err == nil || !strings.Contains(err.Error(), "block 6") {
		t.Fatalf("number mismatch gave %v", err)
	}
}

func TestStorageAt(t *testing.T) {
	var fx struct {
		Block   uint64 `json:"block"`
		Request struct {
			Address string `json:"address"`
			Slot    string `json:"slot"`
		} `json:"request"`
		Result string `json:"result"`
	}
	readJSON(t, fixturePath("calls", "getStorageAt.implementation.json"), &fx)
	srv, log := node(t, func(r request) (any, *Error) { return fx.Result, nil })
	got, err := newClient(t, srv.URL).StorageAt(context.Background(), mustAddress(t, fx.Request.Address), perpl.ImplementationSlot, fx.Block)
	if err != nil {
		t.Fatal(err)
	}
	impl, err := perpl.AddressFromWord(got[:])
	if err != nil || impl.String() != "0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a" {
		t.Fatalf("%s %v", impl, err)
	}
	req := log.at(0)
	if req.Method != "eth_getStorageAt" || req.param(t, 0) != strings.ToLower(fx.Request.Address) || req.param(t, 1) != fx.Request.Slot || req.param(t, 2) != hexBlock(fx.Block) {
		t.Fatalf("%+v", req)
	}
}

func TestStorageAtRejectsShortWord(t *testing.T) {
	srv, _ := node(t, func(r request) (any, *Error) { return "0x1234", nil })
	if _, err := newClient(t, srv.URL).StorageAt(context.Background(), perpl.Address{}, perpl.Hash{}, 1); err == nil {
		t.Fatal("short slot accepted")
	}
}

func TestTransactionByHash(t *testing.T) {
	var txs map[string]map[string]any
	readJSON(t, fixturePath("liquidations", "transactions.raw.json"), &txs)
	var rows []struct {
		TransactionHash string `json:"transactionHash"`
		TransactionFrom string `json:"transactionFrom"`
		BlockNumber     string `json:"blockNumber"`
	}
	readJSON(t, fixturePath("liquidations", "expected.json"), &rows)

	srv, log := node(t, func(r request) (any, *Error) { return txs[r.param(t, 0).(string)], nil })
	c := newClient(t, srv.URL)
	for _, row := range rows {
		tx, err := c.TransactionByHash(context.Background(), mustHash(t, row.TransactionHash))
		if err != nil {
			t.Fatal(err)
		}
		if tx.Hash.String() != row.TransactionHash || tx.From.String() != row.TransactionFrom || strings.ToLower(row.BlockNumber) == "" {
			t.Fatalf("%+v vs %+v", tx, row)
		}
	}
	if log.at(0).Method != "eth_getTransactionByHash" {
		t.Fatal(log.at(0).Method)
	}
}

func TestTransactionByHashNullAndBad(t *testing.T) {
	srv, _ := node(t, func(r request) (any, *Error) { return nil, nil })
	if _, err := newClient(t, srv.URL).TransactionByHash(context.Background(), perpl.Hash{1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	for _, bad := range []map[string]any{
		{"hash": "0x" + strings.Repeat("01", 32)},
		{"hash": "0x" + strings.Repeat("02", 32), "from": "0x" + strings.Repeat("aa", 32)},
	} {
		srv, _ = node(t, func(r request) (any, *Error) { return bad, nil })
		if _, err := newClient(t, srv.URL).TransactionByHash(context.Background(), perpl.Hash{1}); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	srv, _ = node(t, func(r request) (any, *Error) {
		return map[string]any{"hash": "0x" + strings.Repeat("02", 32), "from": "0x" + strings.Repeat("aa", 20)}, nil
	})
	if _, err := newClient(t, srv.URL).TransactionByHash(context.Background(), perpl.Hash{1}); err == nil {
		t.Fatal("hash mismatch accepted")
	}
}

/* Logs */

func TestLogsDecodeFixtures(t *testing.T) {
	var batches []struct {
		Params []map[string]any `json:"params"`
		Result []map[string]any `json:"result"`
	}
	readJSON(t, fixturePath("liquidations", "getlogs.raw.json"), &batches)
	var rows []struct {
		BlockNumber string         `json:"blockNumber"`
		LogIndex    string         `json:"logIndex"`
		Event       map[string]any `json:"event"`
	}
	readJSON(t, fixturePath("liquidations", "expected.json"), &rows)

	byRange := map[string][]map[string]any{}
	for _, b := range batches {
		byRange[b.Params[0]["fromBlock"].(string)] = b.Result
	}
	srv, log := node(t, func(r request) (any, *Error) {
		f := r.param(t, 0).(map[string]any)
		if f["fromBlock"] != f["toBlock"] {
			t.Errorf("window %v-%v", f["fromBlock"], f["toBlock"])
		}
		return byRange[f["fromBlock"].(string)], nil
	})
	c := newClient(t, srv.URL)
	addr := mustAddress(t, exchange)
	total := 0
	for _, b := range batches {
		block := hexNumberOf(t, b.Params[0]["fromBlock"].(string))
		logs, err := c.Logs(context.Background(), addr, perpl.TopicPositionLiquidated, block, block)
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) != len(b.Result) {
			t.Fatalf("block %d: %d logs, want %d", block, len(logs), len(b.Result))
		}
		for _, l := range logs {
			ev, err := perpl.DecodeLiquidation(l.Topics, l.Data, l.Block, l.LogIndex, l.TxHash)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			for _, r := range rows {
				if r.BlockNumber == uitoa(l.Block) && r.LogIndex == uitoa(l.LogIndex) {
					want = r.Event
				}
			}
			if want == nil || want["perpId"] != uitoa(ev.PerpID) || want["markPricePNS"] != ev.MarkPricePNS.String() {
				t.Fatalf("log %d/%d does not match its expected row", l.Block, l.LogIndex)
			}
			if l.BlockTimestamp == 0 || l.Removed || l.Address != addr {
				t.Fatalf("%+v", l)
			}
			total++
		}
	}
	if total != 20 {
		t.Fatalf("%d logs", total)
	}
	req := log.at(0)
	f := req.param(t, 0).(map[string]any)
	topics := f["topics"].([]any)
	if req.Method != "eth_getLogs" || f["address"] != strings.ToLower(exchange) || len(topics) != 1 || topics[0] != perpl.TopicPositionLiquidated.String() {
		t.Fatalf("%+v", req)
	}
}

func uitoa(n uint64) string { return strconv.FormatUint(n, 10) }

func hexNumberOf(t *testing.T, s string) uint64 {
	t.Helper()
	n, err := parseQuantity(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLogsPageAtMostAThousandBlocks(t *testing.T) {
	var windows [][2]uint64
	srv, _ := node(t, func(r request) (any, *Error) {
		f := r.param(t, 0).(map[string]any)
		from, to := hexNumberOf(t, f["fromBlock"].(string)), hexNumberOf(t, f["toBlock"].(string))
		windows = append(windows, [2]uint64{from, to})
		return []any{syntheticLog(from)}, nil
	})
	logs, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 100, 2599)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]uint64{{100, 1099}, {1100, 2099}, {2100, 2599}}
	if len(windows) != 3 || windows[0] != want[0] || windows[1] != want[1] || windows[2] != want[2] {
		t.Fatalf("windows %v", windows)
	}
	if len(logs) != 3 || logs[0].Block != 100 || logs[2].Block != 2100 {
		t.Fatalf("%d logs", len(logs))
	}
}

func syntheticLog(block uint64) map[string]any {
	return map[string]any{
		"address": "0x" + strings.Repeat("11", 20), "topics": []string{"0x" + strings.Repeat("22", 32)},
		"data": "0x01", "blockNumber": quantity(block), "logIndex": "0x0",
		"transactionHash": "0x" + strings.Repeat("33", 32), "removed": false,
	}
}

func TestLogsSingleWindowAndEmpty(t *testing.T) {
	var n atomic.Int32
	srv, _ := node(t, func(r request) (any, *Error) { n.Add(1); return []any{}, nil })
	logs, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 5, 5)
	if err != nil || len(logs) != 0 || n.Load() != 1 {
		t.Fatalf("%v %v %d", logs, err, n.Load())
	}
	if logs == nil {
		t.Fatal("want an empty slice")
	}
	if _, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 6, 5); err == nil {
		t.Fatal("inverted range accepted")
	}
}

func TestLogsHalveOnRangeError(t *testing.T) {
	var windows [][2]uint64
	srv, _ := node(t, func(r request) (any, *Error) {
		f := r.param(t, 0).(map[string]any)
		from, to := hexNumberOf(t, f["fromBlock"].(string)), hexNumberOf(t, f["toBlock"].(string))
		windows = append(windows, [2]uint64{from, to})
		if to-from+1 > 250 {
			return nil, &Error{Code: -32012, Message: "query exceeds max results"}
		}
		return []any{syntheticLog(from)}, nil
	})
	logs, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 0, 599)
	if err != nil {
		t.Fatal(err)
	}
	// the 600 block window is cut to 300 and then 150, and the narrowed width carries on
	want := [][2]uint64{{0, 599}, {0, 299}, {0, 149}, {150, 299}, {300, 449}, {450, 599}}
	if len(windows) != len(want) {
		t.Fatalf("windows %v", windows)
	}
	for i := range want {
		if windows[i] != want[i] {
			t.Fatalf("window %d is %v want %v (all %v)", i, windows[i], want[i], windows)
		}
	}
	if len(logs) != 4 {
		t.Fatalf("%d logs", len(logs))
	}
}

func TestLogsHalvingBottomsOut(t *testing.T) {
	var calls atomic.Int32
	srv, _ := node(t, func(r request) (any, *Error) {
		calls.Add(1)
		return nil, &Error{Code: -32012, Message: "too big"}
	})
	_, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 0, 7)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeRangeTooLarge {
		t.Fatalf("%v", err)
	}
	// spans 8, 4, 2, 1
	if calls.Load() != 4 {
		t.Fatalf("%d calls", calls.Load())
	}
}

func TestLogsOtherErrorsDoNotHalve(t *testing.T) {
	var calls atomic.Int32
	srv, _ := node(t, func(r request) (any, *Error) {
		calls.Add(1)
		return nil, &Error{Code: -32000, Message: "boom"}
	})
	if _, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 0, 5000); err == nil || calls.Load() != 1 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

func TestLogsRejectBadEntries(t *testing.T) {
	good := syntheticLog(5)
	mutate := func(key string, value any) map[string]any {
		m := map[string]any{}
		maps.Copy(m, good)
		m[key] = value
		return m
	}
	for name, entry := range map[string]map[string]any{
		"block":   mutate("blockNumber", "zz"),
		"index":   mutate("logIndex", "1"),
		"address": mutate("address", "0x12"),
		"topic":   mutate("topics", []string{"0x12"}),
		"data":    mutate("data", "0xzz"),
		"tx":      mutate("transactionHash", "0x12"),
		"stamp":   mutate("blockTimestamp", "nope"),
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := node(t, func(r request) (any, *Error) { return []any{entry}, nil })
			if _, err := newClient(t, srv.URL).Logs(context.Background(), perpl.Address{}, perpl.Hash{}, 5, 5); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

/* Errors */

func TestJSONRPCErrorIsReturnedWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	srv, _ := node(t, func(r request) (any, *Error) {
		calls.Add(1)
		return nil, &Error{Code: 3, Message: "execution reverted", Data: "0xb6b1e4a0"}
	})
	_, err := newClient(t, srv.URL).Call(context.Background(), perpl.Address{}, nil, 1)
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != 3 || rpcErr.Data != "0xb6b1e4a0" || rpcErr.Message != "execution reverted" {
		t.Fatalf("%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls", calls.Load())
	}
	if !IsRevert(err) {
		t.Fatal("not a revert")
	}
}

func TestIsRevert(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"code 3":            {&Error{Code: 3, Message: "x"}, true},
		"message":           {&Error{Code: -32000, Message: "execution reverted: nope"}, true},
		"message uppercase": {&Error{Code: -32000, Message: "Execution Reverted"}, true},
		"other":             {&Error{Code: -32000, Message: "header not found"}, false},
		"wrapped":           {errors.Join(errors.New("x"), &Error{Code: 3}), true},
		"http":              {&HTTPError{Status: 500}, false},
		"nil":               {nil, false},
	}
	for name, c := range cases {
		if got := IsRevert(c.err); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestErrorStrings(t *testing.T) {
	if got := (&Error{Code: -32012, Message: "range"}).Error(); got != "rpc error -32012: range" {
		t.Fatal(got)
	}
	if got := (&HTTPError{Status: 503}).Error(); got != "rpc http status 503" {
		t.Fatal(got)
	}
}

func TestErrorDataKeepsNonStringJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"m","data":{"a":1}}}`))
	}))
	defer srv.Close()
	_, err := newClient(t, srv.URL).BlockNumber(context.Background())
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Data != `{"a":1}` {
		t.Fatalf("%v", err)
	}
}

func rawServer(t *testing.T, handle func(w http.ResponseWriter, calls int32)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(w, calls.Add(1))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRetriesTransientStatuses(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) {
				if n < 3 {
					http.Error(w, "busy", status)
					return
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x2a"}`))
			})
			c := newClient(t, srv.URL)
			n, err := c.BlockNumber(context.Background())
			if err != nil || n != 42 || calls.Load() != 3 {
				t.Fatalf("%d %v after %d calls", n, err, calls.Load())
			}
		})
	}
}

func TestRetryGivesUpAfterTwoRetries(t *testing.T) {
	srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) { http.Error(w, "down", http.StatusServiceUnavailable) })
	_, err := newClient(t, srv.URL).BlockNumber(context.Background())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 503 {
		t.Fatalf("%v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("%d calls", calls.Load())
	}
}

func TestRetryKeepsJSONRPCErrorOfLastAttempt(t *testing.T) {
	srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"all upstreams failed"}}`))
	})
	_, err := newClient(t, srv.URL).BlockNumber(context.Background())
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32603 || calls.Load() != 3 {
		t.Fatalf("%v after %d calls", err, calls.Load())
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) { http.Error(w, "no", status) })
		_, err := newClient(t, srv.URL).BlockNumber(context.Background())
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != status || calls.Load() != 1 {
			t.Errorf("%d: %v after %d calls", status, err, calls.Load())
		}
	}
}

func TestRetryBackoffGrows(t *testing.T) {
	var stamps []time.Time
	var mu sync.Mutex
	srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
		http.Error(w, "down", http.StatusBadGateway)
	})
	c, err := New(srv.URL, WithBackoff(40*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.BlockNumber(context.Background())
	if len(stamps) != 3 {
		t.Fatalf("%d attempts", len(stamps))
	}
	first, second := stamps[1].Sub(stamps[0]), stamps[2].Sub(stamps[1])
	if first < 40*time.Millisecond || second < 80*time.Millisecond {
		t.Fatalf("waits %v then %v", first, second)
	}
}

func TestContextCancelStopsBackoff(t *testing.T) {
	srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) { http.Error(w, "down", http.StatusBadGateway) })
	c, err := New(srv.URL, WithBackoff(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for calls.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	done := make(chan error, 1)
	go func() { _, err := c.BlockNumber(ctx); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("backoff ignored the cancelled context")
	}
}

func TestPerCallTimeout(t *testing.T) {
	release := make(chan struct{})
	srv, calls := rawServer(t, func(w http.ResponseWriter, n int32) { <-release })
	defer close(release)
	c, err := New(srv.URL, WithTimeout(50*time.Millisecond), WithBackoff(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.BlockNumber(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 2*time.Second || calls.Load() != 1 {
		t.Fatalf("took %v with %d calls", time.Since(start), calls.Load())
	}
}

func TestParentContextWins(t *testing.T) {
	srv, _ := node(t, func(r request) (any, *Error) { return "0x1", nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newClient(t, srv.URL).BlockNumber(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

func TestErrorsNeverContainTheURL(t *testing.T) {
	const secret = "key-8f3a9c1d"
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL + "/v1/" + secret
	closed.Close()
	_, err := newClient(t, url).BlockNumber(context.Background())
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("the cause is lost: %v", err)
	}
	for name, body := range map[string]string{"garbage": "<html>", "empty": "", "array": "[]"} {
		srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) { _, _ = w.Write([]byte(body)) })
		_, err := newClient(t, srv.URL+"/"+secret).BlockNumber(context.Background())
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNewValidatesURL(t *testing.T) {
	const secret = "key-8f3a9c1d"
	for _, bad := range []string{"", "ftp://x/" + secret, "not a url " + secret, "http://", "/relative/" + secret} {
		_, err := New(bad)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := New("https://node.example/path?apikey=1"); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedEnvelopes(t *testing.T) {
	cases := map[string]string{
		"no result":    `{"jsonrpc":"2.0","id":1}`,
		"not json":     `nope`,
		"error shape":  `{"jsonrpc":"2.0","id":1,"error":"bad"}`,
		"trailing":     `{"jsonrpc":"2.0","id":1,"result":"0x1"} {}`,
		"array result": `{"jsonrpc":"2.0","id":1,"result":["0x1"]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) { _, _ = w.Write([]byte(body)) })
			c := newClient(t, srv.URL)
			if n, err := c.BlockNumber(context.Background()); err == nil {
				t.Fatalf("accepted, got %d", n)
			}
		})
	}
}

func TestResponseSizeLimit(t *testing.T) {
	srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x` + strings.Repeat("ab", 400) + `"}`))
	})
	c := newClient(t, srv.URL, WithMaxResponseBytes(500))
	if _, err := c.Call(context.Background(), perpl.Address{}, nil, 1); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("%v", err)
	}
	c = newClient(t, srv.URL, WithMaxResponseBytes(5000))
	if _, err := c.Call(context.Background(), perpl.Address{}, nil, 1); err != nil {
		t.Fatal(err)
	}
}

/* Paid call watch */

func TestCreditsHeader(t *testing.T) {
	cases := []struct {
		header string
		paid   bool
	}{
		{"drpc:eth_call=20;unknown-monad.xyz:eth_call=1", true},
		{"unknown-monad.xyz:eth_call=1;drpc:eth_call=20", true},
		{"drpc:eth_call=20;drpc:eth_call=20", true},
		{" DRPC:eth_call=20", true},
		{"unknown-monad.xyz:eth_call=1,monadinfra.com:eth_call=1", false},
		{"xdrpc:eth_call=1", false},
		{"monad.xyz:drpc=1", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) {
				if tc.header != "" {
					w.Header().Set("X-ERPC-Credits", tc.header)
				}
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
			})
			type seen struct {
				method, header string
				paid           bool
			}
			var got []seen
			c := newClient(t, srv.URL, WithOnCredits(func(method, header string, paid bool) {
				got = append(got, seen{method, header, paid})
			}))
			if _, err := c.BlockNumber(context.Background()); err != nil {
				t.Fatal(err)
			}
			wantCalls := int64(0)
			if tc.paid {
				wantCalls = 1
			}
			if c.PaidCalls() != wantCalls {
				t.Fatalf("paid calls %d", c.PaidCalls())
			}
			if c.LastCredits() != strings.TrimSpace(tc.header) {
				t.Fatalf("last credits %q", c.LastCredits())
			}
			if tc.header == "" {
				if len(got) != 0 {
					t.Fatalf("callback ran without a header: %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != (seen{"eth_blockNumber", strings.TrimSpace(tc.header), tc.paid}) {
				t.Fatalf("callback %v", got)
			}
		})
	}
}

func TestCreditsCountedOnFailedAttempts(t *testing.T) {
	srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) {
		w.Header().Set("X-ERPC-Credits", "drpc:eth_call=20")
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	c := newClient(t, srv.URL)
	_, _ = c.BlockNumber(context.Background())
	if c.PaidCalls() != 3 {
		t.Fatalf("paid calls %d, want one per billed response", c.PaidCalls())
	}
}

func TestConcurrentUse(t *testing.T) {
	srv, _ := rawServer(t, func(w http.ResponseWriter, n int32) {
		w.Header().Set("X-ERPC-Credits", "drpc:eth_call=1")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":"0x1"}`))
	})
	c := newClient(t, srv.URL)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 5 {
				if _, err := c.BlockNumber(context.Background()); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if c.PaidCalls() != 80 {
		t.Fatalf("paid calls %d", c.PaidCalls())
	}
}

func TestOptionsAreChecked(t *testing.T) {
	for _, opt := range []Option{WithTimeout(0), WithBackoff(-1), WithMaxResponseBytes(0), WithHTTPClient(nil)} {
		if _, err := New("http://localhost:1", opt); err == nil {
			t.Error("bad option accepted")
		}
	}
	custom := &http.Client{Timeout: time.Second}
	c, err := New("http://localhost:1", WithHTTPClient(custom))
	if err != nil || c.http != custom {
		t.Fatalf("%v", err)
	}
}

func TestMalformedResultsPerMethod(t *testing.T) {
	ctx := context.Background()
	hash := "0x" + strings.Repeat("11", 32)
	addr := "0x" + strings.Repeat("22", 20)
	run := func(result any, fn func(c *Client) error) error {
		srv, _ := node(t, func(r request) (any, *Error) { return result, nil })
		return fn(newClient(t, srv.URL))
	}
	cases := map[string]struct {
		result any
		fn     func(c *Client) error
	}{
		"call not a string":  {42, func(c *Client) error { _, err := c.Call(ctx, perpl.Address{}, nil, 1); return err }},
		"call no prefix":     {"abcd", func(c *Client) error { _, err := c.Call(ctx, perpl.Address{}, nil, 1); return err }},
		"call bad hex":       {"0xabc", func(c *Client) error { _, err := c.Call(ctx, perpl.Address{}, nil, 1); return err }},
		"call null":          {nil, func(c *Client) error { _, err := c.Call(ctx, perpl.Address{}, nil, 1); return err }},
		"block shape":        {[]int{1}, func(c *Client) error { _, err := c.BlockByNumber(ctx, 1); return err }},
		"block number":       {map[string]any{"number": "x", "hash": hash, "timestamp": "0x1"}, func(c *Client) error { _, err := c.BlockByNumber(ctx, 1); return err }},
		"block timestamp":    {map[string]any{"number": "0x1", "hash": hash, "timestamp": "x"}, func(c *Client) error { _, err := c.BlockByNumber(ctx, 1); return err }},
		"storage shape":      {7, func(c *Client) error { _, err := c.StorageAt(ctx, perpl.Address{}, perpl.Hash{}, 1); return err }},
		"tx shape":           {"nope", func(c *Client) error { _, err := c.TransactionByHash(ctx, mustHash(t, hash)); return err }},
		"tx hash":            {map[string]any{"hash": "x", "from": addr}, func(c *Client) error { _, err := c.TransactionByHash(ctx, mustHash(t, hash)); return err }},
		"logs shape":         {map[string]any{"a": 1}, func(c *Client) error { _, err := c.Logs(ctx, perpl.Address{}, perpl.Hash{}, 1, 1); return err }},
		"block number shape": {map[string]any{"a": 1}, func(c *Client) error { _, err := c.BlockNumber(ctx); return err }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := run(tc.result, tc.fn); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestTransportErrorKeepsCauseButNotHost(t *testing.T) {
	c, err := New("http://rpc.invalid-host-for-test.example:1/key-abc", WithHTTPClient(&http.Client{Transport: failingTransport{}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.BlockNumber(context.Background())
	if !errors.Is(err, errBoom) || strings.Contains(err.Error(), "invalid-host") || strings.Contains(err.Error(), "key-abc") {
		t.Fatalf("%v", err)
	}
}

var errBoom = errors.New("boom")

type failingTransport struct{}

func (failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial %s: %w", r.URL.Host, errBoom)
}

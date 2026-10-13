package sources_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"perps/internal/config"
	"perps/internal/perpl"
	"perps/internal/sources"
)

const testKey = "hs-test-key-9f3a"

var (
	exchange = mustAddrHex("0x34b6552d57a35a1d042ccae1951bd1c370112a6f")
	topic0   = mustHashHex("0x6fc9c0ea1c0531654320ba06740c802447dbdef7c26cf749c4f12553cdd958a9")
)

func mustAddrHex(s string) perpl.Address {
	a, err := perpl.ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

func mustHashHex(s string) perpl.Hash {
	h, err := perpl.ParseHash(s)
	if err != nil {
		panic(err)
	}
	return h
}

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleeps) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func fastBackoff(s *sleeps) sources.Backoff {
	return sources.Backoff{Initial: 8 * time.Second, Max: 60 * time.Second, Attempts: 8, Sleep: s.sleep}
}

func newHS(t *testing.T, h http.Handler, opts ...sources.HyperSyncOption) *sources.HyperSync {
	t.Helper()
	srv := serve(t, h)
	return sources.NewHyperSync(srv.URL+"/query", config.Secret(testKey), opts...)
}

func recordedQuery() sources.Query {
	return sources.Query{Address: exchange, Topic0: topic0, From: 102493417, To: 102493440}
}

type expectedRow struct {
	BlockNumber     string `json:"blockNumber"`
	LogIndex        string `json:"logIndex"`
	TransactionHash string `json:"transactionHash"`
	BlockTimestamp  string `json:"blockTimestamp"`
	TransactionFrom string `json:"transactionFrom"`
}

func TestPageSendsTheRecordedRequestAndHeaders(t *testing.T) {
	var gotBody []byte
	var header http.Header
	var method, path string
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, header = r.Method, r.URL.Path, r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		replay(fixture(t, "hypersync/response.json"))(w, r)
	}))
	if _, err := h.Page(t.Context(), recordedQuery()); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if err := json.Unmarshal(fixture(t, "hypersync/request.json"), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request body\n got %s\nwant %s", gotBody, fixture(t, "hypersync/request.json"))
	}
	if method != http.MethodPost || path != "/query" {
		t.Fatalf("%s %s", method, path)
	}
	if header.Get("Authorization") != "Bearer "+testKey || header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers Authorization %q, Content-Type %q", header.Get("Authorization"), header.Get("Content-Type"))
	}
	if ua := header.Get("User-Agent"); ua == "" || strings.HasPrefix(ua, "Go-http-client") {
		t.Fatalf("User-Agent %q", ua)
	}
}

func TestPageDecodesTheRecordedResponse(t *testing.T) {
	h := newHS(t, replay(fixture(t, "hypersync/response.json")))
	page, err := h.Page(t.Context(), recordedQuery())
	if err != nil {
		t.Fatal(err)
	}
	var want []expectedRow
	if err := json.Unmarshal(fixture(t, "hypersync/expected.json"), &want); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Data []struct {
			Logs []struct {
				Data string `json:"data"`
			} `json:"logs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fixture(t, "hypersync/response.json"), &raw); err != nil {
		t.Fatal(err)
	}
	if page.NextBlock != 102493440 || page.ArchiveHeight != 112776922 || len(page.Logs) != len(want) || len(want) != 10 {
		t.Fatalf("next %d, archive %d, %d logs, %d expected", page.NextBlock, page.ArchiveHeight, len(page.Logs), len(want))
	}
	dataOf := map[string]string{}
	for _, item := range raw.Data {
		for _, l := range item.Logs {
			dataOf[l.Data] = l.Data
		}
	}
	for i, w := range want {
		l := page.Logs[i]
		wantTS := mustUint(t, w.BlockTimestamp)
		if strconv.FormatUint(l.Block, 10) != w.BlockNumber || strconv.FormatUint(l.LogIndex, 10) != w.LogIndex || l.TxHash.String() != w.TransactionHash ||
			l.BlockTimestamp != wantTS || l.From.String() != w.TransactionFrom {
			t.Errorf("log %d = block %d idx %d tx %s ts %d from %s, want %+v", i, l.Block, l.LogIndex, l.TxHash, l.BlockTimestamp, l.From, w)
		}
		if l.Address != exchange || len(l.Topics) != 1 || l.Topics[0] != topic0 {
			t.Errorf("log %d address %s topics %v", i, l.Address, l.Topics)
		}
		if len(l.Data) != 14*32 || dataOf["0x"+hex.EncodeToString(l.Data)] == "" {
			t.Errorf("log %d data is %d bytes and not the recorded payload", i, len(l.Data))
		}
	}
}

func TestPageLogsFeedThePerplDecoder(t *testing.T) {
	h := newHS(t, replay(fixture(t, "hypersync/response.json")))
	page, err := h.Page(t.Context(), recordedQuery())
	if err != nil {
		t.Fatal(err)
	}
	var want []struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(fixture(t, "hypersync/expected.json"), &want); err != nil {
		t.Fatal(err)
	}
	for i, l := range page.Logs {
		liq, err := perpl.DecodeLiquidation(l.Topics, l.Data, l.Block, l.LogIndex, l.TxHash)
		if err != nil {
			t.Fatalf("log %d: %v", i, err)
		}
		if strconv.FormatUint(liq.PerpID, 10) != want[i].Event["perpId"] || strconv.FormatUint(liq.AccountID, 10) != want[i].Event["posAccountId"] || liq.LiqPricePNS.String() != want[i].Event["liqPricePNS"] {
			t.Errorf("log %d decoded to %+v, want %v", i, liq, want[i].Event)
		}
	}
}

func mustUint(t *testing.T, s string) uint64 {
	t.Helper()
	var n uint64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPageDefaultsAndOverridesMaxLogs(t *testing.T) {
	var bodies []map[string]any
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.UnmarshalRead(r.Body, &m)
		bodies = append(bodies, m)
		replay([]byte(`{"data":[],"next_block":20,"archive_height":99}`))(w, r)
	}))
	q := sources.Query{Address: exchange, Topic0: topic0, From: 10, To: 20}
	if _, err := h.Page(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q.MaxLogs = 25
	if _, err := h.Page(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["max_num_logs"] != 1000.0 || bodies[1]["max_num_logs"] != 25.0 {
		t.Fatalf("max_num_logs %v and %v", bodies[0]["max_num_logs"], bodies[1]["max_num_logs"])
	}
}

func TestPageEmptyRangeSendsNothing(t *testing.T) {
	hits := 0
	h := newHS(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	for _, q := range []sources.Query{{From: 5, To: 5}, {From: 9, To: 5}} {
		q.Address, q.Topic0 = exchange, topic0
		page, err := h.Page(t.Context(), q)
		if err != nil || len(page.Logs) != 0 || page.NextBlock != q.From {
			t.Fatalf("query %+v gave %+v, %v", q, page, err)
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests were sent", hits)
	}
}

func TestPageWithoutAKeyFailsBeforeAnyRequest(t *testing.T) {
	hits := 0
	srv := serve(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	h := sources.NewHyperSync(srv.URL, config.Secret(""))
	if _, err := h.Page(t.Context(), recordedQuery()); !errors.Is(err, sources.ErrNoKey) || hits != 0 {
		t.Fatalf("err %v, %d requests", err, hits)
	}
}

func TestPageAcceptsHexAndNumericQuantities(t *testing.T) {
	body := `{"data":[{"logs":[{"block_number":"0x10","log_index":"0x2","transaction_hash":"` + hash(1) + `","address":"` + exchange.String() + `","data":"0x01","topic0":"` + topic0.String() + `"}],
		"blocks":[{"number":16,"timestamp":"0x6a9d7977"}],
		"transactions":[{"hash":"` + hash(1) + `","from":"0xB3B0F0A1745AE45224930D85E1FCC7F731D374A3"}]}],
		"archive_height":"0x100","next_block":"0x20"}`
	h := newHS(t, replay([]byte(body)))
	page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 16, To: 32})
	if err != nil {
		t.Fatal(err)
	}
	l := page.Logs[0]
	if l.Block != 16 || l.LogIndex != 2 || l.BlockTimestamp != 0x6a9d7977 || l.From.String() != "0xb3b0f0a1745ae45224930d85e1fcc7f731d374a3" ||
		page.NextBlock != 32 || page.ArchiveHeight != 256 || !bytes.Equal(l.Data, []byte{1}) {
		t.Fatalf("log %+v page %+v", l, page)
	}
}

func hash(n int) string { return fmt.Sprintf("0x%064x", n) }

func TestPageMergesBlocksAndTransactionsAcrossDataItems(t *testing.T) {
	body := `{"data":[
		{"logs":[{"block_number":5,"log_index":1,"transaction_hash":"` + hash(1) + `","address":"` + exchange.String() + `","data":"0x","topic0":"` + topic0.String() + `"}]},
		{"logs":[{"block_number":6,"log_index":0,"transaction_hash":"` + hash(2) + `","address":"` + exchange.String() + `","data":"0x","topic0":"` + topic0.String() + `"}],
		 "blocks":[{"number":5,"timestamp":"0x10"},{"number":6,"timestamp":"0x11"}],
		 "transactions":[{"hash":"` + hash(1) + `","from":"` + addrStr(7) + `"},{"hash":"` + hash(2) + `","from":"` + addrStr(8) + `"}]}],
		"archive_height":100,"next_block":7}`
	h := newHS(t, replay([]byte(body)))
	page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 5, To: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 2 || page.Logs[0].BlockTimestamp != 0x10 || page.Logs[1].BlockTimestamp != 0x11 ||
		page.Logs[0].From.String() != addrStr(7) || page.Logs[1].From.String() != addrStr(8) {
		t.Fatalf("logs %+v", page.Logs)
	}
}

func addrStr(n int) string { return fmt.Sprintf("0x%040x", n) }

func TestPageSortsLogsByBlockThenIndex(t *testing.T) {
	log := func(block, idx, tx int) string {
		return fmt.Sprintf(`{"block_number":%d,"log_index":%d,"transaction_hash":"%s","address":"%s","data":"0x","topic0":"%s"}`, block, idx, hash(tx), exchange, topic0)
	}
	body := `{"data":[{"logs":[` + log(9, 4, 3) + `,` + log(8, 7, 1) + `,` + log(9, 1, 2) + `],
		"blocks":[{"number":8,"timestamp":1},{"number":9,"timestamp":2}],
		"transactions":[{"hash":"` + hash(1) + `","from":"` + addrStr(1) + `"},{"hash":"` + hash(2) + `","from":"` + addrStr(1) + `"},{"hash":"` + hash(3) + `","from":"` + addrStr(1) + `"}]}],
		"archive_height":100,"next_block":10}`
	h := newHS(t, replay([]byte(body)))
	page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 8, To: 10})
	if err != nil {
		t.Fatal(err)
	}
	var order [][2]uint64
	for _, l := range page.Logs {
		order = append(order, [2]uint64{l.Block, l.LogIndex})
	}
	if !reflect.DeepEqual(order, [][2]uint64{{8, 7}, {9, 1}, {9, 4}}) {
		t.Fatalf("order %v", order)
	}
}

func TestPageRejectsAResponseThatLacksWhatALogNeeds(t *testing.T) {
	log := `{"block_number":5,"log_index":1,"transaction_hash":"` + hash(1) + `","address":"` + exchange.String() + `","data":"0x","topic0":"` + topic0.String() + `"}`
	block := `{"number":5,"timestamp":"0x10"}`
	tx := `{"hash":"` + hash(1) + `","from":"` + addrStr(7) + `"}`
	tail := `,"archive_height":100,"next_block":6}`
	cases := map[string]string{
		"no block timestamp": `{"data":[{"logs":[` + log + `],"transactions":[` + tx + `]}]` + tail,
		"no transaction":     `{"data":[{"logs":[` + log + `],"blocks":[` + block + `]}]` + tail,
		"no next block":      `{"data":[{"logs":[` + log + `],"blocks":[` + block + `],"transactions":[` + tx + `]}],"archive_height":100}`,
		"not json":           `<html>gateway</html>`,
		"bad tx hash":        `{"data":[{"logs":[` + strings.Replace(log, hash(1), "0x12", 1) + `],"blocks":[` + block + `],"transactions":[` + tx + `]}]` + tail,
		"bad data hex":       `{"data":[{"logs":[` + strings.Replace(log, `"0x"`, `"0xzz"`, 1) + `],"blocks":[` + block + `],"transactions":[` + tx + `]}]` + tail,
		"bad from":           `{"data":[{"logs":[` + log + `],"blocks":[` + block + `],"transactions":[` + strings.Replace(tx, addrStr(7), "0x12", 1) + `]}]` + tail,
		"no block number":    `{"data":[{"logs":[` + strings.Replace(log, `"block_number":5,`, "", 1) + `],"blocks":[` + block + `],"transactions":[` + tx + `]}]` + tail,
		"no log index":       `{"data":[{"logs":[` + strings.Replace(log, `"log_index":1,`, "", 1) + `],"blocks":[` + block + `],"transactions":[` + tx + `]}]` + tail,
		"no topic0":          `{"data":[{"logs":[` + strings.Replace(log, `,"topic0":"`+topic0.String()+`"`, "", 1) + `],"blocks":[` + block + `],"transactions":[` + tx + `]}]` + tail,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHS(t, replay([]byte(body)))
			if page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 5, To: 6}); err == nil {
				t.Fatalf("accepted %+v", page)
			}
		})
	}
}

func TestPageBacksOffOn429WithGrowingDelays(t *testing.T) {
	var s sleeps
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 4 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`))(w, r)
	}), sources.WithBackoff(fastBackoff(&s)))
	page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 10, To: 20})
	if err != nil || page.NextBlock != 20 || calls.Load() != 5 {
		t.Fatalf("page %+v, err %v, %d calls", page, err, calls.Load())
	}
	want := []time.Duration{8 * time.Second, 16 * time.Second, 32 * time.Second, 60 * time.Second}
	if got := s.got(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sleeps %v, want %v", got, want)
	}
}

func TestPageHonoursRetryAfterWithinTheCap(t *testing.T) {
	var s sleeps
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
		case 3:
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`))(w, r)
		}
	}), sources.WithBackoff(fastBackoff(&s)))
	if _, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 10, To: 20}); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{20 * time.Second, 16 * time.Second, 60 * time.Second}
	if got := s.got(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sleeps %v, want %v", got, want)
	}
}

func TestPageRetriesGatewayErrorsToo(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		var s sleeps
		var calls atomic.Int32
		h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(status)
				return
			}
			replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`))(w, r)
		}), sources.WithBackoff(fastBackoff(&s)))
		if _, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 10, To: 20}); err != nil || calls.Load() != 2 {
			t.Errorf("status %d: err %v after %d calls", status, err, calls.Load())
		}
	}
}

func TestPageGivesUpAfterTheAttemptLimit(t *testing.T) {
	var s sleeps
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "no", http.StatusTooManyRequests)
	}), sources.WithBackoff(sources.Backoff{Initial: time.Second, Max: 4 * time.Second, Attempts: 3, Sleep: s.sleep}))
	_, err := h.Page(t.Context(), recordedQuery())
	var se *sources.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTooManyRequests || calls.Load() != 3 || len(s.got()) != 2 {
		t.Fatalf("err %v after %d calls and %d sleeps", err, calls.Load(), len(s.got()))
	}
}

func TestPageDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		var s sleeps
		var calls atomic.Int32
		h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			http.Error(w, "echo Bearer "+testKey, status)
		}), sources.WithBackoff(fastBackoff(&s)))
		_, err := h.Page(t.Context(), recordedQuery())
		var se *sources.StatusError
		if !errors.As(err, &se) || se.Status != status || calls.Load() != 1 || len(s.got()) != 0 {
			t.Errorf("status %d: err %v, %d calls, sleeps %v", status, err, calls.Load(), s.got())
		}
		if err != nil && strings.Contains(err.Error(), testKey) {
			t.Errorf("status %d: error leaks the key: %v", status, err)
		}
	}
}

func TestPageStopsSleepingWhenTheContextEnds(t *testing.T) {
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "no", http.StatusTooManyRequests)
	}), sources.WithBackoff(sources.Backoff{Initial: time.Hour, Max: time.Hour, Attempts: 5}))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := h.Page(ctx, recordedQuery())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second || calls.Load() != 1 {
		t.Fatalf("err %v after %v and %d calls", err, time.Since(start), calls.Load())
	}
}

func TestPageNeverRunsTwoRequestsAtOnce(t *testing.T) {
	var inflight, peak, total atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		total.Add(1)
		time.Sleep(20 * time.Millisecond)
		replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`))(w, r)
	}))
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := h.Page(t.Context(), recordedQuery()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if peak.Load() != 1 || total.Load() != 6 {
		t.Fatalf("peak concurrency %d over %d requests", peak.Load(), total.Load())
	}
}

func TestClientNeverPrintsTheKey(t *testing.T) {
	h := sources.NewHyperSync("https://example.invalid/query", config.Secret(testKey))
	for _, s := range []string{fmt.Sprintf("%v", h), fmt.Sprintf("%+v", h), fmt.Sprintf("%#v", h)} {
		if strings.Contains(s, testKey) {
			t.Fatalf("formatted client contains the key: %s", s)
		}
	}
}

func pagesServer(t *testing.T, calls *[]uint64, next func(from uint64) uint64) *sources.HyperSync {
	t.Helper()
	return newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			From uint64 `json:"from_block"`
			To   uint64 `json:"to_block"`
		}
		if err := json.UnmarshalRead(r.Body, &req); err != nil {
			t.Error(err)
		}
		*calls = append(*calls, req.From)
		replay([]byte(fmt.Sprintf(`{"data":[],"archive_height":1000,"next_block":%d}`, next(req.From))))(w, r)
	}))
}

func TestScanFollowsNextBlockUntilTheEndOfTheRange(t *testing.T) {
	var from []uint64
	h := pagesServer(t, &from, func(f uint64) uint64 { return min(f+40, 1000) })
	var seen []uint64
	next, err := h.Scan(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 100, To: 230}, func(p sources.Page) error {
		seen = append(seen, p.NextBlock)
		return nil
	})
	if err != nil || next != 230 {
		t.Fatalf("next %d, err %v", next, err)
	}
	if !reflect.DeepEqual(from, []uint64{100, 140, 180, 220}) || !reflect.DeepEqual(seen, []uint64{140, 180, 220, 260}) {
		t.Fatalf("requested from %v, pages ended at %v", from, seen)
	}
}

func TestScanStopsWhenTheArchiveHasNoMoreToGive(t *testing.T) {
	var from []uint64
	h := pagesServer(t, &from, func(f uint64) uint64 { return min(f+40, 180) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	next, err := h.Scan(ctx, sources.Query{Address: exchange, Topic0: topic0, From: 100, To: 1000}, func(sources.Page) error { return nil })
	if err != nil || next != 180 {
		t.Fatalf("next %d, err %v", next, err)
	}
	if !reflect.DeepEqual(from, []uint64{100, 140, 180}) {
		t.Fatalf("requested from %v", from)
	}
}

func TestScanReportsTheFirstUnhandledBlockWhenTheHandlerFails(t *testing.T) {
	var from []uint64
	h := pagesServer(t, &from, func(f uint64) uint64 { return f + 40 })
	boom := errors.New("disk full")
	pages := 0
	next, err := h.Scan(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 100, To: 1000}, func(sources.Page) error {
		pages++
		if pages == 3 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || next != 180 {
		t.Fatalf("next %d, err %v, want 180 and the handler error", next, err)
	}
}

func TestScanReportsProgressWhenARequestFails(t *testing.T) {
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 3 {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		replay([]byte(`{"data":[],"archive_height":1000,"next_block":`+strconv.Itoa(int(100+40*calls.Load()))+`}`))(w, r)
	}))
	next, err := h.Scan(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 100, To: 1000}, func(sources.Page) error { return nil })
	var se *sources.StatusError
	if !errors.As(err, &se) || next != 180 {
		t.Fatalf("next %d, err %v", next, err)
	}
}

func TestScanClampsNextBlockToTheEndOfTheRange(t *testing.T) {
	var from []uint64
	h := pagesServer(t, &from, func(uint64) uint64 { return 5000 })
	next, err := h.Scan(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 100, To: 230}, func(sources.Page) error { return nil })
	if err != nil || next != 230 || len(from) != 1 {
		t.Fatalf("next %d, err %v, %d requests", next, err, len(from))
	}
}

func TestScanOfAnEmptyRangeDoesNothing(t *testing.T) {
	var from []uint64
	h := pagesServer(t, &from, func(f uint64) uint64 { return f })
	next, err := h.Scan(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 50, To: 50}, func(sources.Page) error {
		t.Error("handler called")
		return nil
	})
	if err != nil || next != 50 || len(from) != 0 {
		t.Fatalf("next %d, err %v, %d requests", next, err, len(from))
	}
}

func TestPageSurfacesNetworkFailuresWithoutTheKey(t *testing.T) {
	srv := serve(t, http.NotFoundHandler())
	url := srv.URL + "/query"
	srv.Close()
	h := sources.NewHyperSync(url, config.Secret(testKey), sources.WithBackoff(sources.Backoff{Initial: time.Millisecond, Max: time.Millisecond, Attempts: 3}))
	_, err := h.Page(t.Context(), recordedQuery())
	var se *sources.StatusError
	if err == nil || errors.As(err, &se) || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err %v", err)
	}
}

func TestPageRejectsAnUnusableURL(t *testing.T) {
	h := sources.NewHyperSync("http://[::1", config.Secret(testKey))
	_, err := h.Page(t.Context(), recordedQuery())
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err %v", err)
	}
}

func TestPageSleepsForRealWithTheDefaultSleeper(t *testing.T) {
	var calls atomic.Int32
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`))(w, r)
	}), sources.WithBackoff(sources.Backoff{Initial: 5 * time.Millisecond, Max: 20 * time.Millisecond, Attempts: 3}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := h.Page(ctx, recordedQuery()); err != nil || calls.Load() != 2 {
		t.Fatalf("err %v after %d calls", err, calls.Load())
	}
	if d := time.Since(start); d < 15*time.Millisecond || d > 5*time.Second {
		t.Fatalf("slept %v, want the 20ms cap from an hour-long HTTP-date Retry-After", d)
	}
}

type countingTransport struct {
	hits atomic.Int32
	next http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.hits.Add(1)
	return c.next.RoundTrip(r)
}

func TestClientsUseTheHTTPClientTheyAreGiven(t *testing.T) {
	var tr countingTransport
	tr.next = http.DefaultTransport
	hc := &http.Client{Transport: &tr}
	h := newHS(t, replay([]byte(`{"data":[],"archive_height":9,"next_block":20}`)), sources.WithHyperSyncHTTPClient(hc))
	if _, err := h.Page(t.Context(), recordedQuery()); err != nil || tr.hits.Load() != 1 {
		t.Fatalf("hypersync: err %v, %d hits", err, tr.hits.Load())
	}
	r := newREST(t, replay([]byte(`{"d":[]}`)), sources.WithHTTPClient(hc))
	if _, err := r.Leaderboard(t.Context(), sources.PeriodDay); err != nil || tr.hits.Load() != 2 {
		t.Fatalf("rest: err %v, %d hits", err, tr.hits.Load())
	}
}

func TestPageRejectsMoreMalformedLogFields(t *testing.T) {
	good := `{"block_number":5,"log_index":1,"transaction_hash":"` + hash(1) + `","address":"` + exchange.String() + `","data":"0x","topic0":"` + topic0.String() + `"}`
	rest := `],"blocks":[{"number":5,"timestamp":"0x10"}],"transactions":[{"hash":"` + hash(1) + `","from":"` + addrStr(7) + `"}]}],"archive_height":100,"next_block":6}`
	cases := map[string]string{
		"block number text": strings.Replace(good, `"block_number":5`, `"block_number":"five"`, 1),
		"data without 0x":   strings.Replace(good, `"data":"0x"`, `"data":"00"`, 1),
		"bad address":       strings.Replace(good, exchange.String(), "0x12", 1),
		"bad topic":         strings.Replace(good, topic0.String(), "0x12", 1),
	}
	for name, log := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHS(t, replay([]byte(`{"data":[{"logs":[`+log+rest)))
			if page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 5, To: 6}); err == nil {
				t.Fatalf("accepted %+v", page)
			}
		})
	}
}

func TestPageKeepsEveryTopicTheServerSends(t *testing.T) {
	log := `{"block_number":5,"log_index":1,"transaction_hash":"` + hash(1) + `","address":"` + exchange.String() + `","data":"0x","topic0":"` + hash(10) + `","topic1":"` + hash(11) + `","topic2":"` + hash(12) + `"}`
	body := `{"data":[{"logs":[` + log + `],"blocks":[{"number":5,"timestamp":"0x10"}],"transactions":[{"hash":"` + hash(1) + `","from":"` + addrStr(7) + `"}]}],"archive_height":100,"next_block":6}`
	h := newHS(t, replay([]byte(body)))
	page, err := h.Page(t.Context(), sources.Query{Address: exchange, Topic0: topic0, From: 5, To: 6})
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Logs[0].Topics; len(got) != 3 || got[0] != mustHashHex(hash(10)) || got[2] != mustHashHex(hash(12)) {
		t.Fatalf("topics %v", got)
	}
}

func TestPageDoesNotFollowARedirectWithTheKey(t *testing.T) {
	var leaked atomic.Int32
	sink := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		replay(fixture(t, "hypersync/response.json"))(w, r)
	}))
	h := newHS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/query", http.StatusTemporaryRedirect)
	}))
	_, err := h.Page(t.Context(), recordedQuery())
	var se *sources.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err %v, want a 307 status error", err)
	}
	if leaked.Load() != 0 {
		t.Fatal("the redirect target received the Authorization header")
	}
}

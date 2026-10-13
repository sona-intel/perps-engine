package refresh

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"perps/internal/config"
	"perps/internal/perpl"
	"perps/internal/rpc"
	"perps/internal/sources"
	"perps/internal/store"
)

const testdataDir = "../../testdata"

const (
	pinnedBlock  = 112777356
	pinnedTime   = 0x6acced05
	exchangeText = "0x34b6552d57a35a1d042ccae1951bd1c370112a6f"
)

var errBoom = errors.New("boom")

func testdata(parts ...string) string {
	return filepath.Join(append([]string{testdataDir}, parts...)...)
}

func readJSON(t testing.TB, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func mustAddress(t testing.TB, s string) perpl.Address {
	t.Helper()
	a, err := perpl.ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustHash(t testing.TB, s string) perpl.Hash {
	t.Helper()
	h, err := perpl.ParseHash(s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

/* Fixture replay */

type callFixture struct {
	Function string            `json:"function"`
	Args     map[string]string `json:"args"`
	Request  struct {
		Data string `json:"data"`
	} `json:"request"`
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data"`
	} `json:"error"`
}

type reply struct {
	out []byte
	err error
}

type chainJSON struct {
	PerpIDs []uint64 `json:"perp_ids"`
}

// fakeChain answers eth_call from the recorded fixtures whatever the block is and records which block each call used
type fakeChain struct {
	t *testing.T

	mu         sync.Mutex
	head       uint64
	replies    map[string]reply
	blockTimes map[uint64]uint64
	impl       perpl.Address
	logs       []rpc.Log
	txs        map[perpl.Hash]perpl.Address
	paid       int64
	delay      time.Duration
	emptyPage  *reply
	fail       func(selector string, data []byte) error
	headErr    error

	callBlocks   map[uint64]int
	selectors    map[string]int
	pageStarts   map[uint64][]uint64
	multicalls   [][]perpl.Call3
	directCalls  int
	dropResults  int
	logWindows   [][2]uint64
	blockLookups map[uint64]int
	txLookups    map[perpl.Hash]int
	inflight     atomic.Int32
	maxInflight  atomic.Int32
}

const (
	selGetAccountByID = "0x05aca141"
	selGetPositions   = "0x92e19126"
)

// isFirstPage reports whether getPositionsV2 starts at node 0
func isFirstPage(data []byte) bool {
	if len(data) != 4+3*32 {
		return false
	}
	for _, b := range data[4+32 : 4+64] {
		if b != 0 {
			return false
		}
	}
	return true
}

func newFakeChain(t *testing.T) *fakeChain {
	t.Helper()
	c := &fakeChain{
		t:            t,
		head:         pinnedBlock + 2,
		replies:      make(map[string]reply),
		blockTimes:   map[uint64]uint64{pinnedBlock: pinnedTime},
		txs:          make(map[perpl.Hash]perpl.Address),
		callBlocks:   make(map[uint64]int),
		selectors:    make(map[string]int),
		pageStarts:   make(map[uint64][]uint64),
		blockLookups: make(map[uint64]int),
		txLookups:    make(map[perpl.Hash]int),
	}
	c.loadCalls()
	var impl struct {
		Implementation string `json:"implementation"`
	}
	readJSON(t, testdata("calls", "getStorageAt.implementation.expected.json"), &impl)
	c.impl = mustAddress(t, impl.Implementation)
	return c
}

func (c *fakeChain) loadCalls() {
	files, err := filepath.Glob(testdata("calls", "get*.json"))
	if err != nil {
		c.t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, ".expected.json") || strings.Contains(f, "getStorageAt") {
			continue
		}
		var fx callFixture
		readJSON(c.t, f, &fx)
		var r reply
		if fx.Error != nil {
			r.err = &rpc.Error{Code: fx.Error.Code, Message: fx.Error.Message, Data: fx.Error.Data}
		} else {
			r.out = mustHex(c.t, fx.Result)
		}
		c.replies[strings.ToLower(fx.Request.Data)] = r
		if strings.Contains(f, "perp30.empty") {
			c.emptyPage = &r
		}
		if fx.Function == "getPositionsV2" && strings.Contains(f, ".page00.") {
			perp, _ := strconv.ParseUint(fx.Args["perpId"], 10, 64)
			alias := "0x" + hex.EncodeToString(perpl.EncodeGetPositionsV2(perp, 0, 100))
			if _, ok := c.replies[alias]; !ok {
				c.replies[alias] = r
			}
		}
	}
}

func (c *fakeChain) enter() func() {
	n := c.inflight.Add(1)
	for {
		m := c.maxInflight.Load()
		if n <= m || c.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return func() { c.inflight.Add(-1) }
}

func (c *fakeChain) setHead(h uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.head = h
}

func (c *fakeChain) BlockNumber(context.Context) (uint64, error) {
	defer c.enter()()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head, c.headErr
}

func (c *fakeChain) BlockByNumber(_ context.Context, n uint64) (rpc.Block, error) {
	defer c.enter()()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockLookups[n]++
	ts, ok := c.blockTimes[n]
	if !ok {
		if n > pinnedBlock-1000 && n < pinnedBlock+1000 {
			ts = pinnedTime + (n - pinnedBlock)
		} else {
			return rpc.Block{}, fmt.Errorf("fake: no block %d", n)
		}
	}
	return rpc.Block{Number: n, Time: time.Unix(int64(ts), 0).UTC()}, nil
}

func (c *fakeChain) Call(_ context.Context, to perpl.Address, data []byte, block uint64) ([]byte, error) {
	defer c.enter()()
	if to == perpl.Multicall3 {
		return c.multicall(data, block)
	}
	if to.String() != exchangeText {
		c.t.Errorf("call to %s", to)
	}
	c.mu.Lock()
	c.callBlocks[block]++
	c.directCalls++
	c.mu.Unlock()
	return c.answer(data)
}

// answer replays the recorded result for one exchange call
func (c *fakeChain) answer(data []byte) ([]byte, error) {
	key := "0x" + hex.EncodeToString(data)
	sel := key[:10]
	c.mu.Lock()
	c.selectors[sel]++
	fail := c.fail
	if sel == selGetPositions && len(data) == 4+3*32 {
		perp := binary.BigEndian.Uint64(data[4+24 : 4+32])
		start := binary.BigEndian.Uint64(data[4+32+24 : 4+64])
		c.pageStarts[perp] = append(c.pageStarts[perp], start)
	}
	c.mu.Unlock()
	if fail != nil {
		if err := fail(sel, data); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	r, ok := c.replies[key]
	c.mu.Unlock()
	if ok {
		return r.out, r.err
	}
	if sel == selGetPositions && c.emptyPage != nil && isFirstPage(data) {
		return c.emptyPage.out, nil
	}
	if sel == selGetAccountByID {
		return nil, &rpc.Error{Code: rpc.CodeRevert, Message: "execution reverted", Data: "0xb6b1e4a0"}
	}
	return nil, fmt.Errorf("fake: unrecorded call %s", key)
}

/* Multicall3 */

const selAggregate3 = "82ad56cb"

// multicall plays Multicall3, a failing sub-call becomes success false when allowed and reverts the whole call otherwise
func (c *fakeChain) multicall(data []byte, block uint64) ([]byte, error) {
	calls := decodeAggregate3Request(c.t, data)
	c.mu.Lock()
	c.callBlocks[block]++
	c.multicalls = append(c.multicalls, calls)
	trim := c.dropResults
	c.mu.Unlock()
	results := make([]perpl.Result3, len(calls))
	for i, call := range calls {
		if call.Target.String() != exchangeText {
			c.t.Errorf("multicall target %s", call.Target)
		}
		out, err := c.answer(call.Data)
		if err == nil {
			results[i] = perpl.Result3{Success: true, Data: out}
			continue
		}
		if !call.AllowFailure {
			return nil, &rpc.Error{Code: rpc.CodeRevert, Message: "execution reverted: Multicall3: call failed"}
		}
		var rpcErr *rpc.Error
		if errors.As(err, &rpcErr) && rpcErr.Data != "" {
			results[i] = perpl.Result3{Data: mustHex(c.t, rpcErr.Data)}
		}
	}
	if trim > 0 {
		results = results[:len(results)-trim]
	}
	return encodeAggregate3Response(results), nil
}

func testWord(n uint64) []byte {
	w := make([]byte, 32)
	binary.BigEndian.PutUint64(w[24:], n)
	return w
}

// decodeAggregate3Request reads the calldata with its own offsets arithmetic so the production encoder is checked by something else
func decodeAggregate3Request(t testing.TB, data []byte) []perpl.Call3 {
	t.Helper()
	if hex.EncodeToString(data[:4]) != selAggregate3 {
		t.Fatalf("multicall selector %x", data[:4])
	}
	body := data[4:]
	word := func(at int) uint64 {
		if at+32 > len(body) {
			t.Fatalf("multicall calldata ends at %d, wanted a word at %d", len(body), at)
		}
		return new(big.Int).SetBytes(body[at : at+32]).Uint64()
	}
	array := int(word(0))
	n := int(word(array))
	elements := array + 32
	calls := make([]perpl.Call3, n)
	for i := range calls {
		tuple := elements + int(word(elements+32*i))
		copy(calls[i].Target[:], body[tuple+12:tuple+32])
		calls[i].AllowFailure = word(tuple+32) == 1
		bytesAt := tuple + int(word(tuple+64))
		size := int(word(bytesAt))
		calls[i].Data = append([]byte(nil), body[bytesAt+32:bytesAt+32+size]...)
	}
	return calls
}

func encodeAggregate3Response(results []perpl.Result3) []byte {
	var tuples [][]byte
	for _, r := range results {
		ok := uint64(0)
		if r.Success {
			ok = 1
		}
		pad := (32 - len(r.Data)%32) % 32
		tuple := slices.Concat(testWord(ok), testWord(64), testWord(uint64(len(r.Data))), r.Data, make([]byte, pad))
		tuples = append(tuples, tuple)
	}
	out := slices.Concat(testWord(32), testWord(uint64(len(results))))
	offset := 32 * len(results)
	for _, tuple := range tuples {
		out = append(out, testWord(uint64(offset))...)
		offset += len(tuple)
	}
	for _, tuple := range tuples {
		out = append(out, tuple...)
	}
	return out
}

func (c *fakeChain) StorageAt(_ context.Context, addr perpl.Address, slot perpl.Hash, _ uint64) (perpl.Hash, error) {
	defer c.enter()()
	if addr.String() != exchangeText || slot != perpl.ImplementationSlot {
		c.t.Errorf("storage read %s %s", addr, slot)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var h perpl.Hash
	copy(h[12:], c.impl[:])
	return h, nil
}

func (c *fakeChain) Logs(_ context.Context, addr perpl.Address, topic perpl.Hash, from, to uint64) ([]rpc.Log, error) {
	defer c.enter()()
	if addr.String() != exchangeText || topic != perpl.TopicPositionLiquidated {
		c.t.Errorf("logs filter %s %s", addr, topic)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logWindows = append(c.logWindows, [2]uint64{from, to})
	var out []rpc.Log
	for _, l := range c.logs {
		if l.Block >= from && l.Block <= to {
			out = append(out, l)
		}
	}
	return out, nil
}

func (c *fakeChain) TransactionByHash(_ context.Context, h perpl.Hash) (rpc.Tx, error) {
	defer c.enter()()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.txLookups[h]++
	from, ok := c.txs[h]
	if !ok {
		return rpc.Tx{}, rpc.ErrNotFound
	}
	return rpc.Tx{Hash: h, From: from}, nil
}

func (c *fakeChain) PaidCalls() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paid
}

/* Liquidation fixtures */

type rawLogJSON struct {
	Topics          []string `json:"topics"`
	TransactionHash string   `json:"transactionHash"`
	BlockTimestamp  string   `json:"blockTimestamp"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	LogIndex        string   `json:"logIndex"`
}

type liqExpected struct {
	BlockNumber     string `json:"blockNumber"`
	LogIndex        string `json:"logIndex"`
	TransactionHash string `json:"transactionHash"`
	BlockTimestamp  string `json:"blockTimestamp"`
	TransactionFrom string `json:"transactionFrom"`
	Event           struct {
		PerpID       string `json:"perpId"`
		PosAccountID string `json:"posAccountId"`
		PositionType string `json:"positionType"`
		MarkPricePNS string `json:"markPricePNS"`
		LiqPricePNS  string `json:"liqPricePNS"`
		LiqLotLNS    string `json:"liqLotLNS"`
		PosLotLNS    string `json:"posLotLNS"`
		OnOrderBook  bool   `json:"onOrderBook"`
	} `json:"event"`
}

func parseQty(t testing.TB, s string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
	if err != nil {
		t.Fatalf("quantity %q: %v", s, err)
	}
	return n
}

func decUint(t testing.TB, s string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("number %q: %v", s, err)
	}
	return n
}

func decBig(t testing.TB, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("big %q", s)
	}
	return n
}

// loadTailFixtures fills the fake with the 20 recorded getLogs rows and their transactions
func (c *fakeChain) loadTailFixtures(withTimestamps bool) []liqExpected {
	t := c.t
	var windows []struct {
		Result []rawLogJSON `json:"result"`
	}
	readJSON(t, testdata("liquidations", "getlogs.raw.json"), &windows)
	var expected []liqExpected
	readJSON(t, testdata("liquidations", "expected.json"), &expected)
	var blocks map[string]struct {
		Timestamp string `json:"timestamp"`
	}
	readJSON(t, testdata("liquidations", "blocks.raw.json"), &blocks)
	for _, w := range windows {
		for _, r := range w.Result {
			l := rpc.Log{
				Address:  mustAddress(t, exchangeText),
				Data:     mustHex(t, r.Data),
				Block:    parseQty(t, r.BlockNumber),
				LogIndex: parseQty(t, r.LogIndex),
				TxHash:   mustHash(t, r.TransactionHash),
			}
			for _, tp := range r.Topics {
				l.Topics = append(l.Topics, mustHash(t, tp))
			}
			if withTimestamps {
				l.BlockTimestamp = parseQty(t, r.BlockTimestamp)
			}
			c.logs = append(c.logs, l)
		}
	}
	for _, e := range expected {
		c.txs[mustHash(t, e.TransactionHash)] = mustAddress(t, e.TransactionFrom)
		c.blockTimes[decUint(t, e.BlockNumber)] = decUint(t, e.BlockTimestamp)
	}
	for hexBlock, b := range blocks {
		c.blockTimes[parseQty(t, hexBlock)] = parseQty(t, b.Timestamp)
	}
	return expected
}

/* Fake store */

type fakeStore struct {
	mu        sync.Mutex
	markets   []store.MarketSnapshot
	positions []store.PositionSnapshot
	liqs      map[[2]uint64]store.Liquidation
	accounts  map[uint64]perpl.Address
	cursors   map[string]uint64
	pruned    []time.Time
	prunedMkt []time.Time
	oi        []store.OIPoint
	prunedOI  []time.Time
	failures  map[string]error
	writes    map[string]int
	stall     atomic.Bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		liqs:     make(map[[2]uint64]store.Liquidation),
		accounts: make(map[uint64]perpl.Address),
		cursors:  make(map[string]uint64),
		failures: make(map[string]error),
		writes:   make(map[string]int),
	}
}

func (s *fakeStore) enter(name string) error {
	s.writes[name]++
	return s.failures[name]
}

func (s *fakeStore) failOn(name string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.failures, name)
		return
	}
	s.failures[name] = err
}

// hang blocks like a database that stopped answering, until the context ends
func (s *fakeStore) hang(ctx context.Context) error {
	if s.stall.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (s *fakeStore) InsertMarketSnapshots(ctx context.Context, rows []store.MarketSnapshot) error {
	if err := s.hang(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("markets"); err != nil {
		return err
	}
	s.markets = append(s.markets, rows...)
	return nil
}

func (s *fakeStore) InsertPositionSnapshots(_ context.Context, rows []store.PositionSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("positions"); err != nil {
		return err
	}
	s.positions = append(s.positions, rows...)
	return nil
}

func (s *fakeStore) PrunePositionSnapshots(ctx context.Context, before time.Time) (int64, error) {
	if err := s.hang(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("prune"); err != nil {
		return 0, err
	}
	s.pruned = append(s.pruned, before)
	return 0, nil
}

func (s *fakeStore) InsertMarketOI(_ context.Context, rows []store.OIPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("oi"); err != nil {
		return err
	}
	s.oi = append(s.oi, rows...)
	return nil
}

func (s *fakeStore) PruneMarketOI(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("prune_oi"); err != nil {
		return 0, err
	}
	s.prunedOI = append(s.prunedOI, before)
	return 0, nil
}

func (s *fakeStore) PruneMarketSnapshots(ctx context.Context, before time.Time) (int64, error) {
	if err := s.hang(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("prune_markets"); err != nil {
		return 0, err
	}
	s.prunedMkt = append(s.prunedMkt, before)
	return 0, nil
}

func (s *fakeStore) UpsertLiquidations(_ context.Context, rows []store.Liquidation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("liquidations"); err != nil {
		return err
	}
	for _, r := range rows {
		s.liqs[[2]uint64{r.Block, r.LogIndex}] = r
	}
	return nil
}

func (s *fakeStore) CountLiquidations(context.Context) (store.LiquidationTotals, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var tot store.LiquidationTotals
	for k := range s.liqs {
		tot.Count++
		if tot.FirstBlock == 0 || k[0] < tot.FirstBlock {
			tot.FirstBlock = k[0]
		}
		tot.LastBlock = max(tot.LastBlock, k[0])
	}
	return tot, nil
}

func (s *fakeStore) UpsertAccounts(_ context.Context, rows []store.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("accounts"); err != nil {
		return err
	}
	for _, r := range rows {
		s.accounts[r.ID] = r.Address
	}
	return nil
}

func (s *fakeStore) AccountAddresses(_ context.Context, ids []uint64) (map[uint64]perpl.Address, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[uint64]perpl.Address)
	for _, id := range ids {
		if a, ok := s.accounts[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (s *fakeStore) UnresolvedLiquidationAccounts(_ context.Context, limit int) ([]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[uint64]bool{}
	var out []uint64
	for _, l := range s.liqs {
		if _, ok := s.accounts[l.AccountID]; !ok && !seen[l.AccountID] {
			seen[l.AccountID] = true
			out = append(out, l.AccountID)
		}
	}
	slices.Sort(out)
	return out[:min(limit, len(out))], nil
}

func (s *fakeStore) Cursor(_ context.Context, name string) (uint64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.cursors[name]
	return b, ok, nil
}

func (s *fakeStore) SetCursor(_ context.Context, name string, block uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.enter("cursor"); err != nil {
		return err
	}
	s.cursors[name] = block
	return nil
}

func (s *fakeStore) cursor(name string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.cursors[name]
	return b, ok
}

func (s *fakeStore) liquidations() []store.Liquidation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Liquidation, 0, len(s.liqs))
	for _, l := range s.liqs {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b store.Liquidation) int {
		if a.Block != b.Block {
			return int(a.Block) - int(b.Block)
		}
		return int(a.LogIndex) - int(b.LogIndex)
	})
	return out
}

func (s *fakeStore) marketRows() []store.MarketSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.markets)
}

func (s *fakeStore) positionRows() []store.PositionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.positions)
}

/* Fake leaderboard */

type fakeBoard struct {
	mu      sync.Mutex
	rows    map[sources.Period]sources.Leaderboard
	pnlRows map[sources.Period]sources.Leaderboard
	fail    map[boardKey]error
	calls   map[sources.Period]int
	pnlCall map[sources.Period]int
}

type boardKey struct {
	period sources.Period
	by     sources.By
}

func newFakeBoard() *fakeBoard {
	return &fakeBoard{
		rows:    make(map[sources.Period]sources.Leaderboard),
		pnlRows: make(map[sources.Period]sources.Leaderboard),
		fail:    make(map[boardKey]error),
		calls:   make(map[sources.Period]int),
		pnlCall: make(map[sources.Period]int),
	}
}

func (b *fakeBoard) Board(_ context.Context, p sources.Period, by sources.By) (sources.Leaderboard, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.fail[boardKey{p, by}]; err != nil {
		return sources.Leaderboard{}, err
	}
	if by == sources.ByPnl {
		b.pnlCall[p]++
		return b.pnlRows[p], nil
	}
	b.calls[p]++
	return b.rows[p], nil
}

// setFail makes the volume board of a period fail
func (b *fakeBoard) setFail(p sources.Period, err error) { b.setFailBy(p, sources.ByVolume, err) }

func (b *fakeBoard) setFailBy(p sources.Period, by sources.By, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail[boardKey{p, by}] = err
}

/* HyperSync over httptest */

// hyperSyncServer replays the recorded page for the first request and answers later ones with an empty range
func hyperSyncServer(t *testing.T) (*sources.HyperSync, *atomic.Int32) {
	t.Helper()
	recorded, err := os.ReadFile(testdata("hypersync", "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		var q struct {
			FromBlock uint64 `json:"from_block"`
			ToBlock   uint64 `json:"to_block"`
		}
		if err := json.Unmarshal(body, &q); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if q.FromBlock == 102493417 {
			_, _ = w.Write(recorded)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"logs":[],"blocks":[],"transactions":[]}],"archive_height":%d,"next_block":%d}`, pinnedBlock, q.ToBlock)
	}))
	t.Cleanup(srv.Close)
	backoff := sources.Backoff{Initial: time.Millisecond, Max: time.Millisecond, Attempts: 2, Sleep: func(context.Context, time.Duration) error { return nil }}
	return sources.NewHyperSync(srv.URL, config.Secret("test-key"), sources.WithBackoff(backoff)), &requests
}

// scanFunc adapts a function to the Scanner interface
type scanFunc func(ctx context.Context, q sources.Query, fn func(sources.Page) error) (uint64, error)

func (f scanFunc) Scan(ctx context.Context, q sources.Query, fn func(sources.Page) error) (uint64, error) {
	return f(ctx, q, fn)
}

/* Runner wiring */

type rig struct {
	t     *testing.T
	chain *fakeChain
	store *fakeStore
	board *fakeBoard
	cfg   config.Config
	now   *atomic.Int64
	reg   *prometheus.Registry
	r     *Runner
}

func testConfig() config.Config {
	return config.Config{
		Chain:                 "eip155:143",
		Exchange:              exchangeText,
		RPCURL:                "http://router.invalid",
		RefreshInterval:       30 * time.Second,
		SlowRefreshInterval:   10 * time.Minute,
		PositionPageSize:      100,
		MaxInflightCalls:      4,
		Shocks:                []float64{-0.2, -0.1, -0.05, -0.03, 0.03, 0.05, 0.1, 0.2},
		LeaderboardInterval:   10 * time.Minute,
		DeployBlock:           102493417,
		PositionSnapshotEvery: 2,
		PositionRetention:     168 * time.Hour,
		HyperSyncKey:          "test-key",
	}
}

var epoch = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func newRig(t *testing.T, mutate ...func(*Options)) *rig {
	t.Helper()
	g := &rig{t: t, chain: newFakeChain(t), store: newFakeStore(), board: newFakeBoard(), cfg: testConfig(), now: new(atomic.Int64), reg: prometheus.NewRegistry()}
	g.now.Store(epoch.UnixNano())
	opts := Options{
		Config:     g.cfg,
		Chain:      g.chain,
		Store:      g.store,
		Board:      g.board,
		Registerer: g.reg,
		Logger:     quietLogger(),
		Now:        func() time.Time { return time.Unix(0, g.now.Load()).UTC() },
	}
	for _, m := range mutate {
		m(&opts)
	}
	g.cfg = opts.Config
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	return g
}

func (g *rig) advance(d time.Duration) { g.now.Add(int64(d)) }

func (g *rig) refresh() *Snapshot {
	g.t.Helper()
	if err := g.r.refreshOnce(context.Background()); err != nil {
		g.t.Fatalf("refresh: %v", err)
	}
	s := g.r.Latest()
	if s == nil {
		g.t.Fatal("no snapshot after refresh")
	}
	return s
}

func fixturePerpIDs(t testing.TB) []uint64 {
	t.Helper()
	var c chainJSON
	readJSON(t, testdata("chain.json"), &c)
	return c.PerpIDs
}

func lookup(path string) (os.FileInfo, error) { return os.Stat(path) }

func selector(t testing.TB, name string) string {
	t.Helper()
	var sel struct {
		Functions map[string]string `json:"functions"`
	}
	readJSON(t, testdata("selectors.json"), &sel)
	s, ok := sel.Functions[name]
	if !ok {
		t.Fatalf("no selector for %s", name)
	}
	return s
}

type series struct {
	value float64
	count uint64
	kind  string
}

// find returns the first series of a family whose labels include the wanted ones
func find(t testing.TB, reg *prometheus.Registry, name string, labels map[string]string) (series, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			for k, v := range labels {
				found := false
				for _, l := range m.GetLabel() {
					if l.GetName() == k && l.GetValue() == v {
						found = true
					}
				}
				if !found {
					continue next
				}
			}
			switch {
			case m.Counter != nil:
				return series{value: m.GetCounter().GetValue(), kind: "counter"}, true
			case m.Gauge != nil:
				return series{value: m.GetGauge().GetValue(), kind: "gauge"}, true
			case m.Histogram != nil:
				return series{count: m.GetHistogram().GetSampleCount(), kind: "histogram"}, true
			}
		}
	}
	return series{}, false
}

// metricValue reads a counter or gauge and returns 0 when the series does not exist yet
func metricValue(t testing.TB, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	s, _ := find(t, reg, name, labels)
	return s.value
}

func histogramCount(t testing.TB, reg *prometheus.Registry, name string) uint64 {
	t.Helper()
	s, _ := find(t, reg, name, nil)
	return s.count
}

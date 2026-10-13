package server

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/sources"
	"perps/internal/store"
	"perps/internal/testkit"
)

const (
	base      = "/internal/v1/chains/eip155:143/perpl"
	snapBlock = 112777356
)

var snapTime = time.Date(2026, 10, 12, 12, 0, 17, 0, time.UTC)

func cns(usd int64) *big.Int { return new(big.Int).Mul(big.NewInt(usd), big.NewInt(1_000_000)) }

var exchangeInfo = perpl.ExchangeInfo{
	BalanceCNS: cns(3_331_560), ProtocolBalanceCNS: cns(172_856), RecycleBalanceCNS: new(big.Int),
	CollateralDecimals: 6, FundingInterval: 8571,
}

var liquidationInfo = perpl.LiquidationInfo{
	LiqInsAmtPer100K: 10000, LiqUserAmtPer100K: 80000, LiqProtocolAmtPer100K: 10000, BtlPriceThreshPer100K: 95000,
	BtlInsAmtPer100K: 25000, BtlUserAmtPer100K: 25000, BtlBuyerAmtPer100K: 25000, BtlProtocolAmtPer100K: 25000, BtlRestrictBuyers: true,
}

func tstMarket() perpl.Market {
	return perpl.Market{
		PerpID: 7, Name: "Test Perp", Symbol: "TST", PriceDecimals: 2, LotDecimals: 3, Status: 4, RefPriceMaxAgeSec: 60,
		MarkPNS: big.NewInt(10000), MarkTimestamp: uint64(snapTime.Unix()) - 3, OraclePNS: big.NewInt(10050),
		OracleTimestampSec:  uint64(snapTime.Unix()) - 20,
		LongOpenInterestLNS: big.NewInt(15000), ShortOpenInterestLNS: big.NewInt(15000),
		PositionBalanceCNS: cns(300), InsuranceBalanceCNS: cns(25), FundingRatePct100k: 2, AbsFundingClampPctPer100K: 10,
		MaintMarginFracHdths: 2000, InitMarginFracHdths: 1000, Liquidation: liquidationInfo,
	}
}

func altMarket() perpl.Market {
	return perpl.Market{
		PerpID: 12, Name: "Alt Perp", Symbol: "ALT", PriceDecimals: 1, LotDecimals: 0, Status: 4, RefPriceMaxAgeSec: 60,
		MarkPNS: big.NewInt(500), MarkTimestamp: uint64(snapTime.Unix()) - 3, OraclePNS: big.NewInt(500),
		OracleTimestampSec:  uint64(snapTime.Unix()) - 5,
		LongOpenInterestLNS: big.NewInt(4), ShortOpenInterestLNS: big.NewInt(4),
		PositionBalanceCNS: cns(40), InsuranceBalanceCNS: cns(5), FundingRatePct100k: -4, AbsFundingClampPctPer100K: 10,
		MaintMarginFracHdths: 2500, InitMarginFracHdths: 1500, Liquidation: liquidationInfo,
	}
}

func offMarket() perpl.Market {
	return perpl.Market{
		PerpID: 30, Name: "Old Perp", Symbol: "OLD", PriceDecimals: 2, LotDecimals: 3, Status: 0, MarkPNS: new(big.Int), OraclePNS: new(big.Int),
		LongOpenInterestLNS: new(big.Int), ShortOpenInterestLNS: new(big.Int), PositionBalanceCNS: new(big.Int), InsuranceBalanceCNS: new(big.Int),
		MaintMarginFracHdths: 2000, InitMarginFracHdths: 1000,
	}
}

func position(perp, account uint64, long bool, lotLNS, pricePNS, depositUSD int64) perpl.Position {
	return perpl.Position{
		PerpID: perp, AccountID: account, Long: long, LotLNS: big.NewInt(lotLNS), PricePNS: big.NewInt(pricePNS),
		DepositCNS: cns(depositUSD), PremiumPnlCNS: new(big.Int), PnlCNS: new(big.Int), DeltaPnlCNS: new(big.Int),
	}
}

func assessAll(t testing.TB, m perpl.Market, ps ...perpl.Position) risk.MarketBook {
	t.Helper()
	book := risk.MarketBook{Market: m, Positions: []risk.Assessed{}}
	for _, p := range ps {
		a, err := risk.Assess(m, exchangeInfo, p)
		if err != nil {
			t.Fatal(err)
		}
		book.Positions = append(book.Positions, a)
	}
	return book
}

// testSnapshot is small enough to check by hand and its account 104 has a thinner deposit than 103, so a fall and a rise differ
func testSnapshot(t testing.TB) *Snapshot {
	return &Snapshot{
		Block: snapBlock, Time: snapTime, Exchange: exchangeInfo,
		Books: []risk.MarketBook{
			assessAll(t, tstMarket(),
				position(7, 101, true, 10000, 10000, 100),
				position(7, 102, false, 10000, 10000, 100),
				position(7, 103, true, 5000, 10000, 40),
				position(7, 104, false, 5000, 10000, 30)),
			assessAll(t, altMarket(),
				position(12, 101, true, 4, 500, 20),
				position(12, 100, false, 4, 500, 20)),
			assessAll(t, offMarket()),
		},
	}
}

func row(rank int, addr perpl.Address, pnlUSD, volUSD int64) sources.LeaderboardRow {
	return sources.LeaderboardRow{Rank: rank, Address: addr, PnlCNS: cns(pnlUSD), VolumeCNS: cns(volUSD)}
}

var (
	accountAddr = testkit.Addr(101)
	unknownAddr = testkit.Addr(999)
	emptyAddr   = testkit.Addr(555)
)

type fakeSource struct {
	mu       sync.Mutex
	snap     *Snapshot
	rt       Runtime
	ready    bool
	boards   map[sources.Period]sources.Leaderboard
	pnlBoard map[sources.Period]sources.Leaderboard
	accounts map[perpl.Address]perpl.Account
	addrs    map[uint64]perpl.Address
	liqs     []store.Liquidation
	daily    []store.DailyLiquidations
	funding  []store.FundingPoint
	keepers  []store.KeeperCount
	totals   store.LiquidationTotals
	oi       []store.OIPoint
	candles  []sources.Candle
	events   []sources.FundingEvent

	err        error
	errFor     map[string]error
	accountErr error

	filter       store.LiquidationFilter
	dailyPerp    *uint64
	dailySince   time.Time
	fundingPerp  uint64
	fundingSince time.Time
	fundingStep  time.Duration
	keeperLimit  int
	idsAsked     []uint64
	storeCalls   int
	oiMarket     uint64
	oiSince      time.Time
	oiStep       time.Duration
	histMarket   uint64
	histHours    int
}

func newFake(t testing.TB) *fakeSource {
	day := sources.Leaderboard{At: sources.Stamp{Block: snapBlock, Time: snapTime}, Rows: []sources.LeaderboardRow{
		row(1, testkit.Addr(500), 123, 1000), row(2, accountAddr, -5, 400), row(3, testkit.Addr(501), 7, 100),
	}}
	all := sources.Leaderboard{At: sources.Stamp{Block: snapBlock, Time: snapTime}, Rows: []sources.LeaderboardRow{
		row(1, testkit.Addr(502), 9000, 50000), row(2, testkit.Addr(500), 800, 30000), row(17, accountAddr, 12, 20000),
	}}
	return &fakeSource{
		snap: testSnapshot(t),
		rt: Runtime{
			Exchange: mustAddr("0x34b6552d57a35a1d042ccae1951bd1c370112a6f"), Implementation: mustAddr("0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a"),
			Backfill: BackfillComplete,
		},
		ready:  true,
		boards: map[sources.Period]sources.Leaderboard{sources.PeriodDay: day, sources.PeriodAll: all},
		accounts: map[perpl.Address]perpl.Account{
			accountAddr:       {AccountID: 101, BalanceCNS: cns(25), LockedBalanceCNS: cns(5), Address: accountAddr},
			testkit.Addr(555): {AccountID: 555, BalanceCNS: cns(1), LockedBalanceCNS: new(big.Int), Frozen: 1, Address: testkit.Addr(555)},
		},
		addrs: map[uint64]perpl.Address{101: accountAddr, 102: testkit.Addr(102), 103: testkit.Addr(103), 1000: testkit.Addr(1000)},
		liqs:  feed(),
		daily: []store.DailyLiquidations{
			{Day: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), Count: 2, NotionalUSD: "1500.5"},
			{Day: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Count: 1, NotionalUSD: "300"},
		},
		funding: []store.FundingPoint{
			{Block: 100, Time: snapTime.Add(-time.Hour), Mark: "100", Oracle: "100.5", FundingRatePct100k: 2},
			{Block: 200, Time: snapTime.Add(-30 * time.Minute), Mark: "101", Oracle: "100", FundingRatePct100k: -4},
			{Block: 300, Time: snapTime, Mark: "100", Oracle: "0", FundingRatePct100k: 0},
		},
		keepers: []store.KeeperCount{{Keeper: testkit.Addr(1), Liquidations: 30}, {Keeper: testkit.Addr(2), Liquidations: 10}},
		totals:  store.LiquidationTotals{Count: 40, FirstBlock: 54_773_100, LastBlock: 112_777_000},
	}
}

func mustAddr(s string) perpl.Address {
	a, err := perpl.ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

func feed() []store.Liquidation {
	var out []store.Liquidation
	for i := range 5 {
		l := testkit.Liquidation(uint64(112_000_000-i*10), uint64(i%2), 7)
		switch i {
		case 2:
			l.PerpID = 99
		case 3:
			l.PerpID = 30
		}
		if i == 4 {
			l.Keeper = perpl.Address{}
		}
		out = append(out, l)
	}
	return out
}

func (f *fakeSource) Snapshot() *Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeSource) Runtime() Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rt
}

func (f *fakeSource) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeSource) Leaderboard(p sources.Period) (sources.Leaderboard, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boards[p]
	return b, ok
}

func (f *fakeSource) Board(p sources.Period, by sources.By) (sources.Leaderboard, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if by == sources.ByPnl {
		b, ok := f.pnlBoard[p]
		return b, ok
	}
	b, ok := f.boards[p]
	return b, ok
}

func (f *fakeSource) OpenInterestSeries(ctx context.Context, market uint64, since time.Time, step time.Duration) ([]store.OIPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oiMarket, f.oiSince, f.oiStep = market, since, step
	if err := f.call(ctx, "OpenInterestSeries"); err != nil {
		return nil, err
	}
	return f.oi, nil
}

func (f *fakeSource) Candles(ctx context.Context, market uint64, hours int) ([]sources.Candle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histMarket, f.histHours = market, hours
	if err := f.call(ctx, "Candles"); err != nil {
		return nil, err
	}
	return f.candles, nil
}

func (f *fakeSource) FundingEvents(ctx context.Context, market uint64, hours int) ([]sources.FundingEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histMarket, f.histHours = market, hours
	if err := f.call(ctx, "FundingEvents"); err != nil {
		return nil, err
	}
	return f.events, nil
}

func (f *fakeSource) Account(ctx context.Context, addr perpl.Address) (perpl.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return perpl.Account{}, err
	}
	if f.accountErr != nil {
		return perpl.Account{}, f.accountErr
	}
	a, ok := f.accounts[addr]
	if !ok {
		return perpl.Account{}, fmt.Errorf("resolve %s: %w", addr, ErrAccountNotFound)
	}
	return a, nil
}

// call records one store call and returns the injected error, or the error of a context that ended
func (f *fakeSource) call(ctx context.Context, name string) error {
	f.storeCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if err, ok := f.errFor[name]; ok {
		return err
	}
	return f.err
}

func (f *fakeSource) AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idsAsked = slices.Clone(ids)
	if err := f.call(ctx, "AccountAddresses"); err != nil {
		return nil, err
	}
	out := map[uint64]perpl.Address{}
	for _, id := range ids {
		if a, ok := f.addrs[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (f *fakeSource) Liquidations(ctx context.Context, flt store.LiquidationFilter) (store.LiquidationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filter = flt
	if err := f.call(ctx, "Liquidations"); err != nil {
		return store.LiquidationPage{}, err
	}
	page := store.LiquidationPage{Items: []store.Liquidation{}}
	for _, l := range f.liqs {
		if flt.PerpID != nil && l.PerpID != *flt.PerpID {
			continue
		}
		if flt.After != nil && (l.Block > flt.After.Block || l.Block == flt.After.Block && l.LogIndex >= flt.After.LogIndex) {
			continue
		}
		if len(page.Items) == flt.Limit {
			last := page.Items[len(page.Items)-1]
			page.Next = &store.LiquidationCursor{Block: last.Block, LogIndex: last.LogIndex}
			break
		}
		page.Items = append(page.Items, l)
	}
	return page, nil
}

func (f *fakeSource) LiquidationsDaily(ctx context.Context, perp *uint64, since time.Time) ([]store.DailyLiquidations, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dailyPerp, f.dailySince = perp, since
	if err := f.call(ctx, "LiquidationsDaily"); err != nil {
		return nil, err
	}
	return f.daily, nil
}

func (f *fakeSource) FundingSeries(ctx context.Context, perp uint64, since time.Time, step time.Duration) ([]store.FundingPoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fundingPerp, f.fundingSince, f.fundingStep = perp, since, step
	if err := f.call(ctx, "FundingSeries"); err != nil {
		return nil, err
	}
	return f.funding, nil
}

func (f *fakeSource) KeeperCounts(ctx context.Context, limit int) ([]store.KeeperCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keeperLimit = limit
	if err := f.call(ctx, "KeeperCounts"); err != nil {
		return nil, err
	}
	return f.keepers, nil
}

func (f *fakeSource) CountLiquidations(ctx context.Context) (store.LiquidationTotals, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(ctx, "CountLiquidations"); err != nil {
		return store.LiquidationTotals{}, err
	}
	return f.totals, nil
}

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

func testHandler(src Source) http.Handler {
	return New(src, NewMetrics(src), Options{Now: func() time.Time { return snapTime.Add(time.Second) }})
}

type reply struct {
	*httptest.ResponseRecorder
	body any
}

func fetch(t testing.TB, h http.Handler, path string, header ...string) reply {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := reply{ResponseRecorder: rec}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &r.body); err != nil {
			t.Fatalf("%s: body is no JSON: %v\n%s", path, err, rec.Body)
		}
	}
	return r
}

func dig(t testing.TB, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("at %q: %T is no object (%v)", k, v, v)
			}
			next, found := m[k]
			if !found {
				t.Fatalf("no key %q in %v", k, m)
			}
			v = next
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				t.Fatalf("at %d: %T of %d is no array that long", k, v, len(a))
			}
			v = a[k]
		}
	}
	return v
}

// at digs into the record, which is the whole body
func (r reply) at(t testing.TB, path ...any) any {
	t.Helper()
	return dig(t, r.body, path...)
}

func (r reply) code(t testing.TB) string {
	t.Helper()
	return dig(t, r.body, "code").(string)
}

func (r reply) coverage(t testing.TB) []string {
	t.Helper()
	out := []string{}
	for _, c := range r.at(t, "coverage").([]any) {
		out = append(out, c.(string))
	}
	return out
}

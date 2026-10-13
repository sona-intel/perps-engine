package server

import (
	"encoding/json/v2"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/sources"
	"perps/internal/testkit"
)

const testdata = "../../testdata"

func readJSON(t testing.TB, rel string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdata, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func bigOf(t testing.TB, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("%q is no integer", s)
	}
	return n
}

func uintOf(t testing.TB, s string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func fixtureMarket(t testing.TB, perp uint64) perpl.Market {
	t.Helper()
	var info struct {
		Info map[string]any `json:"perpetualInfo"`
	}
	readJSON(t, fmt.Sprintf("calls/getPerpetualInfoV2.perp%d.expected.json", perp), &info)
	var mf map[string]string
	readJSON(t, fmt.Sprintf("calls/getMarginFractions.perp%d.lot0.expected.json", perp), &mf)
	var liq struct {
		Info map[string]any `json:"liquidationInfo"`
	}
	readJSON(t, fmt.Sprintf("calls/getLiquidationInfo.perp%d.expected.json", perp), &liq)
	i := map[string]string{}
	for k, v := range info.Info {
		if str, ok := v.(string); ok {
			i[k] = str
		}
	}
	pct, err := strconv.ParseInt(i["fundingRatePct100k"], 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	per := func(k string) uint64 { return uintOf(t, liq.Info[k].(string)) }
	return perpl.Market{
		PerpID: perp, Name: i["name"], Symbol: i["symbol"], PriceDecimals: uint8(uintOf(t, i["priceDecimals"])), LotDecimals: uint8(uintOf(t, i["lotDecimals"])),
		Status: uint8(uintOf(t, i["status"])), RefPriceMaxAgeSec: uintOf(t, i["refPriceMaxAgeSec"]), MarkPNS: bigOf(t, i["markPNS"]),
		MarkTimestamp: uintOf(t, i["markTimestamp"]), OraclePNS: bigOf(t, i["oraclePNS"]), OracleTimestampSec: uintOf(t, i["oracleTimestampSec"]),
		LongOpenInterestLNS: bigOf(t, i["longOpenInterestLNS"]), ShortOpenInterestLNS: bigOf(t, i["shortOpenInterestLNS"]),
		PositionBalanceCNS: bigOf(t, i["positionBalanceCNS"]), InsuranceBalanceCNS: bigOf(t, i["insuranceBalanceCNS"]),
		FundingRatePct100k: int16(pct), AbsFundingClampPctPer100K: uintOf(t, i["absFundingClampPctPer100K"]),
		MaintMarginFracHdths: uintOf(t, mf["perpMaintMarginFracHdths"]), InitMarginFracHdths: uintOf(t, mf["perpInitMarginFracHdths"]),
		Liquidation: perpl.LiquidationInfo{
			LiqInsAmtPer100K: per("liqInsAmtPer100K"), LiqUserAmtPer100K: per("liqUserAmtPer100K"), LiqProtocolAmtPer100K: per("liqProtocolAmtPer100K"),
			BtlPriceThreshPer100K: per("btlPriceThreshPer100K"), BtlInsAmtPer100K: per("btlInsAmtPer100K"), BtlUserAmtPer100K: per("btlUserAmtPer100K"),
			BtlBuyerAmtPer100K: per("btlBuyerAmtPer100K"), BtlProtocolAmtPer100K: per("btlProtocolAmtPer100K"),
			BtlRestrictBuyers: liq.Info["btlRestrictBuyers"].(bool),
		},
	}
}

func fixtureSnapshot(t testing.TB) *Snapshot {
	t.Helper()
	var chain struct {
		PinnedBlock     uint64   `json:"pinned_block"`
		PinnedTimestamp int64    `json:"pinned_block_timestamp"`
		PerpIDs         []uint64 `json:"perp_ids"`
	}
	readJSON(t, "chain.json", &chain)
	var ex map[string]string
	readJSON(t, "calls/getExchangeInfo.expected.json", &ex)
	var fi map[string]string
	readJSON(t, "calls/getFundingInterval.expected.json", &fi)
	ei := perpl.ExchangeInfo{
		BalanceCNS: bigOf(t, ex["balanceCNS"]), ProtocolBalanceCNS: bigOf(t, ex["protocolBalanceCNS"]), RecycleBalanceCNS: bigOf(t, ex["recycleBalanceCNS"]),
		CollateralDecimals: uint8(uintOf(t, ex["collateralDecimals"])), FundingInterval: uintOf(t, fi["fundingInterval"]),
	}
	s := &Snapshot{Block: chain.PinnedBlock, Time: time.Unix(chain.PinnedTimestamp, 0).UTC(), Exchange: ei}
	for _, id := range chain.PerpIDs {
		m := fixtureMarket(t, id)
		book := risk.MarketBook{Market: m, Positions: []risk.Assessed{}}
		var derived struct {
			Positions []map[string]any `json:"positions"`
		}
		if _, err := os.Stat(filepath.Join(testdata, fmt.Sprintf("derived/positions.perp%d.json", id))); err == nil {
			readJSON(t, fmt.Sprintf("derived/positions.perp%d.json", id), &derived)
		}
		for _, p := range derived.Positions {
			str := func(k string) string { return p[k].(string) }
			a, err := risk.Assess(m, ei, perpl.Position{
				PerpID: id, AccountID: uintOf(t, str("account_id")), Long: str("position_type") == "0", DepositCNS: bigOf(t, str("deposit_cns")),
				PricePNS: bigOf(t, str("price_pns")), LotLNS: bigOf(t, str("lot_lns")), PremiumPnlCNS: bigOf(t, str("premium_pnl_cns")),
				PriceResiduePNSQ16: uintOf(t, str("price_residue_pnsq16")),
			})
			if err != nil {
				t.Fatal(err)
			}
			book.Positions = append(book.Positions, a)
		}
		s.Books = append(s.Books, book)
	}
	return s
}

func fixtureSource(t testing.TB) *fakeSource {
	t.Helper()
	f := newFake(t)
	f.snap = fixtureSnapshot(t)
	var acct struct {
		Info map[string]any `json:"accountInfo"`
	}
	readJSON(t, "calls/getAccountByAddr.btc_top.expected.json", &acct)
	addr, err := perpl.ParseAddress(acct.Info["accountAddr"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f.accounts = map[perpl.Address]perpl.Account{addr: {
		AccountID: uintOf(t, acct.Info["accountId"].(string)), BalanceCNS: bigOf(t, acct.Info["balanceCNS"].(string)),
		LockedBalanceCNS: bigOf(t, acct.Info["lockedBalanceCNS"].(string)), Address: addr,
	}}
	f.boards = map[sources.Period]sources.Leaderboard{
		sources.PeriodDay: {At: sources.Stamp{Block: f.snap.Block, Time: f.snap.Time}, Rows: []sources.LeaderboardRow{row(1, addr, 1200, 90_000)}},
		sources.PeriodAll: {At: sources.Stamp{Block: f.snap.Block, Time: f.snap.Time}, Rows: []sources.LeaderboardRow{row(3, addr, 4800, 700_000)}},
	}
	f.addrs = map[uint64]perpl.Address{f.accounts[addr].AccountID: addr}
	f.accounts[emptyAddr] = perpl.Account{
		AccountID: 555, BalanceCNS: cns(12), LockedBalanceCNS: new(big.Int), Frozen: 1, Address: emptyAddr,
	}
	f.liqs = nil
	for i, perp := range []uint64{1, 10, 1, 20, 30} {
		f.liqs = append(f.liqs, testkit.Liquidation(112_000_000-uint64(i)*10, uint64(i%2), perp))
	}
	return f
}

func TestFixtureSnapshotMatchesTheRecordedChain(t *testing.T) {
	s := fixtureSnapshot(t)
	if len(s.Books) != 17 || s.positions() != 665 {
		t.Fatalf("%d markets, %d positions", len(s.Books), s.positions())
	}
}

package perpl

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	maxUint256 = "115792089237316195423570985008687907853269984665640564039457584007913129639935"
	minInt256  = "-57896044618658097711785492504343953926634992332820282019728792003956564819968"
)

func bigOf(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return n
}

func sampleAddress(t *testing.T, s string) Address {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sampleHash(t *testing.T, s string) Hash {
	t.Helper()
	h, err := ParseHash(s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

/* Address and Hash */

func TestAddressParsesAnyCaseAndPrintsLowercase(t *testing.T) {
	const mixed = "0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"
	a, err := ParseAddress(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.String(), strings.ToLower(mixed); got != want {
		t.Fatalf("String = %s, want %s", got, want)
	}
	if a.IsZero() || !(Address{}).IsZero() {
		t.Fatal("IsZero")
	}
	if a[0] != 0x34 || a[19] != 0x6f {
		t.Fatalf("bytes %x", a[:])
	}
}

func TestAddressRejects(t *testing.T) {
	for _, s := range []string{
		"", "0x", "34b6552d57a35a1d042ccae1951bd1c370112a6f", "0x34b6552d57a35a1d042ccae1951bd1c370112a6",
		"0x34b6552d57a35a1d042ccae1951bd1c370112a6f0", "0xZ4b6552d57a35a1d042ccae1951bd1c370112a6f",
		" 0x34b6552d57a35a1d042ccae1951bd1c370112a6f",
	} {
		if a, err := ParseAddress(s); err == nil {
			t.Errorf("%q accepted as %s", s, a)
		}
	}
}

func TestHashParsesAndRejects(t *testing.T) {
	const tx = "0xc8d1dd61fcd0518b8debaab082f8e7db6d54fe25d350e8462faedbfd3efde1b3"
	if h, err := ParseHash(tx[2:]); err == nil {
		t.Fatalf("hash without prefix accepted: %s", h)
	}
	h, err := ParseHash(tx)
	if err != nil {
		t.Fatal(err)
	}
	if h.String() != tx || h.IsZero() || !(Hash{}).IsZero() {
		t.Fatalf("hash %s", h)
	}
	for _, s := range []string{"", "0x", tx[:len(tx)-1], tx + "0", "0xzz" + tx[4:]} {
		if _, err := ParseHash(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestAddressAndHashAreJSONStrings(t *testing.T) {
	type pair struct {
		A Address `json:"a"`
		H Hash    `json:"h"`
	}
	in := pair{
		A: sampleAddress(t, "0x8dea7a0100000000000000000000000000009f33"),
		H: sampleHash(t, "0xc8d1dd61fcd0518b8debaab082f8e7db6d54fe25d350e8462faedbfd3efde1b3"),
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":"0x8dea7a0100000000000000000000000000009f33","h":"0xc8d1dd61fcd0518b8debaab082f8e7db6d54fe25d350e8462faedbfd3efde1b3"}`
	if string(body) != want {
		t.Fatalf("got %s", body)
	}
	var out pair
	if err := json.Unmarshal(body, &out); err != nil || out != in {
		t.Fatal(out, err)
	}
	if err := json.Unmarshal([]byte(`{"a":"0x12"}`), &out); err == nil {
		t.Fatal("short address accepted")
	}
	if err := json.Unmarshal([]byte(`{"h":"nope"}`), &out); err == nil {
		t.Fatal("bad hash accepted")
	}
}

/* Round trips with the widest values */

func sampleMarket(t *testing.T) Market {
	return Market{
		PerpID: 1, Name: "BTC Perp", Symbol: "BTC", PriceDecimals: 1, LotDecimals: 5, Status: 1, RefPriceMaxAgeSec: 60,
		MarkPNS: bigOf(t, "841448"), MarkTimestamp: 1771139902, OraclePNS: bigOf(t, maxUint256), OracleTimestampSec: 1771139900,
		LongOpenInterestLNS: bigOf(t, "1664000"), ShortOpenInterestLNS: bigOf(t, "1664000"),
		PositionBalanceCNS: bigOf(t, "123456789"), InsuranceBalanceCNS: bigOf(t, "182681000000"),
		FundingRatePct100k: -40, AbsFundingClampPctPer100K: 10_000,
		MaintMarginFracHdths: 2500, InitMarginFracHdths: 1500,
		Liquidation: LiquidationInfo{
			LiqInsAmtPer100K: 10_000, LiqUserAmtPer100K: 80_000, LiqProtocolAmtPer100K: 10_000,
			BtlPriceThreshPer100K: 5_000, BtlInsAmtPer100K: 1, BtlUserAmtPer100K: 2, BtlBuyerAmtPer100K: 3, BtlProtocolAmtPer100K: 4,
			BtlRestrictBuyers: true,
		},
	}
}

func samplePosition(t *testing.T) Position {
	return Position{
		PerpID: 1, AccountID: 66, NextNodeID: 71, PrevNodeID: 0, Long: true,
		DepositCNS: bigOf(t, "305263082"), PricePNS: bigOf(t, "841448"), LotLNS: bigOf(t, "11212"),
		EntryBlock: 57726420, PnlCNS: bigOf(t, minInt256), DeltaPnlCNS: bigOf(t, "-54009600"), PremiumPnlCNS: bigOf(t, "-1"),
		PriceResiduePNSQ16: 65535,
	}
}

func sampleAccount(t *testing.T) Account {
	return Account{
		AccountID: 5, BalanceCNS: bigOf(t, "106491"), LockedBalanceCNS: bigOf(t, "0"), Frozen: 0,
		Address:       sampleAddress(t, "0x8dea7a0100000000000000000000000000009f33"),
		PositionBanks: [4]*big.Int{bigOf(t, "2"), bigOf(t, "0"), bigOf(t, maxUint256), bigOf(t, "0")},
	}
}

func sampleLiquidation(t *testing.T) Liquidation {
	return Liquidation{
		Block: 55493930, LogIndex: 17, TxHash: sampleHash(t, "0xc8d1dd61fcd0518b8debaab082f8e7db6d54fe25d350e8462faedbfd3efde1b3"),
		Time: time.Unix(1771139902, 0).UTC(), Keeper: sampleAddress(t, "0xb3b0f0a100000000000000000000000000074a3a"),
		PerpID: 1, AccountID: 5, Long: false,
		MarkPricePNS: bigOf(t, "706000"), LiqPricePNS: bigOf(t, "707176"), LiqLotLNS: bigOf(t, "10"), PosLotLNS: bigOf(t, "0"),
		DeltaPnlCNS: bigOf(t, "-378790"), FundingCNS: bigOf(t, "-12"), PosAmountCNS: bigOf(t, "-698268"), PosDepositCNS: bigOf(t, "0"),
		AccAmountCNS: bigOf(t, "106491"), AccBalanceCNS: bigOf(t, maxUint256), OnOrderBook: true,
	}
}

func roundTrip[T any](t *testing.T, in T) []byte {
	t.Helper()
	first, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(first, &out); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("round trip changed the value\n%s\n%s", first, second)
	}
	return first
}

func TestJSONRoundTripKeepsEveryDigit(t *testing.T) {
	var m Market
	if err := json.Unmarshal(roundTrip(t, sampleMarket(t)), &m); err != nil {
		t.Fatal(err)
	}
	if m.OraclePNS.String() != maxUint256 {
		t.Fatalf("oracle lost digits: %s", m.OraclePNS)
	}
	var p Position
	if err := json.Unmarshal(roundTrip(t, samplePosition(t)), &p); err != nil {
		t.Fatal(err)
	}
	if p.PnlCNS.String() != minInt256 || !p.Long || p.PriceResiduePNSQ16 != 65535 {
		t.Fatalf("position %+v", p)
	}
	var a Account
	if err := json.Unmarshal(roundTrip(t, sampleAccount(t)), &a); err != nil {
		t.Fatal(err)
	}
	if a.PositionBanks[2].String() != maxUint256 {
		t.Fatalf("banks %v", a.PositionBanks)
	}
	var l Liquidation
	if err := json.Unmarshal(roundTrip(t, sampleLiquidation(t)), &l); err != nil {
		t.Fatal(err)
	}
	if l.AccBalanceCNS.String() != maxUint256 || !l.Time.Equal(time.Unix(1771139902, 0)) || l.Long {
		t.Fatalf("liquidation %+v", l)
	}
	var e ExchangeInfo
	in := ExchangeInfo{
		BalanceCNS: bigOf(t, maxUint256), ProtocolBalanceCNS: bigOf(t, "1"), RecycleBalanceCNS: bigOf(t, "0"), CollateralDecimals: 6,
		CollateralToken: sampleAddress(t, "0x754704bc059f8c67012fed69bc8a327a5aafb603"), VerifierProxy: Address{1}, FundingInterval: 6450,
	}
	if err := json.Unmarshal(roundTrip(t, in), &e); err != nil || e.BalanceCNS.String() != maxUint256 {
		t.Fatal(e, err)
	}
	var page PositionPage
	pageIn := PositionPage{
		Positions: []Position{samplePosition(t)}, NumPositions: 1, MarkPricePNS: bigOf(t, "841448"), MarkPriceValid: true,
	}
	if err := json.Unmarshal(roundTrip(t, pageIn), &page); err != nil || len(page.Positions) != 1 || !page.MarkPriceValid {
		t.Fatal(page, err)
	}
}

/* The frozen contract */

func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]jsontext.Value
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	keys := make(map[string]bool, len(raw))
	for k := range raw {
		keys[k] = true
	}
	return keys
}

func TestFrozenJSONFieldNames(t *testing.T) {
	cases := []struct {
		name  string
		value any
		keys  string
	}{
		{"ExchangeInfo", ExchangeInfo{}, "balance_cns protocol_balance_cns recycle_balance_cns collateral_decimals collateral_token verifier_proxy funding_interval"},
		{"Market", sampleMarket(t), "perp_id name symbol price_decimals lot_decimals status ref_price_max_age_sec mark_pns mark_timestamp oracle_pns oracle_timestamp_sec " +
			"long_open_interest_lns short_open_interest_lns position_balance_cns insurance_balance_cns funding_rate_pct100k abs_funding_clamp_pct_per_100k " +
			"maint_margin_frac_hdths init_margin_frac_hdths liquidation"},
		{"LiquidationInfo", LiquidationInfo{}, "liq_ins_amt_per_100k liq_user_amt_per_100k liq_protocol_amt_per_100k btl_price_thresh_per_100k " +
			"btl_ins_amt_per_100k btl_user_amt_per_100k btl_buyer_amt_per_100k btl_protocol_amt_per_100k btl_restrict_buyers"},
		{"Position", samplePosition(t), "perp_id account_id next_node_id prev_node_id long deposit_cns price_pns lot_lns entry_block pnl_cns delta_pnl_cns premium_pnl_cns price_residue_pnsq16"},
		{"PositionPage", PositionPage{}, "positions num_positions mark_price_pns mark_price_valid"},
		{"PositionIDs", PositionIDs{}, "start_node_id end_node_id"},
		{"Account", sampleAccount(t), "account_id balance_cns locked_balance_cns frozen address position_banks"},
		{"Liquidation", sampleLiquidation(t), "block log_index tx_hash time keeper perp_id account_id long mark_price_pns liq_price_pns liq_lot_lns pos_lot_lns " +
			"delta_pnl_cns funding_cns pos_amount_cns pos_deposit_cns acc_amount_cns acc_balance_cns on_order_book"},
	}
	for _, c := range cases {
		got := keysOf(t, c.value)
		for key := range strings.FieldsSeq(c.keys) {
			if !got[key] {
				t.Errorf("%s lost the JSON key %q", c.name, key)
			}
		}
	}
}

func TestOnChainAmountsAreBigInts(t *testing.T) {
	bigInt := reflect.TypeFor[*big.Int]()
	for _, v := range []any{ExchangeInfo{}, Market{}, LiquidationInfo{}, Position{}, PositionPage{}, Account{}, Liquidation{}} {
		typ := reflect.TypeOf(v)
		for field := range typ.Fields() {
			for _, unit := range []string{"CNS", "PNS", "LNS"} {
				if strings.HasSuffix(field.Name, unit) && field.Type != bigInt {
					t.Errorf("%s.%s is %s, want *big.Int", typ.Name(), field.Name, field.Type)
				}
			}
		}
	}
}

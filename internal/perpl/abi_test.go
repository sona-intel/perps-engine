package perpl

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

type abiItem struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Inputs  []abiArg `json:"inputs"`
	Outputs []abiArg `json:"outputs"`
}

type abiArg struct {
	Type       string   `json:"type"`
	Components []abiArg `json:"components"`
}

func (a abiArg) canonical() string {
	if strings.HasPrefix(a.Type, "tuple") {
		parts := make([]string, len(a.Components))
		for i, c := range a.Components {
			parts[i] = c.canonical()
		}
		return "(" + strings.Join(parts, ",") + ")" + strings.TrimPrefix(a.Type, "tuple")
	}
	return a.Type
}

func (it abiItem) signature() string {
	parts := make([]string, len(it.Inputs))
	for i, a := range it.Inputs {
		parts[i] = a.canonical()
	}
	return it.Name + "(" + strings.Join(parts, ",") + ")"
}

type selectorFile struct {
	Functions  map[string]string `json:"functions"`
	Events     map[string]string `json:"events"`
	Signatures map[string]string `json:"signatures"`
}

func TestKeccakIsLegacy(t *testing.T) {
	// the ERC-20 transfer selector tells legacy keccak from sha3-256
	sel := selectorOf("transfer(address,uint256)")
	if got := hex.EncodeToString(sel[:]); got != "a9059cbb" {
		t.Fatalf("selector %s", got)
	}
	if got := keccak256([]byte("")); hex.EncodeToString(got[:]) != "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470" {
		t.Fatalf("keccak of empty input is %x", got)
	}
}

func TestSelectorsPinned(t *testing.T) {
	var pinned selectorFile
	readJSON(t, testdataDir+"/selectors.json", &pinned)
	var abi []abiItem
	readJSON(t, "exchange.abi.json", &abi)
	bySig := map[string]string{}
	for _, it := range abi {
		bySig[it.Name] = it.signature()
	}

	if len(pinned.Functions) != len(callSignatures) {
		t.Fatalf("pinned %d functions, code has %d", len(pinned.Functions), len(callSignatures))
	}
	for name, want := range pinned.Functions {
		sig, ok := callSignatures[name]
		if !ok {
			t.Errorf("%s: no signature in code", name)
			continue
		}
		if sig != pinned.Signatures[name] || sig != bySig[name] {
			t.Errorf("%s: code %q, selectors.json %q, abi %q", name, sig, pinned.Signatures[name], bySig[name])
		}
		sel := selectorOf(sig)
		if got := "0x" + hex.EncodeToString(sel[:]); got != want {
			t.Errorf("%s: selector %s, pinned %s", name, got, want)
		}
	}

	topic := TopicPositionLiquidated
	if got := topic.String(); got != pinned.Events["PositionLiquidated"] {
		t.Errorf("topic0 %s, pinned %s", got, pinned.Events["PositionLiquidated"])
	}
	if eventSignature != pinned.Signatures["PositionLiquidated"] || eventSignature != bySig["PositionLiquidated"] {
		t.Errorf("event signature %q", eventSignature)
	}
}

func TestEncodeMatchesFixtures(t *testing.T) {
	for _, c := range ethCalls(t) {
		t.Run(c.Name, func(t *testing.T) {
			fx, _, _ := loadCall(t, c.Name)
			got, err := encodeFixture(t, fx)
			if err != nil {
				t.Fatal(err)
			}
			if want := mustHex(t, fx.Request.Data); !bytes.Equal(got, want) {
				t.Fatalf("encoded %x, fixture %x", got, want)
			}
		})
	}
}

func bigArg(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return n
}

func uintArg(t *testing.T, s string) uint64 {
	t.Helper()
	n := bigArg(t, s)
	if !n.IsUint64() {
		t.Fatalf("%q does not fit", s)
	}
	return n.Uint64()
}

func encodeFixture(t *testing.T, fx callFixture) ([]byte, error) {
	t.Helper()
	switch fx.Function {
	case "getExchangeInfo":
		return EncodeGetExchangeInfo(), nil
	case "getPerpetualExistsBitmap":
		return EncodeGetPerpetualExistsBitmap(), nil
	case "getFundingInterval":
		return EncodeGetFundingInterval(), nil
	case "getPerpetualInfoV2":
		return EncodeGetPerpetualInfoV2(uintArg(t, fx.Args["perpId"])), nil
	case "getLiquidationInfo":
		return EncodeGetLiquidationInfo(uintArg(t, fx.Args["perpId"])), nil
	case "getPositionIds":
		return EncodeGetPositionIDs(uintArg(t, fx.Args["perpId"])), nil
	case "getMarginFractions":
		return EncodeGetMarginFractions(uintArg(t, fx.Args["perpId"]), bigArg(t, fx.Args["lotLNS"]))
	case "getPositionsV2":
		return EncodeGetPositionsV2(uintArg(t, fx.Args["perpId"]), uintArg(t, fx.Args["pageStartPositionId"]), uintArg(t, fx.Args["positionsPerPage"])), nil
	case "getAccountById":
		return EncodeGetAccountByID(uintArg(t, fx.Args["accountId"])), nil
	case "getAccountByAddr":
		a, err := ParseAddress(fx.Args["accountAddress"])
		if err != nil {
			t.Fatal(err)
		}
		return EncodeGetAccountByAddr(a), nil
	}
	t.Fatalf("no encoder for %s", fx.Function)
	return nil, nil
}

func TestEncodeRejectsBadArguments(t *testing.T) {
	tooBig := new(big.Int).Lsh(big.NewInt(1), 256)
	cases := map[string]func() error{
		"negative lot": func() error { _, err := EncodeGetMarginFractions(1, big.NewInt(-1)); return err },
		"lot too wide": func() error { _, err := EncodeGetMarginFractions(1, tooBig); return err },
		"nil lot":      func() error { _, err := EncodeGetMarginFractions(1, nil); return err },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err == nil {
				t.Fatal("want error")
			}
		})
	}
	max := new(big.Int).Sub(tooBig, big.NewInt(1))
	got, err := EncodeGetMarginFractions(0, max)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[4+32:], bytes.Repeat([]byte{0xff}, 32)) {
		t.Fatalf("2^256-1 encoded as %x", got[4+32:])
	}
}

func TestEncodeDoesNotAliasSelectors(t *testing.T) {
	a := EncodeGetExchangeInfo()
	a[0] ^= 0xff
	if b := EncodeGetExchangeInfo(); b[0] == a[0] {
		t.Fatal("encoder returned a shared buffer")
	}
}

/* Decoding of every fixture */

func str(n *big.Int) string { return n.String() }

func u(n uint64) string { return new(big.Int).SetUint64(n).String() }

func mapOf(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// getPerpetualInfoV2 fields the engine does not keep, the test fails when the fixture gains one that is not listed
var skippedMarketFields = []string{
	"linkFeedId", "priceTolPer100K", "marginTol", "marginTolDecimals", "lastPNS", "lastTimestamp",
	"fundingStartBlock", "basePricePNS", "maxBidPriceONS", "minBidPriceONS", "maxAskPriceONS",
	"minAskPriceONS", "numOrders", "ignOracle", "fundingSumScalingExp",
}

func marketView(m Market) map[string]any {
	return mapOf("perpetualInfo", mapOf(
		"name", m.Name,
		"symbol", m.Symbol,
		"priceDecimals", u(uint64(m.PriceDecimals)),
		"lotDecimals", u(uint64(m.LotDecimals)),
		"refPriceMaxAgeSec", u(m.RefPriceMaxAgeSec),
		"positionBalanceCNS", str(m.PositionBalanceCNS),
		"insuranceBalanceCNS", str(m.InsuranceBalanceCNS),
		"markPNS", str(m.MarkPNS),
		"markTimestamp", u(m.MarkTimestamp),
		"oraclePNS", str(m.OraclePNS),
		"oracleTimestampSec", u(m.OracleTimestampSec),
		"longOpenInterestLNS", str(m.LongOpenInterestLNS),
		"shortOpenInterestLNS", str(m.ShortOpenInterestLNS),
		"fundingRatePct100k", big.NewInt(int64(m.FundingRatePct100k)).String(),
		"absFundingClampPctPer100K", u(m.AbsFundingClampPctPer100K),
		"status", u(uint64(m.Status)),
	))
}

func positionView(p Position) map[string]any {
	posType := "1"
	if p.Long {
		posType = "0"
	}
	return mapOf(
		"accountId", u(p.AccountID),
		"nextNodeId", u(p.NextNodeID),
		"prevNodeId", u(p.PrevNodeID),
		"positionType", posType,
		"depositCNS", str(p.DepositCNS),
		"pricePNS", str(p.PricePNS),
		"lotLNS", str(p.LotLNS),
		"entryBlock", u(p.EntryBlock),
		"pnlCNS", str(p.PnlCNS),
		"deltaPnlCNS", str(p.DeltaPnlCNS),
		"premiumPnlCNS", str(p.PremiumPnlCNS),
		"priceResiduePNSQ16", u(p.PriceResiduePNSQ16),
	)
}

func accountView(a Account) map[string]any {
	return mapOf("accountInfo", mapOf(
		"accountId", u(a.AccountID),
		"balanceCNS", str(a.BalanceCNS),
		"lockedBalanceCNS", str(a.LockedBalanceCNS),
		"frozen", u(uint64(a.Frozen)),
		"accountAddr", a.Address.String(),
		"positions", mapOf("bank1", str(a.PositionBanks[0]), "bank2", str(a.PositionBanks[1]), "bank3", str(a.PositionBanks[2]), "bank4", str(a.PositionBanks[3])),
	))
}

func liquidationEventView(l Liquidation) map[string]any {
	posType := "1"
	if l.Long {
		posType = "0"
	}
	return mapOf(
		"perpId", u(l.PerpID),
		"posAccountId", u(l.AccountID),
		"positionType", posType,
		"markPricePNS", str(l.MarkPricePNS),
		"liqPricePNS", str(l.LiqPricePNS),
		"liqLotLNS", str(l.LiqLotLNS),
		"posLotLNS", str(l.PosLotLNS),
		"deltaPnlCNS", str(l.DeltaPnlCNS),
		"fundingCNS", str(l.FundingCNS),
		"posAmountCNS", str(l.PosAmountCNS),
		"posDepositCNS", str(l.PosDepositCNS),
		"accAmountCNS", str(l.AccAmountCNS),
		"accBalanceCNS", str(l.AccBalanceCNS),
		"onOrderBook", l.OnOrderBook,
	)
}

func assertEqualView(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded view differs\n got: %v\nwant: %v", got, want)
	}
}

func withoutKeys(t *testing.T, v any, outer string, skip []string) map[string]any {
	t.Helper()
	root := v.(map[string]any)
	inner := root[outer].(map[string]any)
	trimmed := map[string]any{}
	maps.Copy(trimmed, inner)
	for _, k := range skip {
		if _, ok := trimmed[k]; !ok {
			t.Fatalf("skipped field %s is not in the fixture", k)
		}
		delete(trimmed, k)
	}
	return mapOf(outer, trimmed)
}

func TestDecodeFixtures(t *testing.T) {
	counts := map[string]int{}
	for _, c := range ethCalls(t) {
		fx, result, expected := loadCall(t, c.Name)
		if fx.Error != nil {
			continue
		}
		counts[fx.Function]++
		t.Run(c.Name, func(t *testing.T) {
			switch fx.Function {
			case "getExchangeInfo":
				got, err := DecodeExchangeInfo(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, mapOf(
					"balanceCNS", str(got.BalanceCNS),
					"protocolBalanceCNS", str(got.ProtocolBalanceCNS),
					"recycleBalanceCNS", str(got.RecycleBalanceCNS),
					"collateralDecimals", u(uint64(got.CollateralDecimals)),
					"collateralToken", got.CollateralToken.String(),
					"verifierProxy", got.VerifierProxy.String(),
				), expected)
			case "getFundingInterval":
				got, err := DecodeFundingInterval(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, mapOf("fundingInterval", u(got)), expected)
			case "getPerpetualExistsBitmap":
				ids, err := DecodeExistsBitmap(result)
				if err != nil {
					t.Fatal(err)
				}
				var chain struct {
					PerpIDs []uint64 `json:"perp_ids"`
				}
				readJSON(t, testdataDir+"/chain.json", &chain)
				if !reflect.DeepEqual(ids, chain.PerpIDs) {
					t.Fatalf("ids %v, chain.json %v", ids, chain.PerpIDs)
				}
				words := expected.(map[string]any)["bitmap"].([]any)
				rebuilt := make([]*big.Int, 4)
				for i := range rebuilt {
					rebuilt[i] = new(big.Int)
				}
				for _, id := range ids {
					rebuilt[id/256].SetBit(rebuilt[id/256], int(id%256), 1)
				}
				for i, w := range words {
					if rebuilt[i].String() != w.(string) {
						t.Fatalf("bitmap word %d is %s, fixture %s", i, rebuilt[i], w)
					}
				}
			case "getPerpetualInfoV2":
				got, err := DecodeMarket(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, marketView(got), withoutKeys(t, expected, "perpetualInfo", skippedMarketFields))
			case "getMarginFractions":
				got, err := DecodeMarginFractions(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, mapOf(
					"perpInitMarginFracHdths", u(got.PerpInitMarginFracHdths),
					"perpMaintMarginFracHdths", u(got.PerpMaintMarginFracHdths),
					"dynamicInitMarginFracHdths", u(got.DynamicInitMarginFracHdths),
					"oiMaxLNS", str(got.OiMaxLNS),
					"unityDescentThreshHdths", u(got.UnityDescentThreshHdths),
					"overColDescentThreshHdths", u(got.OverColDescentThreshHdths),
				), expected)
			case "getLiquidationInfo":
				got, err := DecodeLiquidationInfo(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, mapOf("liquidationInfo", mapOf(
					"liqInsAmtPer100K", u(got.LiqInsAmtPer100K),
					"liqUserAmtPer100K", u(got.LiqUserAmtPer100K),
					"liqProtocolAmtPer100K", u(got.LiqProtocolAmtPer100K),
					"btlPriceThreshPer100K", u(got.BtlPriceThreshPer100K),
					"btlInsAmtPer100K", u(got.BtlInsAmtPer100K),
					"btlUserAmtPer100K", u(got.BtlUserAmtPer100K),
					"btlBuyerAmtPer100K", u(got.BtlBuyerAmtPer100K),
					"btlProtocolAmtPer100K", u(got.BtlProtocolAmtPer100K),
					"btlRestrictBuyers", got.BtlRestrictBuyers,
				)), expected)
			case "getPositionIds":
				got, err := DecodePositionIDs(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, mapOf("startNodeId", u(got.StartNodeID), "endNodeId", u(got.EndNodeID)), expected)
			case "getPositionsV2":
				checkPositionPage(t, result, expected)
			case "getAccountById", "getAccountByAddr":
				got, err := DecodeAccount(result)
				if err != nil {
					t.Fatal(err)
				}
				assertEqualView(t, accountView(got), expected)
			default:
				t.Fatalf("no decoder case for %s", fx.Function)
			}
		})
	}
	for _, fn := range []string{
		"getExchangeInfo", "getFundingInterval", "getPerpetualExistsBitmap", "getPerpetualInfoV2", "getMarginFractions",
		"getLiquidationInfo", "getPositionIds", "getPositionsV2", "getAccountById", "getAccountByAddr",
	} {
		if counts[fn] == 0 {
			t.Errorf("no fixture exercised %s", fn)
		}
	}
}

func checkPositionPage(t *testing.T, result []byte, expected any) {
	t.Helper()
	got, err := DecodePositionPage(result)
	if err != nil {
		t.Fatal(err)
	}
	want := expected.(map[string]any)
	if u(got.NumPositions) != want["numPositions"].(string) {
		t.Fatalf("numPositions %d, fixture %v", got.NumPositions, want["numPositions"])
	}
	if str(got.MarkPricePNS) != want["markPricePNS"].(string) || got.MarkPriceValid != want["markPriceValid"].(bool) {
		t.Fatalf("mark %s valid %v, fixture %v %v", got.MarkPricePNS, got.MarkPriceValid, want["markPricePNS"], want["markPriceValid"])
	}
	all := want["positions"].([]any)
	if uint64(len(got.Positions)) != got.NumPositions || uint64(len(all)) < got.NumPositions {
		t.Fatalf("decoded %d positions for numPositions %d of %d entries", len(got.Positions), got.NumPositions, len(all))
	}
	for i, p := range got.Positions {
		if !reflect.DeepEqual(positionView(p), all[i]) {
			t.Fatalf("position %d differs\n got: %v\nwant: %v", i, positionView(p), all[i])
		}
		if p.PerpID != 0 {
			t.Fatalf("position %d carries perp id %d, the reader sets it", i, p.PerpID)
		}
	}
	// the fixture keeps the zero filled tail the decoder trims
	for i := int(got.NumPositions); i < len(all); i++ {
		for k, v := range all[i].(map[string]any) {
			if v != "0" {
				t.Fatalf("tail entry %d field %s is %v", i, k, v)
			}
		}
	}
}

func TestDecodeImplementationSlot(t *testing.T) {
	var fx struct {
		Result string `json:"result"`
	}
	readJSON(t, testdataDir+"/calls/getStorageAt.implementation.json", &fx)
	var want struct {
		Implementation string `json:"implementation"`
	}
	readJSON(t, testdataDir+"/calls/getStorageAt.implementation.expected.json", &want)
	got, err := AddressFromWord(mustHex(t, fx.Result))
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.Implementation {
		t.Fatalf("got %s want %s", got, want.Implementation)
	}
	dirty := mustHex(t, fx.Result)
	dirty[0] = 1
	if _, err := AddressFromWord(dirty); err == nil {
		t.Fatal("dirty upper bytes accepted")
	}
	if _, err := AddressFromWord(dirty[:31]); err == nil {
		t.Fatal("short word accepted")
	}
}

/* Liquidation logs */

type rawLog struct {
	Topics   []string `json:"topics"`
	Data     string   `json:"data"`
	LogIndex string   `json:"logIndex"`
	TxHash   string   `json:"transactionHash"`
	Block    string   `json:"blockNumber"`
}

type expectedRow struct {
	BlockNumber     string         `json:"blockNumber"`
	LogIndex        string         `json:"logIndex"`
	TransactionHash string         `json:"transactionHash"`
	Event           map[string]any `json:"event"`
}

func hexNumber(t *testing.T, s string) uint64 {
	t.Helper()
	n, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok || !n.IsUint64() {
		t.Fatalf("bad quantity %q", s)
	}
	return n.Uint64()
}

func TestDecodeLiquidationFixtures(t *testing.T) {
	var batches []struct {
		Result []rawLog `json:"result"`
	}
	readJSON(t, testdataDir+"/liquidations/getlogs.raw.json", &batches)
	var rows []expectedRow
	readJSON(t, testdataDir+"/liquidations/expected.json", &rows)

	byKey := map[[2]uint64]expectedRow{}
	for _, r := range rows {
		byKey[[2]uint64{uint64Of(t, r.BlockNumber), uint64Of(t, r.LogIndex)}] = r
	}
	seen := 0
	for _, b := range batches {
		for _, l := range b.Result {
			block, index := hexNumber(t, l.Block), hexNumber(t, l.LogIndex)
			row, ok := byKey[[2]uint64{block, index}]
			if !ok {
				t.Fatalf("no expected row for %d/%d", block, index)
			}
			topics := make([]Hash, len(l.Topics))
			for i, s := range l.Topics {
				h, err := ParseHash(s)
				if err != nil {
					t.Fatal(err)
				}
				topics[i] = h
			}
			tx, err := ParseHash(l.TxHash)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeLiquidation(topics, mustHex(t, l.Data), block, index, tx)
			if err != nil {
				t.Fatalf("%d/%d: %v", block, index, err)
			}
			assertEqualView(t, liquidationEventView(got), any(row.Event))
			if got.Block != block || got.LogIndex != index || got.TxHash.String() != row.TransactionHash {
				t.Fatalf("position of log is %d/%d/%s", got.Block, got.LogIndex, got.TxHash)
			}
			if !got.Time.IsZero() || !got.Keeper.IsZero() {
				t.Fatal("decoder must leave time and keeper to the caller")
			}
			seen++
		}
	}
	if seen != len(rows) || seen != 20 {
		t.Fatalf("decoded %d logs for %d expected rows", seen, len(rows))
	}
}

func uint64Of(t *testing.T, s string) uint64 {
	t.Helper()
	return uintArg(t, s)
}

func TestDecodeLiquidationHyperSync(t *testing.T) {
	var resp struct {
		Data []struct {
			Logs []struct {
				LogIndex uint64 `json:"log_index"`
				TxHash   string `json:"transaction_hash"`
				Block    uint64 `json:"block_number"`
				Data     string `json:"data"`
				Topic0   string `json:"topic0"`
			} `json:"logs"`
		} `json:"data"`
	}
	readJSON(t, testdataDir+"/hypersync/response.json", &resp)
	var rows []expectedRow
	readJSON(t, testdataDir+"/hypersync/expected.json", &rows)
	byKey := map[[2]uint64]expectedRow{}
	for _, r := range rows {
		byKey[[2]uint64{uint64Of(t, r.BlockNumber), uint64Of(t, r.LogIndex)}] = r
	}
	n := 0
	for _, page := range resp.Data {
		for _, l := range page.Logs {
			topic, err := ParseHash(l.Topic0)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := ParseHash(l.TxHash)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeLiquidation([]Hash{topic}, mustHex(t, l.Data), l.Block, l.LogIndex, tx)
			if err != nil {
				t.Fatal(err)
			}
			assertEqualView(t, liquidationEventView(got), any(byKey[[2]uint64{l.Block, l.LogIndex}].Event))
			n++
		}
	}
	if n != len(rows) || n != 10 {
		t.Fatalf("decoded %d logs for %d rows", n, len(rows))
	}
}

func TestDecodeLiquidationRejects(t *testing.T) {
	var rows []struct {
		Result []rawLog `json:"result"`
	}
	readJSON(t, testdataDir+"/liquidations/getlogs.raw.json", &rows)
	data := mustHex(t, rows[0].Result[0].Data)
	good := []Hash{TopicPositionLiquidated}
	tx := Hash{1}

	bad := func(mut func([]byte) []byte) []byte { return mut(bytes.Clone(data)) }
	cases := map[string]struct {
		topics []Hash
		data   []byte
	}{
		"no topics":       {nil, data},
		"wrong topic":     {[]Hash{{9}}, data},
		"extra topic":     {[]Hash{TopicPositionLiquidated, {1}}, data},
		"short data":      {good, data[:len(data)-1]},
		"long data":       {good, append(bytes.Clone(data), 0)},
		"empty data":      {good, nil},
		"bad position":    {good, bad(func(b []byte) []byte { b[2*32+31] = 2; return b })},
		"bad bool":        {good, bad(func(b []byte) []byte { b[13*32+31] = 2; return b })},
		"perp too wide":   {good, bad(func(b []byte) []byte { b[0] = 1; return b })},
		"account too big": {good, bad(func(b []byte) []byte { b[1*32] = 1; return b })},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeLiquidation(c.topics, c.data, 1, 1, tx); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

/* Malformed results */

func word(v *big.Int) []byte {
	out := make([]byte, 32)
	if v.Sign() >= 0 {
		v.FillBytes(out)
		return out
	}
	return new(big.Int).Add(v, new(big.Int).Lsh(big.NewInt(1), 256)).FillBytes(out)
}

func setWord(b []byte, index int, v *big.Int) []byte {
	copy(b[index*32:], word(v))
	return b
}

func fixtureResult(t *testing.T, name string) []byte {
	t.Helper()
	_, result, _ := loadCall(t, name)
	return bytes.Clone(result)
}

func TestDecodeRejectsMalformed(t *testing.T) {
	tooWide := new(big.Int).Lsh(big.NewInt(1), 64)
	type tc struct {
		name string
		run  func() error
	}
	infoN := 0
	info := func(mut func([]byte) []byte) tc {
		infoN++
		return tc{fmt.Sprintf("market %d", infoN), func() error { _, err := DecodeMarket(mut(fixtureResult(t, "getPerpetualInfoV2.perp1"))); return err }}
	}
	page := func(mut func([]byte) []byte) error {
		_, err := DecodePositionPage(mut(fixtureResult(t, "getPositionsV2.perp1.page02")))
		return err
	}
	exch := func(mut func([]byte) []byte) error {
		_, err := DecodeExchangeInfo(mut(fixtureResult(t, "getExchangeInfo")))
		return err
	}
	cases := []tc{
		{"exchange short", func() error { return exch(func(b []byte) []byte { return b[:5*32] }) }},
		{"exchange decimals too wide", func() error { return exch(func(b []byte) []byte { return setWord(b, 3, big.NewInt(256)) }) }},
		{"exchange token dirty", func() error { return exch(func(b []byte) []byte { b[4*32] = 1; return b }) }},
		{"exchange empty", func() error { _, err := DecodeExchangeInfo(nil); return err }},
		{"funding too wide", func() error { _, err := DecodeFundingInterval(word(tooWide)); return err }},
		{"funding short", func() error { _, err := DecodeFundingInterval(make([]byte, 31)); return err }},
		{"bitmap short", func() error { _, err := DecodeExistsBitmap(make([]byte, 3*32)); return err }},
		{"ids short", func() error { _, err := DecodePositionIDs(make([]byte, 32)); return err }},
		{"ids too wide", func() error { _, err := DecodePositionIDs(append(word(tooWide), word(big.NewInt(1))...)); return err }},
		{"margin short", func() error { _, err := DecodeMarginFractions(make([]byte, 5*32)); return err }},
		{"margin too wide", func() error {
			_, err := DecodeMarginFractions(append(word(tooWide), make([]byte, 5*32)...))
			return err
		}},
		{"liqinfo short", func() error { _, err := DecodeLiquidationInfo(make([]byte, 8*32)); return err }},
		{"liqinfo bad bool", func() error {
			_, err := DecodeLiquidationInfo(append(make([]byte, 8*32), word(big.NewInt(3))...))
			return err
		}},
		{"liqinfo too wide", func() error {
			b := make([]byte, 9*32)
			setWord(b, 2, tooWide)
			_, err := DecodeLiquidationInfo(b)
			return err
		}},
		{"account short", func() error { _, err := DecodeAccount(make([]byte, 8*32)); return err }},
		{"account frozen too wide", func() error {
			_, err := DecodeAccount(setWord(fixtureResult(t, "getAccountById.btc_top"), 3, big.NewInt(300)))
			return err
		}},
		{"account address dirty", func() error {
			b := fixtureResult(t, "getAccountById.btc_top")
			b[4*32] = 7
			_, err := DecodeAccount(b)
			return err
		}},
		{"account id too wide", func() error {
			_, err := DecodeAccount(setWord(fixtureResult(t, "getAccountById.btc_top"), 0, tooWide))
			return err
		}},
		info(func(b []byte) []byte { return b[:3*32] }),
		info(func(b []byte) []byte { return setWord(b, 0, big.NewInt(1<<40)) }),
		info(func(b []byte) []byte { return setWord(b, 1, big.NewInt(1<<40)) }),
		info(func(b []byte) []byte { return setWord(b, 1+2, big.NewInt(256)) }),
		info(func(b []byte) []byte { return setWord(b, 1+3, big.NewInt(256)) }),
		info(func(b []byte) []byte { return setWord(b, 1+20, big.NewInt(32768)) }),
		info(func(b []byte) []byte { return setWord(b, 1+20, big.NewInt(-32769)) }),
		info(func(b []byte) []byte { return setWord(b, 1+22, big.NewInt(256)) }),
		info(func(b []byte) []byte { return setWord(b, 1+12, tooWide) }),
		info(func(b []byte) []byte {
			// a string length that runs past the data
			off := int(new(big.Int).SetBytes(b[32:64]).Int64()) + 32
			return setWord(b, off/32, big.NewInt(1<<30))
		}),
		{"page num above array", func() error {
			return page(func(b []byte) []byte { return setWord(b, 1, big.NewInt(1000)) })
		}},
		{"page offset out of range", func() error {
			return page(func(b []byte) []byte { return setWord(b, 0, big.NewInt(1<<40)) })
		}},
		{"page length out of range", func() error {
			return page(func(b []byte) []byte { return setWord(b, 4, big.NewInt(1<<40)) })
		}},
		{"page truncated", func() error { return page(func(b []byte) []byte { return b[:len(b)-32] }) }},
		{"page bad bool", func() error { return page(func(b []byte) []byte { return setWord(b, 3, big.NewInt(2)) }) }},
		{"page bad position type", func() error {
			return page(func(b []byte) []byte { return setWord(b, 5+3, big.NewInt(2)) })
		}},
		{"page residue too wide", func() error {
			return page(func(b []byte) []byte { return setWord(b, 5+11, tooWide) })
		}},
		{"page entry block too wide", func() error {
			return page(func(b []byte) []byte { return setWord(b, 5+7, tooWide) })
		}},
		{"page account too wide", func() error {
			return page(func(b []byte) []byte { return setWord(b, 5, tooWide) })
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			if err == nil {
				t.Fatal("want error")
			}
			t.Log(err)
		})
	}
}

func TestDecodeSignedValues(t *testing.T) {
	b := fixtureResult(t, "getPerpetualInfoV2.perp1")
	setWord(b, 1+20, big.NewInt(-7))
	m, err := DecodeMarket(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.FundingRatePct100k != -7 {
		t.Fatalf("funding %d", m.FundingRatePct100k)
	}
	setWord(b, 1+20, big.NewInt(-32768))
	if m, err = DecodeMarket(b); err != nil || m.FundingRatePct100k != -32768 {
		t.Fatalf("funding %d err %v", m.FundingRatePct100k, err)
	}

	p := fixtureResult(t, "getPositionsV2.perp1.page02")
	minInt := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 255))
	setWord(p, 5+8, minInt)
	page, err := DecodePositionPage(p)
	if err != nil {
		t.Fatal(err)
	}
	if page.Positions[0].PnlCNS.Cmp(minInt) != 0 {
		t.Fatalf("pnl %s", page.Positions[0].PnlCNS)
	}
}

func TestDecodeHandlesEmptyPage(t *testing.T) {
	page, err := DecodePositionPage(fixtureResult(t, "getPositionsV2.perp30.empty"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Positions) != 0 || page.NumPositions != 0 || page.MarkPriceValid {
		t.Fatalf("%+v", page)
	}
	if page.Positions == nil {
		t.Fatal("positions must be an empty slice, not nil")
	}
}

func TestBitmapUpperWords(t *testing.T) {
	// ids above 255 live in the later words
	out := make([]byte, 4*32)
	setWord(out, 1, big.NewInt(1<<3|1))
	setWord(out, 3, big.NewInt(1))
	ids, err := DecodeExistsBitmap(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint64{256, 259, 768}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids %v want %v", ids, want)
	}
	if ids, err = DecodeExistsBitmap(make([]byte, 4*32)); err != nil || len(ids) != 0 {
		t.Fatalf("empty bitmap gave %v %v", ids, err)
	}
}

func TestImplementationSlotPinned(t *testing.T) {
	var chain struct {
		Slot string `json:"implementation_slot"`
	}
	readJSON(t, testdataDir+"/chain.json", &chain)
	if ImplementationSlot.String() != chain.Slot {
		t.Fatalf("slot %s, fixture %s", ImplementationSlot, chain.Slot)
	}
}

func ethCalls(t *testing.T) []struct {
	Name     string `json:"name"`
	Function string `json:"function"`
	Error    bool   `json:"error"`
} {
	t.Helper()
	var out []struct {
		Name     string `json:"name"`
		Function string `json:"function"`
		Error    bool   `json:"error"`
	}
	for _, c := range loadManifest(t).Calls {
		if _, ok := callSignatures[c.Function]; ok {
			out = append(out, c)
		}
	}
	return out
}

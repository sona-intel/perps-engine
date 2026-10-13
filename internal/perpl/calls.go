package perpl

import (
	"fmt"
	"math/big"
	"slices"
)

/* Signatures */

var callSignatures = map[string]string{
	"getExchangeInfo":          "getExchangeInfo()",
	"getPerpetualExistsBitmap": "getPerpetualExistsBitmap()",
	"getFundingInterval":       "getFundingInterval()",
	"getPerpetualInfoV2":       "getPerpetualInfoV2(uint256)",
	"getMarginFractions":       "getMarginFractions(uint256,uint256)",
	"getLiquidationInfo":       "getLiquidationInfo(uint256)",
	"getPositionIds":           "getPositionIds(uint256)",
	"getPositionsV2":           "getPositionsV2(uint256,uint256,uint256)",
	"getAccountById":           "getAccountById(uint256)",
	"getAccountByAddr":         "getAccountByAddr(address)",
}

const eventSignature = "PositionLiquidated(uint256,uint256,uint8,uint256,uint256,uint256,uint256,int256,int256,int256,uint256,int256,uint256,bool)"

var TopicPositionLiquidated = Hash(keccak256([]byte(eventSignature)))

var ImplementationSlot = mustHash("0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc")

func mustHash(s string) Hash {
	h, err := ParseHash(s)
	if err != nil {
		panic(err)
	}
	return h
}

/* Extra result types */

type MarginFractions struct {
	PerpInitMarginFracHdths    uint64   `json:"perp_init_margin_frac_hdths"`
	PerpMaintMarginFracHdths   uint64   `json:"perp_maint_margin_frac_hdths"`
	DynamicInitMarginFracHdths uint64   `json:"dynamic_init_margin_frac_hdths"`
	OiMaxLNS                   *big.Int `json:"oi_max_lns"`
	UnityDescentThreshHdths    uint64   `json:"unity_descent_thresh_hdths"`
	OverColDescentThreshHdths  uint64   `json:"over_col_descent_thresh_hdths"`
}

/* Encoders, each returns the calldata of one eth_call */

func EncodeGetExchangeInfo() []byte { return newCall(callSignatures["getExchangeInfo"]).bytes() }

func EncodeGetPerpetualExistsBitmap() []byte {
	return newCall(callSignatures["getPerpetualExistsBitmap"]).bytes()
}

func EncodeGetFundingInterval() []byte { return newCall(callSignatures["getFundingInterval"]).bytes() }

func EncodeGetPerpetualInfoV2(perpID uint64) []byte {
	return newCall(callSignatures["getPerpetualInfoV2"]).uint64(perpID).bytes()
}

func EncodeGetLiquidationInfo(perpID uint64) []byte {
	return newCall(callSignatures["getLiquidationInfo"]).uint64(perpID).bytes()
}

func EncodeGetPositionIDs(perpID uint64) []byte {
	return newCall(callSignatures["getPositionIds"]).uint64(perpID).bytes()
}

func EncodeGetMarginFractions(perpID uint64, lotLNS *big.Int) ([]byte, error) {
	c, err := newCall(callSignatures["getMarginFractions"]).uint64(perpID).uint256(lotLNS)
	if err != nil {
		return nil, fmt.Errorf("lot: %w", err)
	}
	return c.bytes(), nil
}

func EncodeGetPositionsV2(perpID, pageStartPositionID, positionsPerPage uint64) []byte {
	return newCall(callSignatures["getPositionsV2"]).uint64(perpID).uint64(pageStartPositionID).uint64(positionsPerPage).bytes()
}

func EncodeGetAccountByID(accountID uint64) []byte {
	return newCall(callSignatures["getAccountById"]).uint64(accountID).bytes()
}

func EncodeGetAccountByAddr(addr Address) []byte {
	return newCall(callSignatures["getAccountByAddr"]).address(addr).bytes()
}

/* Decoders, each takes the raw return data of one eth_call */

// DecodeExchangeInfo leaves FundingInterval at zero because getFundingInterval is a separate call
func DecodeExchangeInfo(out []byte) (ExchangeInfo, error) {
	r := reader{out}
	if err := r.exactly(6); err != nil {
		return ExchangeInfo{}, fmt.Errorf("getExchangeInfo: %w", err)
	}
	var (
		info ExchangeInfo
		errs [6]error
	)
	info.BalanceCNS, errs[0] = r.big(0)
	info.ProtocolBalanceCNS, errs[1] = r.big(1)
	info.RecycleBalanceCNS, errs[2] = r.big(2)
	info.CollateralDecimals, errs[3] = r.uint8(3)
	info.CollateralToken, errs[4] = r.address(4)
	info.VerifierProxy, errs[5] = r.address(5)
	if err := firstError(errs[:]); err != nil {
		return ExchangeInfo{}, fmt.Errorf("getExchangeInfo: %w", err)
	}
	return info, nil
}

func DecodeFundingInterval(out []byte) (uint64, error) {
	r := reader{out}
	if err := r.exactly(1); err != nil {
		return 0, fmt.Errorf("getFundingInterval: %w", err)
	}
	v, err := r.uint64(0)
	if err != nil {
		return 0, fmt.Errorf("getFundingInterval: %w", err)
	}
	return v, nil
}

// DecodeExistsBitmap treats perp id n as bit n of the four words, word 0 first
func DecodeExistsBitmap(out []byte) ([]uint64, error) {
	r := reader{out}
	if err := r.exactly(4); err != nil {
		return nil, fmt.Errorf("getPerpetualExistsBitmap: %w", err)
	}
	ids := []uint64{}
	for i := range 4 {
		w, _ := r.word(i)
		for bit := range 256 {
			if w[wordSize-1-bit/8]>>(bit%8)&1 == 1 {
				ids = append(ids, uint64(i*256+bit))
			}
		}
	}
	return ids, nil
}

const (
	infoName = iota
	infoSymbol
	infoPriceDecimals
	infoLotDecimals
	infoLinkFeedID
	infoPriceTol
	infoMarginTol
	infoMarginTolDecimals
	infoRefPriceMaxAgeSec
	infoPositionBalance
	infoInsuranceBalance
	infoMarkPNS
	infoMarkTimestamp
	infoLastPNS
	infoLastTimestamp
	infoOraclePNS
	infoOracleTimestampSec
	infoLongOI
	infoShortOI
	infoFundingStartBlock
	infoFundingRate
	infoAbsFundingClamp
	infoStatus
	infoFieldCount = 31
)

// DecodeMarket fills the fields of getPerpetualInfoV2 and leaves PerpID, margin fractions and Liquidation to the caller
func DecodeMarket(out []byte) (Market, error) {
	root := reader{out}
	base, err := root.offset(0)
	if err != nil {
		return Market{}, fmt.Errorf("getPerpetualInfoV2: %w", err)
	}
	if err := root.atLeast(base + infoFieldCount); err != nil {
		return Market{}, fmt.Errorf("getPerpetualInfoV2: %w", err)
	}
	at := func(field int) int { return base + field }

	var (
		m    Market
		errs [17]error
	)
	m.Name, errs[0] = root.str(at(infoName), base)
	m.Symbol, errs[1] = root.str(at(infoSymbol), base)
	m.PriceDecimals, errs[2] = root.uint8(at(infoPriceDecimals))
	m.LotDecimals, errs[3] = root.uint8(at(infoLotDecimals))
	m.RefPriceMaxAgeSec, errs[4] = root.uint64(at(infoRefPriceMaxAgeSec))
	m.PositionBalanceCNS, errs[5] = root.big(at(infoPositionBalance))
	m.InsuranceBalanceCNS, errs[6] = root.big(at(infoInsuranceBalance))
	m.MarkPNS, errs[7] = root.big(at(infoMarkPNS))
	m.MarkTimestamp, errs[8] = root.uint64(at(infoMarkTimestamp))
	m.OraclePNS, errs[9] = root.big(at(infoOraclePNS))
	m.OracleTimestampSec, errs[10] = root.uint64(at(infoOracleTimestampSec))
	m.LongOpenInterestLNS, errs[11] = root.big(at(infoLongOI))
	m.ShortOpenInterestLNS, errs[12] = root.big(at(infoShortOI))
	m.FundingRatePct100k, errs[13] = root.int16(at(infoFundingRate))
	m.AbsFundingClampPctPer100K, errs[14] = root.uint64(at(infoAbsFundingClamp))
	m.Status, errs[15] = root.uint8(at(infoStatus))
	if err := firstError(errs[:]); err != nil {
		return Market{}, fmt.Errorf("getPerpetualInfoV2: %w", err)
	}
	return m, nil
}

func DecodeMarginFractions(out []byte) (MarginFractions, error) {
	r := reader{out}
	if err := r.exactly(6); err != nil {
		return MarginFractions{}, fmt.Errorf("getMarginFractions: %w", err)
	}
	var (
		mf   MarginFractions
		errs [6]error
	)
	mf.PerpInitMarginFracHdths, errs[0] = r.uint64(0)
	mf.PerpMaintMarginFracHdths, errs[1] = r.uint64(1)
	mf.DynamicInitMarginFracHdths, errs[2] = r.uint64(2)
	mf.OiMaxLNS, errs[3] = r.big(3)
	mf.UnityDescentThreshHdths, errs[4] = r.uint64(4)
	mf.OverColDescentThreshHdths, errs[5] = r.uint64(5)
	if err := firstError(errs[:]); err != nil {
		return MarginFractions{}, fmt.Errorf("getMarginFractions: %w", err)
	}
	return mf, nil
}

func DecodeLiquidationInfo(out []byte) (LiquidationInfo, error) {
	r := reader{out}
	if err := r.exactly(9); err != nil {
		return LiquidationInfo{}, fmt.Errorf("getLiquidationInfo: %w", err)
	}
	var (
		li   LiquidationInfo
		errs [9]error
	)
	li.LiqInsAmtPer100K, errs[0] = r.uint64(0)
	li.LiqUserAmtPer100K, errs[1] = r.uint64(1)
	li.LiqProtocolAmtPer100K, errs[2] = r.uint64(2)
	li.BtlPriceThreshPer100K, errs[3] = r.uint64(3)
	li.BtlInsAmtPer100K, errs[4] = r.uint64(4)
	li.BtlUserAmtPer100K, errs[5] = r.uint64(5)
	li.BtlBuyerAmtPer100K, errs[6] = r.uint64(6)
	li.BtlProtocolAmtPer100K, errs[7] = r.uint64(7)
	li.BtlRestrictBuyers, errs[8] = r.bool(8)
	if err := firstError(errs[:]); err != nil {
		return LiquidationInfo{}, fmt.Errorf("getLiquidationInfo: %w", err)
	}
	return li, nil
}

func DecodePositionIDs(out []byte) (PositionIDs, error) {
	r := reader{out}
	if err := r.exactly(2); err != nil {
		return PositionIDs{}, fmt.Errorf("getPositionIds: %w", err)
	}
	var (
		ids  PositionIDs
		errs [2]error
	)
	ids.StartNodeID, errs[0] = r.uint64(0)
	ids.EndNodeID, errs[1] = r.uint64(1)
	if err := firstError(errs[:]); err != nil {
		return PositionIDs{}, fmt.Errorf("getPositionIds: %w", err)
	}
	return ids, nil
}

const positionWords = 12

// DecodePositionPage drops the zero filled entries after NumPositions and leaves each Position.PerpID at zero
func DecodePositionPage(out []byte) (PositionPage, error) {
	root := reader{out}
	if err := root.atLeast(5); err != nil {
		return PositionPage{}, fmt.Errorf("getPositionsV2: %w", err)
	}
	var (
		page PositionPage
		errs [4]error
	)
	arrayAt, err := root.offset(0)
	if err != nil {
		return PositionPage{}, fmt.Errorf("getPositionsV2: %w", err)
	}
	page.NumPositions, errs[0] = root.uint64(1)
	page.MarkPricePNS, errs[1] = root.big(2)
	page.MarkPriceValid, errs[2] = root.bool(3)
	length, lenErr := root.uint64(arrayAt)
	errs[3] = lenErr
	if err := firstError(errs[:]); err != nil {
		return PositionPage{}, fmt.Errorf("getPositionsV2: %w", err)
	}
	room := uint64(len(out)/wordSize-arrayAt-1) / positionWords
	if length > room {
		return PositionPage{}, fmt.Errorf("getPositionsV2: array of %d entries does not fit the data", length)
	}
	if page.NumPositions > length {
		return PositionPage{}, fmt.Errorf("getPositionsV2: numPositions %d exceeds the %d entries returned", page.NumPositions, length)
	}
	page.Positions = make([]Position, page.NumPositions)
	for i := range page.Positions {
		p, err := decodePosition(root, arrayAt+1+i*positionWords)
		if err != nil {
			return PositionPage{}, fmt.Errorf("getPositionsV2: position %d: %w", i, err)
		}
		page.Positions[i] = p
	}
	return page, nil
}

func decodePosition(r reader, at int) (Position, error) {
	var (
		p    Position
		errs [12]error
		kind uint8
	)
	p.AccountID, errs[0] = r.uint64(at)
	p.NextNodeID, errs[1] = r.uint64(at + 1)
	p.PrevNodeID, errs[2] = r.uint64(at + 2)
	kind, errs[3] = r.uint8(at + 3)
	p.DepositCNS, errs[4] = r.big(at + 4)
	p.PricePNS, errs[5] = r.big(at + 5)
	p.LotLNS, errs[6] = r.big(at + 6)
	p.EntryBlock, errs[7] = r.uint64(at + 7)
	p.PnlCNS, errs[8] = r.int256(at + 8)
	p.DeltaPnlCNS, errs[9] = r.int256(at + 9)
	p.PremiumPnlCNS, errs[10] = r.int256(at + 10)
	p.PriceResiduePNSQ16, errs[11] = r.uint64(at + 11)
	if err := firstError(errs[:]); err != nil {
		return Position{}, err
	}
	switch kind {
	case 0:
		p.Long = true
	case 1:
	default:
		return Position{}, fmt.Errorf("position type %d is neither long nor short", kind)
	}
	return p, nil
}

func DecodeAccount(out []byte) (Account, error) {
	r := reader{out}
	if err := r.exactly(9); err != nil {
		return Account{}, fmt.Errorf("account: %w", err)
	}
	var (
		a    Account
		errs [9]error
	)
	a.AccountID, errs[0] = r.uint64(0)
	a.BalanceCNS, errs[1] = r.big(1)
	a.LockedBalanceCNS, errs[2] = r.big(2)
	a.Frozen, errs[3] = r.uint8(3)
	a.Address, errs[4] = r.address(4)
	for i := range a.PositionBanks {
		a.PositionBanks[i], errs[5+i] = r.big(5 + i)
	}
	if err := firstError(errs[:]); err != nil {
		return Account{}, fmt.Errorf("account: %w", err)
	}
	return a, nil
}

// DecodeLiquidation decodes one PositionLiquidated log, Time and Keeper stay unset because they come from the block and the transaction
func DecodeLiquidation(topics []Hash, data []byte, block, logIndex uint64, txHash Hash) (Liquidation, error) {
	if len(topics) != 1 || topics[0] != TopicPositionLiquidated {
		return Liquidation{}, fmt.Errorf("log is not a PositionLiquidated event, it has %d topics", len(topics))
	}
	r := reader{data}
	if err := r.exactly(14); err != nil {
		return Liquidation{}, fmt.Errorf("PositionLiquidated: %w", err)
	}
	l := Liquidation{Block: block, LogIndex: logIndex, TxHash: txHash}
	var (
		errs [14]error
		kind uint8
	)
	l.PerpID, errs[0] = r.uint64(0)
	l.AccountID, errs[1] = r.uint64(1)
	kind, errs[2] = r.uint8(2)
	l.MarkPricePNS, errs[3] = r.big(3)
	l.LiqPricePNS, errs[4] = r.big(4)
	l.LiqLotLNS, errs[5] = r.big(5)
	l.PosLotLNS, errs[6] = r.big(6)
	l.DeltaPnlCNS, errs[7] = r.int256(7)
	l.FundingCNS, errs[8] = r.int256(8)
	l.PosAmountCNS, errs[9] = r.int256(9)
	l.PosDepositCNS, errs[10] = r.big(10)
	l.AccAmountCNS, errs[11] = r.int256(11)
	l.AccBalanceCNS, errs[12] = r.big(12)
	l.OnOrderBook, errs[13] = r.bool(13)
	if err := firstError(errs[:]); err != nil {
		return Liquidation{}, fmt.Errorf("PositionLiquidated: %w", err)
	}
	switch kind {
	case 0:
		l.Long = true
	case 1:
	default:
		return Liquidation{}, fmt.Errorf("PositionLiquidated: position type %d is neither long nor short", kind)
	}
	return l, nil
}

func firstError(errs []error) error {
	if i := slices.IndexFunc(errs, func(err error) bool { return err != nil }); i >= 0 {
		return errs[i]
	}
	return nil
}

package perpl

import (
	"math/big"
	"time"
)

// CNS is collateral, PNS is price and LNS is size, each as the raw contract integer

// ExchangeInfo joins getExchangeInfo with getFundingInterval
type ExchangeInfo struct {
	BalanceCNS         *big.Int `json:"balance_cns"`
	ProtocolBalanceCNS *big.Int `json:"protocol_balance_cns"`
	RecycleBalanceCNS  *big.Int `json:"recycle_balance_cns"`
	CollateralDecimals uint8    `json:"collateral_decimals"`
	CollateralToken    Address  `json:"collateral_token"`
	VerifierProxy      Address  `json:"verifier_proxy"`
	FundingInterval    uint64   `json:"funding_interval"`
}

// Market joins getPerpetualInfoV2, getMarginFractions and getLiquidationInfo for one perp
type Market struct {
	PerpID                    uint64          `json:"perp_id"`
	Name                      string          `json:"name"`
	Symbol                    string          `json:"symbol"`
	PriceDecimals             uint8           `json:"price_decimals"`
	LotDecimals               uint8           `json:"lot_decimals"`
	Status                    uint8           `json:"status"`
	RefPriceMaxAgeSec         uint64          `json:"ref_price_max_age_sec"`
	MarkPNS                   *big.Int        `json:"mark_pns"`
	MarkTimestamp             uint64          `json:"mark_timestamp"`
	OraclePNS                 *big.Int        `json:"oracle_pns"`
	OracleTimestampSec        uint64          `json:"oracle_timestamp_sec"`
	LongOpenInterestLNS       *big.Int        `json:"long_open_interest_lns"`
	ShortOpenInterestLNS      *big.Int        `json:"short_open_interest_lns"`
	PositionBalanceCNS        *big.Int        `json:"position_balance_cns"`
	InsuranceBalanceCNS       *big.Int        `json:"insurance_balance_cns"`
	FundingRatePct100k        int16           `json:"funding_rate_pct100k"`
	AbsFundingClampPctPer100K uint64          `json:"abs_funding_clamp_pct_per_100k"`
	MaintMarginFracHdths      uint64          `json:"maint_margin_frac_hdths"`
	InitMarginFracHdths       uint64          `json:"init_margin_frac_hdths"`
	Liquidation               LiquidationInfo `json:"liquidation"`
}

type LiquidationInfo struct {
	LiqInsAmtPer100K      uint64 `json:"liq_ins_amt_per_100k"`
	LiqUserAmtPer100K     uint64 `json:"liq_user_amt_per_100k"`
	LiqProtocolAmtPer100K uint64 `json:"liq_protocol_amt_per_100k"`
	BtlPriceThreshPer100K uint64 `json:"btl_price_thresh_per_100k"`
	BtlInsAmtPer100K      uint64 `json:"btl_ins_amt_per_100k"`
	BtlUserAmtPer100K     uint64 `json:"btl_user_amt_per_100k"`
	BtlBuyerAmtPer100K    uint64 `json:"btl_buyer_amt_per_100k"`
	BtlProtocolAmtPer100K uint64 `json:"btl_protocol_amt_per_100k"`
	BtlRestrictBuyers     bool   `json:"btl_restrict_buyers"`
}

// PerpID is set by the reader because getPositionsV2 does not return it
type Position struct {
	PerpID             uint64   `json:"perp_id"`
	AccountID          uint64   `json:"account_id"`
	NextNodeID         uint64   `json:"next_node_id"`
	PrevNodeID         uint64   `json:"prev_node_id"`
	Long               bool     `json:"long"`
	DepositCNS         *big.Int `json:"deposit_cns"`
	PricePNS           *big.Int `json:"price_pns"`
	LotLNS             *big.Int `json:"lot_lns"`
	EntryBlock         uint64   `json:"entry_block"`
	PnlCNS             *big.Int `json:"pnl_cns"`
	DeltaPnlCNS        *big.Int `json:"delta_pnl_cns"`
	PremiumPnlCNS      *big.Int `json:"premium_pnl_cns"`
	PriceResiduePNSQ16 uint64   `json:"price_residue_pnsq16"`
}

type PositionPage struct {
	Positions      []Position `json:"positions"`
	NumPositions   uint64     `json:"num_positions"`
	MarkPricePNS   *big.Int   `json:"mark_price_pns"`
	MarkPriceValid bool       `json:"mark_price_valid"`
}

type PositionIDs struct {
	StartNodeID uint64 `json:"start_node_id"`
	EndNodeID   uint64 `json:"end_node_id"`
}

type Account struct {
	AccountID        uint64      `json:"account_id"`
	BalanceCNS       *big.Int    `json:"balance_cns"`
	LockedBalanceCNS *big.Int    `json:"locked_balance_cns"`
	Frozen           uint8       `json:"frozen"`
	Address          Address     `json:"address"`
	PositionBanks    [4]*big.Int `json:"position_banks"`
}

// Time and Keeper come from the block and the transaction, not from the log
type Liquidation struct {
	Block         uint64    `json:"block"`
	LogIndex      uint64    `json:"log_index"`
	TxHash        Hash      `json:"tx_hash"`
	Time          time.Time `json:"time"`
	Keeper        Address   `json:"keeper"`
	PerpID        uint64    `json:"perp_id"`
	AccountID     uint64    `json:"account_id"`
	Long          bool      `json:"long"`
	MarkPricePNS  *big.Int  `json:"mark_price_pns"`
	LiqPricePNS   *big.Int  `json:"liq_price_pns"`
	LiqLotLNS     *big.Int  `json:"liq_lot_lns"`
	PosLotLNS     *big.Int  `json:"pos_lot_lns"`
	DeltaPnlCNS   *big.Int  `json:"delta_pnl_cns"`
	FundingCNS    *big.Int  `json:"funding_cns"`
	PosAmountCNS  *big.Int  `json:"pos_amount_cns"`
	PosDepositCNS *big.Int  `json:"pos_deposit_cns"`
	AccAmountCNS  *big.Int  `json:"acc_amount_cns"`
	AccBalanceCNS *big.Int  `json:"acc_balance_cns"`
	OnOrderBook   bool      `json:"on_order_book"`
}

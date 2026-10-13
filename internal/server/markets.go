package server

import (
	"math/big"

	"perps/internal/perpl"
	"perps/internal/problem"
	"perps/internal/risk"
)

const coverShock = 0.1

type oiBody struct {
	Size string `json:"size"`
	USD  string `json:"usd"`
}

type marketBody struct {
	PerpID              uint64   `json:"perp_id"`
	Symbol              string   `json:"symbol"`
	Name                string   `json:"name"`
	Status              uint8    `json:"status"`
	Mark                string   `json:"mark"`
	Oracle              string   `json:"oracle"`
	OracleGapBps        *float64 `json:"oracle_gap_bps"`
	OracleAgeS          int64    `json:"oracle_age_s"`
	LongOI              oiBody   `json:"long_oi"`
	ShortOI             oiBody   `json:"short_oi"`
	Positions           int      `json:"positions"`
	InsuranceFundUSD    string   `json:"insurance_fund_usd"`
	FundingRate         float64  `json:"funding_rate"`
	MaxLeverage         float64  `json:"max_leverage"`
	MaintMarginFraction float64  `json:"maint_margin_fraction"`
	SCRM10              *float64 `json:"scr_m10"`
	SCRP10              *float64 `json:"scr_p10"`
	NoBadDebtM10        bool     `json:"no_bad_debt_m10"`
	NoBadDebtP10        bool     `json:"no_bad_debt_p10"`
}

type btlBody struct {
	PriceThreshold float64 `json:"price_threshold"`
	Insurance      float64 `json:"insurance"`
	User           float64 `json:"user"`
	Buyer          float64 `json:"buyer"`
	Protocol       float64 `json:"protocol"`
	RestrictBuyers bool    `json:"restrict_buyers"`
}

type liquidationSplitBody struct {
	Insurance float64 `json:"insurance"`
	User      float64 `json:"user"`
	Protocol  float64 `json:"protocol"`
	BTL       btlBody `json:"btl"`
}

type marketsBody struct {
	head
	Items    []marketBody `json:"items"`
	Coverage []string     `json:"coverage"`
}

type marketDetailBody struct {
	head
	marketBody
	InitialMarginFraction float64              `json:"initial_margin_fraction"`
	FundingInterval       uint64               `json:"funding_interval"`
	FundingRateClamp      float64              `json:"funding_rate_clamp"`
	RefPriceMaxAgeS       uint64               `json:"ref_price_max_age_s"`
	Liquidation           liquidationSplitBody `json:"liquidation"`
	Coverage              []string             `json:"coverage"`
}

func fraction(n uint64) float64 { return float64(n) / per100K }

// marginFraction turns the contract's divisor in hundredths into the fraction of notional, 2500 becomes 0.04
func marginFraction(hdths uint64) float64 {
	if hdths == 0 {
		return 0
	}
	return 100 / float64(hdths)
}

func cover(s *Snapshot, b risk.MarketBook, shock float64) (*float64, bool, error) {
	res, err := risk.Stress(b.Market, s.Exchange, b.Positions, shock)
	if err != nil {
		return nil, false, err
	}
	if res.SCR != nil {
		v := number(*res.SCR)
		return &v, false, nil
	}
	return nil, res.NoBadDebt, nil
}

func marketOf(s *Snapshot, b risk.MarketBook, now int64) (marketBody, error) {
	m := b.Market
	mark, oracle := scaledRat(m.MarkPNS, m.PriceDecimals), scaledRat(m.OraclePNS, m.PriceDecimals)
	out := marketBody{
		PerpID: m.PerpID, Symbol: m.Symbol, Name: m.Name, Status: m.Status,
		Mark: scaled(m.MarkPNS, m.PriceDecimals), Oracle: scaled(m.OraclePNS, m.PriceDecimals),
		OracleAgeS: max(now-int64(m.OracleTimestampSec), 0), Positions: len(b.Positions),
		LongOI:           oiOf(m.LongOpenInterestLNS, m.LotDecimals, mark),
		ShortOI:          oiOf(m.ShortOpenInterestLNS, m.LotDecimals, mark),
		InsuranceFundUSD: usdRat(scaledRat(m.InsuranceBalanceCNS, s.Exchange.CollateralDecimals)),
		FundingRate:      float64(m.FundingRatePct100k) / per100K,
		MaxLeverage:      float64(m.InitMarginFracHdths) / 100, MaintMarginFraction: marginFraction(m.MaintMarginFracHdths),
	}
	if oracle.Sign() > 0 {
		gap := new(big.Rat).Sub(mark, oracle)
		gap.Quo(gap, oracle)
		gap.Mul(gap, big.NewRat(bpsPerOne, 1))
		out.OracleGapBps = optNumber(gap)
	}
	var err error
	if out.SCRM10, out.NoBadDebtM10, err = cover(s, b, -coverShock); err != nil {
		return out, err
	}
	if out.SCRP10, out.NoBadDebtP10, err = cover(s, b, coverShock); err != nil {
		return out, err
	}
	return out, nil
}

func oiOf(lns *big.Int, lotDecimals uint8, mark *big.Rat) oiBody {
	return oiBody{Size: scaled(lns, lotDecimals), USD: usdRat(new(big.Rat).Mul(scaledRat(lns, lotDecimals), mark))}
}

func (a *app) markets() endpoint {
	return func(c *call) (result, error) {
		if _, err := parseQuery(c.req); err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		priced := s.priced()
		out := marketsBody{head: headOf("perpl_markets"), Items: make([]marketBody, 0, len(priced)), Coverage: codes(a.withStale(s, covSnapshotOnly, covBadDebtFloor)...)}
		for _, b := range priced {
			m, err := marketOf(s, b, s.Time.Unix())
			if err != nil {
				return result{}, err
			}
			out.Items = append(out.Items, m)
		}
		return result{body: out}, nil
	}
}

func pricedMarket(c *call, s *Snapshot) (risk.MarketBook, error) {
	name := c.req.PathValue("market")
	b, err := resolveMarket(name, s.Books)
	if err != nil {
		return b, err
	}
	if !isPriced(b) {
		return b, &httpError{problem.CodeNotFound, "Market " + name + " has no mark price yet."}
	}
	return b, nil
}

func (a *app) market() endpoint {
	return func(c *call) (result, error) {
		if _, err := parseQuery(c.req); err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		b, err := pricedMarket(c, s)
		if err != nil {
			return result{}, err
		}
		mb, err := marketOf(s, b, s.Time.Unix())
		if err != nil {
			return result{}, err
		}
		m := b.Market
		l := m.Liquidation
		detail := marketDetailBody{
			head:                  headOf("perpl_market"),
			marketBody:            mb,
			InitialMarginFraction: marginFraction(m.InitMarginFracHdths),
			FundingInterval:       s.Exchange.FundingInterval,
			FundingRateClamp:      fraction(m.AbsFundingClampPctPer100K),
			RefPriceMaxAgeS:       m.RefPriceMaxAgeSec,
			Liquidation:           liquidationSplit(l),
			Coverage:              codes(a.withStale(s, covSnapshotOnly, covBadDebtFloor)...),
		}
		return result{body: detail}, nil
	}
}

func liquidationSplit(l perpl.LiquidationInfo) liquidationSplitBody {
	return liquidationSplitBody{
		Insurance: fraction(l.LiqInsAmtPer100K), User: fraction(l.LiqUserAmtPer100K), Protocol: fraction(l.LiqProtocolAmtPer100K),
		BTL: btlBody{
			PriceThreshold: fraction(l.BtlPriceThreshPer100K), Insurance: fraction(l.BtlInsAmtPer100K), User: fraction(l.BtlUserAmtPer100K),
			Buyer: fraction(l.BtlBuyerAmtPer100K), Protocol: fraction(l.BtlProtocolAmtPer100K), RestrictBuyers: l.BtlRestrictBuyers,
		},
	}
}

/* Liquidation map */

type bucketBody struct {
	PriceFrom        string `json:"price_from"`
	PriceTo          string `json:"price_to"`
	LongNotionalUSD  string `json:"long_notional_usd"`
	ShortNotionalUSD string `json:"short_notional_usd"`
	LongAccounts     int    `json:"long_accounts"`
	ShortAccounts    int    `json:"short_accounts"`
}

type beyondBody struct {
	LongNotionalUSD  string `json:"long_notional_usd"`
	ShortNotionalUSD string `json:"short_notional_usd"`
	LongPositions    int    `json:"long_positions"`
	ShortPositions   int    `json:"short_positions"`
}

type cumPointBody struct {
	MovePct     int    `json:"move_pct"`
	NotionalUSD string `json:"notional_usd"`
	Positions   int    `json:"positions"`
}

type cumulativeBody struct {
	Long  []cumPointBody `json:"long"`
	Short []cumPointBody `json:"short"`
}

type liquidationMapBody struct {
	head
	PerpID      uint64         `json:"perp_id"`
	Symbol      string         `json:"symbol"`
	Mark        string         `json:"mark"`
	BucketBps   int            `json:"bucket_bps"`
	RangePct    int            `json:"range_pct"`
	Buckets     []bucketBody   `json:"buckets"`
	BeyondRange beyondBody     `json:"beyond_range"`
	Cumulative  cumulativeBody `json:"cumulative"`
	Coverage    []string       `json:"coverage"`
}

func cumPoints(in []risk.CumPoint) []cumPointBody {
	out := make([]cumPointBody, len(in))
	for i, p := range in {
		out[i] = cumPointBody{MovePct: p.MovePct, NotionalUSD: usdOf(p.Notional), Positions: p.Positions}
	}
	return out
}

func (a *app) liquidationMap() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "bucket_bps", "range_pct")
		if err != nil {
			return result{}, err
		}
		bucketBps, err := intParam(q, "bucket_bps", defaultBucket, risk.MinBucketBps, risk.MaxBucketBps)
		if err != nil {
			return result{}, err
		}
		rangePct, err := intParam(q, "range_pct", defaultRange, risk.MinRangePct, risk.MaxRangePct)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		b, err := pricedMarket(c, s)
		if err != nil {
			return result{}, err
		}
		lm, err := risk.LiquidationMap(b.Market, b.Positions, bucketBps, rangePct)
		if err != nil {
			return result{}, err
		}
		out := liquidationMapBody{
			head: headOf("perpl_liquidation_map"), Coverage: codes(a.withStale(s, covSnapshotOnly)...),
			PerpID: b.Market.PerpID, Symbol: b.Market.Symbol, Mark: scaled(b.Market.MarkPNS, b.Market.PriceDecimals),
			BucketBps: lm.BucketBps, RangePct: lm.RangePct, Buckets: make([]bucketBody, len(lm.Buckets)),
			BeyondRange: beyondBody{
				LongNotionalUSD: usdOf(lm.Beyond.LongNotional), ShortNotionalUSD: usdOf(lm.Beyond.ShortNotional),
				LongPositions: lm.Beyond.LongPositions, ShortPositions: lm.Beyond.ShortPositions,
			},
			Cumulative: cumulativeBody{Long: cumPoints(lm.Cumulative.Long), Short: cumPoints(lm.Cumulative.Short)},
		}
		for i, k := range lm.Buckets {
			out.Buckets[i] = bucketBody{
				PriceFrom: priceOf(k.PriceFrom), PriceTo: priceOf(k.PriceTo), LongNotionalUSD: usdOf(k.LongNotional),
				ShortNotionalUSD: usdOf(k.ShortNotional), LongAccounts: k.LongAccounts, ShortAccounts: k.ShortAccounts,
			}
		}
		return result{body: out}, nil
	}
}

/* Stress */

type liquidatingBody struct {
	NotionalUSD string `json:"notional_usd"`
	Positions   int    `json:"positions"`
	Accounts    int    `json:"accounts"`
}

type stressPositionBody struct {
	AccountID       uint64 `json:"account_id"`
	Side            string `json:"side"`
	Size            string `json:"size"`
	NotionalUSD     string `json:"notional_usd"`
	LiqPrice        string `json:"liq_price"`
	BankruptcyPrice string `json:"bankruptcy_price"`
	BadDebtUSD      string `json:"bad_debt_usd"`
}

type stressItemBody struct {
	PerpID           uint64               `json:"perp_id"`
	Symbol           string               `json:"symbol"`
	Shock            float64              `json:"shock"`
	P0               string               `json:"p0"`
	P1               string               `json:"p1"`
	Liquidating      liquidatingBody      `json:"liquidating"`
	GapBadDebtUSD    string               `json:"gap_bad_debt_usd"`
	InsuranceFundUSD string               `json:"insurance_fund_usd"`
	SCR              *float64             `json:"scr"`
	NoBadDebt        bool                 `json:"no_bad_debt"`
	UncoveredUSD     string               `json:"uncovered_usd"`
	TopPositions     []stressPositionBody `json:"top_positions"`
}

type stressBody struct {
	head
	stressItemBody
	Coverage []string `json:"coverage"`
}

type stressAllBody struct {
	head
	Items    []stressItemBody `json:"items"`
	Coverage []string         `json:"coverage"`
}

func stressOf(s *Snapshot, b risk.MarketBook, shock float64) (stressItemBody, error) {
	res, err := risk.Stress(b.Market, s.Exchange, b.Positions, shock)
	if err != nil {
		return stressItemBody{}, err
	}
	out := stressItemBody{
		PerpID: b.Market.PerpID, Symbol: b.Market.Symbol, Shock: res.Shock, P0: priceOf(res.P0), P1: priceOf(res.P1),
		Liquidating: liquidatingBody{
			NotionalUSD: usdOf(res.Liquidating.Notional), Positions: res.Liquidating.Positions, Accounts: res.Liquidating.Accounts,
		},
		GapBadDebtUSD: usdOf(res.GapBadDebt), InsuranceFundUSD: usdOf(res.Insurance), NoBadDebt: res.NoBadDebt,
		UncoveredUSD: usdOf(res.Uncovered), TopPositions: make([]stressPositionBody, len(res.Top)),
	}
	if res.SCR != nil {
		v := number(*res.SCR)
		out.SCR = &v
	}
	for i, p := range res.Top {
		out.TopPositions[i] = stressPositionBody{
			AccountID: p.AccountID, Side: sideOf(p.Long), Size: priceOf(p.Size), NotionalUSD: usdOf(p.Notional),
			LiqPrice: priceOf(p.LiqPrice), BankruptcyPrice: priceOf(p.BankruptcyPrice), BadDebtUSD: usdOf(p.BadDebt),
		}
	}
	return out, nil
}

func (a *app) stress() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "shock")
		if err != nil {
			return result{}, err
		}
		shock, err := parseShock(q)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		b, err := pricedMarket(c, s)
		if err != nil {
			return result{}, err
		}
		item, err := stressOf(s, b, shock)
		if err != nil {
			return result{}, err
		}
		return result{body: stressBody{head: headOf("perpl_market_stress"), stressItemBody: item, Coverage: codes(a.withStale(s, covSnapshotOnly, covBadDebtFloor)...)}}, nil
	}
}

func (a *app) stressAll() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "shock")
		if err != nil {
			return result{}, err
		}
		shock, err := parseShock(q)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		priced := s.priced()
		out := stressAllBody{head: headOf("perpl_stress"), Items: make([]stressItemBody, 0, len(priced)), Coverage: codes(a.withStale(s, covSnapshotOnly, covBadDebtFloor)...)}
		for _, b := range priced {
			st, err := stressOf(s, b, shock)
			if err != nil {
				return result{}, err
			}
			out.Items = append(out.Items, st)
		}
		return result{body: out}, nil
	}
}

package server

import (
	"math/big"
	"slices"
	"time"

	"perps/internal/store"
)

const (
	fundingTargetPoints = 720
	fundingMinStep      = 30 * time.Second
)

type liquidationBody struct {
	Block         uint64  `json:"block"`
	Time          string  `json:"time"`
	Tx            string  `json:"tx"`
	LogIndex      uint64  `json:"log_index"`
	PerpID        uint64  `json:"perp_id"`
	Market        string  `json:"market"`
	AccountID     uint64  `json:"account_id"`
	AccountAddr   *string `json:"account_address"`
	Side          string  `json:"side"`
	Mark          string  `json:"mark"`
	LiqPrice      string  `json:"liq_price"`
	Size          string  `json:"size"`
	RemainingSize string  `json:"remaining_size"`
	NotionalUSD   string  `json:"notional_usd"`
	OnOrderBook   bool    `json:"on_order_book"`
	Keeper        *string `json:"keeper"`
}

type liquidationsBody struct {
	head
	Items      []liquidationBody `json:"items"`
	NextCursor string            `json:"next_cursor,omitzero"`
	Coverage   []string          `json:"coverage"`
}

func symbols(s *Snapshot) map[uint64]string {
	out := make(map[uint64]string, len(s.Books))
	for _, b := range s.Books {
		out[b.Market.PerpID] = b.Market.Symbol
	}
	return out
}

func (a *app) liquidations() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "market", "limit", "cursor")
		if err != nil {
			return result{}, err
		}
		limit, err := intParam(q, "limit", defaultLimit, 1, maxLimit)
		if err != nil {
			return result{}, err
		}
		after, err := parseCursor(q)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		mk, err := marketFilter(q, s.Books)
		if err != nil {
			return result{}, err
		}
		f := store.LiquidationFilter{Limit: limit, After: after}
		if mk != nil {
			f.PerpID = &mk.Market.PerpID
		}
		page, err := a.src.Liquidations(c.ctx, f)
		if err != nil {
			return result{}, err
		}
		ids := make([]uint64, 0, len(page.Items))
		for _, l := range page.Items {
			if !slices.Contains(ids, l.AccountID) {
				ids = append(ids, l.AccountID)
			}
		}
		addrs, err := a.src.AccountAddresses(c.ctx, ids)
		if err != nil {
			return result{}, err
		}
		names := symbols(s)
		out := liquidationsBody{head: headOf("perpl_liquidations"), Items: make([]liquidationBody, len(page.Items)), Coverage: codes(a.historyCoverage()...)}
		for i, l := range page.Items {
			name, ok := names[l.PerpID]
			if !ok {
				name = itoa(l.PerpID)
			}
			out.Items[i] = liquidationBody{
				Block: l.Block, Time: stamp(l.Time), Tx: hashOf(l.Tx), LogIndex: l.LogIndex, PerpID: l.PerpID, Market: name,
				AccountID: l.AccountID, Side: string(l.Side), Mark: l.Mark, LiqPrice: l.LiqPrice, Size: l.LiqLot, RemainingSize: l.PosLot,
				NotionalUSD: l.Notional, OnOrderBook: l.OnOrderBook,
			}
			if addr, ok := addrs[l.AccountID]; ok {
				v := addr.String()
				out.Items[i].AccountAddr = &v
			}
			if !l.Keeper.IsZero() {
				k := l.Keeper.String()
				out.Items[i].Keeper = &k
			}
		}
		if page.Next != nil {
			out.NextCursor = page.Next.String()
		}
		return result{body: out}, nil
	}
}

type dailyBody struct {
	head
	Items    []dayBody `json:"items"`
	Coverage []string  `json:"coverage"`
}

type dayBody struct {
	Day         string `json:"day"`
	Count       int64  `json:"count"`
	NotionalUSD string `json:"notional_usd"`
}

func (a *app) liquidationsDaily() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "market", "days")
		if err != nil {
			return result{}, err
		}
		days, err := intParam(q, "days", defaultDays, 1, maxDays)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		mk, err := marketFilter(q, s.Books)
		if err != nil {
			return result{}, err
		}
		var perp *uint64
		if mk != nil {
			perp = &mk.Market.PerpID
		}
		y, m, d := a.now().UTC().Date()
		first := time.Date(y, m, d-(days-1), 0, 0, 0, 0, time.UTC)
		rows, err := a.src.LiquidationsDaily(c.ctx, perp, first)
		if err != nil {
			return result{}, err
		}
		byDay := make(map[string]store.DailyLiquidations, len(rows))
		for _, r := range rows {
			byDay[r.Day.UTC().Format(time.DateOnly)] = r
		}
		out := dailyBody{head: headOf("perpl_liquidations_daily"), Items: make([]dayBody, days), Coverage: codes(a.historyCoverage()...)}
		for i := range out.Items {
			day := first.AddDate(0, 0, i).Format(time.DateOnly)
			out.Items[i] = dayBody{Day: day, NotionalUSD: "0"}
			if r, ok := byDay[day]; ok {
				out.Items[i].Count, out.Items[i].NotionalUSD = r.Count, r.NotionalUSD
			}
		}
		return result{body: out}, nil
	}
}

type fundingPointBody struct {
	Time        string   `json:"time"`
	Block       uint64   `json:"block"`
	Mark        string   `json:"mark"`
	Oracle      string   `json:"oracle"`
	GapBps      *float64 `json:"gap_bps"`
	FundingRate float64  `json:"funding_rate"`
}

type fundingBody struct {
	head
	PerpID   uint64             `json:"perp_id"`
	Symbol   string             `json:"symbol"`
	Hours    int                `json:"hours"`
	StepS    int64              `json:"step_s"`
	Items    []fundingPointBody `json:"items"`
	Coverage []string           `json:"coverage"`
}

// fundingStep spreads the window over about 720 points and keeps whole multiples of the 30 s refresh
func fundingStep(hours int) time.Duration {
	window := time.Duration(hours) * time.Hour
	step := (window/fundingTargetPoints + fundingMinStep - 1) / fundingMinStep * fundingMinStep
	return max(step, fundingMinStep)
}

func gapBps(mark, oracle string) *float64 {
	m, ok1 := new(big.Rat).SetString(mark)
	o, ok2 := new(big.Rat).SetString(oracle)
	if !ok1 || !ok2 || o.Sign() <= 0 {
		return nil
	}
	g := new(big.Rat).Sub(m, o)
	g.Quo(g, o)
	g.Mul(g, big.NewRat(bpsPerOne, 1))
	return optNumber(g)
}

func (a *app) funding() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "market", "hours")
		if err != nil {
			return result{}, err
		}
		if q.Get("market") == "" {
			return result{}, invalid("market is required.")
		}
		hours, err := intParam(q, "hours", defaultHours, 1, maxHours)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		mk, err := marketFilter(q, s.Books)
		if err != nil {
			return result{}, err
		}
		step := fundingStep(hours)
		since := a.now().Add(-time.Duration(hours) * time.Hour)
		points, err := a.src.FundingSeries(c.ctx, mk.Market.PerpID, since, step)
		if err != nil {
			return result{}, err
		}
		out := fundingBody{
			head: headOf("perpl_funding"), PerpID: mk.Market.PerpID, Symbol: mk.Market.Symbol, Hours: hours, StepS: int64(step.Seconds()),
			Items: make([]fundingPointBody, len(points)), Coverage: codes(covFunding),
		}
		for i, p := range points {
			out.Items[i] = fundingPointBody{
				Time: stamp(p.Time), Block: p.Block, Mark: p.Mark, Oracle: p.Oracle, GapBps: gapBps(p.Mark, p.Oracle),
				FundingRate: float64(p.FundingRatePct100k) / per100K,
			}
		}
		return result{body: out}, nil
	}
}

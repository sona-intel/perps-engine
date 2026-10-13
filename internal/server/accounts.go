package server

import (
	"slices"

	"perps/internal/risk"
	"perps/internal/sources"
)

type standingBody struct {
	Rank      int    `json:"rank"`
	PnlUSD    string `json:"pnl_usd"`
	VolumeUSD string `json:"volume_usd"`
}

type standingsBody struct {
	Day *standingBody `json:"day"`
	All *standingBody `json:"all"`
}

type walletPositionBody struct {
	PerpID               uint64   `json:"perp_id"`
	Market               string   `json:"market"`
	Side                 string   `json:"side"`
	Size                 string   `json:"size"`
	EntryPrice           string   `json:"entry_price"`
	MarkPrice            string   `json:"mark_price"`
	DepositUSD           string   `json:"deposit_usd"`
	PremiumPnlUSD        string   `json:"premium_pnl_usd"`
	NotionalUSD          string   `json:"notional_usd"`
	MaintenanceMarginUSD string   `json:"maintenance_margin_usd"`
	Leverage             *float64 `json:"leverage"`
	LiqPrice             string   `json:"liq_price"`
	BankruptcyPrice      string   `json:"bankruptcy_price"`
	Distance             *float64 `json:"distance"`
	OIShare              float64  `json:"oi_share"`
}

type walletBody struct {
	head
	AccountID        uint64               `json:"account_id"`
	Address          string               `json:"address"`
	BalanceUSD       string               `json:"balance_usd"`
	LockedUSD        string               `json:"locked_usd"`
	Frozen           bool                 `json:"frozen"`
	TotalNotionalUSD string               `json:"total_notional_usd"`
	TotalDepositUSD  string               `json:"total_deposit_usd"`
	MinDistance      *float64             `json:"min_distance"`
	Positions        []walletPositionBody `json:"positions"`
	Standing         standingsBody        `json:"standing"`
	Coverage         []string             `json:"coverage"`
}

func standingOf(r *risk.LeaderRow) *standingBody {
	if r == nil {
		return nil
	}
	return &standingBody{Rank: r.Rank, PnlUSD: usdOf(r.PnlUSD), VolumeUSD: usdOf(r.VolumeUSD)}
}

func (a *app) account() endpoint {
	return func(c *call) (result, error) {
		if _, err := parseQuery(c.req); err != nil {
			return result{}, err
		}
		addr, err := parseAddress(c.req.PathValue("address"))
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		acct, err := a.src.Account(c.ctx, addr)
		if err != nil {
			return result{}, err
		}
		priced := s.priced()
		var held []risk.Assessed
		marks := make(map[uint64]string, len(priced))
		for _, b := range priced {
			marks[b.Market.PerpID] = scaled(b.Market.MarkPNS, b.Market.PriceDecimals)
			for _, p := range b.Positions {
				if p.AccountID == acct.AccountID {
					held = append(held, p)
				}
			}
		}
		cov := []coverage{covSnapshotOnly}
		var st risk.Standing
		for _, v := range []struct {
			period sources.Period
			dst    **risk.LeaderRow
		}{{sources.PeriodDay, &st.Day}, {sources.PeriodAll, &st.All}} {
			lb, ok := a.src.Leaderboard(v.period)
			if !ok {
				if !slices.Contains(cov, covLeaderboard) {
					cov = append(cov, covLeaderboard)
				}
				continue
			}
			for _, r := range lb.Rows {
				if r.Address == addr {
					row := leaderRow(r, s.Exchange.CollateralDecimals)
					*v.dst = &row
					break
				}
			}
		}
		w, err := risk.WalletCard(s.Exchange, acct, held, risk.MarketTotals(priced), st)
		if err != nil {
			return result{}, err
		}
		out := walletBody{
			head: headOf("perpl_account"), Coverage: codes(a.withStale(s, cov...)...), AccountID: w.AccountID, Address: w.Address.String(), BalanceUSD: usdOf(w.BalanceUSD), LockedUSD: usdOf(w.LockedUSD), Frozen: w.Frozen,
			TotalNotionalUSD: usdOf(w.TotalNotional), TotalDepositUSD: usdOf(w.TotalDeposit), MinDistance: w.MinDistance,
			Positions: make([]walletPositionBody, len(w.Positions)),
			Standing:  standingsBody{Day: standingOf(w.Standing.Day), All: standingOf(w.Standing.All)},
		}
		for i, p := range w.Positions {
			out.Positions[i] = walletPositionBody{
				PerpID: p.PerpID, Market: p.Symbol, Side: sideOf(p.Long), Size: priceRat(p.Size), EntryPrice: priceRat(p.Entry),
				MarkPrice: marks[p.PerpID], DepositUSD: usdRat(p.Deposit), PremiumPnlUSD: usdRat(p.PremiumPnl), NotionalUSD: usdRat(p.Notional),
				MaintenanceMarginUSD: usdRat(p.MMR), Leverage: optNumber(p.Leverage), LiqPrice: priceRat(p.LiqPrice),
				BankruptcyPrice: priceRat(p.BankruptcyPrice), Distance: optNumber(p.Distance), OIShare: number(p.OIShare),
			}
		}
		return result{body: out}, nil
	}
}

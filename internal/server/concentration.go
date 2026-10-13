package server

import (
	"slices"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/sources"
)

const keeperLimit = 10

type accountShareBody struct {
	AccountID   uint64  `json:"account_id"`
	Address     *string `json:"address"`
	NotionalUSD string  `json:"notional_usd"`
	Share       float64 `json:"share"`
	Positions   int     `json:"positions"`
}

type marketConcentrationBody struct {
	PerpID           uint64             `json:"perp_id"`
	Symbol           string             `json:"symbol"`
	Positions        int                `json:"positions"`
	LongNotionalUSD  string             `json:"long_notional_usd"`
	ShortNotionalUSD string             `json:"short_notional_usd"`
	TotalNotionalUSD string             `json:"total_notional_usd"`
	Top1             float64            `json:"top1"`
	Top5             float64            `json:"top5"`
	Top10            float64            `json:"top10"`
	TopAccounts      []accountShareBody `json:"top_accounts"`
}

type overallBody struct {
	Accounts         int                `json:"accounts"`
	TotalNotionalUSD string             `json:"total_notional_usd"`
	Top1             float64            `json:"top1"`
	Top5             float64            `json:"top5"`
	Top10            float64            `json:"top10"`
	TopAccounts      []accountShareBody `json:"top_accounts"`
}

type volumeBody struct {
	Rows           int     `json:"rows"`
	TotalVolumeUSD string  `json:"total_volume_usd"`
	Top10VolumeUSD string  `json:"top10_volume_usd"`
	Top1           float64 `json:"top1"`
	Top5           float64 `json:"top5"`
	Top10          float64 `json:"top10"`
}

type volumesBody struct {
	Day *volumeBody `json:"day"`
	All *volumeBody `json:"all"`
}

type keeperBody struct {
	Address      string  `json:"address"`
	Liquidations int64   `json:"liquidations"`
	Share        float64 `json:"share"`
}

type keepersBody struct {
	Total      int64        `json:"total"`
	FirstBlock uint64       `json:"first_block"`
	LastBlock  uint64       `json:"last_block"`
	Items      []keeperBody `json:"items"`
}

type concentrationBody struct {
	head
	Markets  []marketConcentrationBody `json:"markets"`
	Overall  overallBody               `json:"overall"`
	Volume   volumesBody               `json:"volume"`
	Keepers  keepersBody               `json:"keepers"`
	Coverage []string                  `json:"coverage"`
}

func leaderRow(r sources.LeaderboardRow, decimals uint8) risk.LeaderRow {
	return risk.LeaderRow{
		Rank: r.Rank, Address: r.Address, PnlUSD: floatOf(scaledRat(r.PnlCNS, decimals)), VolumeUSD: floatOf(scaledRat(r.VolumeCNS, decimals)),
	}
}

func leaderRows(lb sources.Leaderboard, decimals uint8) []risk.LeaderRow {
	rows := make([]risk.LeaderRow, len(lb.Rows))
	for i, r := range lb.Rows {
		rows[i] = leaderRow(r, decimals)
	}
	return rows
}

func volumeOf(vs risk.VolumeShare) *volumeBody {
	return &volumeBody{
		Rows: vs.Rows, TotalVolumeUSD: usdOf(vs.TotalVolume), Top10VolumeUSD: usdOf(vs.Top10Volume),
		Top1: number(vs.Top1), Top5: number(vs.Top5), Top10: number(vs.Top10),
	}
}

func (a *app) concentration() endpoint {
	return func(c *call) (result, error) {
		if _, err := parseQuery(c.req); err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		res := risk.Concentration(s.priced())
		addrs, err := a.src.AccountAddresses(c.ctx, topAccountIDs(res))
		if err != nil {
			return result{}, err
		}
		out := concentrationBody{
			head:    headOf("perpl_concentration"),
			Markets: make([]marketConcentrationBody, len(res.Markets)),
			Overall: overallBody{
				Accounts: res.Overall.Accounts, TotalNotionalUSD: usdOf(res.Overall.TotalNotional), Top1: number(res.Overall.Top1),
				Top5: number(res.Overall.Top5), Top10: number(res.Overall.Top10), TopAccounts: accountShares(res.Overall.TopAccounts, addrs),
			},
		}
		for i, m := range res.Markets {
			out.Markets[i] = marketConcentrationBody{
				PerpID: m.PerpID, Symbol: m.Symbol, Positions: m.Positions, LongNotionalUSD: usdOf(m.LongNotional),
				ShortNotionalUSD: usdOf(m.ShortNotional), TotalNotionalUSD: usdOf(m.TotalNotional),
				Top1: number(m.Top1), Top5: number(m.Top5), Top10: number(m.Top10), TopAccounts: accountShares(m.TopAccounts, addrs),
			}
		}
		cov := []coverage{covSnapshotOnly}
		decimals := s.Exchange.CollateralDecimals
		for _, v := range []struct {
			period sources.Period
			dst    **volumeBody
		}{{sources.PeriodDay, &out.Volume.Day}, {sources.PeriodAll, &out.Volume.All}} {
			lb, ok := a.src.Leaderboard(v.period)
			if !ok {
				if !slices.Contains(cov, covLeaderboard) {
					cov = append(cov, covLeaderboard)
				}
				continue
			}
			*v.dst = volumeOf(risk.VolumeShares(leaderRows(lb, decimals)))
		}
		if out.Keepers, err = a.keepers(c); err != nil {
			return result{}, err
		}
		out.Coverage = codes(a.withStale(s, append(cov, a.historyCoverage()...)...)...)
		return result{body: out}, nil
	}
}

func (a *app) keepers(c *call) (keepersBody, error) {
	counts, err := a.src.KeeperCounts(c.ctx, keeperLimit)
	if err != nil {
		return keepersBody{}, err
	}
	totals, err := a.src.CountLiquidations(c.ctx)
	if err != nil {
		return keepersBody{}, err
	}
	out := keepersBody{Total: totals.Count, FirstBlock: totals.FirstBlock, LastBlock: totals.LastBlock, Items: make([]keeperBody, len(counts))}
	for i, k := range counts {
		out.Items[i] = keeperBody{Address: k.Keeper.String(), Liquidations: k.Liquidations}
		if totals.Count > 0 {
			out.Items[i].Share = min(float64(k.Liquidations)/float64(totals.Count), 1)
		}
	}
	return out, nil
}

func topAccountIDs(res risk.ConcentrationResult) []uint64 {
	seen := map[uint64]bool{}
	var ids []uint64
	add := func(list []risk.AccountShare) {
		for _, a := range list {
			if !seen[a.AccountID] {
				seen[a.AccountID] = true
				ids = append(ids, a.AccountID)
			}
		}
	}
	for _, m := range res.Markets {
		add(m.TopAccounts)
	}
	add(res.Overall.TopAccounts)
	slices.Sort(ids)
	return ids
}

func accountShares(in []risk.AccountShare, addrs map[uint64]perpl.Address) []accountShareBody {
	out := make([]accountShareBody, len(in))
	for i, a := range in {
		out[i] = accountShareBody{
			AccountID: a.AccountID, NotionalUSD: usdOf(a.Notional), Share: number(a.Share), Positions: a.Positions,
		}
		if addr, ok := addrs[a.AccountID]; ok {
			s := addr.String()
			out[i].Address = &s
		}
	}
	return out
}

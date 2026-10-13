package risk

import (
	"bytes"
	"cmp"
	"slices"

	"perps/internal/perpl"
)

const topAccountsN = 10

type MarketBook struct {
	Market    perpl.Market
	Positions []Assessed
}

type AccountShare struct {
	AccountID uint64
	Notional  float64
	// Share is the notional over the total open notional of the scope, so it stays within 0 and 1
	Share     float64
	Positions int
}

type MarketConcentration struct {
	PerpID        uint64
	Symbol        string
	Positions     int
	LongNotional  float64
	ShortNotional float64
	TotalNotional float64
	Top1          float64
	Top5          float64
	Top10         float64
	TopAccounts   []AccountShare
}

type OverallConcentration struct {
	Accounts      int
	TotalNotional float64
	Top1          float64
	Top5          float64
	Top10         float64
	TopAccounts   []AccountShare
}

type ConcentrationResult struct {
	Markets []MarketConcentration
	Overall OverallConcentration
}

type accountSum struct {
	notional  float64
	positions int
}

func sideTotals(ps []Assessed) (long, short float64) {
	for i := range ps {
		n := ratFloat(ps[i].Notional)
		if ps[i].Long {
			long += n
		} else {
			short += n
		}
	}
	return long, short
}

func addAccounts(sums map[uint64]*accountSum, ps []Assessed) {
	for i := range ps {
		s := sums[ps[i].AccountID]
		if s == nil {
			s = &accountSum{}
			sums[ps[i].AccountID] = s
		}
		s.notional += ratFloat(ps[i].Notional)
		s.positions++
	}
}

func rankAccounts(sums map[uint64]*accountSum, total float64) []AccountShare {
	out := make([]AccountShare, 0, len(sums))
	for id, s := range sums {
		out = append(out, AccountShare{AccountID: id, Notional: s.notional, Positions: s.positions})
	}
	slices.SortFunc(out, func(l, r AccountShare) int {
		if c := cmp.Compare(r.Notional, l.Notional); c != 0 {
			return c
		}
		return cmp.Compare(l.AccountID, r.AccountID)
	})
	if total > 0 {
		for i := range out {
			out[i].Share = out[i].Notional / total
		}
	}
	return out
}

func topSum(ranked []AccountShare, k int) float64 {
	var s float64
	for _, a := range ranked[:min(k, len(ranked))] {
		s += a.Share
	}
	return min(s, 1)
}

func Concentration(books []MarketBook) ConcentrationResult {
	res := ConcentrationResult{Markets: make([]MarketConcentration, 0, len(books))}
	overall := map[uint64]*accountSum{}
	var overallTotal float64
	for _, b := range books {
		long, short := sideTotals(b.Positions)
		total := long + short
		sums := map[uint64]*accountSum{}
		addAccounts(sums, b.Positions)
		addAccounts(overall, b.Positions)
		overallTotal += total
		ranked := rankAccounts(sums, total)
		res.Markets = append(res.Markets, MarketConcentration{
			PerpID:        b.Market.PerpID,
			Symbol:        b.Market.Symbol,
			Positions:     len(b.Positions),
			LongNotional:  long,
			ShortNotional: short,
			TotalNotional: total,
			Top1:          topSum(ranked, 1),
			Top5:          topSum(ranked, 5),
			Top10:         topSum(ranked, 10),
			TopAccounts:   ranked[:min(topAccountsN, len(ranked))],
		})
	}
	ranked := rankAccounts(overall, overallTotal)
	res.Overall = OverallConcentration{
		Accounts:      len(ranked),
		TotalNotional: overallTotal,
		Top1:          topSum(ranked, 1),
		Top5:          topSum(ranked, 5),
		Top10:         topSum(ranked, 10),
		TopAccounts:   ranked[:min(topAccountsN, len(ranked))],
	}
	return res
}

type LeaderRow struct {
	Rank      int
	Address   perpl.Address
	PnlUSD    float64
	VolumeUSD float64
}

type VolumeShare struct {
	Rows        int
	TotalVolume float64
	Top10Volume float64
	Top1        float64
	Top5        float64
	Top10       float64
}

func VolumeShares(rows []LeaderRow) VolumeShare {
	vols := make([]float64, 0, len(rows))
	var total float64
	for _, r := range rows {
		v := max(r.VolumeUSD, 0)
		vols = append(vols, v)
		total += v
	}
	slices.SortFunc(vols, func(a, b float64) int { return cmp.Compare(b, a) })
	out := VolumeShare{Rows: len(rows), TotalVolume: total}
	if total == 0 {
		return out
	}
	prefix := func(k int) float64 {
		var s float64
		for _, v := range vols[:min(k, len(vols))] {
			s += v
		}
		return s
	}
	out.Top10Volume = prefix(10)
	out.Top1, out.Top5, out.Top10 = prefix(1)/total, prefix(5)/total, out.Top10Volume/total
	return out
}

type KeeperStat struct {
	Address      perpl.Address
	Liquidations int
	Share        float64
}

type KeeperSummary struct {
	Keepers    []KeeperStat
	Total      int
	FirstBlock uint64
	LastBlock  uint64
}

func Keepers(liqs []perpl.Liquidation) KeeperSummary {
	out := KeeperSummary{Total: len(liqs), Keepers: []KeeperStat{}}
	if len(liqs) == 0 {
		return out
	}
	counts := map[perpl.Address]int{}
	out.FirstBlock, out.LastBlock = liqs[0].Block, liqs[0].Block
	for _, l := range liqs {
		counts[l.Keeper]++
		out.FirstBlock = min(out.FirstBlock, l.Block)
		out.LastBlock = max(out.LastBlock, l.Block)
	}
	for a, c := range counts {
		out.Keepers = append(out.Keepers, KeeperStat{Address: a, Liquidations: c, Share: float64(c) / float64(len(liqs))})
	}
	slices.SortFunc(out.Keepers, func(l, r KeeperStat) int {
		if c := cmp.Compare(r.Liquidations, l.Liquidations); c != 0 {
			return c
		}
		return bytes.Compare(l.Address[:], r.Address[:])
	})
	return out
}

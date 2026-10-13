package risk

import (
	"cmp"
	"slices"

	"perps/internal/perpl"
)

type MarketTotal struct {
	PerpID        uint64
	Symbol        string
	LongNotional  float64
	ShortNotional float64
}

func MarketTotals(books []MarketBook) map[uint64]MarketTotal {
	out := make(map[uint64]MarketTotal, len(books))
	for _, b := range books {
		long, short := sideTotals(b.Positions)
		out[b.Market.PerpID] = MarketTotal{PerpID: b.Market.PerpID, Symbol: b.Market.Symbol, LongNotional: long, ShortNotional: short}
	}
	return out
}

type Standing struct {
	Day *LeaderRow
	All *LeaderRow
}

type WalletPosition struct {
	Symbol string
	Assessed
	// OIShare is the notional over the open notional of the position's own side
	OIShare float64
}

type Wallet struct {
	AccountID     uint64
	Address       perpl.Address
	BalanceUSD    float64
	LockedUSD     float64
	Frozen        bool
	Positions     []WalletPosition
	TotalNotional float64
	TotalDeposit  float64
	// MinDistance is the smallest distance to liquidation over the positions, nil with no positions
	MinDistance *float64
	Standing    Standing
}

func WalletCard(ei perpl.ExchangeInfo, acct perpl.Account, positions []Assessed, totals map[uint64]MarketTotal, standing Standing) (Wallet, error) {
	if acct.BalanceCNS == nil || acct.LockedBalanceCNS == nil {
		return Wallet{}, invalidInput("account %d has no balance", acct.AccountID)
	}
	w := Wallet{
		AccountID:  acct.AccountID,
		Address:    acct.Address,
		BalanceUSD: ratFloat(scaleDown(acct.BalanceCNS, int(ei.CollateralDecimals))),
		LockedUSD:  ratFloat(scaleDown(acct.LockedBalanceCNS, int(ei.CollateralDecimals))),
		Frozen:     acct.Frozen != 0,
		Positions:  make([]WalletPosition, 0, len(positions)),
		Standing:   standing,
	}
	for _, p := range positions {
		if p.AccountID != acct.AccountID {
			return Wallet{}, invalidInput("position of account %d given for account %d", p.AccountID, acct.AccountID)
		}
		if p.Notional == nil {
			return Wallet{}, invalidInput("position %d/%d has no notional", p.PerpID, p.AccountID)
		}
		mt, ok := totals[p.PerpID]
		if !ok {
			return Wallet{}, invalidInput("no market totals for perp %d", p.PerpID)
		}
		n := ratFloat(p.Notional)
		side := mt.ShortNotional
		if p.Long {
			side = mt.LongNotional
		}
		wp := WalletPosition{Symbol: mt.Symbol, Assessed: p}
		if side > 0 {
			wp.OIShare = min(n/side, 1)
		}
		w.Positions = append(w.Positions, wp)
		w.TotalNotional += n
		w.TotalDeposit += ratFloat(p.Deposit)
		if p.Distance != nil {
			d := ratFloat(p.Distance)
			if w.MinDistance == nil || d < *w.MinDistance {
				w.MinDistance = &d
			}
		}
	}
	slices.SortStableFunc(w.Positions, func(l, r WalletPosition) int {
		if c := r.Notional.Cmp(l.Notional); c != 0 {
			return c
		}
		return cmp.Compare(l.PerpID, r.PerpID)
	})
	return w, nil
}

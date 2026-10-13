package risk

import (
	"cmp"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"

	"perps/internal/perpl"
)

const stressTopN = 10

type Liquidating struct {
	Notional  float64
	Positions int
	Accounts  int
}

type StressPosition struct {
	PerpID          uint64
	AccountID       uint64
	Long            bool
	Size            float64
	Notional        float64
	LiqPrice        float64
	BankruptcyPrice float64
	BadDebt         float64
}

type StressResult struct {
	Shock       float64
	P0          float64
	P1          float64
	Liquidating Liquidating
	GapBadDebt  float64
	Insurance   float64
	// SCR is nil when there is no bad debt
	SCR       *float64
	NoBadDebt bool
	Uncovered float64
	Top       []StressPosition
}

// decimalRat reads a float64 through its shortest decimal form so that 0.15 means exactly 15 percent
func decimalRat(f float64) *big.Rat {
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return r
}

func checkShock(shock float64) error {
	if math.IsNaN(shock) || shock == 0 || shock < -MaxShock || shock > MaxShock {
		return &ParamError{Param: "shock", Reason: fmt.Sprintf("must be nonzero and between -%g and %g", MaxShock, MaxShock)}
	}
	return nil
}

type stressHit struct {
	a       *Assessed
	badDebt *big.Rat
}

func Stress(m perpl.Market, ei perpl.ExchangeInfo, book []Assessed, shock float64) (StressResult, error) {
	if err := checkShock(shock); err != nil {
		return StressResult{}, err
	}
	mark, err := markRat(m)
	if err != nil {
		return StressResult{}, err
	}
	if m.InsuranceBalanceCNS == nil || m.InsuranceBalanceCNS.Sign() < 0 {
		return StressResult{}, invalidInput("market %d has no insurance balance", m.PerpID)
	}
	insurance := scaleDown(m.InsuranceBalanceCNS, int(ei.CollateralDecimals))
	x := decimalRat(shock)
	p1 := new(big.Rat).Add(big.NewRat(1, 1), x)
	p1.Mul(p1, mark)
	down := shock < 0

	notional, debt := new(big.Rat), new(big.Rat)
	var hits []stressHit
	for i := range book {
		a := &book[i]
		var gap *big.Rat
		switch {
		case down && a.Long && a.LiqPrice.Cmp(p1) >= 0:
			gap = new(big.Rat).Sub(a.BankruptcyPrice, p1)
		case !down && !a.Long && a.LiqPrice.Cmp(p1) <= 0:
			gap = new(big.Rat).Sub(p1, a.BankruptcyPrice)
		default:
			continue
		}
		notional.Add(notional, new(big.Rat).Mul(a.Size, p1))
		bad := new(big.Rat)
		if gap.Sign() > 0 {
			bad.Mul(gap, a.Size)
			debt.Add(debt, bad)
		}
		hits = append(hits, stressHit{a: a, badDebt: bad})
	}

	res := StressResult{
		Shock:     shock,
		P0:        ratFloat(mark),
		P1:        ratFloat(p1),
		Insurance: ratFloat(insurance),
		NoBadDebt: debt.Sign() == 0,
		Liquidating: Liquidating{
			Notional:  ratFloat(notional),
			Positions: len(hits),
			Accounts:  distinctAccounts(hits),
		},
		GapBadDebt: ratFloat(debt),
	}
	if debt.Sign() > 0 {
		scr := ratFloat(new(big.Rat).Quo(insurance, debt))
		res.SCR = &scr
		if short := new(big.Rat).Sub(debt, insurance); short.Sign() > 0 {
			res.Uncovered = ratFloat(short)
		}
	}
	res.Top = topHits(hits)
	return res, nil
}

func distinctAccounts(hits []stressHit) int {
	seen := make(map[uint64]struct{}, len(hits))
	for _, h := range hits {
		seen[h.a.AccountID] = struct{}{}
	}
	return len(seen)
}

func topHits(hits []stressHit) []StressPosition {
	slices.SortFunc(hits, func(l, r stressHit) int {
		if c := r.a.Notional.Cmp(l.a.Notional); c != 0 {
			return c
		}
		return cmp.Compare(l.a.AccountID, r.a.AccountID)
	})
	n := min(len(hits), stressTopN)
	out := make([]StressPosition, n)
	for i, h := range hits[:n] {
		out[i] = StressPosition{
			PerpID:          h.a.PerpID,
			AccountID:       h.a.AccountID,
			Long:            h.a.Long,
			Size:            ratFloat(h.a.Size),
			Notional:        ratFloat(h.a.Notional),
			LiqPrice:        ratFloat(h.a.LiqPrice),
			BankruptcyPrice: ratFloat(h.a.BankruptcyPrice),
			BadDebt:         ratFloat(h.badDebt),
		}
	}
	return out
}

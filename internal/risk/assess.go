package risk

import (
	"math/big"

	"perps/internal/perpl"
)

type Assessed struct {
	PerpID          uint64
	AccountID       uint64
	Long            bool
	Entry           *big.Rat
	Size            *big.Rat
	Deposit         *big.Rat
	PremiumPnl      *big.Rat
	MMR             *big.Rat
	LiqPrice        *big.Rat
	BankruptcyPrice *big.Rat
	Notional        *big.Rat
	// Leverage is nil when the deposit is zero
	Leverage *big.Rat
	// Distance is the signed fraction of mark to the liquidation price, zero or below means liquidatable now
	Distance *big.Rat
}

func sideSign(long bool) *big.Rat {
	if long {
		return big.NewRat(1, 1)
	}
	return big.NewRat(-1, 1)
}

func scaleDown(n *big.Int, decimals int) *big.Rat {
	return new(big.Rat).SetFrac(n, pow10(decimals))
}

// EntryPrice follows the SDK effective_entry_price, rebuilding the sub-tick entry from the stored tick and residue
func EntryPrice(long bool, pricePNS *big.Int, residueQ16 uint64, priceDecimals uint8) *big.Rat {
	if residueQ16 == 0 {
		return scaleDown(pricePNS, int(priceDecimals))
	}
	pns := new(big.Int).Set(pricePNS)
	if long && pns.Sign() > 0 {
		pns.Sub(pns, bigOne)
	}
	e := new(big.Rat).SetInt(pns)
	e.Add(e, new(big.Rat).SetFrac(new(big.Int).SetUint64(residueQ16), big.NewInt(1<<16)))
	return e.Quo(e, new(big.Rat).SetInt(pow10(int(priceDecimals))))
}

// MaintMargin is entry * size / MMF where MMF is the contract fraction in hundredths of a percent over 100
func MaintMargin(entry, size *big.Rat, mmfHdths uint64) (*big.Rat, error) {
	if mmfHdths == 0 {
		return nil, invalidInput("maintenance margin fraction is zero")
	}
	r := new(big.Rat).Mul(entry, size)
	r.Mul(r, big.NewRat(100, 1))
	return r.Quo(r, new(big.Rat).SetInt(new(big.Int).SetUint64(mmfHdths))), nil
}

func clampZero(r *big.Rat) *big.Rat {
	if r.Sign() < 0 {
		return new(big.Rat)
	}
	return r
}

func LiquidationPrice(long bool, entry, size, deposit, premium, mmr *big.Rat) *big.Rat {
	gap := new(big.Rat).Sub(mmr, deposit)
	gap.Sub(gap, premium)
	gap.Mul(gap, sideSign(long))
	gap.Quo(gap, size)
	return clampZero(gap.Add(gap, entry))
}

func BankruptcyPrice(long bool, entry, size, deposit, premium *big.Rat) *big.Rat {
	eq := new(big.Rat).Add(deposit, premium)
	eq.Mul(eq, sideSign(long))
	eq.Quo(eq, size)
	return clampZero(eq.Sub(entry, eq))
}

func markRat(m perpl.Market) (*big.Rat, error) {
	if m.MarkPNS == nil || m.MarkPNS.Sign() <= 0 {
		return nil, invalidInput("market %d has no usable mark price", m.PerpID)
	}
	return scaleDown(m.MarkPNS, int(m.PriceDecimals)), nil
}

func checkPosition(p perpl.Position) error {
	switch {
	case p.PricePNS == nil || p.PricePNS.Sign() < 0:
		return invalidInput("position %d/%d has no entry price", p.PerpID, p.AccountID)
	case p.LotLNS == nil || p.LotLNS.Sign() <= 0:
		return invalidInput("position %d/%d has no size", p.PerpID, p.AccountID)
	case p.DepositCNS == nil || p.DepositCNS.Sign() < 0:
		return invalidInput("position %d/%d has no deposit", p.PerpID, p.AccountID)
	case p.PremiumPnlCNS == nil:
		return invalidInput("position %d/%d has no premium pnl", p.PerpID, p.AccountID)
	}
	return nil
}

func Assess(m perpl.Market, ei perpl.ExchangeInfo, p perpl.Position) (Assessed, error) {
	mark, err := markRat(m)
	if err != nil {
		return Assessed{}, err
	}
	if err := checkPosition(p); err != nil {
		return Assessed{}, err
	}
	a := Assessed{
		PerpID:     p.PerpID,
		AccountID:  p.AccountID,
		Long:       p.Long,
		Entry:      EntryPrice(p.Long, p.PricePNS, p.PriceResiduePNSQ16, m.PriceDecimals),
		Size:       scaleDown(p.LotLNS, int(m.LotDecimals)),
		Deposit:    scaleDown(p.DepositCNS, int(ei.CollateralDecimals)),
		PremiumPnl: scaleDown(p.PremiumPnlCNS, int(ei.CollateralDecimals)),
	}
	if a.MMR, err = MaintMargin(a.Entry, a.Size, m.MaintMarginFracHdths); err != nil {
		return Assessed{}, err
	}
	a.LiqPrice = LiquidationPrice(a.Long, a.Entry, a.Size, a.Deposit, a.PremiumPnl, a.MMR)
	a.BankruptcyPrice = BankruptcyPrice(a.Long, a.Entry, a.Size, a.Deposit, a.PremiumPnl)
	a.Notional = new(big.Rat).Mul(a.Size, mark)
	if a.Deposit.Sign() > 0 {
		a.Leverage = new(big.Rat).Quo(a.Notional, a.Deposit)
	}
	d := new(big.Rat).Sub(mark, a.LiqPrice)
	d.Mul(d, sideSign(a.Long))
	a.Distance = d.Quo(d, mark)
	return a, nil
}

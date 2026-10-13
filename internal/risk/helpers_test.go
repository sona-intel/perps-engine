package risk

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"perps/internal/perpl"
)

func rat(t testing.TB, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad rational %q", s)
	}
	return r
}

func bi(t testing.TB, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad integer %q", s)
	}
	return n
}

func relErr(got, want *big.Rat) float64 {
	if want.Sign() == 0 {
		if got.Sign() == 0 {
			return 0
		}
		return 1
	}
	d := new(big.Rat).Sub(got, want)
	d.Quo(d, want)
	f, _ := d.Float64()
	if f < 0 {
		f = -f
	}
	return f
}

func assertRel(t testing.TB, name string, got, want *big.Rat, tol float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil, want %s", name, want.FloatString(12))
		return
	}
	if e := relErr(got, want); e > tol {
		t.Errorf("%s: got %s want %s rel err %g", name, got.FloatString(12), want.FloatString(12), e)
	}
}

func near(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	scale := max(1, a, b, -a, -b)
	return d <= tol*scale
}

type fixturePosition struct {
	AccountID          string  `json:"account_id"`
	PositionType       string  `json:"position_type"`
	LotLNS             string  `json:"lot_lns"`
	PricePNS           string  `json:"price_pns"`
	PriceResiduePNSQ16 string  `json:"price_residue_pnsq16"`
	DepositCNS         string  `json:"deposit_cns"`
	PremiumPnlCNS      string  `json:"premium_pnl_cns"`
	Entry              string  `json:"entry"`
	Size               string  `json:"size"`
	MMR                string  `json:"mmr"`
	LiqPrice           string  `json:"liq_price"`
	BankruptcyPrice    string  `json:"bankruptcy_price"`
	Notional           string  `json:"notional"`
	Leverage           *string `json:"leverage"`
	Distance           string  `json:"distance"`
}

type fixtureMarket struct {
	PerpID           uint64            `json:"perp_id"`
	Symbol           string            `json:"symbol"`
	PriceDecimals    uint8             `json:"price_decimals"`
	LotDecimals      uint8             `json:"lot_decimals"`
	CollateralDecs   uint8             `json:"collateral_decimals"`
	MaintMarginHdths string            `json:"maint_margin_fraction_hdths"`
	MarkPNS          string            `json:"mark_pns"`
	InsuranceBalance string            `json:"insurance_balance_cns"`
	PositionsTotal   int               `json:"positions_total"`
	Positions        []fixturePosition `json:"positions"`
}

func loadFixtureMarket(t testing.TB, perp int) fixtureMarket {
	t.Helper()
	name := filepath.Join("..", "..", "testdata", "derived", "positions.perp"+itoa(perp)+".json")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var fm fixtureMarket
	if err := json.Unmarshal(raw, &fm); err != nil {
		t.Fatal(err)
	}
	return fm
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }

func (fm fixtureMarket) market(t testing.TB) perpl.Market {
	t.Helper()
	return perpl.Market{
		PerpID:               fm.PerpID,
		Symbol:               fm.Symbol,
		PriceDecimals:        fm.PriceDecimals,
		LotDecimals:          fm.LotDecimals,
		MarkPNS:              bi(t, fm.MarkPNS),
		MaintMarginFracHdths: bi(t, fm.MaintMarginHdths).Uint64(),
		InsuranceBalanceCNS:  bi(t, fm.InsuranceBalance),
	}
}

func (fm fixtureMarket) exchange() perpl.ExchangeInfo {
	return perpl.ExchangeInfo{CollateralDecimals: fm.CollateralDecs}
}

func (fm fixtureMarket) position(t testing.TB, fp fixturePosition) perpl.Position {
	t.Helper()
	return perpl.Position{
		PerpID:             fm.PerpID,
		AccountID:          bi(t, fp.AccountID).Uint64(),
		Long:               fp.PositionType == "0",
		DepositCNS:         bi(t, fp.DepositCNS),
		PricePNS:           bi(t, fp.PricePNS),
		LotLNS:             bi(t, fp.LotLNS),
		PremiumPnlCNS:      bi(t, fp.PremiumPnlCNS),
		PriceResiduePNSQ16: bi(t, fp.PriceResiduePNSQ16).Uint64(),
	}
}

func (fm fixtureMarket) assessAll(t testing.TB) (perpl.Market, perpl.ExchangeInfo, []Assessed) {
	t.Helper()
	m, ei := fm.market(t), fm.exchange()
	out := make([]Assessed, 0, len(fm.Positions))
	for _, fp := range fm.Positions {
		a, err := Assess(m, ei, fm.position(t, fp))
		if err != nil {
			t.Fatalf("perp %d account %s: %v", fm.PerpID, fp.AccountID, err)
		}
		out = append(out, a)
	}
	return m, ei, out
}

var fixturePerps = []int{1, 10, 20, 31, 40, 50, 60, 70, 90, 100, 110}

/* Synthetic books */

// synth builds an assessed position with plain decimal inputs for hand-computed cases
func synth(t testing.TB, acct uint64, long bool, size, liq, bk, mark string) Assessed {
	t.Helper()
	sz, mk := rat(t, size), rat(t, mark)
	return Assessed{
		PerpID:          1,
		AccountID:       acct,
		Long:            long,
		Size:            sz,
		LiqPrice:        rat(t, liq),
		BankruptcyPrice: rat(t, bk),
		Notional:        new(big.Rat).Mul(sz, mk),
	}
}

func synthMarket(mark, insurance string) (perpl.Market, perpl.ExchangeInfo) {
	m := perpl.Market{
		PerpID:              1,
		Symbol:              "TST",
		MarkPNS:             mustInt(mark),
		InsuranceBalanceCNS: mustInt(insurance),
	}
	return m, perpl.ExchangeInfo{}
}

func mustInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad int " + s)
	}
	return n
}

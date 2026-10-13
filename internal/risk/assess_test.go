package risk

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"perps/internal/perpl"
)

type sdkGoldens struct {
	EntryPrice []struct {
		Name          string `json:"name"`
		PositionType  string `json:"position_type"`
		PricePNS      string `json:"price_pns"`
		Residue       string `json:"price_residue_pnsq16"`
		PriceDecimals uint8  `json:"price_decimals"`
		Exact         string `json:"exact"`
	} `json:"entry_price"`
	Liquidation []struct {
		Name            string `json:"name"`
		PositionType    string `json:"position_type"`
		Entry           string `json:"entry"`
		Size            string `json:"size"`
		Deposit         string `json:"deposit"`
		PremiumPnl      string `json:"premium_pnl"`
		MMF             string `json:"mmf"`
		MMR             string `json:"mmr"`
		LiqPrice        string `json:"liq_price"`
		BankruptcyPrice string `json:"bankruptcy_price"`
	} `json:"liquidation"`
	MMR []struct {
		Name  string `json:"name"`
		Entry string `json:"entry"`
		Size  string `json:"size"`
		MMF   string `json:"mmf"`
		MMR   string `json:"mmr"`
	} `json:"mmr"`
}

func loadGoldens(t *testing.T) sdkGoldens {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "derived", "sdk_goldens.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g sdkGoldens
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// hdths turns an SDK mmf percent like "20" into the contract's hundredths of a percent
func hdths(t *testing.T, mmf string) uint64 {
	t.Helper()
	return new(big.Int).Quo(new(big.Int).Mul(rat(t, mmf).Num(), big.NewInt(100)), rat(t, mmf).Denom()).Uint64()
}

func TestEntryPriceSDKGoldens(t *testing.T) {
	g := loadGoldens(t)
	if len(g.EntryPrice) != 10 {
		t.Fatalf("expected 10 entry goldens, got %d", len(g.EntryPrice))
	}
	for _, c := range g.EntryPrice {
		t.Run(c.Name, func(t *testing.T) {
			got := EntryPrice(c.PositionType == "0", bi(t, c.PricePNS), bi(t, c.Residue).Uint64(), c.PriceDecimals)
			if got.Cmp(rat(t, c.Exact)) != 0 {
				t.Fatalf("got %s want %s", got.FloatString(25), c.Exact)
			}
		})
	}
}

func TestMMRSDKGoldens(t *testing.T) {
	g := loadGoldens(t)
	if len(g.MMR) != 4 {
		t.Fatalf("expected 4 mmr goldens, got %d", len(g.MMR))
	}
	for _, c := range g.MMR {
		t.Run(c.Name, func(t *testing.T) {
			got, err := MaintMargin(rat(t, c.Entry), rat(t, c.Size), hdths(t, c.MMF))
			if err != nil {
				t.Fatal(err)
			}
			if got.Cmp(rat(t, c.MMR)) != 0 {
				t.Fatalf("got %s want %s", got.FloatString(12), c.MMR)
			}
		})
	}
}

func TestLiquidationSDKGoldens(t *testing.T) {
	g := loadGoldens(t)
	if len(g.Liquidation) != 6 {
		t.Fatalf("expected 6 liquidation goldens, got %d", len(g.Liquidation))
	}
	for _, c := range g.Liquidation {
		t.Run(c.Name, func(t *testing.T) {
			long := c.PositionType == "0"
			entry, size, dep, prem := rat(t, c.Entry), rat(t, c.Size), rat(t, c.Deposit), rat(t, c.PremiumPnl)
			mmr, err := MaintMargin(entry, size, hdths(t, c.MMF))
			if err != nil {
				t.Fatal(err)
			}
			if mmr.Cmp(rat(t, c.MMR)) != 0 {
				t.Fatalf("mmr got %s want %s", mmr.FloatString(12), c.MMR)
			}
			if got := LiquidationPrice(long, entry, size, dep, prem, mmr); got.Cmp(rat(t, c.LiqPrice)) != 0 {
				t.Fatalf("liq got %s want %s", got.FloatString(12), c.LiqPrice)
			}
			if got := BankruptcyPrice(long, entry, size, dep, prem); got.Cmp(rat(t, c.BankruptcyPrice)) != 0 {
				t.Fatalf("bk got %s want %s", got.FloatString(12), c.BankruptcyPrice)
			}
		})
	}
}

func TestLiquidationPriceClampsAtZero(t *testing.T) {
	entry, size := rat(t, "100"), rat(t, "1")
	got := LiquidationPrice(true, entry, size, rat(t, "500"), rat(t, "0"), rat(t, "1"))
	if got.Sign() != 0 {
		t.Fatalf("liq %s want 0", got.FloatString(6))
	}
	if bk := BankruptcyPrice(true, entry, size, rat(t, "500"), rat(t, "0")); bk.Sign() != 0 {
		t.Fatalf("bk %s want 0", bk.FloatString(6))
	}
}

func TestEntryPriceLongZeroPriceKeepsResidue(t *testing.T) {
	got := EntryPrice(true, big.NewInt(0), 32768, 0)
	if got.Cmp(rat(t, "0.5")) != 0 {
		t.Fatalf("got %s", got.FloatString(6))
	}
}

func TestAssessEndToEndSyntheticBook(t *testing.T) {
	m := perpl.Market{
		PerpID: 7, PriceDecimals: 0, LotDecimals: 0,
		MarkPNS: big.NewInt(97), MaintMarginFracHdths: 2000,
		InsuranceBalanceCNS: big.NewInt(0),
	}
	ei := perpl.ExchangeInfo{}
	p := perpl.Position{
		PerpID: 7, AccountID: 9, Long: true,
		DepositCNS: big.NewInt(100), PricePNS: big.NewInt(100), LotLNS: big.NewInt(10),
		PremiumPnlCNS: big.NewInt(0),
	}
	a, err := Assess(m, ei, p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		got  *big.Rat
		want string
	}{
		"entry":    {a.Entry, "100"},
		"size":     {a.Size, "10"},
		"deposit":  {a.Deposit, "100"},
		"mmr":      {a.MMR, "50"},
		"liq":      {a.LiqPrice, "95"},
		"bk":       {a.BankruptcyPrice, "90"},
		"notional": {a.Notional, "970"},
		"leverage": {a.Leverage, "9.7"},
		"distance": {a.Distance, "2/97"},
	}
	for k, w := range want {
		if w.got == nil || w.got.Cmp(rat(t, w.want)) != 0 {
			t.Errorf("%s: got %v want %s", k, w.got, w.want)
		}
	}
	if a.PerpID != 7 || a.AccountID != 9 || !a.Long {
		t.Errorf("identity fields wrong %+v", a)
	}
	// short with mark above liq is liquidatable now
	p.Long = false
	m.MarkPNS = big.NewInt(106)
	a, err = Assess(m, ei, p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Distance.Sign() >= 0 {
		t.Fatalf("short at 106 with liq 105 should have non-positive distance, got %s", a.Distance.FloatString(6))
	}
}

func TestAssessZeroDepositHasNoLeverage(t *testing.T) {
	m := perpl.Market{MarkPNS: big.NewInt(100), MaintMarginFracHdths: 2000, InsuranceBalanceCNS: big.NewInt(0)}
	p := perpl.Position{
		Long: true, DepositCNS: big.NewInt(0), PricePNS: big.NewInt(100), LotLNS: big.NewInt(1),
		PremiumPnlCNS: big.NewInt(0),
	}
	a, err := Assess(m, perpl.ExchangeInfo{}, p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Leverage != nil {
		t.Fatalf("leverage should be nil, got %s", a.Leverage.FloatString(4))
	}
}

func TestAssessRejectsBadInput(t *testing.T) {
	good := func() (perpl.Market, perpl.Position) {
		return perpl.Market{MarkPNS: big.NewInt(100), MaintMarginFracHdths: 2000, InsuranceBalanceCNS: big.NewInt(0)},
			perpl.Position{
				Long: true, DepositCNS: big.NewInt(1), PricePNS: big.NewInt(100), LotLNS: big.NewInt(1),
				PremiumPnlCNS: big.NewInt(0),
			}
	}
	cases := map[string]func(*perpl.Market, *perpl.Position){
		"nil mark":      func(m *perpl.Market, _ *perpl.Position) { m.MarkPNS = nil },
		"zero mark":     func(m *perpl.Market, _ *perpl.Position) { m.MarkPNS = big.NewInt(0) },
		"zero mmf":      func(m *perpl.Market, _ *perpl.Position) { m.MaintMarginFracHdths = 0 },
		"nil price":     func(_ *perpl.Market, p *perpl.Position) { p.PricePNS = nil },
		"nil lot":       func(_ *perpl.Market, p *perpl.Position) { p.LotLNS = nil },
		"zero lot":      func(_ *perpl.Market, p *perpl.Position) { p.LotLNS = big.NewInt(0) },
		"nil deposit":   func(_ *perpl.Market, p *perpl.Position) { p.DepositCNS = nil },
		"nil premium":   func(_ *perpl.Market, p *perpl.Position) { p.PremiumPnlCNS = nil },
		"negative lot":  func(_ *perpl.Market, p *perpl.Position) { p.LotLNS = big.NewInt(-1) },
		"negative mark": func(m *perpl.Market, _ *perpl.Position) { m.MarkPNS = big.NewInt(-5) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, p := good()
			mutate(&m, &p)
			_, err := Assess(m, perpl.ExchangeInfo{}, p)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("want ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestAssessFixtureGoldens(t *testing.T) {
	const tol = 1e-12
	total := 0
	for _, perp := range fixturePerps {
		fm := loadFixtureMarket(t, perp)
		m, ei, assessed := fm.assessAll(t)
		if len(assessed) != fm.PositionsTotal {
			t.Fatalf("perp %d assessed %d want %d", perp, len(assessed), fm.PositionsTotal)
		}
		for i, fp := range fm.Positions {
			a := assessed[i]
			name := fm.Symbol + "/" + fp.AccountID
			assertRel(t, name+" entry", a.Entry, rat(t, fp.Entry), tol)
			assertRel(t, name+" size", a.Size, rat(t, fp.Size), tol)
			assertRel(t, name+" mmr", a.MMR, rat(t, fp.MMR), tol)
			assertRel(t, name+" liq", a.LiqPrice, rat(t, fp.LiqPrice), tol)
			assertRel(t, name+" bk", a.BankruptcyPrice, rat(t, fp.BankruptcyPrice), tol)
			assertRel(t, name+" notional", a.Notional, rat(t, fp.Notional), tol)
			assertRel(t, name+" distance", a.Distance, rat(t, fp.Distance), tol)
			switch {
			case fp.Leverage == nil && a.Leverage != nil:
				t.Errorf("%s leverage should be nil", name)
			case fp.Leverage != nil:
				assertRel(t, name+" leverage", a.Leverage, rat(t, *fp.Leverage), tol)
			}
			total++
		}
		_ = m
		_ = ei
	}
	if total != 665 {
		t.Fatalf("checked %d positions, want 665", total)
	}
}

func TestFormatRatMatchesFixtureStrings(t *testing.T) {
	checked := 0
	for _, perp := range []int{1, 10} {
		fm := loadFixtureMarket(t, perp)
		_, _, assessed := fm.assessAll(t)
		for i, fp := range fm.Positions {
			a := assessed[i]
			for name, pair := range map[string]struct {
				got  *big.Rat
				want string
			}{"entry": {a.Entry, fp.Entry}, "liq": {a.LiqPrice, fp.LiqPrice}, "bk": {a.BankruptcyPrice, fp.BankruptcyPrice}, "mmr": {a.MMR, fp.MMR}, "notional": {a.Notional, fp.Notional}} {
				if got := FormatRat(pair.got, 18); got != pair.want {
					t.Errorf("perp %d acct %s %s: got %s want %s", perp, fp.AccountID, name, got, pair.want)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("nothing checked")
	}
}

func TestFormatRat(t *testing.T) {
	cases := []struct {
		in   string
		sig  int
		want string
	}{
		{"0", 18, "0"},
		{"100", 18, "100"},
		{"-12.5", 18, "-12.5"},
		{"1/3", 5, "0.33333"},
		{"2/3", 5, "0.66667"},
		{"123456789", 4, "123500000"},
		{"0.00012345", 3, "0.000123"},
		{"0.5", 1, "0.5"},
		{"25/10", 1, "2"},
		{"35/10", 1, "4"},
		{"9999/10", 2, "1000"},
	}
	for _, c := range cases {
		if got := FormatRat(rat(t, c.in), c.sig); got != c.want {
			t.Errorf("FormatRat(%s,%d) = %s want %s", c.in, c.sig, got, c.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{0.1 + 0.2, "0.3"},
		{82893.4, "82893.4"},
		{-1.5, "-1.5"},
		{1e-7, "0"},
		{-1e-9, "0"},
		{1234567890.123456, "1234567890.123456"},
	}
	for _, c := range cases {
		if got := FormatUSD(c.in); got != c.want {
			t.Errorf("FormatUSD(%v) = %s want %s", c.in, got, c.want)
		}
	}
	if got := FormatPrice(0.1 + 0.2); got != "0.3" {
		t.Errorf("FormatPrice = %s", got)
	}
	if got := FormatPrice(45807.09749720670123); got != "45807.0974972067" {
		t.Errorf("FormatPrice = %s", got)
	}
}

func TestParamErrorAndNonFiniteFormat(t *testing.T) {
	err := &ParamError{Param: "shock", Reason: "too big"}
	if err.Error() != "risk: invalid shock: too big" {
		t.Fatal(err.Error())
	}
	for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if FormatUSD(f) != "0" || FormatPrice(f) != "0" {
			t.Fatalf("non finite %v", f)
		}
	}
	if ratFloat(nil) != 0 {
		t.Fatal("nil rat")
	}
}

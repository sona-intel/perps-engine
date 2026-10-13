package risk

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

func handBook(t testing.TB) []Assessed {
	return []Assessed{
		synth(t, 1, true, "1", "95", "90", "100"),
		synth(t, 2, true, "2", "85", "80", "100"),
		synth(t, 3, false, "1", "105", "110", "100"),
		synth(t, 4, false, "3", "120", "130", "100"),
	}
}

func TestStressHandComputed(t *testing.T) {
	m, ei := synthMarket("100", "5")
	book := handBook(t)
	cases := []struct {
		name       string
		shock      float64
		p1         float64
		n, d       float64
		positions  int
		scr        float64
		hasSCR     bool
		uncovered  float64
		topAccount []uint64
	}{
		{"-10% no bad debt", -0.10, 90, 90, 0, 1, 0, false, 0, []uint64{1}},
		{"-15% exactly covered", -0.15, 85, 255, 5, 2, 1, true, 0, []uint64{2, 1}},
		{"-20% partly uncovered", -0.20, 80, 240, 10, 2, 0.5, true, 5, []uint64{2, 1}},
		{"+10% no bad debt", 0.10, 110, 110, 0, 1, 0, false, 0, []uint64{3}},
		{"+25%", 0.25, 125, 500, 15, 2, 5.0 / 15.0, true, 10, []uint64{4, 3}},
		{"-1% nothing liquidates", -0.01, 99, 0, 0, 0, 0, false, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Stress(m, ei, book, c.shock)
			if err != nil {
				t.Fatal(err)
			}
			if !near(got.P0, 100, 1e-12) || !near(got.P1, c.p1, 1e-12) || got.Shock != c.shock {
				t.Errorf("prices %+v", got)
			}
			if !near(got.Liquidating.Notional, c.n, 1e-9) || got.Liquidating.Positions != c.positions || got.Liquidating.Accounts != c.positions {
				t.Errorf("liquidating %+v want n=%v positions=%d", got.Liquidating, c.n, c.positions)
			}
			if !near(got.GapBadDebt, c.d, 1e-9) || !near(got.Insurance, 5, 1e-12) || !near(got.Uncovered, c.uncovered, 1e-9) {
				t.Errorf("debt %+v", got)
			}
			if c.hasSCR {
				if got.SCR == nil || !near(*got.SCR, c.scr, 1e-9) || got.NoBadDebt {
					t.Errorf("scr %v noBadDebt %v want %v", got.SCR, got.NoBadDebt, c.scr)
				}
			} else if got.SCR != nil || !got.NoBadDebt {
				t.Errorf("scr should be nil with no_bad_debt, got %v %v", got.SCR, got.NoBadDebt)
			}
			if len(got.Top) != len(c.topAccount) {
				t.Fatalf("top %+v want accounts %v", got.Top, c.topAccount)
			}
			for i, id := range c.topAccount {
				if got.Top[i].AccountID != id {
					t.Errorf("top[%d] = %d want %d", i, got.Top[i].AccountID, id)
				}
			}
		})
	}
}

func TestStressTopPositionFields(t *testing.T) {
	m, ei := synthMarket("100", "5")
	got, err := Stress(m, ei, handBook(t), -0.20)
	if err != nil {
		t.Fatal(err)
	}
	top := got.Top[0]
	if top.AccountID != 2 || !top.Long || top.Size != 2 || top.Notional != 200 || top.LiqPrice != 85 || top.BankruptcyPrice != 80 || top.BadDebt != 0 {
		t.Errorf("top[0] %+v", top)
	}
	second := got.Top[1]
	if second.AccountID != 1 || !near(second.BadDebt, 10, 1e-12) {
		t.Errorf("top[1] %+v", second)
	}
}

func TestStressInsuranceUsesCollateralDecimals(t *testing.T) {
	m, ei := synthMarket("100", "5000000")
	ei.CollateralDecimals = 6
	got, err := Stress(m, ei, handBook(t), -0.20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Insurance != 5 || got.Uncovered != 5 {
		t.Fatalf("insurance %v uncovered %v", got.Insurance, got.Uncovered)
	}
}

func TestStressZeroInsuranceGivesZeroSCR(t *testing.T) {
	m, ei := synthMarket("100", "0")
	got, err := Stress(m, ei, handBook(t), -0.20)
	if err != nil {
		t.Fatal(err)
	}
	if got.SCR == nil || *got.SCR != 0 || got.Uncovered != got.GapBadDebt {
		t.Fatalf("%+v", got)
	}
}

func TestStressTopCapsAtTen(t *testing.T) {
	m, ei := synthMarket("100", "0")
	var book []Assessed
	for i := range 25 {
		book = append(book, synth(t, uint64(i+1), true, "1", "99", "98", "100"))
	}
	got, err := Stress(m, ei, book, -0.05)
	if err != nil {
		t.Fatal(err)
	}
	if got.Liquidating.Positions != 25 || len(got.Top) != 10 {
		t.Fatalf("positions %d top %d", got.Liquidating.Positions, len(got.Top))
	}
	for i, p := range got.Top {
		if p.AccountID != uint64(i+1) {
			t.Fatalf("tie break by account id failed %+v", got.Top)
		}
	}
}

func TestStressShockValidation(t *testing.T) {
	m, ei := synthMarket("100", "5")
	bad := []float64{0, -0.51, 0.51, 1, -1, math.NaN(), math.Inf(1), math.Inf(-1), 100}
	for _, s := range bad {
		_, err := Stress(m, ei, nil, s)
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Param != "shock" || !errors.Is(err, ErrInvalidParam) {
			t.Errorf("shock %v: want ParamError, got %v", s, err)
		}
	}
	for _, s := range []float64{-0.5, 0.5, 0.0001, -0.0001} {
		if _, err := Stress(m, ei, nil, s); err != nil {
			t.Errorf("shock %v rejected: %v", s, err)
		}
	}
}

func TestStressBadMarket(t *testing.T) {
	m, ei := synthMarket("100", "5")
	m.MarkPNS = nil
	if _, err := Stress(m, ei, nil, -0.1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("mark: %v", err)
	}
	m, _ = synthMarket("100", "5")
	m.InsuranceBalanceCNS = nil
	if _, err := Stress(m, ei, nil, -0.1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("insurance: %v", err)
	}
}

func TestStressEmptyBook(t *testing.T) {
	m, ei := synthMarket("100", "5")
	got, err := Stress(m, ei, nil, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	if got.SCR != nil || !got.NoBadDebt || got.GapBadDebt != 0 || got.Liquidating.Positions != 0 || len(got.Top) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestStressProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	shocks := []float64{-0.5, -0.4, -0.3, -0.2, -0.1, -0.05, -0.01, 0.01, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5}
	for iter := range 300 {
		m, ei := synthMarket("250", "10")
		ins := r.Float64() * 5000
		m.InsuranceBalanceCNS = new(big.Int).SetInt64(int64(ins))
		book := randomBook(t, r, r.IntN(150), 250)
		for _, s := range shocks {
			got, err := Stress(m, ei, book, s)
			if err != nil {
				t.Fatal(err)
			}
			checkStressInvariants(t, iter, got)
		}
	}
}

func checkStressInvariants(t *testing.T, iter int, got StressResult) {
	t.Helper()
	if got.GapBadDebt > got.Liquidating.Notional+1e-9 {
		t.Fatalf("iter %d shock %v: D %v > N %v", iter, got.Shock, got.GapBadDebt, got.Liquidating.Notional)
	}
	if got.GapBadDebt < 0 || got.Uncovered < 0 {
		t.Fatalf("iter %d negative debt %+v", iter, got)
	}
	if (got.SCR == nil) != (got.GapBadDebt == 0) || got.NoBadDebt != (got.GapBadDebt == 0) {
		t.Fatalf("iter %d scr nil %v but D %v noBadDebt %v", iter, got.SCR == nil, got.GapBadDebt, got.NoBadDebt)
	}
	if got.SCR != nil && !near(*got.SCR, got.Insurance/got.GapBadDebt, 1e-12) {
		t.Fatalf("iter %d scr %v != I/D", iter, *got.SCR)
	}
	if !near(got.Uncovered, math.Max(0, got.GapBadDebt-got.Insurance), 1e-12) {
		t.Fatalf("iter %d uncovered %v", iter, got.Uncovered)
	}
	if len(got.Top) > 10 || len(got.Top) > got.Liquidating.Positions {
		t.Fatalf("iter %d top size", iter)
	}
	for i := 1; i < len(got.Top); i++ {
		if got.Top[i].Notional > got.Top[i-1].Notional {
			t.Fatalf("iter %d top not sorted", iter)
		}
	}
}

func TestStressMonotoneInShockSize(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for iter := range 100 {
		m, ei := synthMarket("250", "0")
		book := randomBook(t, r, 1+r.IntN(150), 250)
		for _, side := range [][]float64{
			{-0.01, -0.05, -0.1, -0.2, -0.3, -0.4, -0.5},
			{0.01, 0.05, 0.1, 0.2, 0.3, 0.4, 0.5},
		} {
			last := -1
			for _, s := range side {
				got, err := Stress(m, ei, book, s)
				if err != nil {
					t.Fatal(err)
				}
				if got.Liquidating.Positions < last {
					t.Fatalf("iter %d liquidating positions shrank at %v", iter, s)
				}
				last = got.Liquidating.Positions
			}
		}
	}
}

// bruteStress recomputes a shock with exact rationals as an independent oracle
func bruteStress(t *testing.T, markRat *big.Rat, shock string, book []Assessed) (n, d *big.Rat, count int) {
	t.Helper()
	p1 := new(big.Rat).Mul(markRat, new(big.Rat).Add(big.NewRat(1, 1), rat(t, shock)))
	n, d = new(big.Rat), new(big.Rat)
	neg := rat(t, shock).Sign() < 0
	for _, a := range book {
		var hit bool
		var gap *big.Rat
		if a.Long {
			hit = neg && a.LiqPrice.Cmp(p1) >= 0
			gap = new(big.Rat).Sub(a.BankruptcyPrice, p1)
		} else {
			hit = !neg && a.LiqPrice.Cmp(p1) <= 0
			gap = new(big.Rat).Sub(p1, a.BankruptcyPrice)
		}
		if !hit {
			continue
		}
		count++
		n.Add(n, new(big.Rat).Mul(a.Size, p1))
		if gap.Sign() > 0 {
			d.Add(d, new(big.Rat).Mul(a.Size, gap))
		}
	}
	return n, d, count
}

func TestStressFixtureAgainstExactOracle(t *testing.T) {
	for _, perp := range []int{1, 10, 20} {
		fm := loadFixtureMarket(t, perp)
		m, ei, book := fm.assessAll(t)
		mark := scaleDown(m.MarkPNS, int(m.PriceDecimals))
		for _, s := range []string{"-0.2", "-0.1", "-0.05", "-0.03", "0.03", "0.05", "0.1", "0.2"} {
			f, _ := rat(t, s).Float64()
			got, err := Stress(m, ei, book, f)
			if err != nil {
				t.Fatal(err)
			}
			n, d, count := bruteStress(t, mark, s, book)
			if got.Liquidating.Positions != count {
				t.Errorf("%s %s: positions %d want %d", fm.Symbol, s, got.Liquidating.Positions, count)
			}
			if !near(got.Liquidating.Notional, ratFloat(n), 1e-9) || !near(got.GapBadDebt, ratFloat(d), 1e-9) {
				t.Errorf("%s %s: N %v/%v D %v/%v", fm.Symbol, s, got.Liquidating.Notional, ratFloat(n), got.GapBadDebt, ratFloat(d))
			}
		}
	}
}

package risk

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

func TestLiquidationMapParamBounds(t *testing.T) {
	m, _ := synthMarket("100", "0")
	cases := []struct {
		name         string
		bps, rangeP  int
		param        string
		wantAccepted bool
	}{
		{"bps low edge", 5, 20, "", true},
		{"bps high edge", 500, 20, "", true},
		{"bps below", 4, 20, "bucket_bps", false},
		{"bps above", 501, 20, "bucket_bps", false},
		{"bps zero", 0, 20, "bucket_bps", false},
		{"bps negative", -25, 20, "bucket_bps", false},
		{"range low edge", 25, 1, "", true},
		{"range high edge", 25, 50, "", true},
		{"range below", 25, 0, "range_pct", false},
		{"range above", 25, 51, "range_pct", false},
		{"range negative", 25, -3, "range_pct", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LiquidationMap(m, nil, c.bps, c.rangeP)
			if c.wantAccepted {
				if err != nil {
					t.Fatalf("unexpected %v", err)
				}
				return
			}
			var pe *ParamError
			if !errors.As(err, &pe) || pe.Param != c.param || !errors.Is(err, ErrInvalidParam) {
				t.Fatalf("want ParamError for %s, got %v", c.param, err)
			}
		})
	}
}

func TestLiquidationMapHandComputed(t *testing.T) {
	m, _ := synthMarket("100", "0")
	book := []Assessed{
		synth(t, 1, true, "1", "95", "90", "100"),      // 5.00% below, notional 100
		synth(t, 2, true, "2", "85", "80", "100"),      // 15% below, notional 200
		synth(t, 3, true, "1", "50", "40", "100"),      // 50% below, beyond a 20% range
		synth(t, 4, false, "1", "105", "110", "100"),   // 5% above
		synth(t, 5, false, "3", "119.9", "130", "100"), // 19.9% above
		synth(t, 6, false, "1", "140", "150", "100"),   // beyond
		synth(t, 7, true, "1", "102", "99", "100"),     // already liquidatable long, liq above mark
	}
	got, err := LiquidationMap(m, book, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mark != 100 || got.BucketBps != 100 || got.RangePct != 20 {
		t.Fatalf("header %+v", got)
	}
	if len(got.Buckets) != 40 {
		t.Fatalf("buckets %d want 40", len(got.Buckets))
	}
	if got.Buckets[0].PriceFrom != 80 || got.Buckets[0].PriceTo != 81 || got.Buckets[39].PriceTo != 120 {
		t.Fatalf("edges %+v %+v", got.Buckets[0], got.Buckets[39])
	}
	find := func(price float64) Bucket {
		for _, b := range got.Buckets {
			if price >= b.PriceFrom && price < b.PriceTo {
				return b
			}
		}
		t.Fatalf("no bucket for %v", price)
		return Bucket{}
	}
	if b := find(95); b.LongNotional != 100 || b.LongAccounts != 1 || b.ShortNotional != 0 {
		t.Errorf("bucket 95 %+v", b)
	}
	if b := find(85); b.LongNotional != 200 || b.LongAccounts != 1 {
		t.Errorf("bucket 85 %+v", b)
	}
	if b := find(105); b.ShortNotional != 100 || b.ShortAccounts != 1 {
		t.Errorf("bucket 105 %+v", b)
	}
	if b := find(119.9); b.ShortNotional != 300 || b.ShortAccounts != 1 {
		t.Errorf("bucket 119.9 %+v", b)
	}
	// long 7 liq 102 is within +20% so it lands in a bucket above the mark
	if b := find(102); b.LongNotional != 100 || b.LongAccounts != 1 {
		t.Errorf("bucket 102 %+v", b)
	}
	if got.Beyond.LongPositions != 1 || got.Beyond.LongNotional != 100 || got.Beyond.ShortPositions != 1 || got.Beyond.ShortNotional != 100 {
		t.Errorf("beyond %+v", got.Beyond)
	}
	if len(got.Cumulative.Long) != 20 || len(got.Cumulative.Short) != 20 {
		t.Fatalf("cumulative lengths %d %d", len(got.Cumulative.Long), len(got.Cumulative.Short))
	}
	longAt := func(x int) CumPoint { return got.Cumulative.Long[x-1] }
	// acct 7 (102) counts from 1%, acct 1 (95) from 5% and acct 2 (85) from 15%
	if p := longAt(1); p.MovePct != 1 || p.Notional != 100 || p.Positions != 1 {
		t.Errorf("long 1%% %+v", p)
	}
	if p := longAt(5); p.Notional != 200 || p.Positions != 2 {
		t.Errorf("long 5%% %+v", p)
	}
	if p := longAt(15); p.Notional != 400 || p.Positions != 3 {
		t.Errorf("long 15%% %+v", p)
	}
	if p := longAt(20); p.Notional != 400 || p.Positions != 3 {
		t.Errorf("long 20%% %+v", p)
	}
	shortAt := func(x int) CumPoint { return got.Cumulative.Short[x-1] }
	if p := shortAt(4); p.Notional != 0 {
		t.Errorf("short 4%% %+v", p)
	}
	if p := shortAt(5); p.Notional != 100 || p.Positions != 1 {
		t.Errorf("short 5%% %+v", p)
	}
	if p := shortAt(20); p.Notional != 400 || p.Positions != 2 {
		t.Errorf("short 20%% %+v", p)
	}
}

func TestLiquidationMapBoundaryAndPartialBucket(t *testing.T) {
	m, _ := synthMarket("100", "0")
	// range 1% with 30 bps buckets gives edges at -100 -90 ... aligned to mark, so the outer buckets are clipped
	book := []Assessed{
		synth(t, 1, true, "1", "99", "90", "100"),    // exactly -1%, still in range
		synth(t, 2, false, "1", "101", "110", "100"), // exactly +1%, still in range
		synth(t, 3, true, "1", "98.99", "90", "100"), // just outside
		synth(t, 4, false, "1", "101.01", "110", "100"),
	}
	got, err := LiquidationMap(m, book, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Buckets[0].PriceFrom != 99 || got.Buckets[len(got.Buckets)-1].PriceTo != 101 {
		t.Fatalf("outer edges %v %v", got.Buckets[0].PriceFrom, got.Buckets[len(got.Buckets)-1].PriceTo)
	}
	var in float64
	for _, b := range got.Buckets {
		in += b.LongNotional + b.ShortNotional
	}
	if in != 200 || got.Beyond.LongPositions != 1 || got.Beyond.ShortPositions != 1 {
		t.Fatalf("in range %v beyond %+v", in, got.Beyond)
	}
	if got.Buckets[0].LongAccounts != 1 || got.Buckets[len(got.Buckets)-1].ShortAccounts != 1 {
		t.Fatalf("edge accounts %+v %+v", got.Buckets[0], got.Buckets[len(got.Buckets)-1])
	}
}

func TestLiquidationMapEmptyBook(t *testing.T) {
	m, _ := synthMarket("100", "0")
	got, err := LiquidationMap(m, nil, 25, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) != 160 || got.Beyond != (Beyond{}) {
		t.Fatalf("buckets %d beyond %+v", len(got.Buckets), got.Beyond)
	}
}

func TestLiquidationMapRejectsBadMark(t *testing.T) {
	m, _ := synthMarket("100", "0")
	m.MarkPNS = nil
	if _, err := LiquidationMap(m, nil, 25, 20); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}

func randomBook(t testing.TB, r *rand.Rand, n int, mark float64) []Assessed {
	t.Helper()
	book := make([]Assessed, 0, n)
	for i := range n {
		long := r.IntN(2) == 0
		size := 0.01 + r.Float64()*20
		var liq, bk float64
		if long {
			liq = mark * (0.3 + r.Float64()*0.69)
			bk = liq * (0.85 + r.Float64()*0.14)
		} else {
			liq = mark * (0.9 + r.Float64()*0.9)
			bk = liq * (1.01 + r.Float64()*0.15)
		}
		sz := new(big.Rat).SetFloat64(size)
		mk := new(big.Rat).SetFloat64(mark)
		book = append(book, Assessed{
			PerpID: 1, AccountID: uint64(i + 1), Long: long, Size: sz,
			LiqPrice:        new(big.Rat).SetFloat64(liq),
			BankruptcyPrice: new(big.Rat).SetFloat64(bk),
			Notional:        new(big.Rat).Mul(sz, mk),
		})
	}
	return book
}

func TestLiquidationMapProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	m, _ := synthMarket("250", "0")
	for iter := range 200 {
		book := randomBook(t, r, 1+r.IntN(120), 250)
		bps := MinBucketBps + r.IntN(MaxBucketBps-MinBucketBps+1)
		rng := MinRangePct + r.IntN(MaxRangePct)
		got, err := LiquidationMap(m, book, bps, rng)
		if err != nil {
			t.Fatal(err)
		}
		/* Bucket sums equal in-range notional */
		var bucketNotional float64
		var bucketAccts int
		prevTo := got.Buckets[0].PriceFrom
		for _, b := range got.Buckets {
			bucketNotional += b.LongNotional + b.ShortNotional
			bucketAccts += b.LongAccounts + b.ShortAccounts
			if !near(b.PriceFrom, prevTo, 1e-12) || b.PriceTo <= b.PriceFrom {
				t.Fatalf("iter %d buckets not contiguous at %v", iter, b)
			}
			prevTo = b.PriceTo
		}
		if !near(got.Buckets[0].PriceFrom, 250*(1-float64(rng)/100), 1e-12) || !near(prevTo, 250*(1+float64(rng)/100), 1e-12) {
			t.Fatalf("iter %d range edges %v %v", iter, got.Buckets[0].PriceFrom, prevTo)
		}
		var total, beyondN float64
		for _, a := range book {
			n, _ := a.Notional.Float64()
			total += n
		}
		beyondN = got.Beyond.LongNotional + got.Beyond.ShortNotional
		if !near(bucketNotional+beyondN, total, 1e-9) {
			t.Fatalf("iter %d bucket %v + beyond %v != total %v", iter, bucketNotional, beyondN, total)
		}
		if bucketAccts+got.Beyond.LongPositions+got.Beyond.ShortPositions != len(book) {
			t.Fatalf("iter %d account counts do not add up", iter)
		}
		/* Cumulative curves are monotone */
		for _, side := range [][]CumPoint{got.Cumulative.Long, got.Cumulative.Short} {
			if len(side) != rng {
				t.Fatalf("iter %d cumulative length %d want %d", iter, len(side), rng)
			}
			for i := 1; i < len(side); i++ {
				if side[i].Notional < side[i-1].Notional || side[i].Positions < side[i-1].Positions || side[i].MovePct != side[i-1].MovePct+1 {
					t.Fatalf("iter %d cumulative not monotone at %d", iter, i)
				}
			}
		}
		for _, v := range []float64{got.Beyond.LongNotional, got.Beyond.ShortNotional} {
			if v < 0 || math.IsNaN(v) {
				t.Fatalf("bad beyond %v", v)
			}
		}
	}
}

func TestLiquidationMapFixtureBTC(t *testing.T) {
	fm := loadFixtureMarket(t, 1)
	m, _, book := fm.assessAll(t)
	got, err := LiquidationMap(m, book, 25, 20)
	if err != nil {
		t.Fatal(err)
	}
	var in, total float64
	for _, b := range got.Buckets {
		in += b.LongNotional + b.ShortNotional
	}
	for _, a := range book {
		n, _ := a.Notional.Float64()
		total += n
	}
	out := got.Beyond.LongNotional + got.Beyond.ShortNotional
	if !near(in+out, total, 1e-9) || in == 0 {
		t.Fatalf("in %v out %v total %v", in, out, total)
	}
}

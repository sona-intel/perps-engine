package risk

import (
	"fmt"
	"math/big"
	"sort"

	"perps/internal/perpl"
)

type Bucket struct {
	PriceFrom     float64
	PriceTo       float64
	LongNotional  float64
	ShortNotional float64
	// Accounts equal positions because margin is isolated, one position per account per perp
	LongAccounts  int
	ShortAccounts int
}

type Beyond struct {
	LongNotional   float64
	ShortNotional  float64
	LongPositions  int
	ShortPositions int
}

type CumPoint struct {
	MovePct   int
	Notional  float64
	Positions int
}

type Cumulative struct {
	Long  []CumPoint
	Short []CumPoint
}

type LiqMap struct {
	Mark       float64
	BucketBps  int
	RangePct   int
	Buckets    []Bucket
	Beyond     Beyond
	Cumulative Cumulative
}

func ratFloat(r *big.Rat) float64 {
	if r == nil {
		return 0
	}
	f, _ := r.Float64()
	return f
}

func checkMapParams(bucketBps, rangePct int) error {
	if bucketBps < MinBucketBps || bucketBps > MaxBucketBps {
		return &ParamError{Param: "bucket_bps", Reason: fmt.Sprintf("must be between %d and %d", MinBucketBps, MaxBucketBps)}
	}
	if rangePct < MinRangePct || rangePct > MaxRangePct {
		return &ParamError{Param: "range_pct", Reason: fmt.Sprintf("must be between %d and %d", MinRangePct, MaxRangePct)}
	}
	return nil
}

func floorDiv(num, den *big.Int) *big.Int {
	q, _ := new(big.Int).DivMod(num, den, new(big.Int))
	return q
}

func offsetBps(liq, mark *big.Rat) *big.Rat {
	o := new(big.Rat).Sub(liq, mark)
	o.Quo(o, mark)
	return o.Mul(o, big.NewRat(10000, 1))
}

func floorRat(r *big.Rat) *big.Int { return floorDiv(r.Num(), r.Denom()) }

func ceilRat(r *big.Rat) *big.Int {
	return new(big.Int).Neg(floorDiv(new(big.Int).Neg(r.Num()), r.Denom()))
}

func bucketEdges(bucketBps, rangePct int) []int {
	r := rangePct * 100
	edges := []int{-r}
	first := -(r / bucketBps) * bucketBps
	for e := first; e < r; e += bucketBps {
		if e > -r {
			edges = append(edges, e)
		}
	}
	return append(edges, r)
}

func LiquidationMap(m perpl.Market, book []Assessed, bucketBps, rangePct int) (LiqMap, error) {
	if err := checkMapParams(bucketBps, rangePct); err != nil {
		return LiqMap{}, err
	}
	mark, err := markRat(m)
	if err != nil {
		return LiqMap{}, err
	}
	edges := bucketEdges(bucketBps, rangePct)
	out := LiqMap{Mark: ratFloat(mark), BucketBps: bucketBps, RangePct: rangePct, Buckets: make([]Bucket, len(edges)-1)}
	for i := range out.Buckets {
		out.Buckets[i].PriceFrom = ratFloat(edgePrice(mark, edges[i]))
		out.Buckets[i].PriceTo = ratFloat(edgePrice(mark, edges[i+1]))
	}
	longN := make([]float64, rangePct+2)
	shortN := make([]float64, rangePct+2)
	longC := make([]int, rangePct+2)
	shortC := make([]int, rangePct+2)
	lo, hi := big.NewRat(int64(-rangePct*100), 1), big.NewRat(int64(rangePct*100), 1)
	for i := range book {
		a := &book[i]
		n := ratFloat(a.Notional)
		o := offsetBps(a.LiqPrice, mark)
		if o.Cmp(lo) >= 0 && o.Cmp(hi) <= 0 {
			fo := int(floorRat(o).Int64())
			idx := sort.SearchInts(edges, fo+1) - 1
			idx = min(idx, len(out.Buckets)-1)
			b := &out.Buckets[idx]
			if a.Long {
				b.LongNotional += n
				b.LongAccounts++
			} else {
				b.ShortNotional += n
				b.ShortAccounts++
			}
		} else if a.Long {
			out.Beyond.LongNotional += n
			out.Beyond.LongPositions++
		} else {
			out.Beyond.ShortNotional += n
			out.Beyond.ShortPositions++
		}
		// smallest whole percent move at which this position is in the liquidating set
		var xmin *big.Int
		if a.Long {
			xmin = ceilRat(new(big.Rat).Quo(new(big.Rat).Neg(o), big.NewRat(100, 1)))
		} else {
			xmin = ceilRat(new(big.Rat).Quo(o, big.NewRat(100, 1)))
		}
		x := 1
		if xmin.IsInt64() && xmin.Int64() > 1 {
			x = int(min(xmin.Int64(), int64(rangePct+1)))
		}
		if x > rangePct {
			continue
		}
		if a.Long {
			longN[x] += n
			longC[x]++
		} else {
			shortN[x] += n
			shortC[x]++
		}
	}
	out.Cumulative = Cumulative{Long: cumulate(longN, longC, rangePct), Short: cumulate(shortN, shortC, rangePct)}
	return out, nil
}

func edgePrice(mark *big.Rat, bps int) *big.Rat {
	f := big.NewRat(int64(10000+bps), 10000)
	return f.Mul(f, mark)
}

func cumulate(n []float64, c []int, rangePct int) []CumPoint {
	pts := make([]CumPoint, rangePct)
	var sum float64
	var cnt int
	for x := 1; x <= rangePct; x++ {
		sum += n[x]
		cnt += c[x]
		pts[x-1] = CumPoint{MovePct: x, Notional: sum, Positions: cnt}
	}
	return pts
}

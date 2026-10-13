package server

import (
	"math"
	"math/big"
	"strconv"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
)

const (
	priceDigits = 15
	per100K     = 100_000
	bpsPerOne   = 10_000
)

func usdOf(f float64) string { return risk.FormatUSD(f) }

func usdRat(r *big.Rat) string { return risk.FormatUSD(floatOf(r)) }

func priceOf(f float64) string { return risk.FormatPrice(f) }

func priceRat(r *big.Rat) string { return risk.FormatRat(r, priceDigits) }

func floatOf(r *big.Rat) float64 {
	if r == nil {
		return 0
	}
	f, _ := r.Float64()
	return f
}

func scaled(n *big.Int, decimals uint8) string {
	if n == nil {
		return "0"
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return risk.FormatRat(new(big.Rat).SetFrac(n, den), 80)
}

func scaledRat(n *big.Int, decimals uint8) *big.Rat {
	if n == nil {
		return new(big.Rat)
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Rat).SetFrac(n, den)
}

// number keeps NaN and infinities out of a JSON body
func number(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

func optNumber(r *big.Rat) *float64 {
	if r == nil {
		return nil
	}
	f := number(floatOf(r))
	return &f
}

func sideOf(long bool) string {
	if long {
		return "long"
	}
	return "short"
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func hashOf(h perpl.Hash) string { return h.String() }

func ageSeconds(now, then time.Time) int64 { return max(int64(now.Sub(then).Seconds()), 0) }

func itoa(n uint64) string { return strconv.FormatUint(n, 10) }

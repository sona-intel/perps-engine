package risk

import (
	"math"
	"math/big"
	"strings"
)

var (
	bigTen = big.NewInt(10)
	bigOne = big.NewInt(1)
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil)
}

func roundHalfEven(num, den *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(r, 1)
	switch c := twice.Cmp(den); {
	case c > 0, c == 0 && q.Bit(0) == 1:
		q.Add(q, bigOne)
	}
	return q
}

func FormatRat(r *big.Rat, sig int) string {
	if r == nil || r.Sign() == 0 {
		return "0"
	}
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	e := len(new(big.Int).Quo(abs.Num(), abs.Denom()).String()) - 1
	if abs.Cmp(big.NewRat(1, 1)) < 0 {
		e = -1
		for t := new(big.Rat).Mul(abs, big.NewRat(10, 1)); t.Cmp(big.NewRat(1, 1)) < 0; t.Mul(t, big.NewRat(10, 1)) {
			e--
		}
	}
	decimals := sig - 1 - e
	return formatFixed(abs, decimals, neg, true)
}

// formatFixed rounds to decimals places (negative rounds to tens) and trims trailing zeros
func formatFixed(abs *big.Rat, decimals int, neg, trim bool) string {
	var scaled *big.Int
	if decimals >= 0 {
		num := new(big.Int).Mul(abs.Num(), pow10(decimals))
		scaled = roundHalfEven(num, abs.Denom())
	} else {
		den := new(big.Int).Mul(abs.Denom(), pow10(-decimals))
		scaled = roundHalfEven(abs.Num(), den)
		scaled.Mul(scaled, pow10(-decimals))
		decimals = 0
	}
	s := scaled.String()
	if decimals > 0 {
		if len(s) <= decimals {
			s = strings.Repeat("0", decimals-len(s)+1) + s
		}
		s = s[:len(s)-decimals] + "." + s[len(s)-decimals:]
		if trim {
			s = strings.TrimRight(s, "0")
			s = strings.TrimSuffix(s, ".")
		}
	}
	if neg && scaled.Sign() != 0 {
		s = "-" + s
	}
	return s
}

func FormatUSD(f float64) string {
	r, ok := floatRat(f)
	if !ok {
		return "0"
	}
	return formatFixed(new(big.Rat).Abs(r), 6, r.Sign() < 0, true)
}

func FormatPrice(f float64) string {
	r, ok := floatRat(f)
	if !ok {
		return "0"
	}
	return FormatRat(r, 15)
}

func floatRat(f float64) (*big.Rat, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	return new(big.Rat).SetFloat64(f), true
}

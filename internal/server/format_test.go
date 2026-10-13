package server

import (
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFormatGuards(t *testing.T) {
	if floatOf(nil) != 0 || scaled(nil, 6) != "0" || scaledRat(nil, 6).Sign() != 0 || optNumber(nil) != nil {
		t.Error("nil inputs must read as zero")
	}
	if number(math.NaN()) != 0 || number(math.Inf(1)) != 0 || number(math.Inf(-1)) != 0 || number(1.5) != 1.5 {
		t.Error("only finite numbers pass")
	}
	if marginFraction(0) != 0 || marginFraction(2500) != 0.04 {
		t.Error("margin fraction")
	}
}

func TestScaledIsExact(t *testing.T) {
	n, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	for _, c := range []struct {
		n        *big.Int
		decimals uint8
		want     string
	}{
		{big.NewInt(828934), 1, "82893.4"},
		{big.NewInt(25636), 6, "0.025636"},
		{big.NewInt(5), 0, "5"},
		{big.NewInt(100), 2, "1"},
		{big.NewInt(0), 6, "0"},
		{n, 10, "12345678901234567890.123456789"},
	} {
		if got := scaled(c.n, c.decimals); got != c.want {
			t.Errorf("scaled(%v, %d) = %s, want %s", c.n, c.decimals, got, c.want)
		}
	}
}

func TestStatusWriterDefaultsToOK(t *testing.T) {
	w := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	if w.code() != http.StatusOK || w.Unwrap() == nil {
		t.Fatal(w.code())
	}
	w.WriteHeader(http.StatusTeapot)
	w.WriteHeader(http.StatusOK)
	if w.code() != http.StatusTeapot {
		t.Fatal("the first status wins")
	}
}

func TestGapBps(t *testing.T) {
	for _, c := range []struct {
		mark, oracle string
		want         *float64
	}{{"101", "100", new(100.0)}, {"100", "0", nil}, {"100", "x", nil}, {"x", "100", nil}, {"100", "-1", nil}} {
		got := gapBps(c.mark, c.oracle)
		if (got == nil) != (c.want == nil) || got != nil && *got != *c.want {
			t.Errorf("gapBps(%s, %s) = %v", c.mark, c.oracle, got)
		}
	}
}

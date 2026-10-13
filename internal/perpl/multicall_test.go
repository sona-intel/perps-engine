package perpl

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	// built with an independent Python encoder
	aggregate3Request = "" +
		"82ad56cb" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"00000000000000000000000000000000000000000000000000000000000000e0" +
		"00000000000000000000000034b6552d57a35a1d042ccae1951bd1c370112a6f" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000060" +
		"0000000000000000000000000000000000000000000000000000000000000004" +
		"4349b37900000000000000000000000000000000000000000000000000000000" +
		"00000000000000000000000034b6552d57a35a1d042ccae1951bd1c370112a6f" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000060" +
		"0000000000000000000000000000000000000000000000000000000000000024" +
		"9b335b9e00000000000000000000000000000000000000000000000000000000" +
		"0000000a00000000000000000000000000000000000000000000000000000000"

	aggregate3Response = "" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0000000000000000000000000000000000000000000000000000000000000003" +
		"0000000000000000000000000000000000000000000000000000000000000060" +
		"0000000000000000000000000000000000000000000000000000000000000100" +
		"0000000000000000000000000000000000000000000000000000000000000180" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000007" +
		"0000000000000000000000000000000000000000000000000000000000000008" +
		"0000000000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000004" +
		"b6b1e4a000000000000000000000000000000000000000000000000000000000" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000000"
)

func exchangeAddress(t *testing.T) Address {
	t.Helper()
	a, err := ParseAddress("0x34B6552d57a35a1D042CcAe1951BD1C370112a6F")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMulticall3IsTheCanonicalAddress(t *testing.T) {
	if got := Multicall3.String(); got != strings.ToLower("0xcA11bde05977b3631167028862bE2a173976CA11") {
		t.Fatalf("multicall3 at %s", got)
	}
}

func TestAggregate3SelectorMatchesTheSignature(t *testing.T) {
	if got := selectorOf("aggregate3((address,bool,bytes)[])"); hex.EncodeToString(got[:]) != "82ad56cb" {
		t.Fatalf("selector %x", got)
	}
}

func TestEncodeAggregate3MatchesAnIndependentEncoding(t *testing.T) {
	ex := exchangeAddress(t)
	got := EncodeAggregate3([]Call3{
		{Target: ex, AllowFailure: false, Data: EncodeGetPerpetualExistsBitmap()},
		{Target: ex, AllowFailure: true, Data: EncodeGetPerpetualInfoV2(10)},
	})
	want := mustHex(t, aggregate3Request)
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding differs\n got %x\nwant %x", got, want)
	}
}

func TestEncodeAggregate3EmptyList(t *testing.T) {
	got := EncodeAggregate3(nil)
	if len(got) != 4+64 || !bytes.Equal(got[:4], []byte{0x82, 0xad, 0x56, 0xcb}) || got[4+31] != 0x20 {
		t.Fatalf("empty encoding %x", got)
	}
}

func TestDecodeAggregate3(t *testing.T) {
	res, err := DecodeAggregate3(mustHex(t, aggregate3Response), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("%d results", len(res))
	}
	if !res[0].Success || len(res[0].Data) != 64 || res[0].Data[31] != 7 || res[0].Data[63] != 8 {
		t.Fatalf("first result %+v", res[0])
	}
	if res[1].Success || hex.EncodeToString(res[1].Data) != "b6b1e4a0" {
		t.Fatalf("second result %+v", res[1])
	}
	if !res[2].Success || len(res[2].Data) != 0 {
		t.Fatalf("third result %+v", res[2])
	}
}

// encodeResults mirrors what the Multicall3 contract returns, so odd data lengths get checked
func encodeResults(results []Result3) []byte {
	word := func(n int) []byte {
		w := make([]byte, wordSize)
		w[wordSize-2], w[wordSize-1] = byte(n>>8), byte(n)
		return w
	}
	var tuples [][]byte
	for _, r := range results {
		ok := 0
		if r.Success {
			ok = 1
		}
		pad := (wordSize - len(r.Data)%wordSize) % wordSize
		t := append(word(ok), word(64)...)
		t = append(t, word(len(r.Data))...)
		t = append(t, r.Data...)
		t = append(t, make([]byte, pad)...)
		tuples = append(tuples, t)
	}
	out := append(word(32), word(len(results))...)
	offset := wordSize * len(results)
	for _, t := range tuples {
		out = append(out, word(offset)...)
		offset += len(t)
	}
	for _, t := range tuples {
		out = append(out, t...)
	}
	return out
}

func TestDecodeAggregate3KeepsDataOfAnyLength(t *testing.T) {
	var want []Result3
	for i, n := range []int{0, 1, 31, 32, 33, 64, 100} {
		want = append(want, Result3{Success: i%3 != 1, Data: bytes.Repeat([]byte{byte(i + 1)}, n)})
	}
	got, err := DecodeAggregate3(encodeResults(want), len(want))
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if got[i].Success != want[i].Success || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("result %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDecodeAggregate3RejectsBadData(t *testing.T) {
	good := mustHex(t, aggregate3Response)
	setWord := func(index int, v uint16) []byte {
		out := bytes.Clone(good)
		clear(out[index*wordSize : (index+1)*wordSize])
		out[(index+1)*wordSize-2], out[(index+1)*wordSize-1] = byte(v>>8), byte(v)
		return out
	}
	cases := map[string]struct {
		data []byte
		want int
	}{
		"empty":                {nil, 3},
		"short":                {good[:40], 3},
		"count mismatch":       {good, 2},
		"truncated":            {good[:len(good)-32], 3},
		"array offset":         {setWord(0, 0x21), 3},
		"array offset past":    {setWord(0, 0xf0), 3},
		"huge count":           {setWord(1, 0xffff), 65535},
		"tuple offset":         {setWord(2, 0x61), 3},
		"tuple offset past":    {setWord(2, 0xf0), 3},
		"success not a bool":   {setWord(5, 2), 3},
		"bytes offset":         {setWord(6, 0x41), 3},
		"bytes length past":    {setWord(7, 0x1000), 3},
		"zero want with items": {good, 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAggregate3(c.data, c.want); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestDecodeAggregate3ResultsDoNotAliasTheInput(t *testing.T) {
	in := mustHex(t, aggregate3Response)
	res, err := DecodeAggregate3(in, 3)
	if err != nil {
		t.Fatal(err)
	}
	clear(in)
	if res[0].Data[31] != 7 {
		t.Fatal("result shares memory with the response")
	}
}

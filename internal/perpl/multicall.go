package perpl

import (
	"fmt"
	"slices"
)

// Multicall3 is the canonical deployment, found at this address on every chain that has it
var Multicall3 = mustAddress("0xcA11bde05977b3631167028862bE2a173976CA11")

const aggregate3Signature = "aggregate3((address,bool,bytes)[])"

// Call3 is one entry of aggregate3, a failed call reverts the whole batch unless AllowFailure is set
type Call3 struct {
	Target       Address
	AllowFailure bool
	Data         []byte
}

// Result3 is one entry of the aggregate3 answer, Data is the return data or the revert data
type Result3 struct {
	Success bool
	Data    []byte
}

func mustAddress(s string) Address {
	a, err := ParseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

func paddedLen(n int) int { return (n + wordSize - 1) / wordSize * wordSize }

// EncodeAggregate3 returns the calldata of aggregate3 for the calls in order
func EncodeAggregate3(calls []Call3) []byte {
	c := newCall(aggregate3Signature).uint64(wordSize).uint64(uint64(len(calls)))
	offset := wordSize * len(calls)
	for _, call := range calls {
		c.uint64(uint64(offset))
		offset += 4*wordSize + paddedLen(len(call.Data))
	}
	for _, call := range calls {
		allow := uint64(0)
		if call.AllowFailure {
			allow = 1
		}
		c.address(call.Target).uint64(allow).uint64(3 * wordSize).uint64(uint64(len(call.Data)))
		c.buf = append(c.buf, call.Data...)
		c.buf = append(c.buf, make([]byte, paddedLen(len(call.Data))-len(call.Data))...)
	}
	return c.bytes()
}

// DecodeAggregate3 splits the answer of aggregate3 into want results and copies each return data
func DecodeAggregate3(out []byte, want int) ([]Result3, error) {
	root := reader{out}
	arrayAt, err := root.offset(0)
	if err != nil {
		return nil, fmt.Errorf("aggregate3: %w", err)
	}
	n, err := root.uint64(arrayAt)
	if err != nil {
		return nil, fmt.Errorf("aggregate3: %w", err)
	}
	if want < 0 || n != uint64(want) {
		return nil, fmt.Errorf("aggregate3: %d results, want %d", n, want)
	}
	heads := arrayAt + 1
	if err := root.atLeast(heads + want); err != nil {
		return nil, fmt.Errorf("aggregate3: %w", err)
	}
	results := make([]Result3, want)
	for i := range results {
		rel, err := root.offset(heads + i)
		if err != nil {
			return nil, fmt.Errorf("aggregate3: result %d: %w", i, err)
		}
		res, err := decodeResult3(root, heads+rel)
		if err != nil {
			return nil, fmt.Errorf("aggregate3: result %d: %w", i, err)
		}
		results[i] = res
	}
	return results, nil
}

func decodeResult3(r reader, at int) (Result3, error) {
	ok, err := r.bool(at)
	if err != nil {
		return Result3{}, err
	}
	rel, err := r.offset(at + 1)
	if err != nil {
		return Result3{}, err
	}
	lenAt := at + rel
	n, err := r.uint64(lenAt)
	if err != nil {
		return Result3{}, err
	}
	start := (lenAt + 1) * wordSize
	if n > uint64(len(r.data)-start) {
		return Result3{}, fmt.Errorf("return data of %d bytes runs past the data", n)
	}
	return Result3{Success: ok, Data: slices.Clone(r.data[start : start+int(n)])}, nil
}

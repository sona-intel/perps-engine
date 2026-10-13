package perpl

import (
	"errors"
	"fmt"
	"math"
	"math/big"

	"golang.org/x/crypto/sha3"
)

const wordSize = 32

var (
	two256 = new(big.Int).Lsh(big.NewInt(1), 256)
	two255 = new(big.Int).Lsh(big.NewInt(1), 255)
)

/* Hashing and selectors */

func keccak256(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

func selectorOf(signature string) [4]byte {
	sum := keccak256([]byte(signature))
	return [4]byte(sum[:4])
}

/* Encoding */

type callBuilder struct {
	buf []byte
}

func newCall(sig string) *callBuilder {
	sel := selectorOf(sig)
	return &callBuilder{buf: append(make([]byte, 0, 4+3*wordSize), sel[:]...)}
}

func (c *callBuilder) uint64(v uint64) *callBuilder {
	var w [wordSize]byte
	for i := range 8 {
		w[wordSize-1-i] = byte(v >> (8 * i))
	}
	c.buf = append(c.buf, w[:]...)
	return c
}

func (c *callBuilder) address(a Address) *callBuilder {
	var w [wordSize]byte
	copy(w[12:], a[:])
	c.buf = append(c.buf, w[:]...)
	return c
}

func (c *callBuilder) uint256(v *big.Int) (*callBuilder, error) {
	if v == nil || v.Sign() < 0 || v.Cmp(two256) >= 0 {
		return nil, errors.New("value does not fit uint256")
	}
	var w [wordSize]byte
	v.FillBytes(w[:])
	c.buf = append(c.buf, w[:]...)
	return c, nil
}

func (c *callBuilder) bytes() []byte { return c.buf }

/* Decoding */

// reader walks the 32-byte words of one ABI blob and range checks every value it narrows
type reader struct {
	data []byte
}

func (r reader) word(index int) ([]byte, error) {
	start := index * wordSize
	if index < 0 || start+wordSize > len(r.data) {
		return nil, fmt.Errorf("word %d is outside %d bytes of data", index, len(r.data))
	}
	return r.data[start : start+wordSize], nil
}

func (r reader) big(index int) (*big.Int, error) {
	w, err := r.word(index)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(w), nil
}

func (r reader) int256(index int) (*big.Int, error) {
	n, err := r.big(index)
	if err != nil {
		return nil, err
	}
	if n.Cmp(two255) >= 0 {
		n.Sub(n, two256)
	}
	return n, nil
}

func (r reader) uint64(index int) (uint64, error) {
	w, err := r.word(index)
	if err != nil {
		return 0, err
	}
	for _, b := range w[:wordSize-8] {
		if b != 0 {
			return 0, fmt.Errorf("word %d does not fit uint64", index)
		}
	}
	var v uint64
	for _, b := range w[wordSize-8:] {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func (r reader) uint8(index int) (uint8, error) {
	v, err := r.uint64(index)
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint8 {
		return 0, fmt.Errorf("word %d does not fit uint8", index)
	}
	return uint8(v), nil
}

func (r reader) int16(index int) (int16, error) {
	n, err := r.int256(index)
	if err != nil {
		return 0, err
	}
	if !n.IsInt64() || n.Int64() < math.MinInt16 || n.Int64() > math.MaxInt16 {
		return 0, fmt.Errorf("word %d does not fit int16", index)
	}
	return int16(n.Int64()), nil
}

func (r reader) bool(index int) (bool, error) {
	v, err := r.uint64(index)
	if err != nil {
		return false, err
	}
	if v > 1 {
		return false, fmt.Errorf("word %d is not a bool", index)
	}
	return v == 1, nil
}

func (r reader) address(index int) (Address, error) {
	w, err := r.word(index)
	if err != nil {
		return Address{}, err
	}
	return AddressFromWord(w)
}

func (r reader) offset(index int) (int, error) {
	v, err := r.uint64(index)
	if err != nil {
		return 0, err
	}
	if v > uint64(len(r.data)) || v%wordSize != 0 {
		return 0, fmt.Errorf("word %d holds the offset %d, which is outside the data", index, v)
	}
	return int(v / wordSize), nil
}

func (r reader) str(head, base int) (string, error) {
	rel, err := r.offset(head)
	if err != nil {
		return "", err
	}
	lenIndex := base + rel
	n, err := r.uint64(lenIndex)
	if err != nil {
		return "", err
	}
	start := (lenIndex + 1) * wordSize
	if n > uint64(len(r.data)-start) {
		return "", fmt.Errorf("string of %d bytes runs past the data", n)
	}
	return string(r.data[start : start+int(n)]), nil
}

func (r reader) exactly(words int) error {
	if len(r.data) != words*wordSize {
		return fmt.Errorf("want %d words, got %d bytes", words, len(r.data))
	}
	return nil
}

func (r reader) atLeast(words int) error {
	if len(r.data) < words*wordSize {
		return fmt.Errorf("want at least %d words, got %d bytes", words, len(r.data))
	}
	return nil
}

func AddressFromWord(w []byte) (Address, error) {
	if len(w) != wordSize {
		return Address{}, fmt.Errorf("word is %d bytes", len(w))
	}
	for _, b := range w[:12] {
		if b != 0 {
			return Address{}, errors.New("word has bits above the address")
		}
	}
	return Address(w[12:]), nil
}

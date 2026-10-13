package perpl

import (
	"encoding/hex"
	"fmt"
	"strings"
)

type Address [20]byte

type Hash [32]byte

func ParseAddress(s string) (Address, error) {
	var a Address
	if err := parseHex(a[:], s); err != nil {
		return Address{}, fmt.Errorf("address: %w", err)
	}
	return a, nil
}

func ParseHash(s string) (Hash, error) {
	var h Hash
	if err := parseHex(h[:], s); err != nil {
		return Hash{}, fmt.Errorf("hash: %w", err)
	}
	return h, nil
}

func (a Address) String() string { return "0x" + hex.EncodeToString(a[:]) }

func (h Hash) String() string { return "0x" + hex.EncodeToString(h[:]) }

func (a Address) IsZero() bool { return a == Address{} }

func (h Hash) IsZero() bool { return h == Hash{} }

func (a Address) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

func (h Hash) MarshalText() ([]byte, error) { return []byte(h.String()), nil }

func (a *Address) UnmarshalText(text []byte) error {
	parsed, err := ParseAddress(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func (h *Hash) UnmarshalText(text []byte) error {
	parsed, err := ParseHash(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

func parseHex(dst []byte, s string) error {
	body, ok := strings.CutPrefix(s, "0x")
	if !ok {
		return fmt.Errorf("%q has no 0x prefix", s)
	}
	if len(body) != 2*len(dst) {
		return fmt.Errorf("%q is not %d hex digits", s, 2*len(dst))
	}
	if _, err := hex.Decode(dst, []byte(body)); err != nil {
		return fmt.Errorf("%q: %w", s, err)
	}
	return nil
}

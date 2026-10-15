package sources

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	userAgent    = "sona-perps-engine/1"
	maxRESTBody  = 16 << 20
	maxHyperBody = 64 << 20
)

var errTooLarge = errors.New("response body is too large")

// The body stays out of the error because a server can echo request headers
type StatusError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("upstream answered HTTP %d, retry after %s", e.Status, e.RetryAfter)
	}
	return fmt.Sprintf("upstream answered HTTP %d", e.Status)
}

// noRedirect turns a 3xx into a status error so a header never travels to another URL
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: noRedirect, Transport: transport}
}

func statusError(resp *http.Response) *StatusError {
	return &StatusError{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header, time.Now())}
}

func retryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func readBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errTooLarge
	}
	return body, nil
}

/* Numbers on the wire */

// Integers arrive as numbers or quoted strings and must keep every digit
type bigNum struct{ v *big.Int }

func (n *bigNum) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if !integerText(s) {
		return errors.New("not an integer")
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return errors.New("not an integer")
	}
	n.v = v
	return nil
}

func integerText(s string) bool {
	s = strings.TrimPrefix(s, "-")
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// HyperSync mixes JSON numbers, decimal strings and 0x hex for the same kind of field
type quantity uint64

func (q *quantity) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	base := 10
	if digits, ok := strings.CutPrefix(s, "0x"); ok {
		s, base = digits, 16
	}
	n, err := strconv.ParseUint(s, base, 64)
	if err != nil {
		return errors.New("not an unsigned number")
	}
	*q = quantity(n)
	return nil
}

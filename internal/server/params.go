package server

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"perps/internal/perpl"
	"perps/internal/problem"
	"perps/internal/risk"
	"perps/internal/store"
)

const (
	chainName       = "eip155:143"
	defaultShock    = -0.1
	defaultBucket   = 25
	defaultRange    = 20
	defaultLimit    = 50
	maxLimit        = 200
	defaultDays     = 90
	maxDays         = 400
	defaultHours    = 24
	maxHours        = 168
	defaultDeadline = 5000 * time.Millisecond
	maxDeadline     = time.Hour
)

var (
	shockPattern  = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]+)?|\.[0-9]+)$`)
	digitsPattern = regexp.MustCompile(`^[0-9]{1,9}$`)
	marketPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	caip2         = regexp.MustCompile(`^[-a-z0-9]{3,8}:[-_a-zA-Z0-9]{1,32}$`)
)

type httpError struct{ code, detail string }

func (e *httpError) Error() string { return e.code + ": " + e.detail }

// invalid is for a query parameter and invalidID for a path segment
func invalid(detail string) error { return &httpError{problem.CodeInvalidQuery, detail} }

func invalidID(detail string) error { return &httpError{problem.CodeInvalidID, detail} }

func checkChain(s string) error {
	if !caip2.MatchString(s) {
		return invalidID("The chain is not a CAIP-2 identifier.")
	}
	if s != chainName {
		return &httpError{problem.CodeUnsupportedChain, "Only " + chainName + " is served."}
	}
	return nil
}

func parseQuery(req *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return nil, invalid("The query string is malformed.")
	}
	for k, v := range q {
		if !slices.Contains(allowed, k) {
			return nil, invalid("Unknown parameter " + strconv.Quote(k[:min(len(k), 40)]) + ".")
		}
		if len(v) != 1 || v[0] == "" {
			return nil, invalid("Parameter " + k + " takes exactly one non-empty value.")
		}
	}
	return q, nil
}

func parseShock(q url.Values) (float64, error) {
	v := q.Get("shock")
	if v == "" {
		return defaultShock, nil
	}
	bad := invalid("shock is a decimal fraction that is not 0 and lies from -" + strconv.FormatFloat(risk.MaxShock, 'g', -1, 64) + " to " + strconv.FormatFloat(risk.MaxShock, 'g', -1, 64) + ", for example -0.1.")
	if len(v) > 24 || !shockPattern.MatchString(v) {
		return 0, bad
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f == 0 || f < -risk.MaxShock || f > risk.MaxShock {
		return 0, bad
	}
	return f, nil
}

func intParam(q url.Values, name string, def, lo, hi int) (int, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if !digitsPattern.MatchString(v) || err != nil || n < lo || n > hi {
		return 0, invalid(name + " is a whole number from " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi) + ".")
	}
	return n, nil
}

func parseCursor(q url.Values) (*store.LiquidationCursor, error) {
	v := q.Get("cursor")
	if v == "" {
		return nil, nil
	}
	c, err := store.ParseLiquidationCursor(v)
	if err != nil {
		return nil, invalid("cursor is the next_cursor of a previous page.")
	}
	return &c, nil
}

func parseAddress(s string) (perpl.Address, error) {
	a, err := perpl.ParseAddress(s)
	if err != nil {
		return a, invalidID("An address is 0x and 40 hex digits.")
	}
	return a, nil
}

const badMarket = "A market is a symbol such as BTC or a perp id."

func resolveMarket(s string, books []risk.MarketBook) (risk.MarketBook, error) {
	if !marketPattern.MatchString(s) {
		return risk.MarketBook{}, invalidID(badMarket)
	}
	if id, err := strconv.ParseUint(s, 10, 64); err == nil {
		for _, b := range books {
			if b.Market.PerpID == id {
				return b, nil
			}
		}
	} else {
		for _, b := range books {
			if strings.EqualFold(b.Market.Symbol, s) {
				return b, nil
			}
		}
	}
	return risk.MarketBook{}, &httpError{problem.CodeNotFound, "No market " + s + "."}
}

func marketFilter(q url.Values, books []risk.MarketBook) (*risk.MarketBook, error) {
	v := q.Get("market")
	if v == "" {
		return nil, nil
	}
	if !marketPattern.MatchString(v) {
		return nil, invalid(badMarket)
	}
	b, err := resolveMarket(v, books)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func deadline(h http.Header) time.Duration {
	ms, err := strconv.ParseInt(h.Get("Sona-Deadline-Ms"), 10, 64)
	if err != nil {
		return defaultDeadline
	}
	limit := int64(maxDeadline / time.Millisecond)
	return time.Duration(max(min(ms, limit), -limit)) * time.Millisecond
}

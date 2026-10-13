package sources

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	marketDataSuffix   = "/market-data"
	candleResolution   = 3600
	maxRangeItems      = 1024
	marketDataInterval = time.Second
	marketDataTTL      = 5 * time.Minute
	marketDataMaxCache = 256
)

// Candle is one hour of trading, VolumeCNS is in collateral units
type Candle struct {
	Time      time.Time
	VolumeCNS *big.Int
	Trades    uint64
}

// FundingEvent is one applied funding rate, RatePPM is the rate of one funding interval in millionths of notional, which is how Perpl states it (40 is 0.004%)
type FundingEvent struct {
	Time    time.Time
	Block   uint64
	RatePPM int64
}

// MarketDataURL is the market-data root that goes with a leaderboard URL
func MarketDataURL(leaderboardURL string) (string, bool) {
	root, ok := strings.CutSuffix(strings.TrimRight(leaderboardURL, "/"), leaderboardSuffix)
	return root + marketDataSuffix, ok
}

type mdKey struct {
	kind   string
	market uint64
	hours  int
}

type mdEntry struct {
	at    time.Time
	value any
}

type mdFlight struct {
	done  chan struct{}
	value any
	err   error
}

// MarketData reads the public candle and funding history of Perpl. It sends at most one request per interval for the whole process
// and keeps an answer for five minutes, so a burst of callers costs one request.
type MarketData struct {
	base     string
	hc       *http.Client
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	interval time.Duration
	ttl      time.Duration

	paceMu sync.Mutex
	nextAt time.Time

	mu      sync.Mutex
	cache   map[mdKey]mdEntry
	flights map[mdKey]*mdFlight
}

type MarketDataOption func(*MarketData)

func WithMarketDataClient(hc *http.Client) MarketDataOption { return func(m *MarketData) { m.hc = hc } }

func WithMarketDataClock(now func() time.Time, sleep func(context.Context, time.Duration) error) MarketDataOption {
	return func(m *MarketData) { m.now, m.sleep = now, sleep }
}

func WithMarketDataPacing(interval, ttl time.Duration) MarketDataOption {
	return func(m *MarketData) { m.interval, m.ttl = interval, ttl }
}

// NewMarketData takes the market-data root such as https://app.perpl.xyz/api/v1/market-data
func NewMarketData(base string, opts ...MarketDataOption) (*MarketData, error) {
	if !validHTTPURL(base) {
		return nil, errors.New("market data URL must be an http or https URL without credentials")
	}
	m := &MarketData{
		base:     strings.TrimRight(base, "/"),
		hc:       &http.Client{Timeout: restTimeout, CheckRedirect: noRedirect},
		now:      time.Now,
		sleep:    sleepCtx,
		interval: marketDataInterval,
		ttl:      marketDataTTL,
		cache:    make(map[mdKey]mdEntry),
		flights:  make(map[mdKey]*mdFlight),
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Candles returns the hourly candles of the last hours hours, the newest one still open. Hours must stay within 1024 candles.
func (m *MarketData) Candles(ctx context.Context, market uint64, hours int) ([]Candle, error) {
	if hours < 1 || hours > maxRangeItems {
		return nil, fmt.Errorf("candle window of %d hours is outside 1 to %d", hours, maxRangeItems)
	}
	v, err := m.cached(ctx, mdKey{"candles", market, hours}, func(ctx context.Context) (any, error) {
		to := m.now()
		from := to.Truncate(time.Hour).Add(-time.Duration(hours-1) * time.Hour)
		return m.fetchCandles(ctx, market, from, to)
	})
	if err != nil {
		return nil, err
	}
	return v.([]Candle), nil
}

// Funding returns the funding events applied in the last hours hours, oldest first
func (m *MarketData) Funding(ctx context.Context, market uint64, hours int) ([]FundingEvent, error) {
	if hours < 1 {
		return nil, errors.New("funding window must be at least 1 hour")
	}
	v, err := m.cached(ctx, mdKey{"funding", market, hours}, func(ctx context.Context) (any, error) {
		to := m.now()
		return m.fetchFunding(ctx, market, to.Add(-time.Duration(hours)*time.Hour), to)
	})
	if err != nil {
		return nil, err
	}
	return v.([]FundingEvent), nil
}

func (m *MarketData) cached(ctx context.Context, key mdKey, fetch func(context.Context) (any, error)) (any, error) {
	m.mu.Lock()
	if e, ok := m.cache[key]; ok && m.now().Sub(e.at) < m.ttl {
		m.mu.Unlock()
		return e.value, nil
	}
	if f, ok := m.flights[key]; ok {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.value, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &mdFlight{done: make(chan struct{})}
	m.flights[key] = f
	m.mu.Unlock()

	// the request outlives a caller that gives up, so the others waiting on it still get the answer
	f.value, f.err = fetch(context.WithoutCancel(ctx))

	m.mu.Lock()
	delete(m.flights, key)
	if f.err == nil {
		m.store(key, f.value)
	}
	m.mu.Unlock()
	close(f.done)
	return f.value, f.err
}

// store runs under m.mu
func (m *MarketData) store(key mdKey, value any) {
	now := m.now()
	if len(m.cache) >= marketDataMaxCache {
		for k, e := range m.cache {
			if now.Sub(e.at) >= m.ttl {
				delete(m.cache, k)
			}
		}
		if len(m.cache) >= marketDataMaxCache {
			clear(m.cache)
		}
	}
	m.cache[key] = mdEntry{at: now, value: value}
}

// pace reserves the next free slot and waits for it
func (m *MarketData) pace(ctx context.Context) error {
	m.paceMu.Lock()
	now := m.now()
	slot := m.nextAt
	if slot.Before(now) {
		slot = now
	}
	m.nextAt = slot.Add(m.interval)
	m.paceMu.Unlock()
	return m.sleep(ctx, slot.Sub(now))
}

func (m *MarketData) get(ctx context.Context, path string, out any) error {
	if err := m.pace(ctx); err != nil {
		return err
	}
	return getJSON(ctx, m.hc, m.base+path, out)
}

func (m *MarketData) fetchCandles(ctx context.Context, market uint64, from, to time.Time) ([]Candle, error) {
	var raw struct {
		D []struct {
			T int64   `json:"t"`
			V *bigNum `json:"v"`
			N uint64  `json:"n"`
		} `json:"d"`
	}
	path := "/" + strconv.FormatUint(market, 10) + "/candles/" + strconv.Itoa(candleResolution) + "/" + strconv.FormatInt(from.UnixMilli(), 10) + "-" + strconv.FormatInt(to.UnixMilli(), 10)
	if err := m.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, len(raw.D))
	for i, c := range raw.D {
		if c.V == nil {
			return nil, fmt.Errorf("perpl candle %d: missing volume", i)
		}
		if t := time.UnixMilli(c.T).UTC(); !t.Before(from) {
			out = append(out, Candle{Time: t, VolumeCNS: c.V.v, Trades: c.N})
		}
	}
	slices.SortFunc(out, func(a, b Candle) int { return a.Time.Compare(b.Time) })
	return out, nil
}

func (m *MarketData) fetchFunding(ctx context.Context, market uint64, from, to time.Time) ([]FundingEvent, error) {
	var raw struct {
		D []struct {
			At   wireStamp `json:"at"`
			Rate *int64    `json:"rate"`
		} `json:"d"`
	}
	path := "/" + strconv.FormatUint(market, 10) + "/funding/" + strconv.FormatInt(from.UnixMilli(), 10) + "-" + strconv.FormatInt(to.UnixMilli(), 10)
	if err := m.get(ctx, path, &raw); err != nil {
		return nil, err
	}
	out := make([]FundingEvent, 0, len(raw.D))
	for i, e := range raw.D {
		if e.Rate == nil {
			return nil, fmt.Errorf("perpl funding event %d: missing rate", i)
		}
		s := e.At.stamp()
		out = append(out, FundingEvent{Time: s.Time, Block: s.Block, RatePPM: *e.Rate})
	}
	slices.SortFunc(out, func(a, b FundingEvent) int { return a.Time.Compare(b.Time) })
	return out, nil
}

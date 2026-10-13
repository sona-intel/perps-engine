package sources

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"perps/internal/perpl"
)

type Period string

const (
	PeriodDay Period = "day"
	PeriodAll Period = "all"
)

// By names the ranking of a leaderboard
type By string

const (
	ByVolume By = "vol"
	ByPnl    By = "pnl"
)

const (
	leaderboardSuffix = "/trading/leaderboard"
	tickerSuffix      = "/market-data/ticker"
	restTimeout       = 30 * time.Second
)

type Stamp struct {
	Block uint64
	Time  time.Time
}

// The API leaves p undocumented, it is read as PnL in collateral units (6 decimals)
type LeaderboardRow struct {
	Rank      int
	Address   perpl.Address
	PnlCNS    *big.Int
	VolumeCNS *big.Int
}

type Leaderboard struct {
	At   Stamp
	Rows []LeaderboardRow
}

// The leaderboard counts both sides of each trade so shares use its own total
func (l Leaderboard) TotalVolumeCNS() *big.Int {
	total := new(big.Int)
	for _, r := range l.Rows {
		total.Add(total, r.VolumeCNS)
	}
	return total
}

// Optional fields stay nil when the API leaves them out
type TickerMarket struct {
	At              Stamp
	OraclePNS       *big.Int
	MarkPNS         *big.Int
	LastPNS         *big.Int
	MidPNS          *big.Int
	BidPNS          *big.Int
	AskPNS          *big.Int
	PrevPNS         *big.Int
	OpenInterestLNS *big.Int
	DayVolumeLNS    *big.Int
	DayVolumeCNS    *big.Int
}

type Ticker struct {
	Block   uint64
	Markets map[uint64]TickerMarket
}

type REST struct {
	leaderboard string
	ticker      string
	hc          *http.Client
}

type RESTOption func(*REST)

func WithHTTPClient(hc *http.Client) RESTOption { return func(c *REST) { c.hc = hc } }

func WithTickerURL(u string) RESTOption { return func(c *REST) { c.ticker = u } }

// The ticker URL is the sibling of the leaderboard URL unless WithTickerURL sets one
func NewREST(leaderboardURL string, opts ...RESTOption) (*REST, error) {
	if !validHTTPURL(leaderboardURL) {
		return nil, errors.New("leaderboard URL must be an http or https URL without credentials")
	}
	base := strings.TrimRight(leaderboardURL, "/")
	c := &REST{leaderboard: base, hc: &http.Client{Timeout: restTimeout, CheckRedirect: noRedirect}}
	if root, ok := strings.CutSuffix(base, leaderboardSuffix); ok {
		c.ticker = root + tickerSuffix
	}
	for _, o := range opts {
		o(c)
	}
	if c.ticker != "" && !validHTTPURL(c.ticker) {
		return nil, errors.New("ticker URL must be an http or https URL without credentials")
	}
	return c, nil
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// Leaderboard is the volume board of a period
func (c *REST) Leaderboard(ctx context.Context, period Period) (Leaderboard, error) {
	return c.Board(ctx, period, ByVolume)
}

// Board fetches one of the four boards, which list the same accounts in a different order
func (c *REST) Board(ctx context.Context, period Period, by By) (Leaderboard, error) {
	if period != PeriodDay && period != PeriodAll {
		return Leaderboard{}, fmt.Errorf("leaderboard period must be %q or %q", PeriodDay, PeriodAll)
	}
	if by != ByVolume && by != ByPnl {
		return Leaderboard{}, fmt.Errorf("leaderboard ranking must be %q or %q", ByVolume, ByPnl)
	}
	var raw struct {
		At wireStamp  `json:"at"`
		D  []wireRank `json:"d"`
	}
	if err := c.get(ctx, c.leaderboard+"/"+string(period)+"/"+string(by), &raw); err != nil {
		return Leaderboard{}, err
	}
	if raw.D == nil {
		return Leaderboard{}, errors.New("perpl leaderboard has no rows member")
	}
	lb := Leaderboard{At: raw.At.stamp(), Rows: make([]LeaderboardRow, len(raw.D))}
	for i, r := range raw.D {
		addr, err := perpl.ParseAddress(r.A)
		switch {
		case err != nil:
			return Leaderboard{}, fmt.Errorf("perpl leaderboard row %d: bad address", i)
		case r.P == nil || r.V == nil:
			return Leaderboard{}, fmt.Errorf("perpl leaderboard row %d: missing pnl or volume", i)
		}
		lb.Rows[i] = LeaderboardRow{Rank: r.I, Address: addr, PnlCNS: r.P.v, VolumeCNS: r.V.v}
	}
	return lb, nil
}

func (c *REST) Ticker(ctx context.Context) (Ticker, error) {
	if c.ticker == "" {
		return Ticker{}, errors.New("no ticker URL is configured")
	}
	var raw struct {
		SN uint64                `json:"sn"`
		D  map[string]wireTicker `json:"d"`
	}
	if err := c.get(ctx, c.ticker, &raw); err != nil {
		return Ticker{}, err
	}
	if raw.D == nil {
		return Ticker{}, errors.New("perpl ticker has no markets member")
	}
	t := Ticker{Block: raw.SN, Markets: make(map[uint64]TickerMarket, len(raw.D))}
	for key, m := range raw.D {
		id, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			return Ticker{}, fmt.Errorf("perpl ticker market key %q is not a perp id", key)
		}
		if m.Orl == nil || m.Mrk == nil {
			return Ticker{}, fmt.Errorf("perpl ticker market %d: missing oracle or mark", id)
		}
		t.Markets[id] = TickerMarket{
			At: m.At.stamp(), OraclePNS: m.Orl.v, MarkPNS: m.Mrk.v, LastPNS: m.Lst.big(), MidPNS: m.Mid.big(), BidPNS: m.Bid.big(),
			AskPNS: m.Ask.big(), PrevPNS: m.Prv.big(), OpenInterestLNS: m.Oi.big(), DayVolumeLNS: m.Dv.big(), DayVolumeCNS: m.Dva.big(),
		}
	}
	return t, nil
}

/* Wire shapes */

type wireStamp struct {
	B uint64 `json:"b"`
	T int64  `json:"t"`
}

func (s wireStamp) stamp() Stamp { return Stamp{Block: s.B, Time: time.UnixMilli(s.T).UTC()} }

type wireRank struct {
	I int     `json:"i"`
	A string  `json:"a"`
	P *bigNum `json:"p"`
	V *bigNum `json:"v"`
}

type wireTicker struct {
	At  wireStamp `json:"at"`
	Orl *bigNum   `json:"orl"`
	Mrk *bigNum   `json:"mrk"`
	Lst *bigNum   `json:"lst"`
	Mid *bigNum   `json:"mid"`
	Bid *bigNum   `json:"bid"`
	Ask *bigNum   `json:"ask"`
	Prv *bigNum   `json:"prv"`
	Dv  *bigNum   `json:"dv"`
	Dva *bigNum   `json:"dva"`
	Oi  *bigNum   `json:"oi"`
}

func (n *bigNum) big() *big.Int {
	if n == nil {
		return nil
	}
	return n.v
}

func (c *REST) get(ctx context.Context, target string, out any) error {
	return getJSON(ctx, c.hc, target, out)
}

func getJSON(ctx context.Context, hc *http.Client, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("perpl rest request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("perpl rest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("perpl rest: %w", statusError(resp))
	}
	body, err := readBody(resp.Body, maxRESTBody)
	if err != nil {
		return fmt.Errorf("perpl rest: %w", err)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("perpl rest: decode response: %w", err)
	}
	return nil
}

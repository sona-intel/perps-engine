package server

import (
	"math/big"
	"slices"
	"time"

	"perps/internal/sources"
)

const (
	defaultHistoryHours = 168
	defaultBoardLimit   = 25
	maxBoardLimit       = 100
	hourSeconds         = 3600
)

var historyHours = []int{24, 168, 720}

// oiStep thins the five minute points so a window holds a few hundred of them
var oiStep = map[int]time.Duration{24: 5 * time.Minute, 168: 30 * time.Minute, 720: 2 * time.Hour}

type volumePointBody struct {
	Time      string `json:"t"`
	VolumeUSD string `json:"volume_usd"`
	Trades    uint64 `json:"trades"`
}

type fundingEventBody struct {
	Time string  `json:"t"`
	Rate float64 `json:"rate"`
}

type oiPointBody struct {
	Time            string `json:"t"`
	OpenInterestUSD string `json:"open_interest_usd"`
	Positions       int    `json:"positions"`
}

type historyBody struct {
	head
	Market   string   `json:"market"`
	Metric   string   `json:"metric"`
	StepS    int64    `json:"step_s"`
	Items    any      `json:"items"`
	Coverage []string `json:"coverage"`
}

func (a *app) marketHistory() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "metric", "hours")
		if err != nil {
			return result{}, err
		}
		metric := q.Get("metric")
		if !slices.Contains([]string{"oi", "volume", "funding"}, metric) {
			return result{}, invalid("metric is oi, volume or funding.")
		}
		hours, err := intParam(q, "hours", defaultHistoryHours, 1, historyHours[len(historyHours)-1])
		if err != nil || !slices.Contains(historyHours, hours) {
			return result{}, invalid("hours is 24, 168 or 720.")
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		b, err := pricedMarket(c, s)
		if err != nil {
			return result{}, err
		}
		id := b.Market.PerpID
		out := historyBody{head: headOf("perpl_market_history"), Market: b.Market.Symbol, Metric: metric}
		switch metric {
		case "volume":
			cs, err := a.src.Candles(c.ctx, id, hours)
			if err != nil {
				return result{}, err
			}
			items := make([]volumePointBody, len(cs))
			for i, k := range cs {
				items[i] = volumePointBody{Time: stamp(k.Time), VolumeUSD: scaled(k.VolumeCNS, s.Exchange.CollateralDecimals), Trades: k.Trades}
			}
			out.StepS, out.Items, out.Coverage = hourSeconds, items, codes()
		case "funding":
			es, err := a.src.FundingEvents(c.ctx, id, hours)
			if err != nil {
				return result{}, err
			}
			items := make([]fundingEventBody, len(es))
			for i, e := range es {
				items[i] = fundingEventBody{Time: stamp(e.Time), Rate: float64(e.RatePPM) / perMillion}
			}
			out.StepS, out.Items, out.Coverage = medianSpacing(es), items, codes()
		default:
			step := oiStep[hours]
			ps, err := a.src.OpenInterestSeries(c.ctx, id, a.now().Add(-time.Duration(hours)*time.Hour), step)
			if err != nil {
				return result{}, err
			}
			items := make([]oiPointBody, len(ps))
			for i, p := range ps {
				items[i] = oiPointBody{Time: stamp(p.Time), OpenInterestUSD: p.OpenInterestUSD, Positions: p.Positions}
			}
			out.StepS, out.Items, out.Coverage = int64(step.Seconds()), items, codes(a.withStale(s, covOI)...)
		}
		return result{body: out}, nil
	}
}

// medianSpacing is the usual distance between two funding events in seconds, 0 with fewer than two events
func medianSpacing(es []sources.FundingEvent) int64 {
	if len(es) < 2 {
		return 0
	}
	gaps := make([]int64, len(es)-1)
	for i := range gaps {
		gaps[i] = int64(es[i+1].Time.Sub(es[i].Time).Seconds())
	}
	slices.Sort(gaps)
	return gaps[len(gaps)/2]
}

type leaderRowBody struct {
	Rank        int     `json:"rank"`
	Address     string  `json:"address"`
	PnlUSD      string  `json:"pnl_usd"`
	VolumeUSD   string  `json:"volume_usd"`
	VolumeShare float64 `json:"volume_share"`
}

type leaderboardBody struct {
	head
	By       string          `json:"by"`
	Period   string          `json:"period"`
	AsOf     string          `json:"as_of"`
	Items    []leaderRowBody `json:"items"`
	Coverage []string        `json:"coverage"`
}

func (a *app) leaderboard() endpoint {
	return func(c *call) (result, error) {
		q, err := parseQuery(c.req, "by", "period", "limit")
		if err != nil {
			return result{}, err
		}
		by, period := sources.ByPnl, sources.PeriodAll
		switch v := q.Get("by"); v {
		case "":
		case "pnl":
		case "volume":
			by = sources.ByVolume
		default:
			return result{}, invalid("by is pnl or volume.")
		}
		switch v := q.Get("period"); v {
		case "", "all":
		case "day":
			period = sources.PeriodDay
		default:
			return result{}, invalid("period is all or day.")
		}
		limit, err := intParam(q, "limit", defaultBoardLimit, 1, maxBoardLimit)
		if err != nil {
			return result{}, err
		}
		s, err := c.snapshot()
		if err != nil {
			return result{}, err
		}
		decimals := s.Exchange.CollateralDecimals
		lb, ok := a.src.Board(period, by)
		if !ok {
			return result{}, errBoardNotReady
		}
		total := new(big.Rat).SetInt(lb.TotalVolumeCNS())
		out := leaderboardBody{
			head: headOf("perpl_leaderboard"), By: byName(by), Period: string(period), AsOf: stamp(lb.At.Time),
			Items: make([]leaderRowBody, 0, min(limit, len(lb.Rows))), Coverage: codes(covBoardFigures),
		}
		for _, r := range lb.Rows[:min(limit, len(lb.Rows))] {
			var share float64
			if total.Sign() > 0 {
				share = number(floatOf(new(big.Rat).Quo(new(big.Rat).SetInt(r.VolumeCNS), total)))
			}
			out.Items = append(out.Items, leaderRowBody{
				Rank: r.Rank, Address: r.Address.String(), PnlUSD: scaled(r.PnlCNS, decimals),
				VolumeUSD: scaled(r.VolumeCNS, decimals), VolumeShare: share,
			})
		}
		return result{body: out}, nil
	}
}

func byName(by sources.By) string {
	if by == sources.ByVolume {
		return "volume"
	}
	return "pnl"
}

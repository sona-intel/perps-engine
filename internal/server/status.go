package server

type snapshotStatusBody struct {
	Block uint64 `json:"block"`
	Time  string `json:"time"`
	AgeS  int64  `json:"age_s"`
}

type statusBody struct {
	head
	Exchange              string              `json:"exchange"`
	Implementation        *string             `json:"implementation"`
	ImplementationChanged bool                `json:"implementation_changed"`
	Snapshot              *snapshotStatusBody `json:"snapshot"`
	Markets               int                 `json:"markets"`
	Positions             int                 `json:"positions"`
	LiquidationsIndexed   int64               `json:"liquidations_indexed"`
	Backfill              string              `json:"backfill"`
	PaidRPCCalls          int64               `json:"paid_rpc_calls"`
	Coverage              []coverage          `json:"coverage"`
}

func (a *app) status() endpoint {
	return func(c *call) (result, error) {
		if _, err := parseQuery(c.req); err != nil {
			return result{}, err
		}
		totals, err := a.src.CountLiquidations(c.ctx)
		if err != nil {
			return result{}, err
		}
		rt := a.src.Runtime()
		out := statusBody{
			head: headOf("perpl_status"), Coverage: allCoverage, Exchange: rt.Exchange.String(), ImplementationChanged: rt.ImplementationChanged, LiquidationsIndexed: totals.Count,
			Backfill: rt.Backfill, PaidRPCCalls: rt.PaidRPCCalls,
		}
		if !rt.Implementation.IsZero() {
			impl := rt.Implementation.String()
			out.Implementation = &impl
		}
		if s, err := c.snapshot(); err == nil {
			out.Markets, out.Positions = len(s.priced()), s.positions()
			out.Snapshot = &snapshotStatusBody{Block: s.Block, Time: stamp(s.Time), AgeS: ageSeconds(a.now(), s.Time)}
		}
		return result{body: out, unstamped: true}, nil
	}
}

package refresh

import (
	"time"

	"perps/internal/store"
)

const (
	BackfillDisabled = "disabled"
	BackfillPending  = "pending"
	BackfillRunning  = "running"
	BackfillRetrying = "retrying"
	BackfillComplete = "complete"
)

type historyState struct {
	backfill       string
	backfillBlock  uint64
	backfillTarget uint64
	tailBlock      uint64
	totals         store.LiquidationTotals
}

// Status is a point in time view for the status route
type Status struct {
	Exchange              string
	Ready                 bool
	Implementation        string
	ImplementationChanged bool
	Block                 uint64
	SnapshotTime          time.Time
	SnapshotAge           time.Duration
	Markets               int
	Positions             int
	LiquidationsIndexed   int64
	FirstLiquidationBlock uint64
	LastLiquidationBlock  uint64
	Backfill              string
	BackfillBlock         uint64
	BackfillTarget        uint64
	TailBlock             uint64
	PaidRPCCalls          int64
	Refreshes             uint64
	RefreshErrors         uint64
	LastErrorAt           time.Time
	Leaderboard           bool
}

func (r *Runner) Status() Status {
	st := Status{
		Exchange:      r.exchange.String(),
		Refreshes:     r.refreshes.Load(),
		RefreshErrors: r.refreshErrors.Load(),
		PaidRPCCalls:  r.chain.PaidCalls(),
	}
	if ns := r.lastErrorAtNs.Load(); ns != 0 {
		st.LastErrorAt = time.Unix(0, ns).UTC()
	}
	if s := r.latest.Load(); s != nil {
		st.Ready = true
		st.Implementation = s.Implementation.String()
		st.ImplementationChanged = s.ImplementationChanged
		st.Block = s.Block
		st.SnapshotTime = s.Time
		st.SnapshotAge = max(0, r.now().Sub(s.TakenAt))
		st.Markets = len(s.Markets)
		st.Positions = s.Positions
	}
	_, st.Leaderboard = r.Leaderboard()
	r.historyMu.Lock()
	h := r.history
	r.historyMu.Unlock()
	st.Backfill = h.backfill
	if st.Backfill == "" {
		st.Backfill = BackfillPending
	}
	st.BackfillBlock, st.BackfillTarget, st.TailBlock = h.backfillBlock, h.backfillTarget, h.tailBlock
	st.LiquidationsIndexed = h.totals.Count
	st.FirstLiquidationBlock, st.LastLiquidationBlock = h.totals.FirstBlock, h.totals.LastBlock
	return st
}

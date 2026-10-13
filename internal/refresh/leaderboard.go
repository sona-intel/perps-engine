package refresh

import (
	"context"
	"time"

	"perps/internal/sources"
)

// LeaderboardData holds the last good copy of each board and a nil board was never fetched. Day and All are the volume boards,
// DayPnl and AllPnl list the same accounts ranked by PnL.
type LeaderboardData struct {
	Day      *sources.Leaderboard
	All      *sources.Leaderboard
	DayAt    time.Time
	AllAt    time.Time
	DayPnl   *sources.Leaderboard
	AllPnl   *sources.Leaderboard
	DayPnlAt time.Time
	AllPnlAt time.Time
}

// Leaderboard returns the last good copies and false until at least one period was fetched
func (r *Runner) Leaderboard() (LeaderboardData, bool) {
	d := r.leaders.Load()
	if d == nil {
		return LeaderboardData{}, false
	}
	return *d, true
}

func (r *Runner) runLeaderboard(ctx context.Context) {
	r.pollLeaderboard(ctx)
	tick := time.NewTicker(r.cfg.LeaderboardInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.pollLeaderboard(ctx)
		}
	}
}

func (r *Runner) pollLeaderboard(ctx context.Context) {
	if r.board == nil {
		return
	}
	var next LeaderboardData
	if prev := r.leaders.Load(); prev != nil {
		next = *prev
	}
	changed := false
	first := true
	for _, b := range []struct {
		period sources.Period
		by     sources.By
	}{
		{sources.PeriodDay, sources.ByVolume},
		{sources.PeriodAll, sources.ByVolume},
		{sources.PeriodDay, sources.ByPnl},
		{sources.PeriodAll, sources.ByPnl},
	} {
		// the boards are large and the API is shared, so the requests go out one at a time with a pause
		if !first && r.boardGap > 0 && !wait(ctx, r.boardGap) {
			break
		}
		first = false
		lb, err := r.board.Board(ctx, b.period, b.by)
		if err != nil {
			if ctx.Err() == nil {
				r.countError("leaderboard", err)
			}
			continue
		}
		at := r.now()
		changed = true
		switch {
		case b.period == sources.PeriodDay && b.by == sources.ByVolume:
			next.Day, next.DayAt = &lb, at
		case b.period == sources.PeriodAll && b.by == sources.ByVolume:
			next.All, next.AllAt = &lb, at
		case b.period == sources.PeriodDay:
			next.DayPnl, next.DayPnlAt = &lb, at
		default:
			next.AllPnl, next.AllPnlAt = &lb, at
		}
	}
	if changed {
		r.leaders.Store(&next)
	}
}

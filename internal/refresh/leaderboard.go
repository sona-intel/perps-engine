package refresh

import (
	"context"
	"time"

	"perps/internal/sources"
)

// LeaderboardData holds the last good copy of each period and a nil period was never fetched
type LeaderboardData struct {
	Day   *sources.Leaderboard
	All   *sources.Leaderboard
	DayAt time.Time
	AllAt time.Time
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
	for _, period := range []sources.Period{sources.PeriodDay, sources.PeriodAll} {
		lb, err := r.board.Leaderboard(ctx, period)
		if err != nil {
			if ctx.Err() == nil {
				r.countError("leaderboard", err)
			}
			continue
		}
		at := r.now()
		changed = true
		if period == sources.PeriodDay {
			next.Day, next.DayAt = &lb, at
		} else {
			next.All, next.AllAt = &lb, at
		}
	}
	if changed {
		r.leaders.Store(&next)
	}
}

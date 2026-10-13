package refresh

import (
	"context"
	"testing"
	"time"

	"perps/internal/sources"
)

func TestRunRefreshesPollsAndStopsOnCancel(t *testing.T) {
	hs, _ := hyperSyncServer(t)
	g := newRig(t, func(o *Options) {
		o.HyperSync = hs
		o.Config.RefreshInterval = 20 * time.Millisecond
		o.Config.SlowRefreshInterval = time.Hour
		o.Config.LeaderboardInterval = 20 * time.Millisecond
	})
	g.board.rows[sources.PeriodDay] = sources.Leaderboard{At: sources.Stamp{Block: 1}}
	g.board.rows[sources.PeriodAll] = sources.Leaderboard{At: sources.Stamp{Block: 2}}
	g.chain.loadTailFixtures(true)
	g.chain.setHead(pinnedBlock + 2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.r.Run(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		st := g.r.Status()
		_, board := g.r.Leaderboard()
		g.store.mu.Lock()
		accounts := len(g.store.accounts)
		g.store.mu.Unlock()
		if st.Ready && board && st.Backfill == BackfillComplete && accounts > 0 && st.Refreshes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not settle %+v board %v accounts %d", st, board, accounts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !g.r.Ready() || g.r.Latest() == nil {
		t.Fatal("not ready")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
	if got := histogramCount(t, g.reg, "perps_refresh_duration_seconds"); got == 0 {
		t.Fatal("no refresh duration recorded")
	}
	if len(g.store.liquidations()) < 10 {
		t.Fatalf("%d liquidations after the backfill", len(g.store.liquidations()))
	}
}

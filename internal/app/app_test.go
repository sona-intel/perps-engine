package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"perps/internal/config"
	"perps/internal/perpl"
	"perps/internal/rpc"
	"perps/internal/server"
	"perps/internal/store"
)

type downChain struct{}

var errDown = errors.New("down")

func (downChain) BlockNumber(context.Context) (uint64, error) { return 0, errDown }
func (downChain) BlockByNumber(context.Context, uint64) (rpc.Block, error) {
	return rpc.Block{}, errDown
}

func (downChain) Call(context.Context, perpl.Address, []byte, uint64) ([]byte, error) {
	return nil, errDown
}

func (downChain) StorageAt(context.Context, perpl.Address, perpl.Hash, uint64) (perpl.Hash, error) {
	return perpl.Hash{}, errDown
}

func (downChain) Logs(context.Context, perpl.Address, perpl.Hash, uint64, uint64) ([]rpc.Log, error) {
	return nil, errDown
}

func (downChain) TransactionByHash(context.Context, perpl.Hash) (rpc.Tx, error) {
	return rpc.Tx{}, errDown
}
func (downChain) PaidCalls() int64 { return 0 }

type fakeStore struct{ Store }

func (fakeStore) CountLiquidations(context.Context) (store.LiquidationTotals, error) {
	return store.LiquidationTotals{}, nil
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("PERPS_RPC_URL", "")
	t.Setenv("ENVIO_HYPERSYNC_KEY", "")
	cfg, err := config.Load("../../config")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func build(t *testing.T) *App {
	t.Helper()
	a, err := New(Deps{
		Config: testConfig(t), Chain: downChain{}, Store: fakeStore{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil))
	return rec
}

func TestNewRejectsAnInvalidConfig(t *testing.T) {
	if _, err := New(Deps{Config: config.Config{}, Chain: downChain{}, Store: fakeStore{}}); err == nil {
		t.Fatal("accepted an empty config")
	}
}

func TestAPIIsAliveButNotReadyBeforeTheFirstSnapshot(t *testing.T) {
	a := build(t)
	if rec := get(a.API, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz %d", rec.Code)
	}
	if rec := get(a.API, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz %d", rec.Code)
	}
	rec := get(a.API, "/internal/v1/chains/eip155:143/perpl/markets")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "first snapshot of the exchange is not ready") {
		t.Fatalf("markets %d %s", rec.Code, rec.Body.String())
	}
}

func TestStatusRouteReportsTheRunner(t *testing.T) {
	rec := get(build(t).API, "/internal/v1/chains/eip155:143/perpl/status")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "0x34b6552d57a35a1d042ccae1951bd1c370112a6f") {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestDebugServesBothMetricFamilies(t *testing.T) {
	a := build(t)
	get(a.API, "/internal/v1/chains/eip155:143/perpl/markets")
	body := get(server.Debug(":0", a.Metrics).Handler, "/metrics").Body.String()
	for _, name := range []string{
		"perps_request_duration_seconds", "perps_snapshot_block", "perps_refresh_duration_seconds",
		"perps_snapshot_age_seconds", "perps_paid_rpc_calls_total", "go_goroutines",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("metrics lack %s", name)
		}
	}
}

func TestTwoAppsDoNotShareMetrics(t *testing.T) {
	build(t)
	build(t)
}

func TestLimitsFollowTheConfiguredIntervals(t *testing.T) {
	cfg := testConfig(t)
	if got := staleAfter(cfg); got != 4*cfg.RefreshInterval || got != 2*time.Minute {
		t.Fatalf("stale after %s", got)
	}
	if got := leaderboardMaxAge(cfg); got != 3*cfg.LeaderboardInterval || got != 30*time.Minute {
		t.Fatalf("leaderboard max age %s", got)
	}
}

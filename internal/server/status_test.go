package server

import (
	"errors"
	"net/http"
	"testing"

	"perps/internal/perpl"
	"perps/internal/problem"
)

func TestStatus(t *testing.T) {
	r := fetch(t, testHandler(newFake(t)), base+"/status")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	if r.at(t, "exchange") != "0x34b6552d57a35a1d042ccae1951bd1c370112a6f" || r.at(t, "implementation") != "0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a" ||
		r.at(t, "implementation_changed") != false || r.at(t, "backfill") != "complete" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "snapshot", "block"), snapBlock)
	if r.at(t, "snapshot", "time") != "2026-10-12T12:00:17Z" {
		t.Fatal(r.at(t, "snapshot"))
	}
	near(t, r.at(t, "snapshot", "age_s"), 1)
	near(t, r.at(t, "markets"), 2)
	near(t, r.at(t, "positions"), 6)
	near(t, r.at(t, "liquidations_indexed"), 40)
	near(t, r.at(t, "paid_rpc_calls"), 0)
	if r.at(t, "kind") != "perpl_status" || r.at(t, "chain") != "eip155:143" {
		t.Fatal(r.body)
	}
	want := []string{"perpl_snapshot_only", "perpl_bad_debt_is_floor", "perpl_leaderboard_unavailable", "perpl_history_partial", "perpl_funding_from_snapshots", "perpl_snapshot_stale"}
	for i, code := range want {
		if r.at(t, "coverage", i, "code") != code || r.at(t, "coverage", i, "note") == "" {
			t.Fatalf("coverage %d: %v", i, r.at(t, "coverage", i))
		}
	}
	if n := len(r.at(t, "coverage").([]any)); n != len(want) {
		t.Fatalf("%d coverage entries", n)
	}
	if r.Header().Get("Sona-Block") != "" || r.Header().Get("Sona-Finality") != "" || r.Header().Get("X-Request-Id") == "" {
		t.Fatalf("headers %v", r.Header())
	}
}

func TestStatusReportsRuntimeChanges(t *testing.T) {
	f := newFake(t)
	f.rt.ImplementationChanged = true
	f.rt.PaidRPCCalls = 3
	f.rt.Backfill = BackfillDisabled
	r := fetch(t, testHandler(f), base+"/status")
	if r.at(t, "implementation_changed") != true || r.at(t, "backfill") != "disabled" {
		t.Fatal(r.at(t))
	}
	near(t, r.at(t, "paid_rpc_calls"), 3)
	f.rt.Implementation = perpl.Address{}
	isNull(t, fetch(t, testHandler(f), base+"/status").at(t, "implementation"))
}

func TestStatusAnswersBeforeTheFirstSnapshot(t *testing.T) {
	f := newFake(t)
	f.snap = nil
	f.ready = false
	r := fetch(t, testHandler(f), base+"/status")
	if r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body)
	}
	isNull(t, r.at(t, "snapshot"))
	near(t, r.at(t, "markets"), 0)
	near(t, r.at(t, "positions"), 0)
	if r.Header().Get("Sona-Block") != "" {
		t.Fatalf("headers %v", r.Header())
	}
}

func TestStatusNeedsTheDatabaseForTheLiquidationCount(t *testing.T) {
	f := newFake(t)
	f.err = errors.New("dial tcp: connection refused")
	if r := fetch(t, testHandler(f), base+"/status"); r.Code != http.StatusServiceUnavailable || r.code(t) != problem.CodeUpstream {
		t.Fatal(r.Code, r.Body)
	}
}

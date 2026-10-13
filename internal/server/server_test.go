package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"perps/internal/problem"
	"perps/internal/risk"
)

func dataRoutes() map[string]string {
	return map[string]string{
		"markets":         base + "/markets",
		"market":          base + "/markets/TST",
		"liquidation map": base + "/markets/TST/liquidation-map",
		"stress":          base + "/markets/TST/stress",
		"stress all":      base + "/stress",
		"concentration":   base + "/concentration",
		"liquidations":    base + "/liquidations",
		"daily":           base + "/liquidations/daily",
		"funding":         base + "/funding?market=TST",
		"account":         base + "/accounts/" + accountAddr.String(),
	}
}

func near(t testing.TB, got any, want float64) {
	t.Helper()
	f, ok := got.(float64)
	if !ok {
		t.Fatalf("got %v (%T), want the number %v", got, got, want)
	}
	if d := f - want; d > 1e-9 || d < -1e-9 {
		t.Fatalf("got %v, want %v", f, want)
	}
}

func absent(t testing.TB, r reply, key string) {
	t.Helper()
	if _, ok := r.body.(map[string]any)[key]; ok {
		t.Fatalf("%s is present in %v", key, r.body)
	}
}

func isNull(t testing.TB, got any) {
	t.Helper()
	if got != nil {
		t.Fatalf("got %v, want null", got)
	}
}

func TestProbes(t *testing.T) {
	f := newFake(t)
	h := testHandler(f)
	for _, path := range []string{"/healthz", "/readyz"} {
		r := fetch(t, h, path)
		if r.Code != http.StatusOK || r.Body.Len() != 0 || r.Header().Get("X-Request-Id") != "" {
			t.Errorf("%s: %d %q %v", path, r.Code, r.Body, r.Header())
		}
	}
	f.ready = false
	if r := fetch(t, h, "/readyz"); r.Code != http.StatusServiceUnavailable || r.Body.Len() != 0 {
		t.Errorf("readyz not ready: %d %q", r.Code, r.Body)
	}
	if r := fetch(t, h, "/healthz"); r.Code != http.StatusOK {
		t.Errorf("healthz while not ready: %d", r.Code)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	h := testHandler(newFake(t))
	for _, path := range []string{"/nope", "/", "/internal/v1", base, base + "/", base + "/markets/TST/nope", "/internal/v1/chains/eip155:143/x402/status", "/internal/v1/status"} {
		r := fetch(t, h, path)
		if r.Code != http.StatusNotFound || r.code(t) != problem.CodeNotFound || r.Header().Get("X-Request-Id") == "" {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, base+"/markets", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST: %d %s", rec.Code, rec.Body)
	}
}

func TestEverySnapshotRouteRefusesAnotherChain(t *testing.T) {
	h := testHandler(newFake(t))
	paths := dataRoutes()
	paths["status"] = base + "/status"
	for name, path := range paths {
		for chain, want := range map[string]struct {
			status int
			code   string
		}{
			"eip155:1":    {http.StatusNotFound, problem.CodeUnsupportedChain},
			"eip155:0143": {http.StatusNotFound, problem.CodeUnsupportedChain},
			"foo":         {http.StatusBadRequest, problem.CodeInvalidID},
			"EIP155:143":  {http.StatusBadRequest, problem.CodeInvalidID},
		} {
			p := strings.Replace(path, "eip155:143", chain, 1)
			r := fetch(t, h, p)
			if r.Code != want.status || r.code(t) != want.code {
				t.Errorf("%s on %s: %d %s", name, chain, r.Code, r.Body)
			}
		}
	}
}

func TestNoSnapshotAnswersNotReady(t *testing.T) {
	f := newFake(t)
	f.snap = nil
	h := testHandler(f)
	for name, path := range dataRoutes() {
		r := fetch(t, h, path)
		if r.Code != http.StatusServiceUnavailable || r.code(t) != problem.CodeUpstream {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
			continue
		}
		if r.Header().Get("Retry-After") != "5" || r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s: headers %v", name, r.Header())
		}
		near(t, dig(t, r.body, "retry_after_s"), 5)
		if dig(t, r.body, "instance") != strings.SplitN(path, "?", 2)[0] {
			t.Errorf("%s: instance %v", name, dig(t, r.body, "instance"))
		}
	}
}

func TestBadParametersBeatNotReady(t *testing.T) {
	f := newFake(t)
	f.snap = nil
	h := testHandler(f)
	if r := fetch(t, h, base+"/markets/TST/stress?shock=0"); r.Code != http.StatusBadRequest {
		t.Errorf("%d %s", r.Code, r.Body)
	}
}

func TestEveryRouteRejectsUnknownParameters(t *testing.T) {
	h := testHandler(newFake(t))
	paths := dataRoutes()
	paths["status"] = base + "/status"
	for name, path := range paths {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		r := fetch(t, h, path+sep+"foo=1")
		if r.Code != http.StatusBadRequest || r.code(t) != problem.CodeInvalidQuery {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
}

func TestDeadlineOfZeroAnswers504BeforeAnyRead(t *testing.T) {
	f := newFake(t)
	h := testHandler(f)
	paths := dataRoutes()
	paths["status"] = base + "/status"
	for name, path := range paths {
		for _, ms := range []string{"0", "-1"} {
			r := fetch(t, h, path, "Sona-Deadline-Ms", ms)
			if r.Code != http.StatusGatewayTimeout || r.code(t) != problem.CodeDeadline {
				t.Errorf("%s with %s: %d %s", name, ms, r.Code, r.Body)
			}
		}
	}
	if f.storeCalls != 0 {
		t.Errorf("%d store calls", f.storeCalls)
	}
}

func TestSuccessHeadersAndBareRecord(t *testing.T) {
	h := testHandler(newFake(t))
	kinds := map[string]string{
		"markets": "perpl_markets", "market": "perpl_market", "liquidation map": "perpl_liquidation_map", "stress": "perpl_market_stress",
		"stress all": "perpl_stress", "concentration": "perpl_concentration", "liquidations": "perpl_liquidations",
		"daily": "perpl_liquidations_daily", "funding": "perpl_funding", "account": "perpl_account",
	}
	for name, path := range dataRoutes() {
		r := fetch(t, h, path)
		if r.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
			continue
		}
		hdr := r.Header()
		if hdr.Get("Content-Type") != "application/json" || hdr.Get("Content-Length") != strconv.Itoa(r.Body.Len()) {
			t.Errorf("%s: content headers %v", name, hdr)
		}
		if hdr.Get("Sona-Block") != strconv.Itoa(snapBlock) || hdr.Get("Sona-Block-Time") != strconv.FormatInt(snapTime.Unix(), 10) || hdr.Get("Sona-Finality") != "confirmed" {
			t.Errorf("%s: Sona headers %v", name, hdr)
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(hdr.Get("X-Request-Id")) || hdr.Get("Cache-Control") != "" {
			t.Errorf("%s: headers %v", name, hdr)
		}
		rec := r.body.(map[string]any)
		if rec["kind"] != kinds[name] || rec["chain"] != "eip155:143" {
			t.Errorf("%s: kind %v chain %v", name, rec["kind"], rec["chain"])
		}
		for _, banned := range []string{"data", "meta"} {
			if _, ok := rec[banned]; ok {
				t.Errorf("%s: wrapped in %s", name, banned)
			}
		}
		if _, ok := rec["coverage"].([]any); !ok {
			t.Errorf("%s: coverage %v", name, rec["coverage"])
		}
	}
}

func TestRequestIDFollowsTraceparent(t *testing.T) {
	h := testHandler(newFake(t))
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if r := fetch(t, h, base+"/markets", "traceparent", tp); r.Header().Get("X-Request-Id") != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id: %v", r.Header())
	}
	for _, bad := range []string{"", "junk", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"} {
		got := fetch(t, h, base+"/markets", "traceparent", bad).Header().Get("X-Request-Id")
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got) || got == "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Errorf("traceparent %q gave %q", bad, got)
		}
	}
}

func TestStoreErrorsMapToProblems(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unreachable", errors.New("dial tcp: connection refused"), http.StatusServiceUnavailable, problem.CodeUpstream},
		{"connection class", &pgconn.PgError{Code: "08006"}, http.StatusServiceUnavailable, problem.CodeUpstream},
		{"resource class", &pgconn.PgError{Code: "53300"}, http.StatusServiceUnavailable, problem.CodeUpstream},
		{"operator class", &pgconn.PgError{Code: "57P01"}, http.StatusServiceUnavailable, problem.CodeUpstream},
		{"rejected statement", &pgconn.PgError{Code: "42703"}, http.StatusInternalServerError, problem.CodeInternal},
		{"wrapped rejection", fmt.Errorf("query: %w", &pgconn.PgError{Code: "22P02"}), http.StatusInternalServerError, problem.CodeInternal},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, problem.CodeDeadline},
		{"bad data", fmt.Errorf("x: %w", risk.ErrInvalidInput), http.StatusInternalServerError, problem.CodeInternal},
		{"bad parameter", &risk.ParamError{Param: "shock", Reason: "r"}, http.StatusBadRequest, problem.CodeInvalidQuery},
	}
	storeRoutes := []string{base + "/liquidations", base + "/liquidations/daily", base + "/funding?market=TST", base + "/concentration", base + "/status"}
	for _, c := range cases {
		for _, path := range storeRoutes {
			f := newFake(t)
			f.err = c.err
			r := fetch(t, testHandler(f), path)
			if r.Code != c.status || r.code(t) != c.code {
				t.Errorf("%s on %s: %d %s", c.name, path, r.Code, r.Body)
			}
			if c.status == http.StatusServiceUnavailable && r.Header().Get("Retry-After") != "1" {
				t.Errorf("%s on %s: Retry-After %q", c.name, path, r.Header().Get("Retry-After"))
			}
		}
	}
}

func TestInternalErrorsDoNotLeakTheCause(t *testing.T) {
	f := newFake(t)
	f.err = &pgconn.PgError{Code: "42703", Message: "column secret_thing does not exist"}
	r := fetch(t, testHandler(f), base+"/liquidations")
	if strings.Contains(r.Body.String(), "secret_thing") || r.code(t) != problem.CodeInternal {
		t.Fatal(r.Body)
	}
}

func TestClientThatLeftGets499WithoutBody(t *testing.T) {
	f := newFake(t)
	f.err = context.Canceled
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, base+"/liquidations", nil)
	rec := httptest.NewRecorder()
	testHandler(f).ServeHTTP(rec, req)
	if rec.Code != 499 || rec.Body.Len() != 0 {
		t.Fatal(rec.Code, rec.Body)
	}
}

func TestCanceledStoreCallWithLiveClientIsInternal(t *testing.T) {
	f := newFake(t)
	f.err = context.Canceled
	if r := fetch(t, testHandler(f), base+"/liquidations"); r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code, r.Body)
	}
}

func TestBrokenSnapshotDataAnswers500(t *testing.T) {
	f := newFake(t)
	f.snap.Books[0].Market.InsuranceBalanceCNS = nil
	for _, path := range []string{base + "/markets", base + "/markets/TST", base + "/markets/TST/stress", base + "/stress"} {
		if r := fetch(t, testHandler(f), path); r.Code != http.StatusInternalServerError || r.code(t) != problem.CodeInternal {
			t.Errorf("%s: %d %s", path, r.Code, r.Body)
		}
	}
}

func TestConcurrentRequestsShareTheSnapshotSafely(t *testing.T) {
	h := testHandler(newFake(t))
	var wg sync.WaitGroup
	for range 4 {
		for _, path := range dataRoutes() {
			wg.Go(func() {
				for range 5 {
					if r := fetch(t, h, path); r.Code != http.StatusOK {
						t.Errorf("%s: %d %s", path, r.Code, r.Body)
						return
					}
				}
			})
		}
	}
	wg.Wait()
}

func TestEveryConcentrationReadFailsOnItsOwn(t *testing.T) {
	for _, name := range []string{"AccountAddresses", "KeeperCounts", "CountLiquidations"} {
		f := newFake(t)
		f.errFor = map[string]error{name: errors.New("dial tcp: connection refused")}
		if r := fetch(t, testHandler(f), base+"/concentration"); r.Code != http.StatusServiceUnavailable {
			t.Errorf("%s failing: %d %s", name, r.Code, r.Body)
		}
	}
}

func TestInternalErrorIsLoggedUnderTheRequestID(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	f := newFake(t)
	f.err = &pgconn.PgError{Code: "42703", Message: "column does not exist"}
	r := fetch(t, testHandler(f), base+"/liquidations")
	id := r.Header().Get("X-Request-Id")
	if r.Code != http.StatusInternalServerError || !strings.Contains(buf.String(), "request_id="+id) || !strings.Contains(buf.String(), "column does not exist") {
		t.Fatalf("log %q for %s", buf.String(), id)
	}
}

func staleHandler(f *fakeSource, now time.Time, staleAfter time.Duration) http.Handler {
	return New(f, NewMetrics(f), Options{Now: func() time.Time { return now }, StaleAfter: staleAfter})
}

func TestAStaleSnapshotFailsReadinessAndIsFlagged(t *testing.T) {
	f := newFake(t)
	f.snap.TakenAt = snapTime
	snapshotBacked := []string{"markets", "market", "liquidation map", "stress", "stress all", "concentration", "account"}

	fresh := staleHandler(f, snapTime.Add(time.Minute), 2*time.Minute)
	if r := fetch(t, fresh, "/readyz"); r.Code != http.StatusOK {
		t.Fatalf("fresh readyz %d", r.Code)
	}
	for name, path := range dataRoutes() {
		if r := fetch(t, fresh, path); r.Code != http.StatusOK || slices.Contains(r.coverage(t), "perpl_snapshot_stale") {
			t.Errorf("%s flagged while fresh: %d %v", name, r.Code, r.body)
		}
	}

	stale := staleHandler(f, snapTime.Add(3*time.Minute), 2*time.Minute)
	if r := fetch(t, stale, "/readyz"); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale readyz %d", r.Code)
	}
	if r := fetch(t, stale, "/healthz"); r.Code != http.StatusOK {
		t.Fatalf("healthz %d", r.Code)
	}
	for name, path := range dataRoutes() {
		r := fetch(t, stale, path)
		if r.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
			continue
		}
		if got, want := slices.Contains(r.coverage(t), "perpl_snapshot_stale"), slices.Contains(snapshotBacked, name); got != want {
			t.Errorf("%s: stale code %v, want %v (%v)", name, got, want, r.coverage(t))
		}
	}
}

func TestSnapshotAgeIsNotCheckedWithoutALimitOrATakenTime(t *testing.T) {
	f := newFake(t)
	f.snap.TakenAt = snapTime
	for _, h := range []http.Handler{
		staleHandler(f, snapTime.Add(24*time.Hour), 0),
		testHandler(newFake(t)),
	} {
		if r := fetch(t, h, "/readyz"); r.Code != http.StatusOK {
			t.Fatalf("readyz %d", r.Code)
		}
		if r := fetch(t, h, base+"/markets"); slices.Contains(r.coverage(t), "perpl_snapshot_stale") {
			t.Fatal("flagged without a limit")
		}
	}
}

func logWith(t *testing.T, fn func(h http.Handler)) string {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	fn(testHandler(newFake(t)))
	return buf.String()
}

func TestAClientBudgetThatRunsOutIsNotAnError(t *testing.T) {
	out := logWith(t, func(h http.Handler) {
		for _, ms := range []string{"0", "-1"} {
			if r := fetch(t, h, base+"/markets", "Sona-Deadline-Ms", ms); r.Code != http.StatusGatewayTimeout {
				t.Fatalf("budget %s: %d", ms, r.Code)
			}
		}
	})
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("a client budget was logged as an error: %s", out)
	}
}

func TestADeadlineOfOurOwnIsStillAnError(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	f := newFake(t)
	f.err = fmt.Errorf("query: %w", context.DeadlineExceeded)
	if r := fetch(t, testHandler(f), base+"/liquidations"); r.Code != http.StatusGatewayTimeout {
		t.Fatalf("%d", r.Code)
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestADeadlineUnderAClientBudgetIsAWarning(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	f := newFake(t)
	f.err = fmt.Errorf("query: %w", context.DeadlineExceeded)
	if r := fetch(t, testHandler(f), base+"/liquidations", "Sona-Deadline-Ms", "50"); r.Code != http.StatusGatewayTimeout {
		t.Fatalf("%d", r.Code)
	}
	if strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("log %q", buf.String())
	}
}

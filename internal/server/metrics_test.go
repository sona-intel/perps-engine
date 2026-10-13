package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRequestsAreCountedByRoutePatternAndStatus(t *testing.T) {
	f := newFake(t)
	m := NewMetrics(f)
	h := New(f, m, Options{})
	fetch(t, h, base+"/markets")
	fetch(t, h, base+"/markets")
	fetch(t, h, base+"/markets/TST/stress?shock=0")
	fetch(t, h, base+"/markets/DOGE/stress")
	fetch(t, h, "/no/such/route")
	fetch(t, h, "/healthz")
	fetch(t, h, "/readyz")
	body := scrape(t, m)
	for _, want := range []string{
		`perps_request_duration_seconds_count{code="200",route="GET /internal/v1/chains/{chain}/perpl/markets"} 2`,
		`perps_request_duration_seconds_count{code="400",route="GET /internal/v1/chains/{chain}/perpl/markets/{market}/stress"} 1`,
		`perps_request_duration_seconds_count{code="404",route="GET /internal/v1/chains/{chain}/perpl/markets/{market}/stress"} 1`,
		`perps_request_duration_seconds_count{code="404",route="unmatched"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if strings.Contains(body, "healthz") || strings.Contains(body, "readyz") || strings.Contains(body, "DOGE") {
		t.Error("probes or raw paths became labels")
	}
}

func TestSnapshotGaugesFollowTheSource(t *testing.T) {
	f := newFake(t)
	m := NewMetrics(f)
	body := scrape(t, m)
	for _, want := range []string{"perps_snapshot_block 1.12777356e+08\n", "perps_markets 2\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q in\n%s", want, body)
		}
	}
	f.snap = nil
	body = scrape(t, m)
	for _, want := range []string{"perps_snapshot_block 0\n", "perps_markets 0\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("without a snapshot, metrics lack %q", want)
		}
	}
}

func TestMetricNamesThatTheRefreshPackageOwnsStayFree(t *testing.T) {
	body := scrape(t, NewMetrics(newFake(t)))
	for _, name := range []string{"perps_positions", "perps_snapshot_age_seconds", "perps_paid_rpc_calls_total"} {
		if strings.Contains(body, name) {
			t.Errorf("%s is registered twice once the refresh package joins the registry", name)
		}
	}
}

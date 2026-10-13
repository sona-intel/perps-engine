package server

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/jackc/pgx/v5/pgconn"

	"perps/internal/problem"
	"perps/internal/sources"
)

const specHost = "http://127.0.0.1:8201"

type specCase struct {
	name, path string
	// like names the documented path whose operation answers for a path that no operation names
	like   string
	header []string
	status int
	// malformed marks a request that the spec itself rejects
	malformed bool
	gone      bool
	// handler replaces the seeded handler for a case that needs another state of the service
	handler func(t *testing.T) http.Handler
}

var stamped = []string{
	"listMarkets", "getMarket", "getLiquidationMap", "getStress", "getStressAll", "getConcentration", "listLiquidations",
	"listLiquidationsDaily", "getFunding", "getAccount", "getMarketHistory", "getLeaderboard",
}

func loadSpec(t testing.TB) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func withSource(mutate func(*fakeSource)) func(t *testing.T) http.Handler {
	return func(t *testing.T) http.Handler {
		f := fixtureSource(t)
		mutate(f)
		return testHandler(f)
	}
}

func fixtureAddr(t *testing.T) string {
	t.Helper()
	for a := range fixtureSource(t).accounts {
		if a != emptyAddr {
			return a.String()
		}
	}
	t.Fatal("no account")
	return ""
}

func servedCases(t *testing.T) []specCase {
	addr := fixtureAddr(t)
	dbDown := withSource(func(f *fakeSource) { f.err = errors.New("dial tcp: connection refused") })
	noSnapshot := withSource(func(f *fakeSource) { f.snap = nil; f.ready = false })
	return []specCase{
		{name: "healthz", path: "/healthz"},
		{name: "readyz", path: "/readyz"},
		{name: "readyz while not ready", path: "/readyz", status: http.StatusServiceUnavailable, handler: noSnapshot},
		{name: "status", path: base + "/status"},
		{name: "status before the first snapshot", path: base + "/status", handler: noSnapshot},
		{name: "status after an upgrade", path: base + "/status", handler: withSource(func(f *fakeSource) {
			f.rt.ImplementationChanged, f.rt.Backfill, f.rt.PaidRPCCalls = true, BackfillDisabled, 2
		})},
		{name: "markets", path: base + "/markets"},
		{name: "market by symbol", path: base + "/markets/BTC"},
		{name: "market by lowercase symbol with an underscore", path: base + "/markets/sol_v2"},
		{name: "market by perp id", path: base + "/markets/10"},
		{name: "market without positions", path: base + "/markets/TAO"},
		{name: "liquidation map", path: base + "/markets/BTC/liquidation-map"},
		{name: "liquidation map with bounds", path: base + "/markets/ETH/liquidation-map?bucket_bps=5&range_pct=50"},
		{name: "liquidation map narrow", path: base + "/markets/MON/liquidation-map?bucket_bps=500&range_pct=1"},
		{name: "liquidation map without positions", path: base + "/markets/TAO/liquidation-map"},
		{name: "stress", path: base + "/markets/BTC/stress"},
		{name: "stress of a fall", path: base + "/markets/MON/stress?shock=-0.5"},
		{name: "stress of a rise", path: base + "/markets/ETH/stress?shock=0.2"},
		{name: "stress without positions", path: base + "/markets/TAO/stress?shock=0.05"},
		{name: "stress of all markets", path: base + "/stress"},
		{name: "stress of all markets after a rise", path: base + "/stress?shock=0.1"},
		{name: "concentration", path: base + "/concentration"},
		{name: "concentration without leaderboards", path: base + "/concentration", handler: withSource(func(f *fakeSource) {
			clear(f.boards)
			f.rt.Backfill = BackfillRunning
		})},
		{name: "concentration without liquidations", path: base + "/concentration", handler: withSource(func(f *fakeSource) {
			f.keepers, f.totals.Count, f.totals.FirstBlock, f.totals.LastBlock = nil, 0, 0, 0
		})},
		{name: "liquidations first page", path: base + "/liquidations?limit=2"},
		{name: "liquidations after a cursor", path: base + "/liquidations?limit=2&cursor=111999990.1"},
		{name: "liquidations of a market", path: base + "/liquidations?market=btc&limit=200"},
		{name: "liquidations of a retired market", path: base + "/liquidations?market=SOL"},
		{name: "liquidations while the backfill runs", path: base + "/liquidations", handler: withSource(func(f *fakeSource) { f.rt.Backfill = BackfillRunning })},
		{name: "daily liquidations", path: base + "/liquidations/daily"},
		{name: "daily liquidations of a market", path: base + "/liquidations/daily?market=ETH&days=7"},
		{name: "daily liquidations for a year", path: base + "/liquidations/daily?days=400"},
		{name: "funding", path: base + "/funding?market=BTC"},
		{name: "funding for a week", path: base + "/funding?market=eth&hours=168"},
		{name: "funding for an hour", path: base + "/funding?market=1&hours=1"},
		{name: "history of volume", path: base + "/markets/BTC/history?metric=volume&hours=24"},
		{name: "history of funding", path: base + "/markets/eth/history?metric=funding&hours=720"},
		{name: "history of open interest", path: base + "/markets/1/history?metric=oi"},
		{name: "history of an empty market", path: base + "/markets/TAO/history?metric=oi&hours=168"},
		{name: "leaderboard", path: base + "/leaderboard"},
		{name: "leaderboard by volume of a day", path: base + "/leaderboard?by=volume&period=day&limit=100"},
		{name: "leaderboard by pnl of a day", path: base + "/leaderboard?by=pnl&period=day&limit=1"},
		{name: "account", path: base + "/accounts/" + addr},
		{name: "account in uppercase", path: base + "/accounts/0x" + strings.ToUpper(addr[2:])},
		{name: "account without positions", path: base + "/accounts/" + emptyAddr.String()},
		{name: "account with the leaderboard gone", path: base + "/accounts/" + addr, handler: withSource(func(f *fakeSource) { delete(f.boards, sources.PeriodAll) })},

		{name: "bucket width too small", path: base + "/markets/BTC/liquidation-map?bucket_bps=4", status: http.StatusBadRequest, malformed: true},
		{name: "bucket width too large", path: base + "/markets/BTC/liquidation-map?bucket_bps=501", status: http.StatusBadRequest, malformed: true},
		{name: "range too large", path: base + "/markets/BTC/liquidation-map?range_pct=51", status: http.StatusBadRequest, malformed: true},
		{name: "shock of zero", path: base + "/markets/BTC/stress?shock=0", status: http.StatusBadRequest},
		{name: "shock too large", path: base + "/stress?shock=0.51", status: http.StatusBadRequest, malformed: true},
		{name: "shock that is no number", path: base + "/stress?shock=abc", status: http.StatusBadRequest, malformed: true},
		{name: "shock with an exponent", path: base + "/stress?shock=1e-1", status: http.StatusBadRequest},
		{name: "limit zero", path: base + "/liquidations?limit=0", status: http.StatusBadRequest, malformed: true},
		{name: "limit 201", path: base + "/liquidations?limit=201", status: http.StatusBadRequest, malformed: true},
		{name: "cursor that is none", path: base + "/liquidations?cursor=abc", status: http.StatusBadRequest, malformed: true},
		{name: "days zero", path: base + "/liquidations/daily?days=0", status: http.StatusBadRequest, malformed: true},
		{name: "hours 169", path: base + "/funding?market=BTC&hours=169", status: http.StatusBadRequest, malformed: true},
		{name: "funding without a market", path: base + "/funding", status: http.StatusBadRequest, malformed: true},
		{name: "market with a bad character", path: base + "/markets/BT%21", status: http.StatusBadRequest, malformed: true},
		{name: "address that is too short", path: base + "/accounts/0x12", status: http.StatusBadRequest, malformed: true},
		{name: "history hours 48", path: base + "/markets/BTC/history?metric=oi&hours=48", status: http.StatusBadRequest, malformed: true},
		{name: "history without a metric", path: base + "/markets/BTC/history", status: http.StatusBadRequest, malformed: true},
		{name: "history of an unknown metric", path: base + "/markets/BTC/history?metric=price", status: http.StatusBadRequest, malformed: true},
		{name: "history of an unknown market", path: base + "/markets/DOGE/history?metric=oi", status: http.StatusNotFound},
		{name: "history with the market data down", path: base + "/markets/BTC/history?metric=volume", status: http.StatusServiceUnavailable, handler: dbDown},
		{name: "leaderboard of an unknown ranking", path: base + "/leaderboard?by=size", status: http.StatusBadRequest, malformed: true},
		{name: "leaderboard of an unknown period", path: base + "/leaderboard?period=week", status: http.StatusBadRequest, malformed: true},
		{name: "leaderboard limit 101", path: base + "/leaderboard?limit=101", status: http.StatusBadRequest, malformed: true},
		{name: "leaderboard not read yet", path: base + "/leaderboard", status: http.StatusServiceUnavailable, handler: withSource(func(f *fakeSource) {
			clear(f.pnlBoard)
		})},
		{name: "unknown parameter", path: base + "/markets?foo=1", status: http.StatusBadRequest},
		{name: "repeated parameter", path: base + "/liquidations?limit=1&limit=2", status: http.StatusBadRequest},

		{name: "another chain", path: "/internal/v1/chains/eip155:1/perpl/markets", status: http.StatusNotFound},
		{name: "chain that is no CAIP-2 id", path: "/internal/v1/chains/foo/perpl/stress", status: http.StatusBadRequest},
		{name: "unknown market", path: base + "/markets/DOGE", status: http.StatusNotFound},
		{name: "unknown perp id", path: base + "/markets/999/stress", status: http.StatusNotFound},
		{name: "market without a mark price", path: base + "/markets/ENA/liquidation-map", status: http.StatusNotFound, handler: withSource(func(f *fakeSource) {
			f.snap.Books[16].Market.MarkPNS.SetInt64(0)
		})},
		{name: "unknown market in the feed", path: base + "/liquidations?market=DOGE", status: http.StatusNotFound},
		{name: "unknown market in the funding", path: base + "/funding?market=DOGE", status: http.StatusNotFound},
		{name: "address without an account", path: base + "/accounts/" + unknownAddr.String(), status: http.StatusNotFound},
		{name: "unknown route", path: "/nope", like: base + "/markets", status: http.StatusNotFound},

		{name: "markets before the first snapshot", path: base + "/markets", status: http.StatusServiceUnavailable, handler: noSnapshot},
		{name: "stress before the first snapshot", path: base + "/markets/BTC/stress", status: http.StatusServiceUnavailable, handler: noSnapshot},
		{name: "liquidations with the database down", path: base + "/liquidations", status: http.StatusServiceUnavailable, handler: dbDown},
		{name: "status with the database down", path: base + "/status", status: http.StatusServiceUnavailable, handler: dbDown},
		{name: "account with the chain down", path: base + "/accounts/" + addr, status: http.StatusServiceUnavailable, handler: withSource(func(f *fakeSource) {
			f.accountErr = errors.New("dial tcp: connection refused")
		})},
		{name: "markets past their deadline", path: base + "/markets", header: []string{"Sona-Deadline-Ms", "0"}, status: http.StatusGatewayTimeout},
		{name: "status past its deadline", path: base + "/status", header: []string{"Sona-Deadline-Ms", "-1"}, status: http.StatusGatewayTimeout},
		{name: "liquidations that time out", path: base + "/liquidations", status: http.StatusGatewayTimeout, handler: withSource(func(f *fakeSource) {
			f.err = fmt.Errorf("query: %w", context.DeadlineExceeded)
		})},
		{name: "liquidations of a client that left", path: base + "/liquidations", status: statusClientClosed, gone: true},
		{name: "liquidations the database rejects", path: base + "/liquidations", status: http.StatusInternalServerError, handler: withSource(func(f *fakeSource) {
			f.err = &pgconn.PgError{Code: "42703", Message: "column does not exist"}
		})},
		{name: "markets from broken chain data", path: base + "/markets", status: http.StatusInternalServerError, handler: withSource(func(f *fakeSource) {
			f.snap.Books[0].Market.InsuranceBalanceCNS = nil
		})},
	}
}

func checkHeaders(t *testing.T, operation string, rec *httptest.ResponseRecorder, body string) {
	t.Helper()
	h := rec.Header()
	probe := operation == "getHealth" || operation == "getReady"
	if got := h.Get("X-Request-Id") != ""; got == probe {
		t.Errorf("X-Request-Id present %v on %s", got, operation)
	}
	if rec.Code == http.StatusServiceUnavailable && !probe {
		if got := h.Get("Retry-After"); (got != "1" && got != "5") || body != problem.CodeUpstream {
			t.Errorf("Retry-After %q for %q", got, body)
		}
	}
	if rec.Code == statusClientClosed && rec.Body.Len() != 0 {
		t.Errorf("body %q", rec.Body)
	}
	sona := rec.Code == http.StatusOK && slices.Contains(stamped, operation)
	for _, name := range []string{"Sona-Finality", "Sona-Block", "Sona-Block-Time"} {
		if got := h.Get(name) != ""; got != sona {
			t.Errorf("%s present %v, want %v", name, got, sona)
		}
	}
	if rec.Code >= 400 && !probe && rec.Code != statusClientClosed && h.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q on %d", h.Get("Cache-Control"), rec.Code)
	}
}

func checkDeclared(t *testing.T, op *openapi3.Operation, rec *httptest.ResponseRecorder) {
	t.Helper()
	ref := cmp.Or(op.Responses.Status(rec.Code), op.Responses.Default())
	if ref == nil {
		t.Fatalf("%d is not declared", rec.Code)
	}
	declared := make(map[string]bool)
	for name := range ref.Value.Headers {
		declared[http.CanonicalHeaderKey(name)] = true
	}
	for name := range rec.Header() {
		if !declared[name] && name != "Content-Type" && name != "Content-Length" {
			t.Errorf("%d sends %s, which the spec does not declare", rec.Code, name)
		}
	}
}

func TestSpecIsValid(t *testing.T) {
	doc := loadSpec(t)
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.2" {
		t.Errorf("openapi %q", doc.OpenAPI)
	}
	var urls []string
	for _, s := range doc.Servers {
		urls = append(urls, s.URL)
	}
	if want := []string{specHost}; !slices.Equal(urls, want) {
		t.Fatalf("servers %q, want %q", urls, want)
	}
}

func TestSpecIsSelfContained(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`\$ref"?:\s*["']?([^"'\s,}]+)`).FindAllStringSubmatch(string(raw), -1)
	if len(refs) == 0 {
		t.Fatal("the file holds no $ref, so the pattern or the spec is wrong")
	}
	for _, m := range refs {
		if !strings.HasPrefix(m[1], "#/") {
			t.Errorf("$ref %s leaves the file", m[1])
		}
	}
}

func TestSpecOperationsAreTheRoutes(t *testing.T) {
	doc := loadSpec(t)
	var documented, served []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented = append(documented, method+" "+path)
		}
	}
	served = append(served, "GET /healthz", "GET /readyz")
	for _, rt := range routes {
		served = append(served, rt.pattern)
	}
	slices.Sort(documented)
	slices.Sort(served)
	if !slices.Equal(documented, served) {
		t.Fatalf("spec has %q, routes are %q", documented, served)
	}
}

func TestSpecValidatesResponses(t *testing.T) {
	doc := loadSpec(t)
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	seeded := testHandler(fixtureSource(t))
	for _, c := range servedCases(t) {
		t.Run(c.name, func(t *testing.T) {
			h := seeded
			if c.handler != nil {
				h = c.handler(t)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if c.gone {
				cancel()
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, specHost+c.path, nil)
			for i := 0; i+1 < len(c.header); i += 2 {
				req.Header.Set(c.header[i], c.header[i+1])
			}
			lookup := req
			if c.like != "" {
				lookup = httptest.NewRequestWithContext(t.Context(), http.MethodGet, specHost+c.like, nil)
			}
			route, params, err := router.FindRoute(lookup)
			if err != nil {
				t.Fatal(err)
			}
			in := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route}
			if c.like == "" {
				if err := openapi3filter.ValidateRequest(t.Context(), in); (err != nil) != c.malformed {
					t.Fatalf("malformed %v, request: %v", c.malformed, err)
				}
			}
			want := cmp.Or(c.status, http.StatusOK)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != want {
				t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
			}
			out := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: in,
				Status:                 rec.Code,
				Header:                 rec.Header(),
				Options:                &openapi3filter.Options{IncludeResponseStatus: true},
			}
			if err := openapi3filter.ValidateResponse(t.Context(), out.SetBodyBytes(rec.Body.Bytes())); err != nil {
				t.Fatalf("response: %v\n%s", err, rec.Body)
			}
			code := ""
			if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") {
				code = decodeCode(t, rec.Body.Bytes())
			}
			checkHeaders(t, route.Operation.OperationID, rec, code)
			checkDeclared(t, route.Operation, rec)
		})
	}
}

func decodeCode(t *testing.T, body []byte) string {
	t.Helper()
	var p problem.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p.Code
}

func TestEveryOperationDeclaresItsProblems(t *testing.T) {
	data := []string{"400", "404", "499", "500", "503", "504"}
	want := map[string][]string{
		"getHealth":             {"200"},
		"getReady":              {"200", "503"},
		"getStatus":             {"400", "404", "499", "500", "503", "504"},
		"listMarkets":           data,
		"getMarket":             data,
		"getLiquidationMap":     data,
		"getStress":             data,
		"getStressAll":          data,
		"getConcentration":      data,
		"listLiquidations":      data,
		"listLiquidationsDaily": data,
		"getFunding":            data,
		"getAccount":            data,
		"getMarketHistory":      data,
		"getLeaderboard":        data,
	}
	got := make(map[string]*openapi3.Operation)
	for _, item := range loadSpec(t).Paths.Map() {
		for _, op := range item.Operations() {
			got[op.OperationID] = op
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d operations, want %d", len(got), len(want))
	}
	for id, codes := range want {
		op := got[id]
		if op == nil {
			t.Errorf("operation %s is not documented", id)
			continue
		}
		for _, code := range codes {
			if op.Responses.Value(code) == nil {
				t.Errorf("%s does not declare %s", id, code)
			}
		}
	}
}

func TestSpecExamplesMatchTheirSchemas(t *testing.T) {
	doc := loadSpec(t)
	seen := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			for status, ref := range op.Responses.Map() {
				for mediaType, content := range ref.Value.Content {
					for name, ex := range content.Examples {
						seen++
						if err := content.Schema.Value.VisitJSON(ex.Value.Value, openapi3.MultiErrors()); err != nil {
							t.Errorf("%s %s %s %s example %s: %v", method, path, status, mediaType, name, err)
						}
					}
				}
			}
		}
	}
	for name, ref := range doc.Components.Responses {
		for mediaType, content := range ref.Value.Content {
			for exName, ex := range content.Examples {
				seen++
				if err := content.Schema.Value.VisitJSON(ex.Value.Value, openapi3.MultiErrors()); err != nil {
					t.Errorf("response %s %s example %s: %v", name, mediaType, exName, err)
				}
			}
		}
	}
	if seen < 20 {
		t.Errorf("only %d examples", seen)
	}
}

func TestSpecDocumentsEveryProblemCodeAndCoverageCode(t *testing.T) {
	doc := loadSpec(t)
	enum := func(schema string, prop string) []string {
		s := doc.Components.Schemas[schema].Value.Properties[prop].Value
		var out []string
		for _, v := range s.Enum {
			out = append(out, v.(string))
		}
		return out
	}
	problems := enum("Problem", "code")
	for _, code := range []string{
		problem.CodeInvalidQuery, problem.CodeInvalidID, problem.CodeUnsupportedChain,
		problem.CodeNotFound, problem.CodeInternal, problem.CodeUpstream, problem.CodeDeadline,
	} {
		if !slices.Contains(problems, code) {
			t.Errorf("problem code %s is not documented", code)
		}
	}
	if len(problems) != 7 {
		t.Errorf("%d problem codes documented: %v", len(problems), problems)
	}
	var covs []string
	for _, v := range doc.Components.Schemas["CoverageCode"].Value.Enum {
		covs = append(covs, v.(string))
	}
	if !slices.Equal(covs, codes(allCoverage...)) {
		t.Errorf("coverage codes documented %v, served %v", covs, codes(allCoverage...))
	}
	for _, c := range []coverage{covSnapshotOnly, covBadDebtFloor, covLeaderboard, covHistory, covFunding, covOI, covBoardFigures, covStale} {
		if !slices.Contains(covs, c.Code) {
			t.Errorf("coverage code %s is not documented", c.Code)
		}
	}
	if len(covs) != 8 {
		t.Errorf("%d coverage codes documented: %v", len(covs), covs)
	}
	backfill := doc.Components.Schemas["Status"].Value.Properties["backfill"].Value
	var states []string
	for _, v := range backfill.Enum {
		states = append(states, v.(string))
	}
	want := []string{BackfillComplete, BackfillRunning, BackfillPending, BackfillRetrying, BackfillDisabled}
	slices.Sort(states)
	slices.Sort(want)
	if !slices.Equal(states, want) {
		t.Errorf("backfill states documented %v, constants %v", states, want)
	}
}

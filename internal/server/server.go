package server

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"perps/internal/problem"
	"perps/internal/risk"
)

const (
	// nginx's code for a client that closed the request before the answer
	statusClientClosed = 499
	prefix             = "/internal/v1/chains/{chain}/perpl"
)

var errNotReady = errors.New("the first snapshot is not ready")

type Options struct {
	// Now is the clock, time.Now when nil
	Now func() time.Time
	// StaleAfter is the age of a snapshot from which readiness fails and records carry perpl_snapshot_stale, zero turns the check off
	StaleAfter time.Duration
}

type app struct {
	src        Source
	now        func() time.Time
	staleAfter time.Duration
}

var routes = []struct {
	pattern string
	handler func(*app) endpoint
}{
	{"GET " + prefix + "/status", (*app).status},
	{"GET " + prefix + "/markets", (*app).markets},
	{"GET " + prefix + "/markets/{market}", (*app).market},
	{"GET " + prefix + "/markets/{market}/liquidation-map", (*app).liquidationMap},
	{"GET " + prefix + "/markets/{market}/stress", (*app).stress},
	{"GET " + prefix + "/stress", (*app).stressAll},
	{"GET " + prefix + "/concentration", (*app).concentration},
	{"GET " + prefix + "/liquidations", (*app).liquidations},
	{"GET " + prefix + "/liquidations/daily", (*app).liquidationsDaily},
	{"GET " + prefix + "/funding", (*app).funding},
	{"GET " + prefix + "/accounts/{address}", (*app).account},
}

func New(src Source, m *Metrics, opts Options) http.Handler {
	a := &app{src: src, now: opts.Now, staleAfter: opts.StaleAfter}
	if a.now == nil {
		a.now = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !src.Ready() || a.stale(src.Snapshot()) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	for _, rt := range routes {
		mux.HandleFunc(rt.pattern, a.serve(rt.handler(a)))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		problem.Write(w, problem.New(problem.CodeNotFound, "No such route.", req.URL.EscapedPath()))
	})
	return instrument(m, mux)
}

func marshal(v any) []byte {
	// AllowInvalidUTF8 leaves no encode error to handle
	b, _ := json.Marshal(v, jsontext.AllowInvalidUTF8(true))
	return b
}

/* Middleware */

func instrument(m *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/healthz" || req.URL.Path == "/readyz" {
			next.ServeHTTP(w, req)
			return
		}
		start := time.Now()
		w.Header().Set("X-Request-Id", requestID(req.Header.Get("traceparent")))
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req)
		route := req.Pattern
		if route == "" || route == "/" {
			route = "unmatched"
		}
		m.observe(route, sw.code(), time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func requestID(traceparent string) string {
	if len(traceparent) == 55 && traceparent[:3] == "00-" && traceparent[35] == '-' && traceparent[52] == '-' &&
		isLowerHex(traceparent[3:35]) && isLowerHex(traceparent[36:52]) && isLowerHex(traceparent[53:]) &&
		strings.Trim(traceparent[3:35], "0") != "" && strings.Trim(traceparent[36:52], "0") != "" {
		return traceparent[3:35]
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], rand.Uint64())
	binary.BigEndian.PutUint64(b[8:], rand.Uint64())
	return hex.EncodeToString(b[:])
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

/* Serving */

type call struct {
	a    *app
	ctx  context.Context
	req  *http.Request
	snap *Snapshot
}

// snapshot returns the latest snapshot or errNotReady, and remembers it for the response headers
func (c *call) snapshot() (*Snapshot, error) {
	s := c.a.src.Snapshot()
	if s == nil {
		return nil, errNotReady
	}
	c.snap = s
	return s, nil
}

type result struct {
	body any
	// unstamped leaves the Sona headers off a route that is about the engine and not about one block
	unstamped bool
}

type endpoint func(c *call) (result, error)

// head opens every record, kind names its shape and chain is the CAIP-2 id
type head struct {
	Kind  string `json:"kind"`
	Chain string `json:"chain"`
}

func headOf(kind string) head { return head{Kind: kind, Chain: chainName} }

func (a *app) serve(h endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		budget := deadline(req.Header)
		if budget <= 0 {
			a.fail(w, req, context.DeadlineExceeded)
			return
		}
		if err := checkChain(req.PathValue("chain")); err != nil {
			a.fail(w, req, err)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), budget)
		defer cancel()
		c := &call{a: a, ctx: ctx, req: req}
		res, err := h(c)
		if err != nil {
			a.fail(w, req, err)
			return
		}
		hdr := w.Header()
		if s := c.snap; s != nil && !res.unstamped {
			hdr.Set("Sona-Finality", "confirmed")
			hdr.Set("Sona-Block", strconv.FormatUint(s.Block, 10))
			hdr.Set("Sona-Block-Time", strconv.FormatInt(s.Time.Unix(), 10))
		}
		body := marshal(res.body)
		hdr.Set("Content-Type", "application/json")
		hdr.Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}
}

func (a *app) fail(w http.ResponseWriter, req *http.Request, err error) {
	instance := req.URL.EscapedPath()
	var he *httpError
	var pe *risk.ParamError
	var p problem.Problem
	switch {
	case errors.As(err, &he):
		p = problem.New(he.code, he.detail, instance)
	case errors.Is(err, errNotReady):
		p = problem.NotReady("The first snapshot of the exchange is not ready yet.", instance)
	case errors.Is(err, ErrAccountNotFound):
		p = problem.New(problem.CodeNotFound, "This address has no Perpl account.", instance)
	case errors.As(err, &pe):
		p = problem.New(problem.CodeInvalidQuery, pe.Param+" "+pe.Reason+".", instance)
	case errors.Is(err, context.DeadlineExceeded):
		p = problem.New(problem.CodeDeadline, "The deadline passed before the data source answered.", instance)
		a.logDeadline(w, req, err)
	case errors.Is(err, context.Canceled) && req.Context().Err() != nil:
		w.WriteHeader(statusClientClosed)
		return
	case errors.Is(err, risk.ErrInvalidInput):
		p = problem.New(problem.CodeInternal, "The request failed.", instance)
		a.log(w, req, err)
	case unreachable(err):
		p = problem.New(problem.CodeUpstream, "A data source did not answer.", instance)
		a.log(w, req, err)
	default:
		p = problem.New(problem.CodeInternal, "The request failed.", instance)
		a.log(w, req, err)
	}
	problem.Write(w, p)
}

func (*app) log(w http.ResponseWriter, req *http.Request, err error) {
	slog.ErrorContext(req.Context(), "request failed", "request_id", w.Header().Get("X-Request-Id"), "route", req.Pattern, "error", err)
}

// logDeadline keeps the error level for the default budget, since only a budget the caller chose is the caller's own doing
func (a *app) logDeadline(w http.ResponseWriter, req *http.Request, err error) {
	if req.Header.Get("Sona-Deadline-Ms") == "" {
		a.log(w, req, err)
		return
	}
	slog.WarnContext(req.Context(), "request past the budget of the caller", "request_id", w.Header().Get("X-Request-Id"), "route", req.Pattern, "error", err)
}

// unreachable is true for every error that is not a statement the database rejected
func unreachable(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return true
	}
	switch pg.Code[:min(2, len(pg.Code))] {
	case "08", "53", "57":
		return true
	}
	return false
}

/* Coverage */

type coverage struct {
	Code string `json:"code"`
	Note string `json:"note"`
}

var (
	covSnapshotOnly = coverage{"perpl_snapshot_only", "Positions are read at one block. Collateral added or positions closed after that block are not in these numbers."}
	covBadDebtFloor = coverage{"perpl_bad_debt_is_floor", "Gap bad debt assumes every liquidation fills at the shocked price, so it is a floor. A real book fills worse, and the share of the fund inflow from positions that close above bankruptcy is ignored."}
	covLeaderboard  = coverage{"perpl_leaderboard_unavailable", "The public leaderboard could not be read, so the volume shares or the rank that depend on it are left out."}
	covHistory      = coverage{"perpl_history_partial", "The liquidation history may be incomplete because the backfill of older events is disabled or still running. See backfill in the status."}
	covStale        = coverage{"perpl_snapshot_stale", "The latest snapshot is older than the engine allows, so refreshes are failing and these numbers describe an earlier block. See the status for the block and its age."}
	covFunding      = coverage{"perpl_funding_from_snapshots", "The series is built from the snapshots this engine stored, so it starts when the engine started and a gap means the engine was not running."}
)

func (a *app) historyCoverage() []coverage {
	if a.src.Runtime().Backfill == BackfillComplete {
		return nil
	}
	return []coverage{covHistory}
}

// withStale appends the stale limit to the limits of a record when its snapshot is past the allowed age
func (a *app) withStale(s *Snapshot, cs ...coverage) []coverage {
	if a.stale(s) {
		return append(cs, covStale)
	}
	return cs
}

func (a *app) stale(s *Snapshot) bool {
	return s != nil && a.staleAfter > 0 && !s.TakenAt.IsZero() && a.now().Sub(s.TakenAt) > a.staleAfter
}

// allCoverage is every limit the service can state, in the order the status lists them
var allCoverage = []coverage{covSnapshotOnly, covBadDebtFloor, covLeaderboard, covHistory, covFunding, covStale}

// codes is the coverage field of a record, an empty array when no limit applies
func codes(cs ...coverage) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Code
	}
	return out
}

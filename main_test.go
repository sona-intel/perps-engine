package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"perps/internal/config"
	"perps/internal/store"
	"perps/internal/testkit"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("PERPS_CONFIG_DIR", "config")
	t.Setenv("PERPS_RPC_URL", "")
	t.Setenv("ENVIO_HYPERSYNC_KEY", "")
}

/* Subcommands */

func TestUnknownCommandIsAUsageError(t *testing.T) {
	isolate(t)
	err := run(t.Context(), []string{"explode"})
	if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "explode") {
		t.Fatalf("got %v", err)
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l := listen(t)
	defer l.Close()
	return l.Addr().String()
}

// withDatabase points the commands at a throwaway schema and skips without PERPS_TEST_PG
func withDatabase(t *testing.T) {
	t.Helper()
	db := testkit.New(t)
	prevDSN, prevSchema := postgresDSN, postgresSchema
	postgresDSN, postgresSchema = os.Getenv(testkit.EnvDSN), db.Schema
	t.Cleanup(func() { postgresDSN, postgresSchema = prevDSN, prevSchema })
}

func unreachableDatabase(t *testing.T) {
	t.Helper()
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGUSER", "perps")
	t.Setenv("PGDATABASE", "perps")
	t.Setenv("PGCONNECT_TIMEOUT", "2")
}

func TestServeRunsUntilTheContextEnds(t *testing.T) {
	isolate(t)
	withDatabase(t)
	api, debug := freeAddr(t), freeAddr(t)
	prevAPI, prevDebug := apiAddr, debugAddr
	apiAddr, debugAddr = api, debug
	t.Cleanup(func() { apiAddr, debugAddr = prevAPI, prevDebug })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil) }()

	var last error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if last = healthcheck(ctx, "http://"+api+"/healthz"); last == nil {
			break
		}
	}
	if last != nil {
		t.Fatalf("never became healthy: %v", last)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+debug+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "perps_refresh_duration_seconds") {
		t.Fatal("metrics lack the refresh histogram")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ignored the cancelled context")
	}
}

func TestServeFailsOnBadConfig(t *testing.T) {
	isolate(t)
	t.Setenv("PERPS_CONFIG_DIR", t.TempDir())
	if err := run(t.Context(), []string{"serve"}); err == nil {
		t.Fatal("serve started without engine.yaml")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "engine.yaml"), []byte("chain: eip155:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERPS_CONFIG_DIR", dir)
	if err := run(t.Context(), []string{"serve"}); err == nil {
		t.Fatal("serve started on an invalid engine.yaml")
	}
}

func TestServeFailsWhenPostgresIsUnreachable(t *testing.T) {
	isolate(t)
	unreachableDatabase(t)
	if err := run(t.Context(), []string{"serve"}); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("got %v", err)
	}
}

func TestServeFailsWhenAPortIsTaken(t *testing.T) {
	isolate(t)
	withDatabase(t)
	taken := listen(t)
	defer taken.Close()
	prevAPI, prevDebug := apiAddr, debugAddr
	apiAddr, debugAddr = taken.Addr().String(), freeAddr(t)
	t.Cleanup(func() { apiAddr, debugAddr = prevAPI, prevDebug })
	done := make(chan error, 1)
	go func() { done <- run(t.Context(), nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve ignored a busy port")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve kept running with a busy port")
	}
}

func TestMigrateValidatesTheConfig(t *testing.T) {
	isolate(t)
	t.Setenv("PERPS_CONFIG_DIR", t.TempDir())
	if err := run(t.Context(), []string{"migrate"}); err == nil {
		t.Fatal("migrate ran without engine.yaml")
	}
}

func TestMigrateFailsWhenPostgresIsUnreachable(t *testing.T) {
	isolate(t)
	unreachableDatabase(t)
	if err := run(t.Context(), []string{"migrate"}); err == nil {
		t.Fatal("migrate claimed success without a database")
	}
}

func TestMigrateAppliesTheSchemaAndIsRepeatable(t *testing.T) {
	isolate(t)
	withDatabase(t)
	for range 2 {
		if err := run(t.Context(), []string{"migrate"}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Connect(t.Context(), postgresDSN, postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.Pool.QueryRow(t.Context(), "select count(*) from liquidations").Scan(&n); err != nil {
		t.Fatal(err)
	}
}

/* RPC client */

func TestChainClientCarriesTheConfiguredUpstreamFilter(t *testing.T) {
	isolate(t)
	cases := map[string]string{"seed config": "!drpc-*", "filter cleared": ""}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			var got []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = append(got, r.Header.Values("X-ERPC-Use-Upstream")...)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x5"}`))
			}))
			defer srv.Close()
			cfg, err := config.Load("config")
			if err != nil {
				t.Fatal(err)
			}
			cfg.RPCURL = srv.URL
			if want == "" {
				cfg.RPCUpstreamFilter = ""
			}
			chain, err := newChain(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := chain.BlockNumber(t.Context()); err != nil {
				t.Fatal(err)
			}
			if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || got[0] != want) {
				t.Fatalf("header values %q, want %q", got, want)
			}
		})
	}
}

/* Healthcheck */

func TestHealthcheckTargetsLoopbackPort8080(t *testing.T) {
	if healthURL != "http://127.0.0.1:8080/healthz" {
		t.Fatal(healthURL)
	}
}

func TestHealthcheckPassesOnlyOn200(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	if err := healthcheck(t.Context(), srv.URL+"/healthz"); err != nil {
		t.Fatal(err)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := healthcheck(t.Context(), srv.URL+"/healthz"); err == nil {
		t.Fatal("503 counted as healthy")
	}
	if err := healthcheck(t.Context(), srv.URL+"/other"); err == nil {
		t.Fatal("404 counted as healthy")
	}
}

func TestHealthcheckFailsWhenNothingListens(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/healthz"
	srv.Close()
	if err := healthcheck(t.Context(), url); err == nil {
		t.Fatal("closed port counted as healthy")
	}
}

func TestHealthcheckGivesUpOnASlowServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	if err := healthcheck(t.Context(), srv.URL); err == nil {
		t.Fatal("a hung server counted as healthy")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("took %s", elapsed)
	}
}

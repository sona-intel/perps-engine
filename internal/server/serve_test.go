package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewServerTimeouts(t *testing.T) {
	s := NewServer(Addr, http.NotFoundHandler())
	if s.Addr != Addr || s.ReadHeaderTimeout != 2*time.Second || s.ReadTimeout != 5*time.Second || s.WriteTimeout != 10*time.Second ||
		s.IdleTimeout != 120*time.Second || s.MaxHeaderBytes != 64<<10 {
		t.Errorf("server %+v", s)
	}
}

func TestDebugServesMetricsAndPprof(t *testing.T) {
	s := Debug(DebugAddr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("metrics")) }))
	if s.Addr != DebugAddr {
		t.Errorf("addr %s", s.Addr)
	}
	for path, want := range map[string]int{"/metrics": 200, "/debug/pprof/": 200, "/debug/pprof/cmdline": 200, "/debug/pprof/heap": 200, "/other": 404} {
		rec := httptest.NewRecorder()
		s.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s -> %d", path, rec.Code)
		}
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, NewServer("127.0.0.1:0", http.NotFoundHandler())) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunReturnsServeErrorAndStopsTheOthers(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- Run(t.Context(), NewServer("127.0.0.1:0", http.NotFoundHandler()), NewServer("127.0.0.1:99999", http.NotFoundHandler()))
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("serve error swallowed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestMetricsHandlerServesTheRegistry(t *testing.T) {
	f := newFake(t)
	m := NewMetrics(f)
	srv := Debug(DebugAddr, m.Handler())
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatal(rec.Code)
	}
}

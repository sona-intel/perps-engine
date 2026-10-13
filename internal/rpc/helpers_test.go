package rpc

import (
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"perps/internal/perpl"
)

const testdataDir = "../../testdata"

type request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id"`
	Method  string         `json:"method"`
	Params  []any          `json:"params"`
}

func (r request) param(t *testing.T, i int) any {
	t.Helper()
	if i >= len(r.Params) {
		t.Fatalf("%s has %d params, want index %d", r.Method, len(r.Params), i)
	}
	return r.Params[i]
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixturePath(parts ...string) string {
	return filepath.Join(append([]string{testdataDir}, parts...)...)
}

type serverLog struct {
	mu       sync.Mutex
	requests []request
	headers  []http.Header
}

func (l *serverLog) add(r request, h http.Header) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, r)
	l.headers = append(l.headers, h)
}

func (l *serverLog) at(i int) request {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.requests[i]
}

func node(t *testing.T, handle func(r request) (result any, rpcErr *Error)) (*httptest.Server, *serverLog) {
	t.Helper()
	log := &serverLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, hr *http.Request) {
		body, _ := io.ReadAll(hr.Body)
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.add(req, hr.Header.Clone())
		result, rpcErr := handle(req)
		w.Header().Set("Content-Type", "application/json")
		reply(w, req.ID, result, rpcErr)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func reply(w http.ResponseWriter, id jsontext.Value, result any, rpcErr *Error) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		out["error"] = map[string]any{"code": rpcErr.Code, "message": rpcErr.Message, "data": rpcErr.Data}
	} else {
		out["result"] = result
	}
	body, _ := json.Marshal(out)
	_, _ = w.Write(body)
}

func newClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	opts = append([]Option{WithBackoff(time.Millisecond)}, opts...)
	c, err := New(url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustAddress(t *testing.T, s string) perpl.Address {
	t.Helper()
	a, err := perpl.ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustHash(t *testing.T, s string) perpl.Hash {
	t.Helper()
	h, err := perpl.ParseHash(s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

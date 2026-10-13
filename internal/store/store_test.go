package store_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"perps/internal/store"
	"perps/internal/testkit"
)

func TestConnectRejectsAMalformedDSN(t *testing.T) {
	_, err := store.Connect(t.Context(), "postgres://user@host:notaport/db", "")
	if err == nil {
		t.Fatal("Connect accepted a malformed DSN")
	}
}

func TestConnectDoesNotLeakThePasswordInErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, err := store.Connect(ctx, "postgres://user:hunter2secret@127.0.0.1:1/db?connect_timeout=1", "")
	if err == nil {
		defer s.Close()
		err = s.Ping(ctx)
	}
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if msg := err.Error(); strings.Contains(msg, "hunter2secret") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestConnectUsesASmallPoolAndTheGivenSchema(t *testing.T) {
	dsn := os.Getenv(testkit.EnvDSN)
	if dsn == "" {
		t.Skip(testkit.EnvDSN + " is not set")
	}
	s, err := store.Connect(t.Context(), dsn, "probe_schema")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var path string
	if err := s.Pool.QueryRow(t.Context(), `show search_path`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != `"probe_schema"` || s.Pool.Config().MaxConns != 4 {
		t.Fatalf("search_path %q, MaxConns %d", path, s.Pool.Config().MaxConns)
	}
	if err := s.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectWithoutDSNReadsTheLibpqEnvironment(t *testing.T) {
	dsn := os.Getenv(testkit.EnvDSN)
	if dsn == "" {
		t.Skip(testkit.EnvDSN + " is not set")
	}
	cc, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGHOST", cc.Host)
	t.Setenv("PGPORT", strconv.Itoa(int(cc.Port)))
	t.Setenv("PGUSER", cc.User)
	t.Setenv("PGPASSWORD", cc.Password)
	t.Setenv("PGDATABASE", cc.Database)
	s, err := store.Connect(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
}

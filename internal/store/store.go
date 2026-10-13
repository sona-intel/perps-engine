package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"perps/migrations"
)

const maxConns = 4

// Numeric columns travel as decimal strings so no precision is lost
type Store struct {
	Pool *pgxpool.Pool
}

// An empty dsn reads the libpq PG variables and a schema is only for tests
func Connect(ctx context.Context, dsn, schema string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres config: %w", err)
	}
	cfg.MaxConns = maxConns
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() { s.Pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }

/* Migrations */

// All pending files run in one transaction behind an advisory lock
func (s *Store) Migrate(ctx context.Context) error {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(files)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('perps_migrate:' || current_schema()))`); err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}
		const create = `create table if not exists schema_migrations (version text primary key, applied_at timestamptz not null default now())`
		if _, err := tx.Exec(ctx, create); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		rows, err := tx.Query(ctx, `select version from schema_migrations`)
		if err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		applied, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		for _, name := range files {
			if slices.Contains(applied, name) {
				continue
			}
			body, err := migrations.FS.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, name); err != nil {
				return fmt.Errorf("record migration %s: %w", name, err)
			}
		}
		return nil
	})
}

/* Conversions */

// Go ids are unsigned like the perpl types but Postgres columns are signed
var errRange = errors.New("value is out of range for its column")

func i64(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("%d: %w", v, errRange)
	}
	return int64(v), nil
}

func i32(v uint64) (int32, error) {
	if v > math.MaxInt32 {
		return 0, fmt.Errorf("%d: %w", v, errRange)
	}
	return int32(v), nil
}

type colErr struct{ err error }

func (c *colErr) i64(v uint64, what string) int64 {
	n, err := i64(v)
	if err != nil && c.err == nil {
		c.err = fmt.Errorf("%s %w", what, err)
	}
	return n
}

func (c *colErr) i32(v uint64, what string) int32 {
	n, err := i32(v)
	if err != nil && c.err == nil {
		c.err = fmt.Errorf("%s %w", what, err)
	}
	return n
}

func (c *colErr) count(v int, what string) int32 {
	if (v < 0 || v > math.MaxInt32) && c.err == nil {
		c.err = fmt.Errorf("%s %d: %w", what, v, errRange)
	}
	return int32(v)
}

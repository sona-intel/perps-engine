package store_test

import (
	"io/fs"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"perps/internal/testkit"
	"perps/migrations"
)

var fileName = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.sql$`)

func column(t *testing.T, d *testkit.DB, table string) []string {
	t.Helper()
	rows, err := d.Store.Pool.Query(t.Context(), `select column_name from information_schema.columns where table_schema = $1 and table_name = $2 order by ordinal_position`, d.Schema, table)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMigrationFilesAreNumberedAndSorted(t *testing.T) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations")
	}
	for _, n := range names {
		if !fileName.MatchString(n) {
			t.Errorf("migration %q does not match NNNN_name.sql", n)
		}
	}
	if !slices.IsSorted(names) {
		t.Fatalf("migrations %q are not in order", names)
	}
}

func TestMigrateCreatesTheTablesOfThePlan(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	rows, err := d.Store.Pool.Query(t.Context(), `select table_name from information_schema.tables where table_schema = $1 and table_type = 'BASE TABLE' order by 1`, d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accounts", "cursor", "liquidations", "market_snapshots", "position_snapshots", "schema_migrations"}
	if !slices.Equal(got, want) {
		t.Fatalf("tables %q, want %q", got, want)
	}
}

func TestMigrateColumnsMatchPlanSection5(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	want := map[string][]string{
		"market_snapshots": {
			"perp_id", "block", "ts", "mark", "oracle", "oracle_ts", "long_oi", "short_oi", "long_notional", "short_notional",
			"positions", "insurance", "funding_rate_pct100k", "scr_m10", "scr_p10",
		},
		"position_snapshots": {
			"block", "ts", "perp_id", "account_id", "side", "size", "entry", "deposit", "premium_pnl", "delta_pnl", "liq_price", "bk_price",
		},
		"liquidations": {
			"block", "log_index", "tx", "ts", "perp_id", "account_id", "side", "mark", "liq_price", "liq_lot", "pos_lot", "notional",
			"on_order_book", "keeper",
		},
		"accounts": {"account_id", "address"},
		"cursor":   {"name", "block"},
	}
	for table, cols := range want {
		if got := column(t, d, table); !slices.Equal(got, cols) {
			t.Errorf("%s columns %q, want %q", table, got, cols)
		}
	}
}

func TestMigratePrimaryKeys(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	rows, err := d.Store.Pool.Query(t.Context(), `
		select tc.table_name || ':' || string_agg(kcu.column_name, ',' order by kcu.ordinal_position)
		from information_schema.table_constraints tc
		join information_schema.key_column_usage kcu using (constraint_schema, constraint_name)
		where tc.table_schema = $1 and tc.constraint_type = 'PRIMARY KEY'
		group by tc.table_name order by 1`, d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"accounts:account_id", "cursor:name", "liquidations:block,log_index", "market_snapshots:perp_id,block",
		"position_snapshots:block,perp_id,account_id", "schema_migrations:version",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("primary keys %q, want %q", got, want)
	}
}

func TestMigrateIsIdempotentAndRecordsEveryFile(t *testing.T) {
	d := testkit.New(t)
	for range 2 {
		d.Migrate(t)
	}
	rows, err := d.Store.Pool.Query(t.Context(), `select version from schema_migrations order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("recorded versions %q, want %q", got, want)
	}
}

func TestMigrateToleratesConcurrentCallers(t *testing.T) {
	d := testkit.New(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = d.Store.Migrate(t.Context()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
}

func TestMigrateRejectsBadSideValues(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	_, err := d.Store.Pool.Exec(t.Context(), `insert into position_snapshots values (1, now(), 1, 1, 'up', 1, 1, 1, 0, 0, 0, 0)`)
	if err == nil {
		t.Fatal("position_snapshots accepted side 'up'")
	}
}

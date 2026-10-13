package testkit_test

import (
	"testing"

	"perps/internal/perpl"
	"perps/internal/store"
	"perps/internal/testkit"
)

type skipRecorder struct {
	testing.TB
	skipped bool
}

type stop struct{}

func (s *skipRecorder) Helper() {}

func (s *skipRecorder) Skip(...any) {
	s.skipped = true
	panic(stop{})
}

func newWithoutDSN(t *testing.T) (skipped bool) {
	t.Helper()
	t.Setenv(testkit.EnvDSN, "")
	rec := &skipRecorder{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stop); !ok {
				panic(r)
			}
		}
		skipped = rec.skipped
	}()
	testkit.New(rec)
	return false
}

func TestNewSkipsWithoutTheDSN(t *testing.T) {
	if !newWithoutDSN(t) {
		t.Fatal("New did not skip when PERPS_TEST_PG is empty")
	}
}

func TestHashAndAddrAreDeterministicAndDistinct(t *testing.T) {
	h7, h7again, h8 := testkit.Hash(7), testkit.Hash(7), testkit.Hash(8)
	if h7 != h7again || h7 == h8 || h7.IsZero() {
		t.Fatalf("Hash(7) %v, again %v, Hash(8) %v", h7, h7again, h8)
	}
	a3, a3again, a4 := testkit.Addr(3), testkit.Addr(3), testkit.Addr(4)
	if a3 != a3again || a3 == a4 || a3.IsZero() {
		t.Fatalf("Addr(3) %v, again %v, Addr(4) %v", a3, a3again, a4)
	}
}

func TestHashAndAddrDoNotCollide(t *testing.T) {
	seen := map[perpl.Hash]int{}
	addrs := map[perpl.Address]int{}
	for n := range 500 {
		h, a := testkit.Hash(n), testkit.Addr(n)
		if prev, dup := seen[h]; dup {
			t.Fatalf("Hash(%d) equals Hash(%d)", n, prev)
		}
		if prev, dup := addrs[a]; dup {
			t.Fatalf("Addr(%d) equals Addr(%d)", n, prev)
		}
		seen[h], addrs[a] = n, n
	}
}

func TestNewGivesAnIsolatedSchemaAndDropsIt(t *testing.T) {
	d := testkit.New(t)
	var path string
	if err := d.Store.Pool.QueryRow(t.Context(), `show search_path`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != `"`+d.Schema+`"` {
		t.Fatalf("search_path %q, schema %q", path, d.Schema)
	}
	other := testkit.New(t)
	if other.Schema == d.Schema {
		t.Fatalf("two tests share schema %q", d.Schema)
	}
}

func TestNewDropsTheSchemaOnCleanup(t *testing.T) {
	var schema string
	t.Run("create", func(t *testing.T) {
		d := testkit.New(t)
		d.Migrate(t)
		schema = d.Schema
	})
	d := testkit.New(t)
	var n int
	err := d.Store.Pool.QueryRow(t.Context(), `select count(*) from information_schema.schemata where schema_name = $1`, schema).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("schema %q survived its test", schema)
	}
}

func TestBuildersAreDeterministicAndKeyed(t *testing.T) {
	m := testkit.Market(20, 500)
	if m.PerpID != 20 || m.Block != 500 || m.Mark == "" || m != testkit.Market(20, 500) || m.Block == testkit.Market(20, 501).Block {
		t.Fatalf("Market = %+v", m)
	}
	p := testkit.Position(500, 1, 9)
	if p.Block != 500 || p.PerpID != 1 || p.AccountID != 9 || p.Side == "" || p != testkit.Position(500, 1, 9) {
		t.Fatalf("Position = %+v", p)
	}
	l := testkit.Liquidation(500, 3, 20)
	if l.Block != 500 || l.LogIndex != 3 || l.PerpID != 20 || l.Tx != testkit.Hash(500*1000+3) || l.Keeper.IsZero() || l != testkit.Liquidation(500, 3, 20) {
		t.Fatalf("Liquidation = %+v", l)
	}
	if testkit.Liquidation(500, 4, 20).Tx == l.Tx {
		t.Fatal("two logs share a transaction hash")
	}
}

func TestBuildersProduceRowsTheStoreAccepts(t *testing.T) {
	d := testkit.New(t)
	d.Migrate(t)
	if err := d.Store.InsertMarketSnapshots(t.Context(), []store.MarketSnapshot{testkit.Market(1, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.InsertPositionSnapshots(t.Context(), []store.PositionSnapshot{testkit.Position(1, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.UpsertLiquidations(t.Context(), []store.Liquidation{testkit.Liquidation(1, 1, 1)}); err != nil {
		t.Fatal(err)
	}
}

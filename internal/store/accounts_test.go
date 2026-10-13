package store_test

import (
	"slices"
	"testing"

	"perps/internal/perpl"
	"perps/internal/store"
	"perps/internal/testkit"
)

func TestUpsertAccountsInsertsAndUpdates(t *testing.T) {
	d := migrated(t)
	rows := []store.Account{{ID: 4848, Address: testkit.Addr(1)}, {ID: 4159, Address: testkit.Addr(2)}}
	if err := d.Store.UpsertAccounts(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	moved := []store.Account{{ID: 4848, Address: testkit.Addr(9)}, {ID: 1, Address: testkit.Addr(3)}}
	if err := d.Store.UpsertAccounts(t.Context(), moved); err != nil {
		t.Fatal(err)
	}
	got, err := d.Store.AccountAddresses(t.Context(), []uint64{4848, 4159, 1})
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint64]perpl.Address{4848: testkit.Addr(9), 4159: testkit.Addr(2), 1: testkit.Addr(3)}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for id, a := range want {
		if got[id] != a {
			t.Errorf("account %d = %v, want %v", id, got[id], a)
		}
	}
	if n := count(t, d, "accounts"); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

func TestUpsertAccountsAcceptsTheSameIDTwiceInOneBatch(t *testing.T) {
	d := migrated(t)
	if err := d.Store.UpsertAccounts(t.Context(), []store.Account{{ID: 7, Address: testkit.Addr(1)}, {ID: 7, Address: testkit.Addr(2)}}); err != nil {
		t.Fatal(err)
	}
	got, err := d.Store.AccountAddresses(t.Context(), []uint64{7})
	if err != nil || got[7] != testkit.Addr(2) {
		t.Fatalf("got %v, %v, want the later row", got, err)
	}
}

func TestUpsertAccountsNoRowsIsANoOp(t *testing.T) {
	d := migrated(t)
	if err := d.Store.UpsertAccounts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, "accounts"); n != 0 {
		t.Fatalf("%d rows", n)
	}
}

func TestAccountAddressesOmitsUnknownIDs(t *testing.T) {
	d := migrated(t)
	if err := d.Store.UpsertAccounts(t.Context(), []store.Account{{ID: 5, Address: testkit.Addr(5)}}); err != nil {
		t.Fatal(err)
	}
	got, err := d.Store.AccountAddresses(t.Context(), []uint64{5, 6, 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[6]; ok || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	none, err := d.Store.AccountAddresses(t.Context(), nil)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no ids gave %#v, %v", none, err)
	}
}

func TestUnresolvedLiquidationAccountsSkipsResolvedAndOrdersNewestFirst(t *testing.T) {
	d := migrated(t)
	rows := []store.Liquidation{liq(100, 0, 1), liq(200, 1, 1), liq(300, 0, 1), liq(400, 1, 1)}
	rows[0].AccountID, rows[1].AccountID, rows[2].AccountID, rows[3].AccountID = 11, 12, 13, 11
	if err := d.Store.UpsertLiquidations(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.UpsertAccounts(t.Context(), []store.Account{{ID: 12, Address: testkit.Addr(12)}}); err != nil {
		t.Fatal(err)
	}
	got, err := d.Store.UnresolvedLiquidationAccounts(t.Context(), 10)
	if err != nil || !slices.Equal(got, []uint64{11, 13}) {
		t.Fatalf("got %v, %v, want [11 13]", got, err)
	}
	one, err := d.Store.UnresolvedLiquidationAccounts(t.Context(), 1)
	if err != nil || !slices.Equal(one, []uint64{11}) {
		t.Fatalf("got %v, %v, want [11]", one, err)
	}
}

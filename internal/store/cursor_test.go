package store_test

import (
	"testing"

	"perps/internal/store"
)

func TestCursorIsMissingUntilSet(t *testing.T) {
	d := migrated(t)
	block, found, err := d.Store.Cursor(t.Context(), store.CursorBackfill)
	if err != nil || found || block != 0 {
		t.Fatalf("got %d, %v, %v", block, found, err)
	}
}

func TestSetCursorStoresAndOverwrites(t *testing.T) {
	d := migrated(t)
	for _, b := range []uint64{54773010, 102493440, 7} {
		if err := d.Store.SetCursor(t.Context(), store.CursorBackfill, b); err != nil {
			t.Fatal(err)
		}
		got, found, err := d.Store.Cursor(t.Context(), store.CursorBackfill)
		if err != nil || !found || got != b {
			t.Fatalf("after setting %d got %d, %v, %v", b, got, found, err)
		}
	}
}

func TestCursorsAreIndependentByName(t *testing.T) {
	d := migrated(t)
	if err := d.Store.SetCursor(t.Context(), store.CursorBackfill, 10); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SetCursor(t.Context(), store.CursorTail, 20); err != nil {
		t.Fatal(err)
	}
	a, _, _ := d.Store.Cursor(t.Context(), store.CursorBackfill)
	b, _, _ := d.Store.Cursor(t.Context(), store.CursorTail)
	if a != 10 || b != 20 || store.CursorBackfill == store.CursorTail {
		t.Fatalf("backfill %d tail %d", a, b)
	}
}

func TestSetCursorRejectsABlockBeyondInt64(t *testing.T) {
	d := migrated(t)
	if err := d.Store.SetCursor(t.Context(), store.CursorTail, 1<<63); err == nil {
		t.Fatal("a block above int64 was accepted")
	}
}

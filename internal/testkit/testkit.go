package testkit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"perps/internal/perpl"
	"perps/internal/store"
)

const EnvDSN = "PERPS_TEST_PG"

var Epoch = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type DB struct {
	Store  *store.Store
	Schema string
}

// Skips unless PERPS_TEST_PG names a database the tests may create schemas in
func New(t testing.TB) *DB {
	t.Helper()
	dsn := os.Getenv(EnvDSN)
	if dsn == "" {
		t.Skip(EnvDSN + " is not set")
	}
	d := &DB{Schema: "t_" + strconv.FormatUint(rand.Uint64(), 16)}
	st, err := store.Connect(t.Context(), dsn, d.Schema)
	if err != nil {
		t.Fatal(err)
	}
	d.Store = st
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := st.Pool.Exec(ctx, "drop schema if exists "+pgx.Identifier{d.Schema}.Sanitize()+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", d.Schema, err)
		}
		st.Close()
	})
	if _, err := st.Pool.Exec(t.Context(), "create schema "+pgx.Identifier{d.Schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	return d
}

func (d *DB) Migrate(t testing.TB) {
	t.Helper()
	if err := d.Store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func digest(kind string, n int) [sha256.Size]byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	return sha256.Sum256(append([]byte(kind), b[:]...))
}

func Hash(n int) perpl.Hash { return perpl.Hash(digest("hash", n)) }

func Addr(n int) perpl.Address {
	d := digest("addr", n)
	return perpl.Address(d[:len(perpl.Address{})])
}

func at(block uint64) time.Time { return Epoch.Add(time.Duration(block) * time.Second) }

func Market(perp, block uint64) store.MarketSnapshot {
	return store.MarketSnapshot{
		PerpID: perp, Block: block, Time: at(block), Mark: "2534.51", Oracle: "2534.3", OracleTime: at(block).Add(-2 * time.Second),
		LongOI: "1800.123", ShortOI: "1800.123", LongNotional: "4562341.77", ShortNotional: "4562341.77", Positions: 84,
		Insurance: "13021.5", FundingRatePct100k: -12,
	}
}

func Position(block, perp, account uint64) store.PositionSnapshot {
	return store.PositionSnapshot{
		Block: block, Time: at(block), PerpID: perp, AccountID: account, Side: store.SideLong, Size: "3.25", Entry: "82912.1",
		Deposit: "9000.5", PremiumPnl: "-12.25", DeltaPnl: "140.1", LiqPrice: "75123.4", BkPrice: "71000",
	}
}

func Liquidation(block, logIndex, perp uint64) store.Liquidation {
	side := store.SideLong
	if (block+logIndex)%2 == 1 {
		side = store.SideShort
	}
	return store.Liquidation{
		Block: block, LogIndex: logIndex, Tx: Hash(int(block*1000 + logIndex)), Time: at(block), PerpID: perp, AccountID: 1000 + logIndex,
		Side: side, Mark: "2534.51", LiqPrice: "2501.2", LiqLot: "1.5", PosLot: "0", Notional: "3751.8", OnOrderBook: true,
		Keeper: Addr(int(logIndex % 3)),
	}
}

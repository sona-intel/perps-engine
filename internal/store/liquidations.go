package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"perps/internal/perpl"
)

const MaxLiquidationLimit = 1000

type Liquidation struct {
	Block       uint64
	LogIndex    uint64
	Tx          perpl.Hash
	Time        time.Time
	PerpID      uint64
	AccountID   uint64
	Side        Side
	Mark        string
	LiqPrice    string
	LiqLot      string
	PosLot      string
	Notional    string
	OnOrderBook bool
	Keeper      perpl.Address
}

type LiquidationCursor struct {
	Block    uint64
	LogIndex uint64
}

func (c LiquidationCursor) String() string {
	return strconv.FormatUint(c.Block, 10) + "." + strconv.FormatUint(c.LogIndex, 10)
}

var errCursor = errors.New("liquidation cursor must look like block.log_index")

func ParseLiquidationCursor(s string) (LiquidationCursor, error) {
	block, index, ok := strings.Cut(s, ".")
	if !ok || !digits(block) || !digits(index) {
		return LiquidationCursor{}, errCursor
	}
	b, err := strconv.ParseUint(block, 10, 63)
	if err != nil {
		return LiquidationCursor{}, errCursor
	}
	i, err := strconv.ParseUint(index, 10, 31)
	if err != nil {
		return LiquidationCursor{}, errCursor
	}
	return LiquidationCursor{Block: b, LogIndex: i}, nil
}

func digits(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// A nil PerpID means every market and a nil After starts at the newest row
type LiquidationFilter struct {
	PerpID *uint64
	Limit  int
	After  *LiquidationCursor
}

// Next is nil on the last page
type LiquidationPage struct {
	Items []Liquidation
	Next  *LiquidationCursor
}

type DailyLiquidations struct {
	Day         time.Time
	Count       int64
	NotionalUSD string
}

type KeeperCount struct {
	Keeper       perpl.Address
	Liquidations int64
}

type LiquidationTotals struct {
	Count      int64
	FirstBlock uint64
	LastBlock  uint64
}

const upsertLiquidations = `
insert into liquidations (block, log_index, tx, ts, perp_id, account_id, side, mark, liq_price, liq_lot, pos_lot, notional,
    on_order_book, keeper)
select block, log_index, tx, ts, perp_id, account_id, side, mark::numeric, liq_price::numeric, liq_lot::numeric, pos_lot::numeric,
    notional::numeric, on_order_book, keeper
from unnest($1::bigint[], $2::integer[], $3::bytea[], $4::timestamptz[], $5::integer[], $6::bigint[], $7::text[], $8::text[],
    $9::text[], $10::text[], $11::text[], $12::text[], $13::boolean[], $14::bytea[])
    as t(block, log_index, tx, ts, perp_id, account_id, side, mark, liq_price, liq_lot, pos_lot, notional, on_order_book, keeper)
on conflict (block, log_index) do update set tx = excluded.tx, ts = excluded.ts, perp_id = excluded.perp_id,
    account_id = excluded.account_id, side = excluded.side, mark = excluded.mark, liq_price = excluded.liq_price,
    liq_lot = excluded.liq_lot, pos_lot = excluded.pos_lot, notional = excluded.notional,
    on_order_book = excluded.on_order_book, keeper = excluded.keeper`

func (s *Store) UpsertLiquidations(ctx context.Context, rows []Liquidation) error {
	rows = lastByKey(rows, func(l Liquidation) [2]uint64 { return [2]uint64{l.Block, l.LogIndex} })
	if len(rows) == 0 {
		return nil
	}
	var c colErr
	n := len(rows)
	block, account := make([]int64, n), make([]int64, n)
	logIndex, perp := make([]int32, n), make([]int32, n)
	tx, keeper := make([][]byte, n), make([][]byte, n)
	ts := make([]time.Time, n)
	side := make([]string, n)
	mark, liq, liqLot, posLot, notional := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	onBook := make([]bool, n)
	for i, r := range rows {
		block[i], logIndex[i], perp[i], account[i] = c.i64(r.Block, "block"), c.i32(r.LogIndex, "log index"), c.i32(r.PerpID, "perp id"), c.i64(r.AccountID, "account id")
		tx[i], keeper[i] = slices.Clone(r.Tx[:]), slices.Clone(r.Keeper[:])
		ts[i], side[i], onBook[i] = r.Time, string(r.Side), r.OnOrderBook
		mark[i], liq[i], liqLot[i], posLot[i], notional[i] = r.Mark, r.LiqPrice, r.LiqLot, r.PosLot, r.Notional
	}
	if c.err != nil {
		return fmt.Errorf("liquidations: %w", c.err)
	}
	_, err := s.Pool.Exec(ctx, upsertLiquidations, block, logIndex, tx, ts, perp, account, side, mark, liq, liqLot, posLot, notional, onBook, keeper)
	if err != nil {
		return fmt.Errorf("upsert liquidations: %w", err)
	}
	return nil
}

// One upsert statement cannot touch a row twice so the later row of a key wins
func lastByKey[T any, K comparable](rows []T, key func(T) K) []T {
	at := make(map[K]int, len(rows))
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		k := key(r)
		if i, seen := at[k]; seen {
			out[i] = r
			continue
		}
		at[k] = len(out)
		out = append(out, r)
	}
	return out
}

const selectLiquidations = `
select block, log_index, tx, ts, perp_id, account_id, side, trim_scale(mark)::text, trim_scale(liq_price)::text,
    trim_scale(liq_lot)::text, trim_scale(pos_lot)::text, trim_scale(notional)::text, on_order_book, keeper
from liquidations`

func (s *Store) Liquidations(ctx context.Context, f LiquidationFilter) (LiquidationPage, error) {
	if f.Limit < 1 || f.Limit > MaxLiquidationLimit {
		return LiquidationPage{}, fmt.Errorf("liquidation limit must be 1 to %d, got %d", MaxLiquidationLimit, f.Limit)
	}
	var where []string
	var args []any
	if f.PerpID != nil {
		perp, err := i64(*f.PerpID)
		if err != nil {
			return LiquidationPage{}, fmt.Errorf("perp id %w", err)
		}
		args = append(args, perp)
		where = append(where, "perp_id = $"+strconv.Itoa(len(args)))
	}
	if f.After != nil {
		block, err := i64(f.After.Block)
		if err != nil {
			return LiquidationPage{}, fmt.Errorf("cursor block %w", err)
		}
		index, err := i64(f.After.LogIndex)
		if err != nil {
			return LiquidationPage{}, fmt.Errorf("cursor log index %w", err)
		}
		args = append(args, block, index)
		where = append(where, "(block, log_index) < ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
	}
	query := selectLiquidations
	if len(where) > 0 {
		query += " where " + strings.Join(where, " and ")
	}
	args = append(args, f.Limit+1)
	query += " order by block desc, log_index desc limit $" + strconv.Itoa(len(args))
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return LiquidationPage{}, fmt.Errorf("liquidations: %w", err)
	}
	items, err := pgx.CollectRows(rows, scanLiquidation)
	if err != nil {
		return LiquidationPage{}, fmt.Errorf("liquidations: %w", err)
	}
	page := LiquidationPage{Items: items}
	if len(items) > f.Limit {
		page.Items = items[:f.Limit]
		last := page.Items[f.Limit-1]
		page.Next = &LiquidationCursor{Block: last.Block, LogIndex: last.LogIndex}
	}
	return page, nil
}

func scanLiquidation(row pgx.CollectableRow) (Liquidation, error) {
	var l Liquidation
	var tx, keeper []byte
	var side string
	err := row.Scan(&l.Block, &l.LogIndex, &tx, &l.Time, &l.PerpID, &l.AccountID, &side, &l.Mark, &l.LiqPrice, &l.LiqLot, &l.PosLot,
		&l.Notional, &l.OnOrderBook, &keeper)
	if err != nil {
		return Liquidation{}, err
	}
	if len(tx) != len(l.Tx) || len(keeper) != len(l.Keeper) {
		return Liquidation{}, errors.New("liquidation row has a malformed tx hash or keeper")
	}
	copy(l.Tx[:], tx)
	copy(l.Keeper[:], keeper)
	l.Side = Side(side)
	return l, nil
}

// Days without rows are absent
func (s *Store) LiquidationsDaily(ctx context.Context, perpID *uint64, since time.Time) ([]DailyLiquidations, error) {
	query := `select (ts at time zone 'UTC')::date, count(*), trim_scale(sum(notional))::text from liquidations where ts >= $1`
	args := []any{since.UTC().Truncate(24 * time.Hour)}
	if perpID != nil {
		perp, err := i64(*perpID)
		if err != nil {
			return nil, fmt.Errorf("perp id %w", err)
		}
		query += " and perp_id = $2"
		args = append(args, perp)
	}
	query += " group by 1 order by 1"
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("daily liquidations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (DailyLiquidations, error) {
		var d DailyLiquidations
		err := row.Scan(&d.Day, &d.Count, &d.NotionalUSD)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("daily liquidations: %w", err)
	}
	return out, nil
}

// A limit of zero or less means all
func (s *Store) KeeperCounts(ctx context.Context, limit int) ([]KeeperCount, error) {
	var lim *int
	if limit > 0 {
		lim = &limit
	}
	rows, err := s.Pool.Query(ctx, `select keeper, count(*) from liquidations group by keeper order by count(*) desc, keeper limit $1::bigint`, lim)
	if err != nil {
		return nil, fmt.Errorf("keeper counts: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (KeeperCount, error) {
		var k KeeperCount
		var addr []byte
		if err := row.Scan(&addr, &k.Liquidations); err != nil {
			return k, err
		}
		if len(addr) != len(k.Keeper) {
			return k, errors.New("keeper has a malformed address")
		}
		copy(k.Keeper[:], addr)
		return k, nil
	})
	if err != nil {
		return nil, fmt.Errorf("keeper counts: %w", err)
	}
	return out, nil
}

func (s *Store) CountLiquidations(ctx context.Context) (LiquidationTotals, error) {
	var t LiquidationTotals
	err := s.Pool.QueryRow(ctx, `select count(*), coalesce(min(block), 0), coalesce(max(block), 0) from liquidations`).Scan(&t.Count, &t.FirstBlock, &t.LastBlock)
	if err != nil {
		return LiquidationTotals{}, fmt.Errorf("count liquidations: %w", err)
	}
	return t, nil
}

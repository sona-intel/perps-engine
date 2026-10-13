package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Side string

const (
	SideLong  Side = "long"
	SideShort Side = "short"
)

// SCR fields are nil when the market has no bad debt
type MarketSnapshot struct {
	PerpID             uint64
	Block              uint64
	Time               time.Time
	Mark               string
	Oracle             string
	OracleTime         time.Time
	LongOI             string
	ShortOI            string
	LongNotional       string
	ShortNotional      string
	Positions          int
	Insurance          string
	FundingRatePct100k int16
	SCRM10             *string
	SCRP10             *string
}

type PositionSnapshot struct {
	Block      uint64
	Time       time.Time
	PerpID     uint64
	AccountID  uint64
	Side       Side
	Size       string
	Entry      string
	Deposit    string
	PremiumPnl string
	DeltaPnl   string
	LiqPrice   string
	BkPrice    string
}

type FundingPoint struct {
	Block              uint64
	Time               time.Time
	Mark               string
	Oracle             string
	FundingRatePct100k int16
}

const insertMarketSnapshots = `
insert into market_snapshots (perp_id, block, ts, mark, oracle, oracle_ts, long_oi, short_oi, long_notional, short_notional,
    positions, insurance, funding_rate_pct100k, scr_m10, scr_p10)
select perp_id, block, ts, mark::numeric, oracle::numeric, oracle_ts, long_oi::numeric, short_oi::numeric, long_notional::numeric,
    short_notional::numeric, positions, insurance::numeric, funding_rate_pct100k, scr_m10::numeric, scr_p10::numeric
from unnest($1::integer[], $2::bigint[], $3::timestamptz[], $4::text[], $5::text[], $6::timestamptz[], $7::text[], $8::text[],
    $9::text[], $10::text[], $11::integer[], $12::text[], $13::smallint[], $14::text[], $15::text[])
    as t(perp_id, block, ts, mark, oracle, oracle_ts, long_oi, short_oi, long_notional, short_notional, positions, insurance,
    funding_rate_pct100k, scr_m10, scr_p10)
on conflict (perp_id, block) do nothing`

// An existing (perp_id, block) keeps its first write
func (s *Store) InsertMarketSnapshots(ctx context.Context, rows []MarketSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	var c colErr
	n := len(rows)
	perp, block, positions := make([]int32, n), make([]int64, n), make([]int32, n)
	ts, oracleTS := make([]time.Time, n), make([]time.Time, n)
	mark, oracle, longOI, shortOI := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	longN, shortN, insurance := make([]string, n), make([]string, n), make([]string, n)
	rate := make([]int16, n)
	m10, p10 := make([]*string, n), make([]*string, n)
	for i, r := range rows {
		perp[i], block[i], positions[i] = c.i32(r.PerpID, "perp id"), c.i64(r.Block, "block"), c.count(r.Positions, "positions")
		ts[i], oracleTS[i] = r.Time, r.OracleTime
		mark[i], oracle[i], longOI[i], shortOI[i] = r.Mark, r.Oracle, r.LongOI, r.ShortOI
		longN[i], shortN[i], insurance[i] = r.LongNotional, r.ShortNotional, r.Insurance
		rate[i], m10[i], p10[i] = r.FundingRatePct100k, r.SCRM10, r.SCRP10
	}
	if c.err != nil {
		return fmt.Errorf("market snapshots: %w", c.err)
	}
	_, err := s.Pool.Exec(ctx, insertMarketSnapshots, perp, block, ts, mark, oracle, oracleTS, longOI, shortOI, longN, shortN, positions,
		insurance, rate, m10, p10)
	if err != nil {
		return fmt.Errorf("insert market snapshots: %w", err)
	}
	return nil
}

const insertPositionSnapshots = `
insert into position_snapshots (block, ts, perp_id, account_id, side, size, entry, deposit, premium_pnl, delta_pnl, liq_price, bk_price)
select block, ts, perp_id, account_id, side, size::numeric, entry::numeric, deposit::numeric, premium_pnl::numeric, delta_pnl::numeric,
    liq_price::numeric, bk_price::numeric
from unnest($1::bigint[], $2::timestamptz[], $3::integer[], $4::bigint[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[],
    $10::text[], $11::text[], $12::text[])
    as t(block, ts, perp_id, account_id, side, size, entry, deposit, premium_pnl, delta_pnl, liq_price, bk_price)
on conflict (block, perp_id, account_id) do nothing`

func (s *Store) InsertPositionSnapshots(ctx context.Context, rows []PositionSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	var c colErr
	n := len(rows)
	block, account := make([]int64, n), make([]int64, n)
	perp := make([]int32, n)
	ts := make([]time.Time, n)
	side := make([]string, n)
	size, entry, deposit, premium := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	delta, liq, bk := make([]string, n), make([]string, n), make([]string, n)
	for i, r := range rows {
		block[i], perp[i], account[i] = c.i64(r.Block, "block"), c.i32(r.PerpID, "perp id"), c.i64(r.AccountID, "account id")
		ts[i], side[i] = r.Time, string(r.Side)
		size[i], entry[i], deposit[i], premium[i] = r.Size, r.Entry, r.Deposit, r.PremiumPnl
		delta[i], liq[i], bk[i] = r.DeltaPnl, r.LiqPrice, r.BkPrice
	}
	if c.err != nil {
		return fmt.Errorf("position snapshots: %w", c.err)
	}
	_, err := s.Pool.Exec(ctx, insertPositionSnapshots, block, ts, perp, account, side, size, entry, deposit, premium, delta, liq, bk)
	if err != nil {
		return fmt.Errorf("insert position snapshots: %w", err)
	}
	return nil
}

func (s *Store) PrunePositionSnapshots(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from position_snapshots where ts < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune position snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) PruneMarketSnapshots(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from market_snapshots where ts < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune market snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}

const fundingAll = `
select block, ts, trim_scale(mark)::text, trim_scale(oracle)::text, funding_rate_pct100k
from market_snapshots where perp_id = $1 and ts >= $2 order by block`

const fundingStepped = `
select block, ts, mark, oracle, rate from (
    select distinct on (floor(extract(epoch from ts)::float8 / $3::float8))
        block, ts, trim_scale(mark)::text as mark, trim_scale(oracle)::text as oracle, funding_rate_pct100k as rate
    from market_snapshots where perp_id = $1 and ts >= $2
    order by floor(extract(epoch from ts)::float8 / $3::float8), block desc
) x order by block`

// A step keeps only the last snapshot of each step wide window
func (s *Store) FundingSeries(ctx context.Context, perpID uint64, since time.Time, step time.Duration) ([]FundingPoint, error) {
	if step < 0 {
		return nil, errors.New("funding step must not be negative")
	}
	perp, err := i64(perpID)
	if err != nil {
		return nil, fmt.Errorf("perp id %w", err)
	}
	query, args := fundingAll, []any{perp, since}
	if step > 0 {
		query, args = fundingStepped, append(args, step.Seconds())
	}
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("funding series: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FundingPoint, error) {
		var p FundingPoint
		err := row.Scan(&p.Block, &p.Time, &p.Mark, &p.Oracle, &p.FundingRatePct100k)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("funding series: %w", err)
	}
	return out, nil
}

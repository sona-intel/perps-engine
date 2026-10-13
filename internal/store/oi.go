package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// OIPoint is the open interest of one market at one time, long and short are equal so one side is stored
type OIPoint struct {
	Market          uint64
	Time            time.Time
	OpenInterestUSD string
	Positions       int
}

const insertMarketOI = `
insert into market_oi (market, t, open_interest_usd, positions)
select market, t, open_interest_usd::numeric, positions
from unnest($1::integer[], $2::timestamptz[], $3::text[], $4::integer[]) as x(market, t, open_interest_usd, positions)
on conflict (market, t) do nothing`

// An existing (market, t) keeps its first write
func (s *Store) InsertMarketOI(ctx context.Context, rows []OIPoint) error {
	if len(rows) == 0 {
		return nil
	}
	var c colErr
	n := len(rows)
	market, positions := make([]int32, n), make([]int32, n)
	ts := make([]time.Time, n)
	usd := make([]string, n)
	for i, r := range rows {
		market[i], positions[i] = c.i32(r.Market, "market"), c.count(r.Positions, "positions")
		ts[i], usd[i] = r.Time, r.OpenInterestUSD
	}
	if c.err != nil {
		return fmt.Errorf("market oi: %w", c.err)
	}
	if _, err := s.Pool.Exec(ctx, insertMarketOI, market, ts, usd, positions); err != nil {
		return fmt.Errorf("insert market oi: %w", err)
	}
	return nil
}

func (s *Store) PruneMarketOI(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from market_oi where t < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("prune market oi: %w", err)
	}
	return tag.RowsAffected(), nil
}

const oiAll = `
select market, t, trim_scale(open_interest_usd)::text, positions from market_oi where market = $1 and t >= $2 order by t`

const oiStepped = `
select market, t, oi, positions from (
    select distinct on (floor(extract(epoch from t)::float8 / $3::float8))
        market, t, trim_scale(open_interest_usd)::text as oi, positions
    from market_oi where market = $1 and t >= $2
    order by floor(extract(epoch from t)::float8 / $3::float8), t desc
) x order by t`

// A step keeps only the last point of each step wide window
func (s *Store) OpenInterestSeries(ctx context.Context, market uint64, since time.Time, step time.Duration) ([]OIPoint, error) {
	if step < 0 {
		return nil, errors.New("open interest step must not be negative")
	}
	id, err := i64(market)
	if err != nil {
		return nil, fmt.Errorf("market %w", err)
	}
	query, args := oiAll, []any{id, since}
	if step > 0 {
		query, args = oiStepped, append(args, step.Seconds())
	}
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("open interest series: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (OIPoint, error) {
		var p OIPoint
		var m int32
		err := row.Scan(&m, &p.Time, &p.OpenInterestUSD, &p.Positions)
		p.Market = uint64(m)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("open interest series: %w", err)
	}
	return out, nil
}

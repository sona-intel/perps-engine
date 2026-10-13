package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	CursorBackfill = "backfill"
	// CursorBackfillTarget is the tail position of the first start, where the backfill must end
	CursorBackfillTarget = "backfill_target"
	CursorTail           = "tail"
)

func (s *Store) Cursor(ctx context.Context, name string) (block uint64, found bool, err error) {
	err = s.Pool.QueryRow(ctx, `select block from cursor where name = $1`, name).Scan(&block)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read cursor %s: %w", name, err)
	}
	return block, true, nil
}

func (s *Store) SetCursor(ctx context.Context, name string, block uint64) error {
	b, err := i64(block)
	if err != nil {
		return fmt.Errorf("cursor %s block %w", name, err)
	}
	_, err = s.Pool.Exec(ctx, `insert into cursor (name, block) values ($1, $2) on conflict (name) do update set block = excluded.block`, name, b)
	if err != nil {
		return fmt.Errorf("write cursor %s: %w", name, err)
	}
	return nil
}

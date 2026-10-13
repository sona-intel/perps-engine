package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"perps/internal/perpl"
)

type Account struct {
	ID      uint64
	Address perpl.Address
}

func (s *Store) UpsertAccounts(ctx context.Context, rows []Account) error {
	rows = lastByKey(rows, func(a Account) uint64 { return a.ID })
	if len(rows) == 0 {
		return nil
	}
	var c colErr
	ids, addrs := make([]int64, len(rows)), make([][]byte, len(rows))
	for i, r := range rows {
		ids[i], addrs[i] = c.i64(r.ID, "account id"), slices.Clone(r.Address[:])
	}
	if c.err != nil {
		return fmt.Errorf("accounts: %w", c.err)
	}
	_, err := s.Pool.Exec(ctx, `
		insert into accounts (account_id, address) select * from unnest($1::bigint[], $2::bytea[])
		on conflict (account_id) do update set address = excluded.address`, ids, addrs)
	if err != nil {
		return fmt.Errorf("upsert accounts: %w", err)
	}
	return nil
}

func (s *Store) AccountAddresses(ctx context.Context, ids []uint64) (map[uint64]perpl.Address, error) {
	var c colErr
	args := make([]int64, len(ids))
	for i, id := range ids {
		args[i] = c.i64(id, "account id")
	}
	if c.err != nil {
		return nil, fmt.Errorf("accounts: %w", c.err)
	}
	rows, err := s.Pool.Query(ctx, `select account_id, address from accounts where account_id = any($1::bigint[])`, args)
	if err != nil {
		return nil, fmt.Errorf("account addresses: %w", err)
	}
	defer rows.Close()
	out := make(map[uint64]perpl.Address, len(ids))
	for rows.Next() {
		var id uint64
		var addr []byte
		if err := rows.Scan(&id, &addr); err != nil {
			return nil, fmt.Errorf("account addresses: %w", err)
		}
		var a perpl.Address
		if len(addr) != len(a) {
			return nil, errors.New("account has a malformed address")
		}
		copy(a[:], addr)
		out[id] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("account addresses: %w", err)
	}
	return out, nil
}

// UnresolvedLiquidationAccounts lists the ids of liquidated accounts that have no address row yet, newest liquidation first
func (s *Store) UnresolvedLiquidationAccounts(ctx context.Context, limit int) ([]uint64, error) {
	rows, err := s.Pool.Query(ctx, `
		select l.account_id from liquidations l
		where not exists (select 1 from accounts a where a.account_id = l.account_id)
		group by l.account_id order by max(l.block) desc limit $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("unresolved liquidation accounts: %w", err)
	}
	defer rows.Close()
	var out []uint64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("unresolved liquidation accounts: %w", err)
		}
		out = append(out, uint64(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unresolved liquidation accounts: %w", err)
	}
	return out, nil
}

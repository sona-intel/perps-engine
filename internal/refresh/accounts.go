package refresh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/rpc"
	"perps/internal/store"
)

type accountEntry struct {
	account perpl.Account
	found   bool
	at      time.Time
}

// lookupSlots bounds the chain reads that requests cause, apart from the slots the refresh loop uses
const lookupSlots = 2

type flight struct {
	done chan struct{}
	acct perpl.Account
	err  error
}

// ResolveAccount looks an address up with getAccountByAddr and caches the answer, a missing account included, for five minutes
func (r *Runner) ResolveAccount(ctx context.Context, addr perpl.Address) (perpl.Account, error) {
	snap := r.latest.Load()
	if snap == nil {
		return perpl.Account{}, ErrNotReady
	}
	now := r.now()
	for {
		r.accountMu.Lock()
		e, ok := r.accountCache[addr]
		if ok && now.Sub(e.at) < accountCacheTTL {
			r.accountMu.Unlock()
			if !e.found {
				return perpl.Account{}, ErrAccountNotFound
			}
			return e.account, nil
		}
		if f, ok := r.flights[addr]; ok {
			r.accountMu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return perpl.Account{}, ctx.Err()
			}
			// the caller that led the lookup may have given up while this one still has time
			if f.err != nil && ctx.Err() == nil && (errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded)) {
				continue
			}
			return f.acct, f.err
		}
		f := &flight{done: make(chan struct{})}
		r.flights[addr] = f
		r.accountMu.Unlock()

		f.acct, f.err = r.lookupAccount(ctx, snap.Block, addr, now)
		r.accountMu.Lock()
		delete(r.flights, addr)
		r.accountMu.Unlock()
		close(f.done)
		return f.acct, f.err
	}
}

func (r *Runner) lookupAccount(ctx context.Context, block uint64, addr perpl.Address, now time.Time) (perpl.Account, error) {
	out, err := r.lookupCall(ctx, perpl.EncodeGetAccountByAddr(addr), block)
	if err != nil && !rpc.IsRevert(err) {
		return perpl.Account{}, fmt.Errorf("account lookup: %w", err)
	}
	entry := accountEntry{at: now}
	if err == nil {
		acct, derr := perpl.DecodeAccount(out)
		if derr != nil {
			return perpl.Account{}, fmt.Errorf("account lookup: %w", derr)
		}
		entry.account, entry.found = acct, acct.AccountID != 0
	}
	r.rememberAccount(addr, entry)
	if !entry.found {
		return perpl.Account{}, ErrAccountNotFound
	}
	r.saveAccounts(ctx, []store.Account{{ID: entry.account.AccountID, Address: addr}})
	return entry.account, nil
}

func (r *Runner) lookupCall(ctx context.Context, data []byte, block uint64) ([]byte, error) {
	select {
	case r.lookupSem <- struct{}{}:
		defer func() { <-r.lookupSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return r.chain.Call(ctx, r.exchange, data, block)
}

func (r *Runner) rememberAccount(addr perpl.Address, e accountEntry) {
	r.accountMu.Lock()
	defer r.accountMu.Unlock()
	if len(r.accountCache) >= accountCacheMax {
		for k, v := range r.accountCache {
			if e.at.Sub(v.at) >= accountCacheTTL {
				delete(r.accountCache, k)
			}
		}
		if len(r.accountCache) >= accountCacheMax {
			clear(r.accountCache)
		}
	}
	r.accountCache[addr] = e
}

// saveAccounts upserts addresses and remembers the ids so they are not looked up again
func (r *Runner) saveAccounts(ctx context.Context, rows []store.Account) {
	if len(rows) == 0 {
		return
	}
	if err := r.store.UpsertAccounts(ctx, rows); err != nil {
		r.countError("accounts", fmt.Errorf("store accounts: %w", err))
		return
	}
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	for _, a := range rows {
		r.known[a.ID] = struct{}{}
	}
}

func (r *Runner) runAccounts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-r.accountsCh:
			r.resolveTop(ctx, s)
		}
	}
}

// resolveTop stores the addresses of the accounts the concentration view lists
func (r *Runner) resolveTop(ctx context.Context, snap *Snapshot) {
	conc := risk.Concentration(snap.Books())
	var want []uint64
	add := func(shares []risk.AccountShare) {
		for _, a := range shares {
			if !slices.Contains(want, a.AccountID) {
				want = append(want, a.AccountID)
			}
		}
	}
	add(conc.Overall.TopAccounts)
	for _, m := range conc.Markets {
		add(m.TopAccounts)
	}

	want = r.unresolved(ctx, want)
	want = r.addLiquidated(ctx, want)
	if len(want) > maxTopLookups {
		want = want[:maxTopLookups]
	}
	if len(want) == 0 {
		return
	}

	var (
		mu   sync.Mutex
		rows []store.Account
	)
	g := newGroup(ctx)
	for _, id := range want {
		g.Go(func(ctx context.Context) error {
			out, err := r.call(ctx, perpl.EncodeGetAccountByID(id), snap.Block)
			if err != nil {
				if rpc.IsRevert(err) {
					r.knownMu.Lock()
					r.missing[id] = struct{}{}
					r.knownMu.Unlock()
				} else if ctx.Err() == nil {
					r.countError("accounts", fmt.Errorf("account %d: %w", id, err))
				}
				return nil
			}
			acct, err := perpl.DecodeAccount(out)
			if err != nil {
				r.countError("accounts", fmt.Errorf("account %d: %w", id, err))
				return nil
			}
			mu.Lock()
			rows = append(rows, store.Account{ID: id, Address: acct.Address})
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	r.saveAccounts(ctx, rows)
}

// unresolved drops the ids whose address is known or stored, and the ids the exchange has no account for
func (r *Runner) unresolved(ctx context.Context, want []uint64) []uint64 {
	r.knownMu.Lock()
	want = slices.DeleteFunc(want, func(id uint64) bool {
		_, known := r.known[id]
		_, gone := r.missing[id]
		return known || gone
	})
	r.knownMu.Unlock()
	if len(want) == 0 {
		return want
	}
	stored, err := r.store.AccountAddresses(ctx, want)
	if err != nil {
		r.countError("accounts", fmt.Errorf("read accounts: %w", err))
		return want
	}
	r.knownMu.Lock()
	for id := range stored {
		r.known[id] = struct{}{}
	}
	r.knownMu.Unlock()
	return slices.DeleteFunc(want, func(id uint64) bool { _, ok := stored[id]; return ok })
}

// addLiquidated appends the liquidated accounts that have no stored address, newest liquidation first
func (r *Runner) addLiquidated(ctx context.Context, want []uint64) []uint64 {
	room := maxTopLookups - len(want)
	if room <= 0 {
		return want
	}
	r.knownMu.Lock()
	skip := len(r.missing)
	r.knownMu.Unlock()
	ids, err := r.store.UnresolvedLiquidationAccounts(ctx, room+skip)
	if err != nil {
		r.countError("accounts", fmt.Errorf("read liquidated accounts: %w", err))
		return want
	}
	r.knownMu.Lock()
	defer r.knownMu.Unlock()
	for _, id := range ids {
		if _, gone := r.missing[id]; !gone && !slices.Contains(want, id) {
			want = append(want, id)
		}
	}
	return want
}

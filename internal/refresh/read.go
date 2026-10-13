package refresh

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sync"
	"time"

	"perps/internal/perpl"
	"perps/internal/rpc"
)

type slowData struct {
	at       time.Time
	exchange perpl.ExchangeInfo
	margin   map[uint64]perpl.MarginFractions
	liq      map[uint64]perpl.LiquidationInfo
}

func (s *slowData) covers(ids []uint64) bool {
	for _, id := range ids {
		if _, ok := s.margin[id]; !ok {
			return false
		}
		if _, ok := s.liq[id]; !ok {
			return false
		}
	}
	return true
}

// refresh runs one cycle and keeps the previous snapshot when it fails
func (r *Runner) refresh(ctx context.Context) {
	start := r.now()
	err := r.refreshOnce(ctx)
	r.m.duration.Observe(r.now().Sub(start).Seconds())
	if err != nil && ctx.Err() == nil {
		r.refreshErrors.Add(1)
		r.countError("refresh", err)
	}
}

func (r *Runner) refreshOnce(ctx context.Context) error {
	head, err := r.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("block number: %w", err)
	}
	if head < pinDepth {
		return fmt.Errorf("head %d is below the pin depth", head)
	}
	block := head - pinDepth
	if prev := r.latest.Load(); prev != nil && block <= prev.Block {
		return nil
	}

	var (
		header rpc.Block
		slot   perpl.Hash
		ids    []uint64
		reads  map[uint64]*marketRead
	)
	g := newGroup(ctx)
	g.Go(func(ctx context.Context) (err error) {
		header, err = limited(ctx, r, func() (rpc.Block, error) { return r.chain.BlockByNumber(ctx, block) })
		return wrap("block header", err)
	})
	g.Go(func(ctx context.Context) (err error) {
		slot, err = limited(ctx, r, func() (perpl.Hash, error) {
			return r.chain.StorageAt(ctx, r.exchange, perpl.ImplementationSlot, block)
		})
		return wrap("implementation slot", err)
	})
	g.Go(func(ctx context.Context) (err error) {
		if r.cfg.Multicall {
			ids, reads, err = r.readFirstRound(ctx, block)
			return err
		}
		out, err := r.call(ctx, perpl.EncodeGetPerpetualExistsBitmap(), block)
		if err != nil {
			return wrap("perp bitmap", err)
		}
		ids, err = perpl.DecodeExistsBitmap(out)
		return wrap("perp bitmap", err)
	})
	if err := g.Wait(); err != nil {
		return err
	}
	impl, err := perpl.AddressFromWord(slot[:])
	if err != nil {
		return fmt.Errorf("implementation slot: %w", err)
	}

	slow, err := r.slowFor(ctx, block, ids)
	if err != nil {
		return err
	}
	var views []*MarketView
	if r.cfg.Multicall {
		if err = r.readLaterPages(ctx, block, reads); err == nil {
			views, err = r.buildBatched(ids, reads, slow, header.Time)
		}
	} else {
		views, err = r.readMarkets(ctx, block, ids, slow, header.Time)
	}
	if err != nil {
		return err
	}

	snap := newSnapshot(views, r.cfg.Shocks)
	snap.Block = block
	snap.Time = header.Time
	snap.TakenAt = r.now()
	snap.Exchange = slow.exchange
	snap.Implementation = impl
	snap.ImplementationChanged = r.implementationChanged(impl)
	snap.PaidRPCCalls = r.chain.PaidCalls()

	n := r.refreshes.Add(1)
	r.latest.Store(snap)
	r.m.positions.Set(float64(snap.Positions))
	select {
	case r.accountsCh <- snap:
	default:
	}
	r.warnOnPaid(snap.PaidRPCCalls)
	r.persist(ctx, snap, n-1)
	return nil
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (r *Runner) implementationChanged(impl perpl.Address) bool {
	if r.expectedImpl != nil {
		return impl != *r.expectedImpl
	}
	if r.baselineImpl == nil {
		r.baselineImpl = &impl
	}
	return impl != *r.baselineImpl
}

func (r *Runner) warnOnPaid(paid int64) {
	if paid > r.lastPaid {
		r.log.Warn("paid rpc calls", "total", paid, "new", paid-r.lastPaid)
	}
	r.lastPaid = paid
}

/* Slow data */

func (r *Runner) slowFor(ctx context.Context, block uint64, ids []uint64) (*slowData, error) {
	if r.slow != nil && r.now().Sub(r.slow.at) < r.cfg.SlowRefreshInterval && r.slow.covers(ids) {
		return r.slow, nil
	}
	fresh, err := r.readSlow(ctx, block, ids)
	if err == nil {
		r.slow = fresh
		return fresh, nil
	}
	if r.slow == nil || !r.slow.covers(ids) {
		return nil, fmt.Errorf("slow data: %w", err)
	}
	r.countError("slow", err)
	return r.slow, nil
}

func (r *Runner) readSlow(ctx context.Context, block uint64, ids []uint64) (*slowData, error) {
	if r.cfg.Multicall {
		return r.readSlowBatched(ctx, block, ids)
	}
	sd := &slowData{
		at:     r.now(),
		margin: make(map[uint64]perpl.MarginFractions, len(ids)),
		liq:    make(map[uint64]perpl.LiquidationInfo, len(ids)),
	}
	var mu sync.Mutex
	g := newGroup(ctx)
	g.Go(func(ctx context.Context) error {
		out, err := r.call(ctx, perpl.EncodeGetExchangeInfo(), block)
		if err != nil {
			return wrap("exchange info", err)
		}
		ei, err := perpl.DecodeExchangeInfo(out)
		if err != nil {
			return wrap("exchange info", err)
		}
		out, err = r.call(ctx, perpl.EncodeGetFundingInterval(), block)
		if err != nil {
			return wrap("funding interval", err)
		}
		if ei.FundingInterval, err = perpl.DecodeFundingInterval(out); err != nil {
			return wrap("funding interval", err)
		}
		mu.Lock()
		sd.exchange = ei
		mu.Unlock()
		return nil
	})
	for _, id := range ids {
		g.Go(func(ctx context.Context) error {
			data, err := perpl.EncodeGetMarginFractions(id, new(big.Int))
			if err != nil {
				return err
			}
			out, err := r.call(ctx, data, block)
			if err != nil {
				return wrap(fmt.Sprintf("margin fractions %d", id), err)
			}
			mf, err := perpl.DecodeMarginFractions(out)
			if err != nil {
				return wrap(fmt.Sprintf("margin fractions %d", id), err)
			}
			mu.Lock()
			sd.margin[id] = mf
			mu.Unlock()
			return nil
		})
		g.Go(func(ctx context.Context) error {
			out, err := r.call(ctx, perpl.EncodeGetLiquidationInfo(id), block)
			if err != nil {
				return wrap(fmt.Sprintf("liquidation info %d", id), err)
			}
			li, err := perpl.DecodeLiquidationInfo(out)
			if err != nil {
				return wrap(fmt.Sprintf("liquidation info %d", id), err)
			}
			mu.Lock()
			sd.liq[id] = li
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return sd, nil
}

/* Markets */

func (r *Runner) readMarkets(ctx context.Context, block uint64, ids []uint64, slow *slowData, blockTime time.Time) ([]*MarketView, error) {
	ordered := slices.Clone(ids)
	slices.Sort(ordered)
	views := make([]*MarketView, len(ordered))
	g := newGroup(ctx)
	for i, id := range ordered {
		g.Go(func(ctx context.Context) error {
			m, err := r.readMarket(ctx, block, id, slow)
			if err != nil {
				return err
			}
			pages, err := r.readPositions(ctx, block, id)
			if err != nil {
				return err
			}
			r.rememberDecimals(m)
			views[i], err = r.buildView(m, pages, slow.exchange, blockTime)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return views, nil
}

func (r *Runner) readMarket(ctx context.Context, block, id uint64, slow *slowData) (perpl.Market, error) {
	out, err := r.call(ctx, perpl.EncodeGetPerpetualInfoV2(id), block)
	if err != nil {
		return perpl.Market{}, wrap(fmt.Sprintf("perp info %d", id), err)
	}
	m, err := perpl.DecodeMarket(out)
	if err != nil {
		return perpl.Market{}, wrap(fmt.Sprintf("perp info %d", id), err)
	}
	return withSlow(m, id, slow)
}

// withSlow adds the fields that come from the slow reads
func withSlow(m perpl.Market, id uint64, slow *slowData) (perpl.Market, error) {
	mf, ok := slow.margin[id]
	if !ok {
		return perpl.Market{}, fmt.Errorf("no margin fractions for market %d", id)
	}
	m.PerpID = id
	m.MaintMarginFracHdths = mf.PerpMaintMarginFracHdths
	m.InitMarginFracHdths = mf.PerpInitMarginFracHdths
	m.Liquidation = slow.liq[id]
	return m, nil
}

// readPositions follows the linked list one page at a time because each page starts where the last one ended
func (r *Runner) readPositions(ctx context.Context, block, perp uint64) ([]perpl.Position, error) {
	size := uint64(r.cfg.PositionPageSize)
	w := newWalker(perp)
	for range maxPositionPage {
		out, err := r.call(ctx, perpl.EncodeGetPositionsV2(perp, w.next, size), block)
		if err != nil {
			return nil, wrap(fmt.Sprintf("positions %d from %d", perp, w.next), err)
		}
		if err := w.feed(out); err != nil {
			return nil, err
		}
		if w.done {
			return w.all, nil
		}
	}
	return nil, errors.New("positions list is too long")
}

package refresh

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"perps/internal/perpl"
	"perps/internal/rpc"
)

// maxMulticallCalls keeps one batch well inside the response limit and the gas cap of a node
const maxMulticallCalls = 64

// unwrap turns a failed sub-call into the revert error a plain eth_call would have returned
func unwrap(res perpl.Result3) ([]byte, error) {
	if res.Success {
		return res.Data, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeRevert, Message: "execution reverted", Data: "0x" + hex.EncodeToString(res.Data)}
}

// multicall sends the calls through Multicall3 in chunks that run on separate slots and returns the results in call order
func (r *Runner) multicall(ctx context.Context, calls []perpl.Call3, block uint64) ([]perpl.Result3, error) {
	results := make([]perpl.Result3, len(calls))
	g := newGroup(ctx)
	for from := 0; from < len(calls); from += r.batchLimit {
		chunk := calls[from:min(from+r.batchLimit, len(calls))]
		g.Go(func(ctx context.Context) error {
			got, err := limited(ctx, r, func() ([]perpl.Result3, error) {
				out, err := r.chain.Call(ctx, perpl.Multicall3, perpl.EncodeAggregate3(chunk), block)
				if err != nil {
					return nil, err
				}
				return perpl.DecodeAggregate3(out, len(chunk))
			})
			if err != nil {
				return err
			}
			copy(results[from:], got)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *Runner) exchangeCall(data []byte, allowFailure bool) perpl.Call3 {
	return perpl.Call3{Target: r.exchange, AllowFailure: allowFailure, Data: data}
}

/* Position walk */

// walker follows the linked list of one market, whatever delivers its pages
type walker struct {
	perp uint64
	all  []perpl.Position
	seen map[uint64]struct{}
	next uint64
	done bool
}

func newWalker(perp uint64) walker { return walker{perp: perp, seen: map[uint64]struct{}{}} }

// feed takes the page that starts at w.next
func (w *walker) feed(out []byte) error {
	page, err := perpl.DecodePositionPage(out)
	if err != nil {
		return wrap(fmt.Sprintf("positions %d from %d", w.perp, w.next), err)
	}
	for i := range page.Positions {
		page.Positions[i].PerpID = w.perp
	}
	w.all = append(w.all, page.Positions...)
	if len(page.Positions) == 0 {
		w.done = true
		return nil
	}
	next := page.Positions[len(page.Positions)-1].NextNodeID
	if next == 0 {
		w.done = true
		return nil
	}
	if _, dup := w.seen[next]; dup {
		return fmt.Errorf("positions %d loop at node %d", w.perp, next)
	}
	w.seen[next] = struct{}{}
	w.next = next
	return nil
}

/* Markets */

type marketRead struct {
	market perpl.Market
	walk   walker
}

// marketCalls asks for the info and the first page of each market
func (r *Runner) marketCalls(ids []uint64, allowFailure bool) []perpl.Call3 {
	size := uint64(r.cfg.PositionPageSize)
	calls := make([]perpl.Call3, 0, 2*len(ids))
	for _, id := range ids {
		calls = append(calls,
			r.exchangeCall(perpl.EncodeGetPerpetualInfoV2(id), allowFailure),
			r.exchangeCall(perpl.EncodeGetPositionsV2(id, 0, size), allowFailure),
		)
	}
	return calls
}

func decodeMarketRead(id uint64, info, page perpl.Result3) (*marketRead, error) {
	out, err := unwrap(info)
	if err != nil {
		return nil, wrap(fmt.Sprintf("perp info %d", id), err)
	}
	m, err := perpl.DecodeMarket(out)
	if err != nil {
		return nil, wrap(fmt.Sprintf("perp info %d", id), err)
	}
	mr := &marketRead{market: m, walk: newWalker(id)}
	if out, err = unwrap(page); err != nil {
		return nil, wrap(fmt.Sprintf("positions %d from 0", id), err)
	}
	if err := mr.walk.feed(out); err != nil {
		return nil, err
	}
	return mr, nil
}

func (r *Runner) knownIDs() []uint64 {
	snap := r.latest.Load()
	if snap == nil {
		return nil
	}
	ids := make([]uint64, len(snap.Markets))
	for i, mv := range snap.Markets {
		ids[i] = mv.Market.PerpID
	}
	return ids
}

// readFirstRound batches the bitmap with the markets of the last snapshot, a market the bitmap adds costs one more multicall and a dropped one is ignored
func (r *Runner) readFirstRound(ctx context.Context, block uint64) ([]uint64, map[uint64]*marketRead, error) {
	guess := r.knownIDs()
	calls := append([]perpl.Call3{r.exchangeCall(perpl.EncodeGetPerpetualExistsBitmap(), false)}, r.marketCalls(guess, true)...)
	res, err := r.multicall(ctx, calls, block)
	if err != nil {
		return nil, nil, wrap("market multicall", err)
	}
	out, err := unwrap(res[0])
	if err != nil {
		return nil, nil, wrap("perp bitmap", err)
	}
	ids, err := perpl.DecodeExistsBitmap(out)
	if err != nil {
		return nil, nil, wrap("perp bitmap", err)
	}
	reads := make(map[uint64]*marketRead, len(ids))
	for i, id := range guess {
		if !slices.Contains(ids, id) {
			continue
		}
		if reads[id], err = decodeMarketRead(id, res[1+2*i], res[2+2*i]); err != nil {
			return nil, nil, err
		}
	}
	missing := slices.DeleteFunc(slices.Clone(ids), func(id uint64) bool { return reads[id] != nil })
	if len(missing) == 0 {
		return ids, reads, nil
	}
	if res, err = r.multicall(ctx, r.marketCalls(missing, false), block); err != nil {
		return nil, nil, wrap("market multicall", err)
	}
	for i, id := range missing {
		if reads[id], err = decodeMarketRead(id, res[2*i], res[2*i+1]); err != nil {
			return nil, nil, err
		}
	}
	return ids, reads, nil
}

// readLaterPages asks one multicall per round for the next page of every market whose list goes on
func (r *Runner) readLaterPages(ctx context.Context, block uint64, reads map[uint64]*marketRead) error {
	size := uint64(r.cfg.PositionPageSize)
	for range maxPositionPage {
		var pending []uint64
		for id, mr := range reads {
			if !mr.walk.done {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		slices.Sort(pending)
		calls := make([]perpl.Call3, len(pending))
		for i, id := range pending {
			calls[i] = r.exchangeCall(perpl.EncodeGetPositionsV2(id, reads[id].walk.next, size), false)
		}
		res, err := r.multicall(ctx, calls, block)
		if err != nil {
			return wrap("positions multicall", err)
		}
		for i, id := range pending {
			w := &reads[id].walk
			out, err := unwrap(res[i])
			if err != nil {
				return wrap(fmt.Sprintf("positions %d from %d", id, w.next), err)
			}
			if err := w.feed(out); err != nil {
				return err
			}
		}
	}
	return errors.New("positions list is too long")
}

func (r *Runner) buildBatched(ids []uint64, reads map[uint64]*marketRead, slow *slowData, blockTime time.Time) ([]*MarketView, error) {
	ordered := slices.Sorted(slices.Values(ids))
	views := make([]*MarketView, len(ordered))
	for i, id := range ordered {
		mr := reads[id]
		m, err := withSlow(mr.market, id, slow)
		if err != nil {
			return nil, err
		}
		r.rememberDecimals(m)
		if views[i], err = r.buildView(m, mr.walk.all, slow.exchange, blockTime); err != nil {
			return nil, err
		}
	}
	return views, nil
}

/* Slow data */

func (r *Runner) readSlowBatched(ctx context.Context, block uint64, ids []uint64) (*slowData, error) {
	calls := []perpl.Call3{
		r.exchangeCall(perpl.EncodeGetExchangeInfo(), false),
		r.exchangeCall(perpl.EncodeGetFundingInterval(), false),
	}
	for _, id := range ids {
		margin, err := perpl.EncodeGetMarginFractions(id, new(big.Int))
		if err != nil {
			return nil, err
		}
		calls = append(calls, r.exchangeCall(margin, false), r.exchangeCall(perpl.EncodeGetLiquidationInfo(id), false))
	}
	res, err := r.multicall(ctx, calls, block)
	if err != nil {
		return nil, wrap("slow multicall", err)
	}
	sd := &slowData{
		at:     r.now(),
		margin: make(map[uint64]perpl.MarginFractions, len(ids)),
		liq:    make(map[uint64]perpl.LiquidationInfo, len(ids)),
	}
	out, err := unwrap(res[0])
	if err != nil {
		return nil, wrap("exchange info", err)
	}
	if sd.exchange, err = perpl.DecodeExchangeInfo(out); err != nil {
		return nil, wrap("exchange info", err)
	}
	if out, err = unwrap(res[1]); err != nil {
		return nil, wrap("funding interval", err)
	}
	if sd.exchange.FundingInterval, err = perpl.DecodeFundingInterval(out); err != nil {
		return nil, wrap("funding interval", err)
	}
	for i, id := range ids {
		if out, err = unwrap(res[2+2*i]); err != nil {
			return nil, wrap(fmt.Sprintf("margin fractions %d", id), err)
		}
		if sd.margin[id], err = perpl.DecodeMarginFractions(out); err != nil {
			return nil, wrap(fmt.Sprintf("margin fractions %d", id), err)
		}
		if out, err = unwrap(res[3+2*i]); err != nil {
			return nil, wrap(fmt.Sprintf("liquidation info %d", id), err)
		}
		if sd.liq[id], err = perpl.DecodeLiquidationInfo(out); err != nil {
			return nil, wrap(fmt.Sprintf("liquidation info %d", id), err)
		}
	}
	return sd, nil
}

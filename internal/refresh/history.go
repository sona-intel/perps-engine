package refresh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
	"perps/internal/rpc"
	"perps/internal/sources"
	"perps/internal/store"
)

const cacheLimit = 2048

var errBackfillBehind = errors.New("hypersync archive is behind the target")

type scales struct {
	price uint8
	lot   uint8
}

func (r *Runner) rememberDecimals(m perpl.Market) {
	r.decimalsMu.Lock()
	defer r.decimalsMu.Unlock()
	r.decimals[m.PerpID] = scales{price: m.PriceDecimals, lot: m.LotDecimals}
}

// decimalsFor reads the decimals of a market that no refresh has seen yet
func (r *Runner) decimalsFor(ctx context.Context, perp uint64) (scales, error) {
	r.decimalsMu.Lock()
	d, ok := r.decimals[perp]
	r.decimalsMu.Unlock()
	if ok {
		return d, nil
	}
	block, err := r.pinnedBlock(ctx)
	if err != nil {
		return scales{}, err
	}
	out, err := r.call(ctx, perpl.EncodeGetPerpetualInfoV2(perp), block)
	if err != nil {
		return scales{}, wrap(fmt.Sprintf("perp info %d", perp), err)
	}
	m, err := perpl.DecodeMarket(out)
	if err != nil {
		return scales{}, wrap(fmt.Sprintf("perp info %d", perp), err)
	}
	m.PerpID = perp
	r.rememberDecimals(m)
	return scales{price: m.PriceDecimals, lot: m.LotDecimals}, nil
}

func (r *Runner) pinnedBlock(ctx context.Context) (uint64, error) {
	if s := r.latest.Load(); s != nil {
		return s.Block, nil
	}
	head, err := r.blockNumber(ctx)
	if err != nil {
		return 0, err
	}
	if head < pinDepth {
		return 0, fmt.Errorf("head %d is below the pin depth", head)
	}
	return head - pinDepth, nil
}

func (r *Runner) setHistory(fn func(h *historyState)) {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	fn(&r.history)
}

func (r *Runner) backfillState() historyState {
	r.historyMu.Lock()
	defer r.historyMu.Unlock()
	return r.history
}

/* Run loops */

func (r *Runner) runHistory(ctx context.Context) {
	for {
		err := r.initHistory(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return
		}
		r.countError("history", err)
		if !wait(ctx, r.cfg.RefreshInterval) {
			return
		}
	}
	if r.backfillActive {
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.runBackfill(ctx)
		}()
		defer func() { <-done }()
	}
	tick := time.NewTicker(r.cfg.RefreshInterval)
	defer tick.Stop()
	for {
		if err := r.tailOnce(ctx); err != nil && ctx.Err() == nil {
			r.countError("tail", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (r *Runner) runBackfill(ctx context.Context) {
	for ctx.Err() == nil {
		err := r.backfill(ctx)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		r.countError("backfill", err)
		if !wait(ctx, r.cfg.RefreshInterval) {
			return
		}
	}
}

/* Cursors */

// initHistory decides where the tail and the backfill start
func (r *Runner) initHistory(ctx context.Context) error {
	head, err := r.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("history head: %w", err)
	}
	if head < pinDepth {
		return fmt.Errorf("head %d is below the pin depth", head)
	}
	tail, found, err := r.store.Cursor(ctx, store.CursorTail)
	if err != nil {
		return err
	}
	if !found {
		tail = head - pinDepth + 1
		if !r.backfillActive {
			tail = head - min(head, fallbackWindow)
		}
		if err := r.store.SetCursor(ctx, store.CursorTail, tail); err != nil {
			return err
		}
	}
	r.setHistory(func(h *historyState) { h.tailBlock = tail })
	r.refreshTotals(ctx)

	if !r.backfillActive {
		r.setHistory(func(h *historyState) { h.backfill = BackfillDisabled })
		return nil
	}
	start, bfFound, err := r.store.Cursor(ctx, store.CursorBackfill)
	if err != nil {
		return err
	}
	if !bfFound {
		start = r.cfg.DeployBlock
	}
	// the tail covers everything after the first start, so the backfill must end there and not at the live tail
	target, targetFound, err := r.store.Cursor(ctx, store.CursorBackfillTarget)
	if err != nil {
		return err
	}
	if !targetFound {
		target = tail
		if err := r.store.SetCursor(ctx, store.CursorBackfillTarget, target); err != nil {
			return err
		}
	}
	state := BackfillRunning
	if start >= target {
		state = BackfillComplete
		if !bfFound {
			if err := r.store.SetCursor(ctx, store.CursorBackfill, target); err != nil {
				return err
			}
		}
	}
	r.setHistory(func(h *historyState) {
		h.backfill, h.backfillBlock, h.backfillTarget = state, start, target
	})
	return nil
}

func (r *Runner) refreshTotals(ctx context.Context) {
	tot, err := r.store.CountLiquidations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.countError("history", fmt.Errorf("count liquidations: %w", err))
		}
		return
	}
	r.setHistory(func(h *historyState) { h.totals = tot })
	r.m.liquidations.Set(float64(tot.Count))
}

/* Backfill */

// backfill makes one attempt and returns nil only when the range is complete or the backfill is off
func (r *Runner) backfill(ctx context.Context) error {
	st := r.backfillState()
	if !r.backfillActive || st.backfill == BackfillDisabled || st.backfill == BackfillComplete {
		return nil
	}
	if st.backfill == "" {
		return errors.New("history is not initialised")
	}
	q := sources.Query{Address: r.exchange, Topic0: perpl.TopicPositionLiquidated, From: st.backfillBlock, To: st.backfillTarget}
	next, err := r.hyper.Scan(ctx, q, func(page sources.Page) error {
		records := make([]logRecord, len(page.Logs))
		for i, l := range page.Logs {
			keeper := l.From
			records[i] = logRecord{topics: l.Topics, data: l.Data, block: l.Block, index: l.LogIndex, tx: l.TxHash, timestamp: l.BlockTimestamp, keeper: &keeper}
		}
		if err := r.storeRecords(ctx, records, nil); err != nil {
			return err
		}
		cursor := min(page.NextBlock, st.backfillTarget)
		if cursor <= r.backfillState().backfillBlock {
			return nil
		}
		if err := r.store.SetCursor(ctx, store.CursorBackfill, cursor); err != nil {
			return err
		}
		r.setHistory(func(h *historyState) { h.backfillBlock = cursor })
		return nil
	})
	if err == nil && next < st.backfillTarget {
		err = fmt.Errorf("%w at block %d of %d", errBackfillBehind, next, st.backfillTarget)
	}
	if err != nil {
		r.setHistory(func(h *historyState) { h.backfill = BackfillRetrying })
		return fmt.Errorf("backfill: %w", err)
	}
	if err := r.store.SetCursor(ctx, store.CursorBackfill, st.backfillTarget); err != nil {
		return err
	}
	r.setHistory(func(h *historyState) { h.backfill, h.backfillBlock = BackfillComplete, st.backfillTarget })
	return nil
}

/* Tail */

// tailOnce reads everything between the tail cursor and the pinned head
func (r *Runner) tailOnce(ctx context.Context) error {
	head, err := r.blockNumber(ctx)
	if err != nil {
		return fmt.Errorf("tail head: %w", err)
	}
	if head < pinDepth {
		return nil
	}
	to := head - pinDepth
	cur, found, err := r.store.Cursor(ctx, store.CursorTail)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("tail cursor is missing")
	}
	caches := &tailCaches{times: map[uint64]time.Time{}, keepers: map[perpl.Hash]perpl.Address{}}
	for cur <= to {
		end := min(cur+r.tailChunk-1, to)
		logs, err := limited(ctx, r, func() ([]rpc.Log, error) {
			return r.chain.Logs(ctx, r.exchange, perpl.TopicPositionLiquidated, cur, end)
		})
		if err != nil {
			return fmt.Errorf("tail logs %d-%d: %w", cur, end, err)
		}
		if len(caches.times) > cacheLimit || len(caches.keepers) > cacheLimit {
			clear(caches.times)
			clear(caches.keepers)
		}
		records := make([]logRecord, 0, len(logs))
		for _, l := range logs {
			if l.Removed {
				continue
			}
			records = append(records, logRecord{topics: l.Topics, data: l.Data, block: l.Block, index: l.LogIndex, tx: l.TxHash, timestamp: l.BlockTimestamp})
		}
		if err := r.storeRecords(ctx, records, caches); err != nil {
			return err
		}
		if err := r.store.SetCursor(ctx, store.CursorTail, end+1); err != nil {
			return err
		}
		r.setHistory(func(h *historyState) { h.tailBlock = end + 1 })
		cur = end + 1
	}
	return nil
}

type tailCaches struct {
	times   map[uint64]time.Time
	keepers map[perpl.Hash]perpl.Address
}

/* Rows */

// logRecord is a log from either source, with the keeper known up front for HyperSync
type logRecord struct {
	topics    []perpl.Hash
	data      []byte
	block     uint64
	index     uint64
	tx        perpl.Hash
	timestamp uint64
	keeper    *perpl.Address
}

// storeRecords decodes, completes and upserts the records and refreshes the totals when any row was written
func (r *Runner) storeRecords(ctx context.Context, records []logRecord, caches *tailCaches) error {
	slices.SortFunc(records, func(a, b logRecord) int {
		if a.block != b.block {
			return cmpUint(a.block, b.block)
		}
		return cmpUint(a.index, b.index)
	})
	rows := make([]store.Liquidation, 0, len(records))
	for _, rec := range records {
		row, ok, err := r.liquidationRow(ctx, rec, caches)
		if err != nil {
			return err
		}
		if ok {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if err := r.store.UpsertLiquidations(ctx, rows); err != nil {
		return fmt.Errorf("store liquidations: %w", err)
	}
	r.refreshTotals(ctx)
	return nil
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// liquidationRow returns false for a log that can never be stored, which is counted and skipped
func (r *Runner) liquidationRow(ctx context.Context, rec logRecord, caches *tailCaches) (store.Liquidation, bool, error) {
	l, err := perpl.DecodeLiquidation(rec.topics, rec.data, rec.block, rec.index, rec.tx)
	if err != nil {
		r.countError("decode", fmt.Errorf("log %d.%d: %w", rec.block, rec.index, err))
		return store.Liquidation{}, false, nil
	}
	dec, err := r.decimalsFor(ctx, l.PerpID)
	if err != nil {
		if rpc.IsRevert(err) {
			r.countError("decode", fmt.Errorf("log %d.%d: %w", rec.block, rec.index, err))
			return store.Liquidation{}, false, nil
		}
		return store.Liquidation{}, false, err
	}
	keeper, err := r.keeperOf(ctx, rec, caches)
	if err != nil {
		return store.Liquidation{}, false, err
	}
	when, err := r.timeOf(ctx, rec, caches)
	if err != nil {
		return store.Liquidation{}, false, err
	}

	mark := scaled(l.MarkPricePNS, dec.price)
	lot := scaled(l.LiqLotLNS, dec.lot)
	side := store.SideShort
	if l.Long {
		side = store.SideLong
	}
	return store.Liquidation{
		Block:       l.Block,
		LogIndex:    l.LogIndex,
		Tx:          l.TxHash,
		Time:        when,
		PerpID:      l.PerpID,
		AccountID:   l.AccountID,
		Side:        side,
		Mark:        risk.FormatRat(mark, 18),
		LiqPrice:    risk.FormatRat(scaled(l.LiqPricePNS, dec.price), 18),
		LiqLot:      risk.FormatRat(lot, 18),
		PosLot:      risk.FormatRat(scaled(l.PosLotLNS, dec.lot), 18),
		Notional:    risk.FormatRat(lot.Mul(lot, mark), 18),
		OnOrderBook: l.OnOrderBook,
		Keeper:      keeper,
	}, true, nil
}

func (r *Runner) keeperOf(ctx context.Context, rec logRecord, caches *tailCaches) (perpl.Address, error) {
	if rec.keeper != nil {
		return *rec.keeper, nil
	}
	if k, ok := caches.keepers[rec.tx]; ok {
		return k, nil
	}
	tx, err := limited(ctx, r, func() (rpc.Tx, error) { return r.chain.TransactionByHash(ctx, rec.tx) })
	if err != nil {
		return perpl.Address{}, fmt.Errorf("transaction %s: %w", rec.tx, err)
	}
	caches.keepers[rec.tx] = tx.From
	return tx.From, nil
}

func (r *Runner) timeOf(ctx context.Context, rec logRecord, caches *tailCaches) (time.Time, error) {
	if rec.timestamp != 0 {
		return time.Unix(int64(rec.timestamp), 0).UTC(), nil
	}
	if t, ok := caches.times[rec.block]; ok {
		return t, nil
	}
	b, err := r.blockByNumber(ctx, rec.block)
	if err != nil {
		return time.Time{}, fmt.Errorf("block %d: %w", rec.block, err)
	}
	caches.times[rec.block] = b.Time
	return b.Time, nil
}

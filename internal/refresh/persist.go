package refresh

import (
	"context"
	"fmt"
	"time"

	"perps/internal/risk"
	"perps/internal/store"
)

func (r *Runner) persist(ctx context.Context, snap *Snapshot, index uint64) {
	ctx, cancel := context.WithTimeout(ctx, r.dbTimeout)
	defer cancel()
	if err := r.store.InsertMarketSnapshots(ctx, marketRows(snap)); err != nil {
		r.countError("persist", fmt.Errorf("market snapshots: %w", err))
	}
	if index%uint64(r.cfg.PositionSnapshotEvery) != 0 {
		return
	}
	if err := r.store.InsertPositionSnapshots(ctx, positionRows(snap)); err != nil {
		r.countError("persist", fmt.Errorf("position snapshots: %w", err))
	}
}

// pruneIfDue drops old position and market snapshots at most once an hour
func (r *Runner) pruneIfDue(ctx context.Context) {
	now := r.now()
	if !r.lastPrune.IsZero() && now.Sub(r.lastPrune) < pruneEvery {
		return
	}
	r.lastPrune = now
	ctx, cancel := context.WithTimeout(ctx, 2*r.dbTimeout)
	defer cancel()
	if _, err := r.store.PrunePositionSnapshots(ctx, now.Add(-r.cfg.PositionRetention)); err != nil {
		r.countError("prune", err)
	}
	if _, err := r.store.PruneMarketSnapshots(ctx, now.Add(-marketRetention)); err != nil {
		r.countError("prune", err)
	}
}

func marketRows(snap *Snapshot) []store.MarketSnapshot {
	rows := make([]store.MarketSnapshot, 0, len(snap.Markets))
	cd := snap.Exchange.CollateralDecimals
	for _, mv := range snap.Markets {
		if !mv.Priced {
			continue
		}
		m := mv.Market
		row := store.MarketSnapshot{
			PerpID:             m.PerpID,
			Block:              snap.Block,
			Time:               snap.Time,
			Mark:               risk.FormatRat(scaled(m.MarkPNS, m.PriceDecimals), 18),
			Oracle:             risk.FormatRat(scaled(m.OraclePNS, m.PriceDecimals), 18),
			OracleTime:         time.Unix(int64(m.OracleTimestampSec), 0).UTC(),
			LongOI:             risk.FormatRat(scaled(m.LongOpenInterestLNS, m.LotDecimals), 18),
			ShortOI:            risk.FormatRat(scaled(m.ShortOpenInterestLNS, m.LotDecimals), 18),
			LongNotional:       risk.FormatUSD(mv.Metrics.LongNotional),
			ShortNotional:      risk.FormatUSD(mv.Metrics.ShortNotional),
			Positions:          len(mv.Book),
			Insurance:          risk.FormatRat(scaled(m.InsuranceBalanceCNS, cd), 18),
			FundingRatePct100k: m.FundingRatePct100k,
			SCRM10:             ratioText(mv.Metrics.SCRM10),
			SCRP10:             ratioText(mv.Metrics.SCRP10),
		}
		rows = append(rows, row)
	}
	return rows
}

func ratioText(v *float64) *string {
	if v == nil {
		return nil
	}
	s := risk.FormatPrice(*v)
	return &s
}

func positionRows(snap *Snapshot) []store.PositionSnapshot {
	rows := make([]store.PositionSnapshot, 0, snap.Positions)
	cd := snap.Exchange.CollateralDecimals
	for _, mv := range snap.Markets {
		for i, a := range mv.Book {
			side := store.SideShort
			if a.Long {
				side = store.SideLong
			}
			rows = append(rows, store.PositionSnapshot{
				Block:      snap.Block,
				Time:       snap.Time,
				PerpID:     a.PerpID,
				AccountID:  a.AccountID,
				Side:       side,
				Size:       risk.FormatRat(a.Size, 18),
				Entry:      risk.FormatRat(a.Entry, 18),
				Deposit:    risk.FormatRat(a.Deposit, 18),
				PremiumPnl: risk.FormatRat(a.PremiumPnl, 18),
				DeltaPnl:   risk.FormatRat(scaled(mv.Raw[i].DeltaPnlCNS, cd), 18),
				LiqPrice:   risk.FormatRat(a.LiqPrice, 18),
				BkPrice:    risk.FormatRat(a.BankruptcyPrice, 18),
			})
		}
	}
	return rows
}

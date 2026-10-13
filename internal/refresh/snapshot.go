package refresh

import (
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"perps/internal/perpl"
	"perps/internal/risk"
)

// Snapshot is immutable once published
type Snapshot struct {
	Block uint64
	// Time is the time of the pinned block and TakenAt is when the refresh finished
	Time                  time.Time
	TakenAt               time.Time
	Exchange              perpl.ExchangeInfo
	Implementation        perpl.Address
	ImplementationChanged bool
	// Markets are sorted by perp id
	Markets      []*MarketView
	Positions    int
	PaidRPCCalls int64

	byPerp    map[uint64]*MarketView
	bySymbol  map[string]*MarketView
	byAccount map[uint64][]risk.Assessed
	shocks    []float64
}

// MarketView is one market with its positions and the metrics derived from them
type MarketView struct {
	Market perpl.Market
	// Priced is false while the contract holds no mark price for the market
	Priced bool
	// Raw and Book are parallel, in the order the contract lists the positions
	Raw     []perpl.Position
	Book    []risk.Assessed
	Metrics MarketMetrics
	// Stress holds one result per configured shock in configuration order
	Stress []risk.StressResult
}

type MarketMetrics struct {
	Mark   float64
	Oracle float64
	// OracleGapBps is (mark - oracle) / oracle in basis points
	OracleGapBps float64
	OracleAgeSec int64
	// LongOI and ShortOI are sizes in base units
	LongOI        float64
	ShortOI       float64
	LongNotional  float64
	ShortNotional float64
	Positions     int
	// SCRM10 and SCRP10 are nil when the shock causes no bad debt
	SCRM10 *float64
	SCRP10 *float64
}

func (s *Snapshot) Market(perpID uint64) (*MarketView, bool) {
	mv, ok := s.byPerp[perpID]
	return mv, ok
}

// Find takes a symbol in any case or a perp id
func (s *Snapshot) Find(sel string) (*MarketView, bool) {
	if mv, ok := s.bySymbol[strings.ToUpper(sel)]; ok {
		return mv, true
	}
	if id, err := strconv.ParseUint(sel, 10, 64); err == nil {
		return s.Market(id)
	}
	return nil, false
}

// Account returns the account's positions across markets, ordered by perp id
func (s *Snapshot) Account(accountID uint64) []risk.Assessed { return s.byAccount[accountID] }

func (s *Snapshot) Books() []risk.MarketBook {
	books := make([]risk.MarketBook, len(s.Markets))
	for i, mv := range s.Markets {
		books[i] = risk.MarketBook{Market: mv.Market, Positions: mv.Book}
	}
	return books
}

// Stress answers from the precomputed results when the shock is configured and computes it otherwise
func (s *Snapshot) Stress(perpID uint64, shock float64) (risk.StressResult, error) {
	mv, ok := s.byPerp[perpID]
	if !ok {
		return risk.StressResult{}, fmt.Errorf("%w %d", ErrUnknownMarket, perpID)
	}
	if i := slices.Index(s.shocks, shock); i >= 0 && i < len(mv.Stress) {
		return mv.Stress[i], nil
	}
	return risk.Stress(mv.Market, s.Exchange, mv.Book, shock)
}

func (s *Snapshot) LiquidationMap(perpID uint64, bucketBps, rangePct int) (risk.LiqMap, error) {
	mv, ok := s.byPerp[perpID]
	if !ok {
		return risk.LiqMap{}, fmt.Errorf("%w %d", ErrUnknownMarket, perpID)
	}
	return risk.LiquidationMap(mv.Market, mv.Book, bucketBps, rangePct)
}

/* Building */

func scaled(n *big.Int, decimals uint8) *big.Rat {
	if n == nil {
		return new(big.Rat)
	}
	d := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Rat).SetFrac(n, d)
}

func ratFloat(r *big.Rat) float64 {
	f, _ := r.Float64()
	return f
}

func (r *Runner) buildView(m perpl.Market, pages []perpl.Position, ei perpl.ExchangeInfo, blockTime time.Time) (*MarketView, error) {
	mv := &MarketView{Market: m, Raw: pages, Book: make([]risk.Assessed, 0, len(pages))}
	mv.Priced = m.MarkPNS != nil && m.MarkPNS.Sign() > 0
	if !mv.Priced {
		if len(pages) > 0 {
			return nil, fmt.Errorf("market %d has %d positions and no mark price", m.PerpID, len(pages))
		}
		return mv, nil
	}
	for _, p := range pages {
		a, err := risk.Assess(m, ei, p)
		if err != nil {
			return nil, fmt.Errorf("assess market %d account %d: %w", m.PerpID, p.AccountID, err)
		}
		mv.Book = append(mv.Book, a)
	}
	mv.Stress = make([]risk.StressResult, len(r.cfg.Shocks))
	for i, shock := range r.cfg.Shocks {
		st, err := risk.Stress(m, ei, mv.Book, shock)
		if err != nil {
			return nil, fmt.Errorf("stress market %d at %v: %w", m.PerpID, shock, err)
		}
		mv.Stress[i] = st
	}
	var err error
	if mv.Metrics, err = r.metricsFor(mv, ei, blockTime); err != nil {
		return nil, err
	}
	return mv, nil
}

func (r *Runner) metricsFor(mv *MarketView, ei perpl.ExchangeInfo, blockTime time.Time) (MarketMetrics, error) {
	m := mv.Market
	mm := MarketMetrics{
		Mark:      ratFloat(scaled(m.MarkPNS, m.PriceDecimals)),
		Oracle:    ratFloat(scaled(m.OraclePNS, m.PriceDecimals)),
		LongOI:    ratFloat(scaled(m.LongOpenInterestLNS, m.LotDecimals)),
		ShortOI:   ratFloat(scaled(m.ShortOpenInterestLNS, m.LotDecimals)),
		Positions: len(mv.Book),
	}
	if mm.Oracle > 0 {
		mm.OracleGapBps = (mm.Mark - mm.Oracle) / mm.Oracle * 10000
	}
	mm.OracleAgeSec = max(0, blockTime.Unix()-int64(m.OracleTimestampSec))
	long, short := new(big.Rat), new(big.Rat)
	for i := range mv.Book {
		if mv.Book[i].Long {
			long.Add(long, mv.Book[i].Notional)
		} else {
			short.Add(short, mv.Book[i].Notional)
		}
	}
	mm.LongNotional, mm.ShortNotional = ratFloat(long), ratFloat(short)
	var err error
	if mm.SCRM10, err = r.scrAt(mv, ei, -0.1); err != nil {
		return mm, err
	}
	if mm.SCRP10, err = r.scrAt(mv, ei, 0.1); err != nil {
		return mm, err
	}
	return mm, nil
}

func (r *Runner) scrAt(mv *MarketView, ei perpl.ExchangeInfo, shock float64) (*float64, error) {
	if i := slices.Index(r.cfg.Shocks, shock); i >= 0 {
		return mv.Stress[i].SCR, nil
	}
	st, err := risk.Stress(mv.Market, ei, mv.Book, shock)
	if err != nil {
		return nil, fmt.Errorf("stress market %d at %v: %w", mv.Market.PerpID, shock, err)
	}
	return st.SCR, nil
}

func newSnapshot(views []*MarketView, shocks []float64) *Snapshot {
	s := &Snapshot{
		Markets:   views,
		byPerp:    make(map[uint64]*MarketView, len(views)),
		bySymbol:  make(map[string]*MarketView, len(views)),
		byAccount: make(map[uint64][]risk.Assessed),
		shocks:    slices.Clone(shocks),
	}
	for _, mv := range views {
		s.byPerp[mv.Market.PerpID] = mv
		s.bySymbol[strings.ToUpper(mv.Market.Symbol)] = mv
		s.Positions += len(mv.Book)
		for _, a := range mv.Book {
			s.byAccount[a.AccountID] = append(s.byAccount[a.AccountID], a)
		}
	}
	return s
}

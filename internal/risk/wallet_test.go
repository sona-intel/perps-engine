package risk

import (
	"errors"
	"math/big"
	"testing"

	"perps/internal/perpl"
)

func walletFixture(t *testing.T) (perpl.Account, perpl.ExchangeInfo, []Assessed, map[uint64]MarketTotal) {
	t.Helper()
	acct := perpl.Account{
		AccountID:        10,
		BalanceCNS:       big.NewInt(12_500_000),
		LockedBalanceCNS: big.NewInt(2_000_000),
		Address:          addr(t, "0x0cae9940b85079ef776946bc48ec2b1b1d625812"),
	}
	ei := perpl.ExchangeInfo{CollateralDecimals: 6}
	mk := func(perp uint64, long bool, notional, deposit, dist string) Assessed {
		return Assessed{
			PerpID: perp, AccountID: 10, Long: long,
			Notional: rat(t, notional), Deposit: rat(t, deposit), Distance: rat(t, dist),
			Size: big.NewRat(1, 1), LiqPrice: big.NewRat(1, 1), BankruptcyPrice: big.NewRat(1, 1),
		}
	}
	positions := []Assessed{
		mk(1, true, "100", "20", "0.30"),
		mk(2, false, "300", "50", "0.10"),
	}
	totals := map[uint64]MarketTotal{
		1: {PerpID: 1, Symbol: "AAA", LongNotional: 1000, ShortNotional: 1000},
		2: {PerpID: 2, Symbol: "BBB", LongNotional: 600, ShortNotional: 600},
	}
	return acct, ei, positions, totals
}

func TestWalletCard(t *testing.T) {
	acct, ei, positions, totals := walletFixture(t)
	day := &LeaderRow{Rank: 3, PnlUSD: -12.5, VolumeUSD: 900}
	all := &LeaderRow{Rank: 41, PnlUSD: 77, VolumeUSD: 12345}
	got, err := WalletCard(ei, acct, positions, totals, Standing{Day: day, All: all})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != 10 || got.Address != acct.Address || got.BalanceUSD != 12.5 || got.LockedUSD != 2 || got.Frozen {
		t.Fatalf("header %+v", got)
	}
	if len(got.Positions) != 2 || got.Positions[0].Symbol != "BBB" || got.Positions[1].Symbol != "AAA" {
		t.Fatalf("positions %+v", got.Positions)
	}
	if !near(got.Positions[0].OIShare, 0.5, 1e-12) || !near(got.Positions[1].OIShare, 0.1, 1e-12) {
		t.Fatalf("oi shares %v %v", got.Positions[0].OIShare, got.Positions[1].OIShare)
	}
	if got.TotalNotional != 400 || got.TotalDeposit != 70 {
		t.Fatalf("totals %v %v", got.TotalNotional, got.TotalDeposit)
	}
	if got.MinDistance == nil || !near(*got.MinDistance, 0.10, 1e-12) {
		t.Fatalf("min distance %v", got.MinDistance)
	}
	if got.Standing.Day != day || got.Standing.All != all {
		t.Fatalf("standing %+v", got.Standing)
	}
}

func TestWalletCardUsesOwnSideForOIShare(t *testing.T) {
	acct, ei, positions, totals := walletFixture(t)
	totals[2] = MarketTotal{PerpID: 2, Symbol: "BBB", LongNotional: 900, ShortNotional: 600}
	got, err := WalletCard(ei, acct, positions, totals, Standing{})
	if err != nil {
		t.Fatal(err)
	}
	// the BBB position is short and 300 of 600 short notional
	if !near(got.Positions[0].OIShare, 0.5, 1e-12) {
		t.Fatalf("share %v", got.Positions[0].OIShare)
	}
}

func TestWalletCardNoPositions(t *testing.T) {
	acct, ei, _, totals := walletFixture(t)
	acct.Frozen = 1
	got, err := WalletCard(ei, acct, nil, totals, Standing{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Positions) != 0 || got.MinDistance != nil || got.TotalNotional != 0 || !got.Frozen || got.Standing.Day != nil {
		t.Fatalf("%+v", got)
	}
}

func TestWalletCardRejectsBadInput(t *testing.T) {
	acct, ei, positions, totals := walletFixture(t)
	if _, err := WalletCard(ei, acct, positions, map[uint64]MarketTotal{1: totals[1]}, Standing{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("missing market totals: %v", err)
	}
	bad := acct
	bad.BalanceCNS = nil
	if _, err := WalletCard(ei, bad, positions, totals, Standing{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("nil balance: %v", err)
	}
	bad = acct
	bad.LockedBalanceCNS = nil
	if _, err := WalletCard(ei, bad, positions, totals, Standing{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("nil locked: %v", err)
	}
	other := append([]Assessed(nil), positions...)
	other[0].AccountID = 99
	if _, err := WalletCard(ei, acct, other, totals, Standing{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("foreign position: %v", err)
	}
}

func TestMarketTotalsFromBooks(t *testing.T) {
	a := MarketBook{Market: perpl.Market{PerpID: 1, Symbol: "AAA"}, Positions: []Assessed{
		notionalPos(t, 1, 1, true, "30"), notionalPos(t, 1, 2, true, "20"), notionalPos(t, 1, 3, false, "50"),
	}}
	got := MarketTotals([]MarketBook{a})
	mt := got[1]
	if mt.Symbol != "AAA" || mt.LongNotional != 50 || mt.ShortNotional != 50 {
		t.Fatalf("%+v", mt)
	}
}

func TestWalletCardFromFixture(t *testing.T) {
	var books []MarketBook
	for _, perp := range []int{1, 10} {
		fm := loadFixtureMarket(t, perp)
		m, _, assessed := fm.assessAll(t)
		books = append(books, MarketBook{Market: m, Positions: assessed})
	}
	totals := MarketTotals(books)
	top := Concentration(books).Markets[0].TopAccounts[0].AccountID
	var mine []Assessed
	for _, b := range books {
		for _, p := range b.Positions {
			if p.AccountID == top {
				mine = append(mine, p)
			}
		}
	}
	acct := perpl.Account{AccountID: top, BalanceCNS: big.NewInt(1), LockedBalanceCNS: big.NewInt(0)}
	got, err := WalletCard(perpl.ExchangeInfo{CollateralDecimals: 6}, acct, mine, totals, Standing{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Positions) != len(mine) || got.Positions[0].Symbol == "" || got.Positions[0].OIShare <= 0 || got.Positions[0].OIShare > 1 {
		t.Fatalf("%+v", got.Positions)
	}
}

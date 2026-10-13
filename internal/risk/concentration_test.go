package risk

import (
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"perps/internal/perpl"
)

func notionalPos(t testing.TB, perp, acct uint64, long bool, notional string) Assessed {
	t.Helper()
	n := rat(t, notional)
	return Assessed{PerpID: perp, AccountID: acct, Long: long, Size: big.NewRat(1, 1), Notional: n}
}

func TestConcentrationHandComputed(t *testing.T) {
	// market 1 longs sum 200, shorts sum 200 so the two-sided total is 400
	a := MarketBook{
		Market: perpl.Market{PerpID: 1, Symbol: "AAA"},
		Positions: []Assessed{
			notionalPos(t, 1, 10, true, "100"),
			notionalPos(t, 1, 11, true, "50"),
			notionalPos(t, 1, 12, true, "30"),
			notionalPos(t, 1, 13, true, "20"),
			notionalPos(t, 1, 10, false, "0"), // a zero short must not break ranking
			notionalPos(t, 1, 20, false, "120"),
			notionalPos(t, 1, 21, false, "80"),
		},
	}
	// market 2 is one long and one short of 60, account 10 is long in both markets
	b := MarketBook{
		Market: perpl.Market{PerpID: 2, Symbol: "BBB"},
		Positions: []Assessed{
			notionalPos(t, 2, 10, true, "60"),
			notionalPos(t, 2, 30, false, "60"),
		},
	}
	got := Concentration([]MarketBook{a, b})
	if len(got.Markets) != 2 {
		t.Fatalf("markets %d", len(got.Markets))
	}
	ma := got.Markets[0]
	if ma.PerpID != 1 || ma.Symbol != "AAA" || ma.LongNotional != 200 || ma.ShortNotional != 200 || ma.TotalNotional != 400 {
		t.Fatalf("market a %+v", ma)
	}
	if !near(ma.Top1, 120.0/400, 1e-12) || !near(ma.Top5, (120+100+80+50+30)/400.0, 1e-12) || !near(ma.Top10, 1, 1e-12) {
		t.Fatalf("market a shares %v %v %v", ma.Top1, ma.Top5, ma.Top10)
	}
	if len(ma.TopAccounts) != 6 || ma.TopAccounts[0].AccountID != 20 || ma.TopAccounts[0].Notional != 120 || !near(ma.TopAccounts[0].Share, 0.3, 1e-12) {
		t.Fatalf("top accounts %+v", ma.TopAccounts)
	}
	if ma.TopAccounts[1].AccountID != 10 || ma.TopAccounts[2].AccountID != 21 {
		t.Fatalf("order %+v", ma.TopAccounts)
	}
	if ma.Positions != 7 {
		t.Fatalf("positions %d", ma.Positions)
	}

	o := got.Overall
	// account 10 holds 100 + 60 in two markets, total open notional is 400 + 120
	if o.TotalNotional != 520 || o.Accounts != 7 {
		t.Fatalf("overall %+v", o)
	}
	if o.TopAccounts[0].AccountID != 10 || o.TopAccounts[0].Notional != 160 || o.TopAccounts[0].Positions != 3 {
		t.Fatalf("overall top %+v", o.TopAccounts[0])
	}
	if !near(o.Top1, 160.0/520, 1e-12) {
		t.Fatalf("overall top1 %v", o.Top1)
	}
	if !near(o.Top10, 1, 1e-12) {
		t.Fatalf("overall top10 %v", o.Top10)
	}
}

func TestConcentrationEmpty(t *testing.T) {
	got := Concentration(nil)
	if len(got.Markets) != 0 || got.Overall.TotalNotional != 0 || got.Overall.Top1 != 0 || len(got.Overall.TopAccounts) != 0 {
		t.Fatalf("%+v", got)
	}
	got = Concentration([]MarketBook{{Market: perpl.Market{PerpID: 5, Symbol: "EMP"}}})
	if len(got.Markets) != 1 || got.Markets[0].Top1 != 0 || math.IsNaN(got.Markets[0].Top10) {
		t.Fatalf("%+v", got.Markets)
	}
}

func TestConcentrationTopTenCap(t *testing.T) {
	var ps []Assessed
	for i := range 30 {
		ps = append(ps, notionalPos(t, 1, uint64(i+1), i%2 == 0, "10"))
	}
	got := Concentration([]MarketBook{{Market: perpl.Market{PerpID: 1}, Positions: ps}})
	if len(got.Markets[0].TopAccounts) != 10 || len(got.Overall.TopAccounts) != 10 {
		t.Fatalf("caps %d %d", len(got.Markets[0].TopAccounts), len(got.Overall.TopAccounts))
	}
	for i, a := range got.Markets[0].TopAccounts {
		if a.AccountID != uint64(i+1) {
			t.Fatalf("tie break by id failed %+v", got.Markets[0].TopAccounts)
		}
	}
	if !near(got.Markets[0].Top10, 100.0/300, 1e-12) {
		t.Fatalf("top10 %v", got.Markets[0].Top10)
	}
}

func TestConcentrationSharesBoundedAndOrdered(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for iter := range 200 {
		book := randomBook(t, r, r.IntN(80), 250)
		got := Concentration([]MarketBook{{Market: perpl.Market{PerpID: 1}, Positions: book}})
		mk := got.Markets[0]
		for _, s := range []float64{mk.Top1, mk.Top5, mk.Top10, got.Overall.Top1, got.Overall.Top5, got.Overall.Top10} {
			if s < 0 || s > 1+1e-12 || math.IsNaN(s) {
				t.Fatalf("iter %d share %v out of bounds", iter, s)
			}
		}
		if mk.Top1 > mk.Top5+1e-12 || mk.Top5 > mk.Top10+1e-12 {
			t.Fatalf("iter %d shares not ordered", iter)
		}
		for i := 1; i < len(mk.TopAccounts); i++ {
			if mk.TopAccounts[i].Notional > mk.TopAccounts[i-1].Notional {
				t.Fatalf("iter %d top accounts not sorted", iter)
			}
		}
	}
}

func TestConcentrationFixtures(t *testing.T) {
	var books []MarketBook
	for _, perp := range fixturePerps {
		fm := loadFixtureMarket(t, perp)
		m, _, assessed := fm.assessAll(t)
		books = append(books, MarketBook{Market: m, Positions: assessed})
	}
	got := Concentration(books)
	if len(got.Markets) != len(fixturePerps) {
		t.Fatalf("markets %d", len(got.Markets))
	}
	btc := got.Markets[0]
	if btc.Symbol != "BTC" || btc.Positions != 246 {
		t.Fatalf("btc %+v", btc)
	}
	// long and short notional of a complete market agree
	if !near(btc.LongNotional, btc.ShortNotional, 1e-9) {
		t.Fatalf("btc long %v short %v", btc.LongNotional, btc.ShortNotional)
	}
	// accounts in the top ten of BTC by hand from the fixture
	var best float64
	var bestID uint64
	for _, a := range books[0].Positions {
		n, _ := a.Notional.Float64()
		if n > best {
			best, bestID = n, a.AccountID
		}
	}
	if btc.TopAccounts[0].AccountID != bestID || !near(btc.TopAccounts[0].Notional, best, 1e-12) {
		t.Fatalf("btc top %+v want %d %v", btc.TopAccounts[0], bestID, best)
	}
	if got.Overall.Accounts == 0 || got.Overall.Accounts > 665 {
		t.Fatalf("overall accounts %d", got.Overall.Accounts)
	}
}

func TestVolumeShares(t *testing.T) {
	rows := make([]LeaderRow, 0, 20)
	for i := range 20 {
		rows = append(rows, LeaderRow{Rank: i + 1, VolumeUSD: float64(100 - i*4)})
	}
	got := VolumeShares(rows)
	var total, top10 float64
	for i, r := range rows {
		total += r.VolumeUSD
		if i < 10 {
			top10 += r.VolumeUSD
		}
	}
	if got.Rows != 20 || !near(got.TotalVolume, total, 1e-12) || !near(got.Top10Volume, top10, 1e-12) {
		t.Fatalf("%+v", got)
	}
	if !near(got.Top1, 100/total, 1e-12) || !near(got.Top5, (100+96+92+88+84)/total, 1e-12) || !near(got.Top10, top10/total, 1e-12) {
		t.Fatalf("%+v", got)
	}
}

func TestVolumeSharesSortsByVolumeNotRank(t *testing.T) {
	rows := []LeaderRow{{Rank: 1, VolumeUSD: 10}, {Rank: 2, VolumeUSD: 90}}
	got := VolumeShares(rows)
	if !near(got.Top1, 0.9, 1e-12) {
		t.Fatalf("%+v", got)
	}
}

func TestVolumeSharesEmptyAndZero(t *testing.T) {
	for _, rows := range [][]LeaderRow{nil, {{Rank: 1}}, {{Rank: 1, VolumeUSD: -5}}} {
		got := VolumeShares(rows)
		if got.Top1 != 0 || got.Top10 != 0 || got.TotalVolume != 0 {
			t.Fatalf("%+v", got)
		}
	}
}

func TestVolumeSharesFewerThanTenRows(t *testing.T) {
	got := VolumeShares([]LeaderRow{{VolumeUSD: 1}, {VolumeUSD: 3}})
	if !near(got.Top10, 1, 1e-12) || !near(got.Top5, 1, 1e-12) || !near(got.Top1, 0.75, 1e-12) {
		t.Fatalf("%+v", got)
	}
}

func addr(t testing.TB, s string) perpl.Address {
	t.Helper()
	a, err := perpl.ParseAddress(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestKeepers(t *testing.T) {
	k1 := addr(t, "0x1111111111111111111111111111111111111111")
	k2 := addr(t, "0x2222222222222222222222222222222222222222")
	k3 := addr(t, "0x3333333333333333333333333333333333333333")
	liqs := []perpl.Liquidation{
		{Block: 50, Keeper: k2},
		{Block: 10, Keeper: k1},
		{Block: 70, Keeper: k1},
		{Block: 60, Keeper: k2},
		{Block: 20, Keeper: k1},
		{Block: 30, Keeper: k3},
	}
	got := Keepers(liqs)
	if got.Total != 6 || got.FirstBlock != 10 || got.LastBlock != 70 || len(got.Keepers) != 3 {
		t.Fatalf("%+v", got)
	}
	if got.Keepers[0].Address != k1 || got.Keepers[0].Liquidations != 3 || !near(got.Keepers[0].Share, 0.5, 1e-12) {
		t.Fatalf("first %+v", got.Keepers[0])
	}
	if got.Keepers[1].Address != k2 || got.Keepers[1].Liquidations != 2 || got.Keepers[2].Address != k3 {
		t.Fatalf("order %+v", got.Keepers)
	}
}

func TestKeepersTieBreaksByAddress(t *testing.T) {
	lo := addr(t, "0x0000000000000000000000000000000000000001")
	hi := addr(t, "0xffffffffffffffffffffffffffffffffffffffff")
	got := Keepers([]perpl.Liquidation{{Block: 1, Keeper: hi}, {Block: 2, Keeper: lo}})
	if got.Keepers[0].Address != lo {
		t.Fatalf("%+v", got.Keepers)
	}
}

func TestKeepersEmpty(t *testing.T) {
	got := Keepers(nil)
	if got.Total != 0 || len(got.Keepers) != 0 || got.FirstBlock != 0 || got.LastBlock != 0 {
		t.Fatalf("%+v", got)
	}
}

package refresh

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"perps/internal/perpl"
	"perps/internal/rpc"
)

func withMulticall(on bool) func(*Options) {
	return func(o *Options) { o.Config.Multicall = on }
}

// describe prints everything a snapshot holds that comes from the chain
func describe(t testing.TB, s *Snapshot) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "block %d time %v impl %s changed %v\n", s.Block, s.Time, s.Implementation, s.ImplementationChanged)
	fmt.Fprintf(&b, "exchange %+v\n", s.Exchange)
	for _, mv := range s.Markets {
		scr := func(p *float64) string {
			if p == nil {
				return "none"
			}
			return fmt.Sprint(*p)
		}
		m := mv.Metrics
		fmt.Fprintf(&b, "market %+v priced %v\n", mv.Market, mv.Priced)
		fmt.Fprintf(&b, "metrics %+v %s %s\n", struct{ A, B, C, D, E, F, G, H float64 }{m.Mark, m.Oracle, m.OracleGapBps, m.LongOI, m.ShortOI, m.LongNotional, m.ShortNotional, float64(m.Positions)}, scr(m.SCRM10), scr(m.SCRP10))
		fmt.Fprintf(&b, "raw %+v\n", mv.Raw)
		fmt.Fprintf(&b, "book %+v\n", mv.Book)
		for _, st := range mv.Stress {
			ratio := scr(st.SCR)
			st.SCR = nil
			fmt.Fprintf(&b, "stress %+v scr %s\n", st, ratio)
		}
	}
	out := b.String()
	if strings.Contains(out, "0xc000") {
		t.Fatal("snapshot description holds pointer addresses")
	}
	return out
}

func sameSnapshot(t testing.TB, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range min(len(wl), len(gl)) {
		if wl[i] != gl[i] {
			col := 0
			for col < min(len(wl[i]), len(gl[i])) && wl[i][col] == gl[i][col] {
				col++
			}
			from := max(0, col-80)
			t.Fatalf("snapshots differ at line %d column %d\nper call  %.200s\nmulticall %.200s", i, col, wl[i][from:], gl[i][from:])
		}
	}
	t.Fatalf("snapshots differ in length, %d lines against %d", len(wl), len(gl))
}

func selectorsOf(calls []perpl.Call3) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = hex.EncodeToString(c.Data[:4])
	}
	return out
}

func countSelector(calls []perpl.Call3, sel string) int {
	n := 0
	for _, got := range selectorsOf(calls) {
		if got == strings.TrimPrefix(sel, "0x") {
			n++
		}
	}
	return n
}

func bitmapReply(ids []uint64) []byte {
	out := make([]byte, 4*32)
	for _, id := range ids {
		word, bit := id/256, id%256
		out[word*32+31-bit/8] |= 1 << (bit % 8)
	}
	return out
}

func setBitmap(g *rig, ids []uint64) {
	g.chain.replies["0x"+hex.EncodeToString(perpl.EncodeGetPerpetualExistsBitmap())] = reply{out: bitmapReply(ids)}
}

func perpArg(data []byte) uint64 {
	var v uint64
	for _, b := range data[4+24 : 4+32] {
		v = v<<8 | uint64(b)
	}
	return v
}

func TestMulticallPathReproducesThePerCallSnapshot(t *testing.T) {
	plain := newRig(t, withMulticall(false))
	batched := newRig(t, withMulticall(true))
	want := describe(t, plain.refresh())
	got := describe(t, batched.refresh())
	sameSnapshot(t, want, got)
	if !strings.Contains(want, "BTC") || len(plain.r.Latest().Markets) != 17 {
		t.Fatalf("comparison ran on an empty snapshot")
	}
	if !reflect.DeepEqual(plain.chain.pageStarts, batched.chain.pageStarts) {
		t.Fatalf("page walks differ\nper call %v\nmulticall %v", plain.chain.pageStarts, batched.chain.pageStarts)
	}
	if starts := batched.chain.pageStarts[1]; len(starts) != 3 || starts[0] != 0 || starts[1] == 0 || starts[2] == 0 {
		t.Fatalf("BTC page starts %v", starts)
	}
	if batched.chain.directCalls != 0 {
		t.Fatalf("%d calls went straight to the exchange", batched.chain.directCalls)
	}

	// a second refresh at a new block, now with the market list known
	for _, g := range []*rig{plain, batched} {
		g.chain.setHead(pinnedBlock + 3)
	}
	sameSnapshot(t, describe(t, plain.refresh()), describe(t, batched.refresh()))
}

func TestMulticallCallCounts(t *testing.T) {
	g := newRig(t, withMulticall(true))
	ids := len(fixturePerpIDs(t))
	g.refresh()

	m := g.chain.multicalls
	if len(m) != 5 {
		t.Fatalf("%d multicalls in the first refresh, want 5", len(m))
	}
	if len(m[0]) != 1 || countSelector(m[0], selector(t, "getPerpetualExistsBitmap")) != 1 {
		t.Fatalf("first multicall %v", selectorsOf(m[0]))
	}
	if len(m[1]) != 2*ids || countSelector(m[1], selector(t, "getPerpetualInfoV2")) != ids || countSelector(m[1], selector(t, "getPositionsV2")) != ids {
		t.Fatalf("market multicall %v", selectorsOf(m[1]))
	}
	if len(m[2]) != 2+2*ids || countSelector(m[2], selector(t, "getMarginFractions")) != ids || countSelector(m[2], selector(t, "getLiquidationInfo")) != ids ||
		countSelector(m[2], selector(t, "getExchangeInfo")) != 1 || countSelector(m[2], selector(t, "getFundingInterval")) != 1 {
		t.Fatalf("slow multicall %v", selectorsOf(m[2]))
	}
	for i := 3; i < 5; i++ {
		if len(m[i]) != 1 || countSelector(m[i], selector(t, "getPositionsV2")) != 1 || perpArg(m[i][0].Data) != 1 {
			t.Fatalf("page multicall %d %v", i, selectorsOf(m[i]))
		}
	}
	for i, calls := range m {
		for _, c := range calls {
			if c.AllowFailure {
				t.Fatalf("multicall %d lets a call fail without a market list to check it against", i)
			}
		}
	}

	g.chain.setHead(pinnedBlock + 3)
	g.refresh()
	m = g.chain.multicalls[5:]
	if len(m) != 3 {
		t.Fatalf("%d multicalls in the second refresh, want 3", len(m))
	}
	if len(m[0]) != 1+2*ids || countSelector(m[0], selector(t, "getPerpetualExistsBitmap")) != 1 {
		t.Fatalf("second refresh opens with %d calls", len(m[0]))
	}
	if m[0][0].AllowFailure {
		t.Fatal("the bitmap may not fail")
	}
	if g.chain.directCalls != 0 {
		t.Fatalf("%d direct calls", g.chain.directCalls)
	}
	if n := g.chain.selectors[selector(t, "getExchangeInfo")]; n != 1 {
		t.Fatalf("exchange info read %d times", n)
	}
}

func TestMulticallPinsEveryCallToOneBlock(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.refresh()
	if len(g.chain.callBlocks) != 1 || g.chain.callBlocks[pinnedBlock] != 5 {
		t.Fatalf("calls by block %v", g.chain.callBlocks)
	}
}

func TestMulticallSwitchOffUsesOnlyDirectCalls(t *testing.T) {
	g := newRig(t, withMulticall(false))
	g.refresh()
	if len(g.chain.multicalls) != 0 || g.chain.directCalls < 50 {
		t.Fatalf("%d multicalls and %d direct calls", len(g.chain.multicalls), g.chain.directCalls)
	}
}

func TestMulticallSlowReadsOnlyWhenDue(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.refresh()
	g.chain.setHead(pinnedBlock + 3)
	g.refresh()
	if n := g.chain.selectors[selector(t, "getExchangeInfo")]; n != 1 {
		t.Fatalf("exchange info read %d times in two refreshes", n)
	}
	g.advance(11 * time.Minute)
	g.chain.setHead(pinnedBlock + 4)
	g.refresh()
	if n := g.chain.selectors[selector(t, "getExchangeInfo")]; n != 2 {
		t.Fatalf("exchange info read %d times after the slow interval", n)
	}
	slow := 0
	for _, calls := range g.chain.multicalls {
		if countSelector(calls, selector(t, "getExchangeInfo")) == 1 {
			slow++
		}
	}
	if slow != 2 {
		t.Fatalf("%d slow multicalls", slow)
	}
}

func TestMulticallSlowFailureKeepsCachedValues(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.refresh()
	g.advance(11 * time.Minute)
	g.chain.setHead(pinnedBlock + 3)
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getExchangeInfo") {
			return errBoom
		}
		return nil
	}
	s := g.refresh()
	if s.Exchange.CollateralDecimals != 6 || s.Exchange.FundingInterval != 8571 {
		t.Fatalf("exchange info lost %+v", s.Exchange)
	}
	if got := metricValue(t, g.reg, "perps_refresh_errors_total", map[string]string{"stage": "slow"}); got != 1 {
		t.Fatalf("slow error metric %v", got)
	}
}

func TestMulticallFirstRefreshFailsWithoutSlowData(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.chain.fail = func(sel string, _ []byte) error {
		if sel == selector(t, "getLiquidationInfo") {
			return errBoom
		}
		return nil
	}
	err := g.r.refreshOnce(context.Background())
	if err == nil || !rpc.IsRevert(err) {
		t.Fatalf("first refresh error %v", err)
	}
	if g.r.Ready() {
		t.Fatal("runner is ready without a snapshot")
	}
}

func TestMulticallFailedSubCallFailsTheRefreshLikeAFailedCall(t *testing.T) {
	sel := func(name string) string { return selector(t, name) }
	cases := map[string]struct {
		warm bool
		hit  func(sel string, data []byte) bool
		text string
	}{
		"bitmap":                  {true, func(s string, _ []byte) bool { return s == sel("getPerpetualExistsBitmap") }, ""},
		"bitmap on the first run": {false, func(s string, _ []byte) bool { return s == sel("getPerpetualExistsBitmap") }, ""},
		"info of a known market": {true, func(s string, d []byte) bool {
			return s == sel("getPerpetualInfoV2") && perpArg(d) == 10
		}, "perp info 10"},
		"info of a new market": {false, func(s string, d []byte) bool {
			return s == sel("getPerpetualInfoV2") && perpArg(d) == 10
		}, ""},
		"first page of a known market": {true, func(s string, d []byte) bool {
			return s == sel("getPositionsV2") && perpArg(d) == 20
		}, "positions 20 from 0"},
		"later page": {true, func(s string, d []byte) bool {
			return s == sel("getPositionsV2") && perpArg(d) == 1 && !isFirstPage(d)
		}, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newRig(t, withMulticall(true))
			var first *Snapshot
			if c.warm {
				first = g.refresh()
				g.chain.setHead(pinnedBlock + 3)
			}
			g.chain.fail = func(s string, d []byte) error {
				if c.hit(s, d) {
					return errBoom
				}
				return nil
			}
			err := g.r.refreshOnce(context.Background())
			if err == nil {
				t.Fatal("refresh succeeded with a failing sub-call")
			}
			if !rpc.IsRevert(err) {
				t.Fatalf("error is not a revert: %v", err)
			}
			if !strings.Contains(err.Error(), c.text) {
				t.Fatalf("error %q does not name %q", err, c.text)
			}
			if g.r.Latest() != first {
				t.Fatal("a failed refresh replaced the snapshot")
			}
			g.chain.fail = nil
			if s := g.refresh(); s.Positions != 665 {
				t.Fatalf("no recovery, %d positions", s.Positions)
			}
		})
	}
}

func TestUnwrapReportsAFailedResultAsARevert(t *testing.T) {
	ok, err := unwrap(perpl.Result3{Success: true, Data: []byte{1, 2}})
	if err != nil || len(ok) != 2 {
		t.Fatalf("unwrap of a success %v %v", ok, err)
	}
	_, err = unwrap(perpl.Result3{Data: []byte{0xb6, 0xb1, 0xe4, 0xa0}})
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || !rpc.IsRevert(err) || rpcErr.Data != "0xb6b1e4a0" {
		t.Fatalf("unwrap of a failure %v", err)
	}
}

func TestMulticallShortAnswerFailsTheRefresh(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.chain.dropResults = 1
	if err := g.r.refreshOnce(context.Background()); err == nil {
		t.Fatal("a multicall answer with a missing result was accepted")
	}
	if g.r.Ready() {
		t.Fatal("snapshot built from a short answer")
	}
}

func TestPositionListLoopFailsTheRefresh(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("multicall %v", on), func(t *testing.T) {
			g := newRig(t, withMulticall(on))
			first := perpl.EncodeGetPositionsV2(1, 0, 100)
			out, err := g.chain.answer(first)
			if err != nil {
				t.Fatal(err)
			}
			page, err := perpl.DecodePositionPage(out)
			if err != nil {
				t.Fatal(err)
			}
			next := page.Positions[len(page.Positions)-1].NextNodeID
			// the page that follows points back at itself
			g.chain.replies["0x"+hex.EncodeToString(perpl.EncodeGetPositionsV2(1, next, 100))] = reply{out: out}
			err = g.r.refreshOnce(context.Background())
			if err == nil || !strings.Contains(err.Error(), "loop at node") {
				t.Fatalf("error %v", err)
			}
			if g.r.Ready() {
				t.Fatal("snapshot built from a looping list")
			}
		})
	}
}

func TestMulticallDropsRetiredMarketsWithoutFailing(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.refresh()
	g.chain.setHead(pinnedBlock + 3)
	ids := slices.DeleteFunc(slices.Clone(fixturePerpIDs(t)), func(id uint64) bool { return id == 150 })
	setBitmap(g, ids)
	g.chain.fail = func(sel string, d []byte) error {
		if (sel == selector(t, "getPerpetualInfoV2") || sel == selector(t, "getPositionsV2")) && perpArg(d) == 150 {
			return &rpc.Error{Code: rpc.CodeRevert, Message: "execution reverted"}
		}
		return nil
	}
	s := g.refresh()
	if len(s.Markets) != 16 {
		t.Fatalf("%d markets", len(s.Markets))
	}
	if _, ok := s.Market(150); ok {
		t.Fatal("retired market is still listed")
	}
}

func TestMulticallReadsMarketsThatAppearInTheBitmap(t *testing.T) {
	g := newRig(t, withMulticall(true))
	g.refresh()
	g.chain.setHead(pinnedBlock + 3)

	// perp 160 answers like perp 150 so the fixtures can serve it
	clone := func(data150, data160 []byte) {
		out, err := g.chain.answer(data150)
		if err != nil {
			t.Fatal(err)
		}
		g.chain.replies["0x"+hex.EncodeToString(data160)] = reply{out: out}
	}
	clone(perpl.EncodeGetPerpetualInfoV2(150), perpl.EncodeGetPerpetualInfoV2(160))
	clone(perpl.EncodeGetPositionsV2(150, 0, 100), perpl.EncodeGetPositionsV2(160, 0, 100))
	clone(perpl.EncodeGetLiquidationInfo(150), perpl.EncodeGetLiquidationInfo(160))
	margin150, err := perpl.EncodeGetMarginFractions(150, new(big.Int))
	if err != nil {
		t.Fatal(err)
	}
	margin160, err := perpl.EncodeGetMarginFractions(160, new(big.Int))
	if err != nil {
		t.Fatal(err)
	}
	clone(margin150, margin160)
	setBitmap(g, append(slices.Clone(fixturePerpIDs(t)), 160))

	before := len(g.chain.multicalls)
	s := g.refresh()
	if len(s.Markets) != 18 {
		t.Fatalf("%d markets", len(s.Markets))
	}
	added, ok := s.Market(160)
	if !ok || added.Market.PerpID != 160 || added.Market.Symbol != s.Markets[len(s.Markets)-2].Market.Symbol {
		t.Fatalf("new market %+v", added)
	}
	extra := g.chain.multicalls[before:]
	if len(extra[1]) != 2 || perpArg(extra[1][0].Data) != 160 {
		t.Fatalf("catch-up multicall %v", selectorsOf(extra[1]))
	}
	for _, c := range extra[1] {
		if c.AllowFailure {
			t.Fatal("a market the bitmap lists may not fail quietly")
		}
	}
}

func TestMulticallSplitsLargeBatches(t *testing.T) {
	plain := newRig(t, withMulticall(false))
	g := newRig(t, withMulticall(true))
	g.r.batchLimit = 7
	want := describe(t, plain.refresh())
	sameSnapshot(t, want, describe(t, g.refresh()))
	for _, calls := range g.chain.multicalls {
		if len(calls) > 7 {
			t.Fatalf("multicall of %d calls with a limit of 7", len(calls))
		}
	}
	if n := len(g.chain.multicalls); n <= 5 {
		t.Fatalf("%d multicalls, the limit did not split anything", n)
	}
}

func TestMulticallHonoursMaxInflight(t *testing.T) {
	g := newRig(t, withMulticall(true), func(o *Options) { o.Config.MaxInflightCalls = 2 })
	g.r.batchLimit = 9
	g.chain.delay = 2 * time.Millisecond
	g.refresh()
	if got := g.chain.maxInflight.Load(); got != 2 {
		t.Fatalf("max in flight %d with a limit of 2", got)
	}
}

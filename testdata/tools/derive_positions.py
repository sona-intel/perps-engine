"""Computes expected per-position entry, MMR, liquidation and bankruptcy prices exactly (plan section 4)"""
import glob
import os
import sys
from fractions import Fraction as F

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import TESTDATA, decode_outputs, read_json, sig18, write_json  # noqa: E402

Q = 65536


def entry_pns(side, price_pns, residue):
    if residue == 0:
        return F(price_pns)
    base = price_pns - 1 if side == 1 and price_pns >= 1 else price_pns
    return F(base) + F(residue, Q)


def assess(p, mark, pd, ld, cd, mmf):
    s = 1 if p["positionType"] == 0 else -1
    E = entry_pns(s, p["pricePNS"], p["priceResiduePNSQ16"]) / 10**pd
    L = F(p["lotLNS"], 10**ld)
    dep = F(p["depositCNS"], 10**cd)
    prem = F(p["premiumPnlCNS"], 10**cd)
    mmr = E * L / mmf
    liq = max(F(0), E + s * (mmr - dep - prem) / L)
    bk = max(F(0), E - s * (dep + prem) / L)
    notional = L * mark
    return s, E, L, dep, prem, mmr, liq, bk, notional


def build(pid):
    cdir = os.path.join(TESTDATA, "calls")
    info = decode_outputs("getPerpetualInfoV2", read_json(cdir + "/getPerpetualInfoV2.perp%d.json" % pid)["result"])["perpetualInfo"]
    mf = decode_outputs("getMarginFractions", read_json(cdir + "/getMarginFractions.perp%d.lot0.json" % pid)["result"])
    pages = sorted(glob.glob(cdir + "/getPositionsV2.perp%d.page[0-9][0-9].json" % pid))
    if not pages:
        return None
    pd, ld = info["priceDecimals"], info["lotDecimals"]
    cd = decode_outputs("getExchangeInfo", read_json(cdir + "/getExchangeInfo.json")["result"])["collateralDecimals"]
    mmf = F(mf["perpMaintMarginFracHdths"], 100)
    rows = []
    marks = set()
    complete = True
    for f in pages:
        r = decode_outputs("getPositionsV2", read_json(f)["result"])
        marks.add((r["markPricePNS"], r["markPriceValid"]))
        real = r["positions"][: r["numPositions"]]
        complete = bool(real) and real[-1]["nextNodeId"] == 0
        rows.extend(real)
    assert len(marks) == 1, marks
    mark_pns, valid = marks.pop()
    mark = F(mark_pns, 10**pd)
    out = []
    for p in rows:
        assert p["lotLNS"] > 0
        s, E, L, dep, prem, mmr, liq, bk, N = assess(p, mark, pd, ld, cd, mmf)
        out.append(
            {
                "account_id": str(p["accountId"]),
                "position_type": str(p["positionType"]),
                "lot_lns": str(p["lotLNS"]),
                "price_pns": str(p["pricePNS"]),
                "price_residue_pnsq16": str(p["priceResiduePNSQ16"]),
                "deposit_cns": str(p["depositCNS"]),
                "premium_pnl_cns": str(p["premiumPnlCNS"]),
                "entry": sig18(E),
                "size": sig18(L),
                "mmr": sig18(mmr),
                "liq_price": sig18(liq),
                "bankruptcy_price": sig18(bk),
                "notional": sig18(N),
                "leverage": sig18(N / dep) if dep > 0 else None,
                "distance": sig18(s * (mark - liq) / mark),
            }
        )
    return {
        "perp_id": pid,
        "symbol": info["symbol"],
        "block": read_json(pages[0])["block"],
        "price_decimals": pd,
        "lot_decimals": ld,
        "collateral_decimals": cd,
        "maint_margin_fraction_hdths": str(mf["perpMaintMarginFracHdths"]),
        "mmf": sig18(mmf),
        "mark_pns": str(mark_pns),
        "mark": sig18(mark),
        "mark_price_valid": valid,
        "info_mark_pns": str(info["markPNS"]),
        "insurance_balance_cns": str(info["insuranceBalanceCNS"]),
        "pages": len(pages),
        "positions_total": len(out),
        "complete": complete,
        "positions": out,
    }


if __name__ == "__main__":
    ids = read_json(os.path.join(TESTDATA, "chain.json"))["perp_ids"]
    total = 0
    for pid in ids:
        d = build(pid)
        if d is None:
            continue
        write_json(os.path.join(TESTDATA, "derived", "positions.perp%d.json" % pid), d, indent=1)
        total += d["positions_total"]
        print(pid, d["symbol"], d["positions_total"], "pages", d["pages"], "complete", d["complete"], "mark", d["mark"])
    print("total positions", total)

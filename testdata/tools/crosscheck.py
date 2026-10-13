"""Writes derived/sdk_goldens.json and cross-checks the exact math against the SDK vectors and the research float code"""
import glob
import os
import sys
from decimal import ROUND_DOWN, ROUND_HALF_UP, Decimal, localcontext
from fractions import Fraction as F

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from derive_positions import assess, entry_pns  # noqa: E402
from lib import TESTDATA, read_json, sig18, write_json  # noqa: E402

DER = os.path.join(TESTDATA, "derived")


def liq_bk(side, E, L, dep, prem, mmf):
    s = 1 if side == 0 else -1
    mmr = E * L / mmf
    return mmr, max(F(0), E + s * (mmr - dep - prem) / L), max(F(0), E - s * (dep + prem) / L)


def d(x):
    return F(Decimal(x))


# SDK vectors from state_position.rs tests
ENTRY_VECTORS = [
    # name, side (0 long 1 short), price_pns, residue, price_decimals, sdk expected entry (as asserted in the test)
    ("long_1_res0", 0, 1, 0, 4, "0.0001"),
    ("long_1_res1", 0, 1, 1, 4, "0.00000000152587890625"),
    ("long_10001_res8", 0, 10001, 8, 4, "1.00000001220703125"),
    ("long_10000_res65535", 0, 10000, 65535, 4, "0.9999999984741210937"),
    ("short_1_res0", 1, 1, 0, 4, "0.0001"),
    ("short_0_res1", 1, 0, 1, 4, "0.00000000152587890625"),
    ("short_10000_res8", 1, 10000, 8, 4, "1.00000001220703125"),
    ("short_9999_res65535", 1, 9999, 65535, 4, "0.9999999984741210937"),
    ("long_half_lsb_pd6", 0, 100000000, 32768, 6, "99.9999995"),
    ("short_half_lsb_pd6", 1, 100000000, 32768, 6, "100.0000005"),
]
# premium is the funding effect on premium PnL in collateral units, SDK entry 100 size 10 deposit 100 mm 20
LIQ_VECTORS = [
    # name, side, premium, expected mmr, liq, bankruptcy
    ("long_base", 0, 0, "50", "95", "90"),
    ("short_base", 1, 0, "50", "105", "110"),
    ("long_funding_paid", 0, -50, "50", "100", "95"),
    ("short_funding_paid", 1, -50, "50", "100", "105"),
    ("long_funding_received", 0, 50, "50", "90", "85"),
    ("short_funding_received", 1, 50, "50", "110", "115"),
]
MMR_VECTORS = [
    # name, entry, size, mmf, expected mmr
    ("mm20_e100_s10", "100", "10", "20", "50"),
    ("mm20_e80_s10", "80", "10", "20", "40"),
    ("mm20_e80_s20", "80", "20", "20", "80"),
    ("mm10_e80_s20", "80", "20", "10", "160"),
]

out = {"entry_price": [], "liquidation": [], "mmr": []}
worst_entry = F(0)
for name, side, pns, res, pdc, sdk in ENTRY_VECTORS:
    exact = entry_pns(1 if side == 0 else -1, pns, res) / 10**pdc
    diff = abs(exact - d(sdk)) / exact
    worst_entry = max(worst_entry, diff)
    assert diff < F(1, 10**18), (name, diff)
    out["entry_price"].append(
        {
            "name": name,
            "position_type": str(side),
            "price_pns": str(pns),
            "price_residue_pnsq16": str(res),
            "price_decimals": pdc,
            "sdk_asserted": sdk,
            "exact": sig18(exact, 30),
        }
    )
for name, side, prem, mmr_e, liq_e, bk_e in LIQ_VECTORS:
    E, L, dep, mmf = F(100), F(10), F(100), F(20)
    mmr, liq, bk = liq_bk(side, E, L, dep, F(prem), mmf)
    assert (mmr, liq, bk) == (d(mmr_e), d(liq_e), d(bk_e)), name
    out["liquidation"].append(
        {
            "name": name,
            "position_type": str(side),
            "entry": "100",
            "size": "10",
            "deposit": "100",
            "premium_pnl": str(prem),
            "mmf": "20",
            "mmr": mmr_e,
            "liq_price": liq_e,
            "bankruptcy_price": bk_e,
        }
    )
for name, e, s, mmf, exp in MMR_VECTORS:
    assert d(e) * d(s) / d(mmf) == d(exp), name
    out["mmr"].append({"name": name, "entry": e, "size": s, "mmf": mmf, "mmr": exp})
write_json(os.path.join(DER, "sdk_goldens.json"), out)
print("sdk vectors reproduced exactly: entry %d, liquidation %d, mmr %d" % (len(out["entry_price"]), len(out["liquidation"]), len(out["mmr"])))
print("worst relative gap of SDK asserted entry vs exact: %.3e" % float(worst_entry))


def round_sig(x, digits, mode):
    with localcontext() as ctx:
        ctx.prec = digits
        ctx.rounding = mode
        return +Decimal(x.numerator) / Decimal(x.denominator)


def fixed(x, mode=ROUND_HALF_UP):
    return F(round_sig(x, 19, mode))


worst_float = F(0)
worst_sdk = F(0)
n = 0
for path in sorted(glob.glob(os.path.join(DER, "positions.perp*.json"))):
    base = read_json(path)
    cd, pd, ld = base["collateral_decimals"], base["price_decimals"], base["lot_decimals"]
    mmf = F(int(base["maint_margin_fraction_hdths"]), 100)
    mark = F(int(base["mark_pns"]), 10**pd)
    for p in base["positions"]:
        raw = {
            "positionType": int(p["position_type"]),
            "pricePNS": int(p["price_pns"]),
            "priceResiduePNSQ16": int(p["price_residue_pnsq16"]),
            "lotLNS": int(p["lot_lns"]),
            "depositCNS": int(p["deposit_cns"]),
            "premiumPnlCNS": int(p["premium_pnl_cns"]),
        }
        s, E, L, dep, prem, mmr, liq, bk, _ = assess(raw, mark, pd, ld, cd, mmf)
        assert sig18(liq) == p["liq_price"] and sig18(bk) == p["bankruptcy_price"]
        # research code positions2.py in binary floats
        pp = raw["pricePNS"]
        if raw["priceResiduePNSQ16"]:
            pp = (pp - 1 if (s == 1 and pp >= 1) else pp) + raw["priceResiduePNSQ16"] / 65536
        entry_f = pp / 10**pd
        lot_f = raw["lotLNS"] / 10**ld
        mmr_f = entry_f * lot_f * 100 / int(base["maint_margin_fraction_hdths"])
        liq_f = entry_f + s * (mmr_f - raw["depositCNS"] / 1e6 - raw["premiumPnlCNS"] / 1e6) / lot_f
        liq_f = max(0.0, liq_f)
        if liq > 0:
            worst_float = max(worst_float, abs(F(liq_f) - liq) / liq)
        # fixed 19 digit emulation of the SDK types, rounding mode half up (fastnum default is assumed)
        e19 = fixed(E, ROUND_DOWN)
        mmr19 = fixed(e19 * L / mmf)
        liq19 = max(F(0), fixed(e19 + fixed(s * (mmr19 - dep - prem) / L)))
        bk19 = max(F(0), fixed(e19 - fixed(s * (dep + prem) / L)))
        if liq > 0:
            worst_sdk = max(worst_sdk, abs(liq19 - liq) / liq)
        if bk > 0:
            worst_sdk = max(worst_sdk, abs(bk19 - bk) / bk)
        n += 1
print("positions checked", n)
print("worst relative gap, exact vs research float code (liq price): %.3e" % float(worst_float))
print("worst relative gap, exact vs 19 digit SDK emulation (liq and bankruptcy): %.3e" % float(worst_sdk))

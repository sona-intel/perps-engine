"""Records the raw eth_call fixtures for one pinned block (head minus 5) through the local router"""
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import (  # noqa: E402
    EXCHANGE,
    FALLBACK,
    FUNCS,
    IMPL_SLOT,
    ROUTER,
    TESTDATA,
    Client,
    decode_outputs,
    encode_call,
    read_json,
    write_json,
)

CALLS = os.path.join(TESTDATA, "calls")
PAGE = 100
FULL_PAGES = (1, 10)
LOT_LADDER = (0, 1, 1000, 10**6, 10**9, 10**12)
LADDER_PERPS = (1, 10, 20)
NO_ACCOUNT = "0x000000000000000000000000000000000000dEaD"
MISSING_ID = 999999999

c = Client(ROUTER)
try:
    head = int(c.result("eth_blockNumber", []), 16)
except RuntimeError:
    c = Client(FALLBACK)
    head = int(c.result("eth_blockNumber", []), 16)
blk = head - 5
blkhex = hex(blk)
block = c.result("eth_getBlockByNumber", [blkhex, False])
manifest = []


def record(fn, args, tag=None, perp=None):
    names = [i["name"] for i in FUNCS[fn]["inputs"]]
    data = encode_call(fn, list(args))
    d = c.post("eth_call", [{"to": EXCHANGE, "data": data}, blkhex])
    entry = {
        "function": fn,
        "args": dict(zip(names, [str(a) for a in args])),
        "block": blk,
        "request": {"to": EXCHANGE, "data": data},
    }
    if "error" in d:
        entry["error"] = d["error"]
    else:
        entry["result"] = d["result"]
    name = fn + ("." + tag if tag else "")
    write_json(os.path.join(CALLS, name + ".json"), entry)
    manifest.append({"name": name, "function": fn, "perp_id": perp, "error": "error" in d})
    return entry


def ok(entry):
    if "error" in entry:
        raise RuntimeError("%s failed: %s" % (entry["function"], str(entry["error"])[:200]))
    return decode_outputs(entry["function"], entry["result"])


t0 = time.time()
info = ok(record("getExchangeInfo", ()))
cd = info["collateralDecimals"]
bitmap = ok(record("getPerpetualExistsBitmap", ()))["bitmap"]
ok(record("getFundingInterval", ()))
perps = [w * 256 + b for w, word in enumerate(bitmap) for b in range(256) if word >> b & 1]
print("block", blk, "collateralDecimals", cd, "perps", perps, flush=True)

pos = {}
mf0 = {}
for pid in perps:
    pi = ok(record("getPerpetualInfoV2", (pid,), "perp%d" % pid, pid))["perpetualInfo"]
    mf0[pid] = ok(record("getMarginFractions", (pid, 0), "perp%d.lot0" % pid, pid))
    ok(record("getLiquidationInfo", (pid,), "perp%d" % pid, pid))
    ids = ok(record("getPositionIds", (pid,), "perp%d" % pid, pid))
    cur = ids["startNodeId"]
    page = 0
    all_pos = []
    while cur:
        r = ok(record("getPositionsV2", (pid, cur, PAGE), "perp%d.page%02d" % (pid, page), pid))
        ps = r["positions"][: r["numPositions"]]
        all_pos.extend(ps)
        page += 1
        nxt = ps[-1]["nextNodeId"] if ps else 0
        if pid not in FULL_PAGES or not ps or nxt == 0 or nxt == cur:
            break
        cur = nxt
    pos[pid] = all_pos
    print(pid, pi["symbol"], "status", pi["status"], "positions read", len(all_pos), "pages", page, flush=True)

for pid in LADDER_PERPS:
    if pid in perps:
        oi_max = mf0[pid]["oiMaxLNS"]
        lots = set(LOT_LADDER[1:]) | {oi_max // 10, oi_max // 2, oi_max, 2 * oi_max}
        for lot in sorted(lots):
            record("getMarginFractions", (pid, lot), "perp%d.lot%d" % (pid, lot), pid)


def top_account(pid):
    best = max(pos[pid], key=lambda p: p["lotLNS"])
    return best["accountId"]


record("getPositionsV2", (1, 0, PAGE), "perp1.start0", 1)
record("getPositionsV2", (30, 0, PAGE), "perp30.empty", 30)
btc_top = top_account(1)
mon_top = top_account(10)
by_id = {}
for tag, aid in (("btc_top", btc_top), ("mon_top", mon_top), ("id1", 1)):
    by_id[tag] = ok(record("getAccountById", (aid,), tag))["accountInfo"]
record("getAccountById", (MISSING_ID,), "missing")
record("getAccountByAddr", (by_id["btc_top"]["accountAddr"],), "btc_top")
record("getAccountByAddr", (NO_ACCOUNT,), "no_account")

slot = c.result("eth_getStorageAt", [EXCHANGE, IMPL_SLOT, blkhex])
write_json(
    os.path.join(CALLS, "getStorageAt.implementation.json"),
    {
        "method": "eth_getStorageAt",
        "block": blk,
        "request": {"address": EXCHANGE, "slot": IMPL_SLOT},
        "result": slot,
    },
)
write_json(
    os.path.join(CALLS, "eth_getBlockByNumber.pinned.json"),
    {"method": "eth_getBlockByNumber", "block": blk, "request": {"params": [blkhex, False]}, "result": block},
)
write_json(
    os.path.join(TESTDATA, "chain.json"),
    {
        "chain": "eip155:143",
        "exchange": EXCHANGE,
        "pinned_block": blk,
        "pinned_block_hex": blkhex,
        "pinned_block_hash": block["hash"],
        "pinned_block_timestamp": int(block["timestamp"], 16),
        "head_when_pinned": head,
        "perp_ids": perps,
        "collateral_decimals": cd,
        "implementation_slot": IMPL_SLOT,
        "implementation_address": "0x" + slot[-40:],
        "router_credit_sources": sorted(c.credit_headers),
        "eth_calls": c.calls,
        "router_paid_calls": c.paid_calls,
    },
)
manifest.append({"name": "getStorageAt.implementation", "function": "eth_getStorageAt", "perp_id": None, "error": False})
manifest.append({"name": "eth_getBlockByNumber.pinned", "function": "eth_getBlockByNumber", "perp_id": None, "error": False})
write_json(os.path.join(TESTDATA, "manifest.json"), {"pinned_block": blk, "calls": manifest})
print("done in %.0fs, %d rpc calls, credit sources %s" % (time.time() - t0, c.calls, sorted(c.credit_headers)))

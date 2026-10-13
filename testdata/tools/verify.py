"""Offline consistency checks over all fixtures"""
import glob
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import EXCHANGE, FUNCS, TESTDATA, decode_event_data, decode_outputs, jsonable, read_json, selector, topic0  # noqa: E402

T = TESTDATA
checks = 0


def check(cond, msg):
    global checks
    checks += 1
    if not cond:
        print("FAIL", msg)
        sys.exit(1)


sel = read_json(os.path.join(T, "selectors.json"))
chain = read_json(os.path.join(T, "chain.json"))
for name in FUNCS:
    check(sel["functions"][name] == "0x" + selector(name), name)
check(sel["events"]["PositionLiquidated"] == topic0("PositionLiquidated"), "topic0")

raw_files = [p for p in glob.glob(os.path.join(T, "calls", "*.json")) if not p.endswith(".expected.json")]
n_ok = n_err = 0
for p in sorted(raw_files):
    raw = read_json(p)
    exp = p[:-5] + ".expected.json"
    if "method" in raw:
        check(raw["block"] == chain["pinned_block"], p)
        continue
    check(raw["block"] == chain["pinned_block"], p)
    check(raw["request"]["to"] == EXCHANGE, p)
    check(raw["request"]["data"].startswith("0x" + selector(raw["function"])), p)
    if "error" in raw:
        check(not os.path.exists(exp) and raw["error"]["code"] == 3, p)
        n_err += 1
        continue
    check(read_json(exp) == jsonable(decode_outputs(raw["function"], raw["result"])), p)
    n_ok += 1
print("calls decoded", n_ok, "reverts", n_err)

for pid in chain["perp_ids"]:
    info = read_json(os.path.join(T, "calls", "getPerpetualInfoV2.perp%d.expected.json" % pid))["perpetualInfo"]
    longs = shorts = 0
    for f in sorted(glob.glob(os.path.join(T, "calls", "getPositionsV2.perp%d.page[0-9][0-9].expected.json" % pid))):
        r = read_json(f)
        for q in r["positions"][: int(r["numPositions"])]:
            if q["positionType"] == "0":
                longs += int(q["lotLNS"])
            else:
                shorts += int(q["lotLNS"])
    check(longs == shorts == int(info["longOpenInterestLNS"]) == int(info["shortOpenInterestLNS"]), "OI perp %d" % pid)
print("lots equal contract OI in", len(chain["perp_ids"]), "markets")

mf = {}
for f in glob.glob(os.path.join(T, "calls", "getMarginFractions.perp[0-9]*.lot[0-9]*.expected.json")):
    pid = os.path.basename(f).split(".")[1]
    mf.setdefault(pid, set()).add(read_json(f)["perpMaintMarginFracHdths"])
check(all(len(v) == 1 for v in mf.values()), "maintenance fraction depends on lot")
print("maintenance fraction constant over all lots in", len(mf), "markets")

exp = read_json(os.path.join(T, "liquidations", "expected.json"))
rawlogs = [lg for q in read_json(os.path.join(T, "liquidations", "getlogs.raw.json")) for lg in q["result"]]
txs = read_json(os.path.join(T, "liquidations", "transactions.raw.json"))
check(len(exp) == len(rawlogs) == 20, "20 logs")
for e, lg in zip(exp, sorted(rawlogs, key=lambda x: (int(x["blockNumber"], 16), int(x["logIndex"], 16)))):
    check(e["event"] == jsonable(decode_event_data("PositionLiquidated", lg["data"])), "event decode")
    check(e["transactionFrom"] == txs[lg["transactionHash"]]["from"].lower(), "tx from")
print("liquidation logs", len(exp), "txs", len(txs))

hs = read_json(os.path.join(T, "hypersync", "expected.json"))
check(len(hs) == 10, "hypersync count")
for name, n in (("leaderboard_day_vol", 50), ("leaderboard_all_vol", 50)):
    check(len(read_json(os.path.join(T, "rest", name + ".json"))["d"]) == n, name)

total = 0
for dirpath, _, files in os.walk(T):
    total += sum(os.path.getsize(os.path.join(dirpath, f)) for f in files)
check(total < 5 * 1024 * 1024, "size")
print("all %d checks passed, fixture bytes %d" % (checks, total))

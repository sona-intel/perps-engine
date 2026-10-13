"""Records 20 raw PositionLiquidated logs with their transactions and blocks, then decodes them"""
import collections
import json
import os
import random
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import (  # noqa: E402
    EXCHANGE,
    FALLBACK,
    ROUTER,
    TESTDATA,
    Client,
    decode_event_data,
    jsonable,
    topic0,
    write_json,
)

HISTORY = sys.argv[1]
OUT = os.path.join(TESTDATA, "liquidations")
rows = json.load(open(HISTORY))
per_block = collections.Counter(r["bn"] for r in rows)
rng = random.Random(143)

# one single-event block per market, the rest spread over time, plus one block with three events
singles = [r for r in rows if per_block[r["bn"]] == 1]
chosen = {}
for perp in sorted({r["perp"] for r in rows}):
    pool = [r for r in singles if r["perp"] == perp]
    pick = pool[len(pool) // 2]
    chosen[pick["bn"]] = pick
rest = sorted(singles, key=lambda r: r["bn"])
step = len(rest) // 8
for k in range(8):
    if len(chosen) >= 17:
        break
    pick = rest[k * step + step // 2]
    chosen.setdefault(pick["bn"], pick)
triples = sorted({r["bn"] for r in rows if per_block[r["bn"]] == 3})
tri = triples[len(triples) // 2]
blocks = sorted(chosen) + [tri]
assert sum(per_block[b] for b in blocks) == 20, sum(per_block[b] for b in blocks)

c = Client(ROUTER)
topic = topic0("PositionLiquidated")
requests = []
logs = []
for bn in blocks:
    params = [{"fromBlock": hex(bn), "toBlock": hex(bn), "address": EXCHANGE, "topics": [topic]}]
    res = c.result("eth_getLogs", params)
    assert len(res) == per_block[bn], (bn, len(res))
    requests.append({"method": "eth_getLogs", "params": params, "result": res})
    logs.extend(res)
write_json(os.path.join(OUT, "getlogs.raw.json"), requests)

txs = {}
for lg in logs:
    h = lg["transactionHash"]
    if h not in txs:
        txs[h] = c.result("eth_getTransactionByHash", [h])
write_json(os.path.join(OUT, "transactions.raw.json"), txs)

blks = {}
for bn in blocks:
    blks[hex(bn)] = c.result("eth_getBlockByNumber", [hex(bn), False])
write_json(os.path.join(OUT, "blocks.raw.json"), blks)

expected = []
for lg in sorted(logs, key=lambda x: (int(x["blockNumber"], 16), int(x["logIndex"], 16))):
    assert lg["topics"] == [topic] and lg["address"].lower() == EXCHANGE.lower() and not lg["removed"]
    bhex = hex(int(lg["blockNumber"], 16))
    expected.append(
        {
            "blockNumber": str(int(lg["blockNumber"], 16)),
            "logIndex": str(int(lg["logIndex"], 16)),
            "transactionHash": lg["transactionHash"],
            "blockTimestamp": str(int(blks[bhex]["timestamp"], 16)),
            "transactionFrom": txs[lg["transactionHash"]]["from"].lower(),
            "event": jsonable(decode_event_data("PositionLiquidated", lg["data"])),
        }
    )
write_json(os.path.join(OUT, "expected.json"), expected)
print("blocks", len(blocks), "logs", len(logs), "distinct txs", len(txs), "credit sources", sorted(c.credit_headers))
print("perps", sorted(collections.Counter(e["event"]["perpId"] for e in expected).items()))
print("sides", collections.Counter(e["event"]["positionType"] for e in expected))
print("onOrderBook", collections.Counter(e["event"]["onOrderBook"] for e in expected))
print("keepers", collections.Counter(e["transactionFrom"] for e in expected))

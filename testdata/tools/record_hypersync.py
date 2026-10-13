"""Records one HyperSync page of PositionLiquidated logs. The API key comes from the environment and is only sent as a header"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import EXCHANGE, TESTDATA, UA, decode_event_data, jsonable, topic0, write_json  # noqa: E402

HISTORY = sys.argv[1]
OUT = os.path.join(TESTDATA, "hypersync")
URL = "https://monad.hypersync.xyz/query"
rows = sorted(json.load(open(HISTORY)), key=lambda r: (r["bn"], r["tx"]))
blocks = sorted({r["bn"] for r in rows})
first = len(blocks) * 4 // 5
start = blocks[first]
count = 0
end = start
for b in blocks[first:]:
    n = sum(1 for r in rows if r["bn"] == b)
    if count + n > 10:
        break
    count += n
    end = b
to_block = end + 1

request = {
    "from_block": start,
    "to_block": to_block,
    "logs": [{"address": [EXCHANGE.lower()], "topics": [[topic0("PositionLiquidated")]]}],
    "field_selection": {
        "log": ["block_number", "log_index", "transaction_index", "transaction_hash", "address", "data", "topic0"],
        "block": ["number", "hash", "timestamp"],
        "transaction": ["block_number", "transaction_index", "hash", "from"],
    },
    "max_num_logs": 1000,
}
key = os.environ["ENVIO_HYPERSYNC_KEY"]
payload = json.dumps(request).encode()
headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json", "User-Agent": UA}
delay = 8
for attempt in range(8):
    try:
        with urllib.request.urlopen(urllib.request.Request(URL, data=payload, headers=headers), timeout=120) as r:
            status = r.status
            body = json.loads(r.read())
        break
    except urllib.error.HTTPError as e:
        print("HTTP", e.code, flush=True)
        if e.code not in (429, 502, 503):
            sys.exit(1)
        time.sleep(delay)
        delay = min(delay * 2, 60)
else:
    sys.exit(1)

write_json(os.path.join(OUT, "request.json"), request)
write_json(os.path.join(OUT, "response.json"), body)

def num(v):
    return int(v, 16) if isinstance(v, str) else v

tsmap = {}
fromof = {}
logs = []
for item in body["data"]:
    for b in item.get("blocks", []):
        tsmap[num(b["number"])] = num(b["timestamp"])
    for t in item.get("transactions", []):
        fromof[t["hash"]] = t["from"].lower()
    logs.extend(item["logs"])
expected = []
for lg in sorted(logs, key=lambda x: (num(x["block_number"]), num(x["log_index"]))):
    bn = num(lg["block_number"])
    expected.append(
        {
            "blockNumber": str(bn),
            "logIndex": str(num(lg["log_index"])),
            "transactionHash": lg["transaction_hash"],
            "blockTimestamp": str(tsmap[bn]),
            "transactionFrom": fromof[lg["transaction_hash"]],
            "event": jsonable(decode_event_data("PositionLiquidated", lg["data"])),
        }
    )
write_json(os.path.join(OUT, "expected.json"), expected)
print("status", status, "from", start, "to(exclusive)", to_block, "logs", len(logs), "expected rows", len(expected))
print("response keys", sorted(body.keys()), "next_block", body.get("next_block"), "archive_height", body.get("archive_height"))
print("data items", len(body["data"]), {k: len(v) for k, v in body["data"][0].items()})
print("research window count", sum(1 for r in rows if start <= r["bn"] < to_block))

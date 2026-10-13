"""Finds the first block with code at the Exchange proxy by binary search, and cross-checks with the creation transaction"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import EXCHANGE, ROUTER, ROUTER_HISTORY, TESTDATA, Client, read_json, write_json  # noqa: E402

CREATION_TX = sys.argv[1]
c = Client(ROUTER_HISTORY)
live = Client(ROUTER)
hi = read_json(os.path.join(TESTDATA, "chain.json"))["pinned_block"]
lo = 0
steps = 0


def has_code(n):
    code = c.result("eth_getCode", [EXCHANGE, hex(n)])
    return code not in ("0x", "0x0")


assert has_code(hi)
while lo + 1 < hi:
    mid = (lo + hi) // 2
    steps += 1
    if has_code(mid):
        hi = mid
    else:
        lo = mid
print("first block with code", hi, "after", steps, "steps; credit sources", sorted(c.credit_headers))
tx = live.result("eth_getTransactionByHash", [CREATION_TX])
rc = live.result("eth_getTransactionReceipt", [CREATION_TX])
blk = live.result("eth_getBlockByNumber", [hex(hi), False])
res = {
    "exchange_proxy": EXCHANGE,
    "deploy_block": hi,
    "deploy_block_hash": blk["hash"],
    "deploy_block_timestamp": int(blk["timestamp"], 16),
    "found_by": "binary search on eth_getCode via the history router, %d steps" % steps,
    "creation_tx": CREATION_TX,
    "creation_tx_block": int(tx["blockNumber"], 16),
    "creation_tx_from": tx["from"].lower(),
    "receipt_contract_address": (rc.get("contractAddress") or "").lower(),
}
write_json(os.path.join(TESTDATA, "deploy.json"), res)
print(res)

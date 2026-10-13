"""Decodes every recorded eth_call result into <name>.expected.json (numbers as decimal strings)"""
import glob
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import TESTDATA, decode_outputs, jsonable, read_json, write_json  # noqa: E402

n = 0
for path in sorted(glob.glob(os.path.join(TESTDATA, "calls", "*.json"))):
    if path.endswith(".expected.json"):
        continue
    raw = read_json(path)
    out = path[: -len(".json")] + ".expected.json"
    if raw.get("method") == "eth_getStorageAt":
        write_json(out, {"implementation": "0x" + raw["result"][-40:]})
    elif "method" in raw:
        continue
    elif "error" in raw:
        if os.path.exists(out):
            os.remove(out)
        continue
    else:
        write_json(out, jsonable(decode_outputs(raw["function"], raw["result"])))
    n += 1
print("expected files written", n)

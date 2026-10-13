"""Writes testdata/selectors.json from the ABI subset"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import EVENTS, EXCHANGE, FUNCS, TESTDATA, selector, signature, topic0, write_json  # noqa: E402

out = {
    "contract": EXCHANGE,
    "functions": {n: "0x" + selector(n) for n in sorted(FUNCS)},
    "events": {n: topic0(n) for n in sorted(EVENTS)},
    "signatures": {n: signature(x) for n, x in sorted({**FUNCS, **EVENTS}.items())},
}
write_json(os.path.join(TESTDATA, "selectors.json"), out)

"""Shared helpers for the fixture tools. Standard library only."""
import json
import os
import sys
import time
import urllib.error
import urllib.request
from decimal import Decimal, localcontext, ROUND_HALF_EVEN
from fractions import Fraction

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from keccak import keccak256  # noqa: E402

TESTDATA = os.path.dirname(HERE)
ABI_PATH = os.path.join(TESTDATA, "..", "internal", "perpl", "exchange.abi.json")
EXCHANGE = "0x34B6552d57a35a1D042CcAe1951BD1C370112a6F"
IMPL_SLOT = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"
ROUTER = "http://127.0.0.1:4000/sona/monad"
ROUTER_HISTORY = "http://127.0.0.1:4000/sona-history/monad"
FALLBACK = "https://rpc2.monad.xyz"
UA = "sona-perps-fixtures"

ABI = json.load(open(ABI_PATH))
FUNCS = {x["name"]: x for x in ABI if x.get("type") == "function"}
EVENTS = {x["name"]: x for x in ABI if x.get("type") == "event"}


def canon(i):
    if i["type"].startswith("tuple"):
        return "(" + ",".join(canon(c) for c in i["components"]) + ")" + i["type"][5:]
    return i["type"]


def signature(item):
    return item["name"] + "(" + ",".join(canon(i) for i in item["inputs"]) + ")"


def selector(name):
    return keccak256(signature(FUNCS[name]).encode())[:8]


def topic0(name):
    return "0x" + keccak256(signature(EVENTS[name]).encode())


def encode_call(name, args):
    fn = FUNCS[name]
    out = selector(name)
    for i, a in zip(fn["inputs"], args):
        if i["type"] == "address":
            out += int(a, 16).to_bytes(32, "big").hex()
        else:
            out += int(a).to_bytes(32, "big").hex()
    return "0x" + out


def is_dyn(t):
    ty = t["type"]
    if ty in ("string", "bytes") or ty.endswith("[]"):
        return True
    if ty.startswith("tuple"):
        if ty.endswith("]"):
            base = dict(t)
            base["type"] = ty[: ty.rindex("[")]
            return is_dyn(base)
        return any(is_dyn(c) for c in t["components"])
    if ty.endswith("]"):
        return is_dyn({"type": ty[: ty.rindex("[")], "components": t.get("components", [])})
    return False


def size_static(t):
    ty = t["type"]
    if ty.startswith("tuple") and not ty.endswith("]"):
        return sum(size_static(c) for c in t["components"])
    if ty.endswith("]") and not ty.endswith("[]"):
        n = int(ty[ty.rindex("[") + 1 : -1])
        base = dict(t)
        base["type"] = ty[: ty.rindex("[")]
        return n * size_static(base)
    return 1


def dec(t, data, off):
    ty = t["type"]
    if ty.endswith("]"):
        base = dict(t)
        base["type"] = ty[: ty.rindex("[")]
        if ty.endswith("[]"):
            n = int.from_bytes(data[off : off + 32], "big")
            off += 32
        else:
            n = int(ty[ty.rindex("[") + 1 : -1])
        res = []
        if is_dyn(base):
            for k in range(n):
                o = int.from_bytes(data[off + 32 * k : off + 32 * k + 32], "big")
                res.append(dec(base, data, off + o))
        else:
            w = size_static(base) * 32
            for k in range(n):
                res.append(dec(base, data, off + k * w))
        return res
    if ty.startswith("tuple"):
        res = {}
        cur = off
        for c in t["components"]:
            if is_dyn(c):
                o = int.from_bytes(data[cur : cur + 32], "big")
                res[c["name"]] = dec(c, data, off + o)
                cur += 32
            else:
                res[c["name"]] = dec(c, data, cur)
                cur += 32 * size_static(c)
        return res
    w = data[off : off + 32]
    if ty == "string":
        n = int.from_bytes(w, "big")
        return data[off + 32 : off + 32 + n].decode()
    if ty == "bytes":
        n = int.from_bytes(w, "big")
        return "0x" + data[off + 32 : off + 32 + n].hex()
    if ty.startswith("uint"):
        return int.from_bytes(w, "big")
    if ty.startswith("int"):
        v = int.from_bytes(w, "big")
        return v - (1 << 256) if v >= 1 << 255 else v
    if ty == "bool":
        return int.from_bytes(w, "big") != 0
    if ty == "address":
        return "0x" + w[12:].hex()
    if ty.startswith("bytes"):
        return "0x" + w.hex()
    raise ValueError(ty)


def decode_outputs(name, result_hex):
    raw = bytes.fromhex(result_hex[2:])
    res = {}
    cur = 0
    for k, o in enumerate(FUNCS[name]["outputs"]):
        if is_dyn(o):
            offv = int.from_bytes(raw[cur : cur + 32], "big")
            v = dec(o, raw, offv)
            cur += 32
        else:
            v = dec(o, raw, cur)
            cur += 32 * size_static(o)
        res[o["name"] or str(k)] = v
    return res


def decode_event_data(name, data_hex):
    raw = bytes.fromhex(data_hex[2:])
    res = {}
    cur = 0
    for i in EVENTS[name]["inputs"]:
        assert not i["indexed"] and not is_dyn(i)
        res[i["name"]] = dec(i, raw, cur)
        cur += 32 * size_static(i)
    assert cur == len(raw), (cur, len(raw))
    return res


def jsonable(v):
    if isinstance(v, bool):
        return v
    if isinstance(v, int):
        return str(v)
    if isinstance(v, dict):
        return {k: jsonable(x) for k, x in v.items()}
    if isinstance(v, list):
        return [jsonable(x) for x in v]
    return v


def write_json(path, obj, indent=2):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=indent, ensure_ascii=False)
        f.write("\n")


def read_json(path):
    with open(path) as f:
        return json.load(f)


class Client:
    def __init__(self, url=ROUTER):
        self.url = url
        self.calls = 0
        self.paid_calls = 0
        self.credit_headers = set()

    def post(self, method, params, url=None, tries=5):
        body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": method, "params": params}).encode()
        delay = 1.0
        last = None
        for _ in range(tries):
            req = urllib.request.Request(
                url or self.url, data=body, headers={"Content-Type": "application/json", "User-Agent": UA}
            )
            try:
                with urllib.request.urlopen(req, timeout=60) as resp:
                    credits = resp.headers.get("X-Erpc-Credits", "")
                    d = json.loads(resp.read())
            except (urllib.error.URLError, TimeoutError, ConnectionError) as e:
                last = e
                time.sleep(delay)
                delay *= 2
                continue
            self.calls += 1
            if credits:
                self.credit_headers.add(credits.split("=")[0].split(";")[0])
            if "drpc" in credits.lower():
                self.paid_calls += 1
                self.url = FALLBACK
                print("warning, router answered through a paid upstream, switching to", FALLBACK, flush=True)
            return d
        raise RuntimeError("rpc failed: %s" % type(last).__name__)

    def result(self, method, params, url=None):
        d = self.post(method, params, url)
        if "error" in d:
            raise RuntimeError("%s: %s" % (method, str(d["error"])[:200]))
        return d["result"]



def sig18(x, digits=18):
    """Fraction to a plain decimal string with at most `digits` significant digits"""
    x = Fraction(x)
    if x == 0:
        return "0"
    with localcontext() as ctx:
        ctx.prec = digits
        ctx.rounding = ROUND_HALF_EVEN
        d = Decimal(x.numerator) / Decimal(x.denominator)
    s = format(d, "f")
    if "." in s:
        s = s.rstrip("0").rstrip(".")
    return s

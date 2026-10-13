"""Records the Perpl public REST samples, keeping the original JSON shape and trimming the leaderboards to the top 50"""
import json
import os
import sys
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from lib import TESTDATA, UA, write_json  # noqa: E402

BASE = "https://app.perpl.xyz/api/v1"
OUT = os.path.join(TESTDATA, "rest")


def get(path):
    req = urllib.request.Request(BASE + path, headers={"User-Agent": UA, "Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.status, json.loads(r.read())


for period in ("day", "all"):
    status, body = get("/trading/leaderboard/%s/vol" % period)
    total = len(body["d"])
    body["d"] = body["d"][:50]
    write_json(os.path.join(OUT, "leaderboard_%s_vol.json" % period), body, indent=None)
    print(period, status, "entries upstream", total, "kept", len(body["d"]), "keys", list(body.keys()))
status, body = get("/market-data/ticker")
write_json(os.path.join(OUT, "ticker.json"), body, indent=None)
print("ticker", status, list(body.keys()))

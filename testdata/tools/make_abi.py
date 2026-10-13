"""Writes the ABI subset the engine uses, copied verbatim from the Perpl SDK Exchange.json"""
import json
import sys

src, dst = sys.argv[1], sys.argv[2]
keep = {
    ("function", "getExchangeInfo"),
    ("function", "getPerpetualExistsBitmap"),
    ("function", "getPerpetualInfoV2"),
    ("function", "getMarginFractions"),
    ("function", "getLiquidationInfo"),
    ("function", "getPositionIds"),
    ("function", "getPositionsV2"),
    ("function", "getAccountByAddr"),
    ("function", "getAccountById"),
    ("function", "getFundingInterval"),
    ("event", "PositionLiquidated"),
}
abi = json.load(open(src))["abi"]
sub = [x for x in abi if (x.get("type"), x.get("name")) in keep]
assert len(sub) == len(keep), (len(sub), len(keep))
with open(dst, "w") as f:
    json.dump(sub, f, indent=2, ensure_ascii=False)
    f.write("\n")
print(len(sub), "entries")

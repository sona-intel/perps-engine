# perps-engine fixtures

Recorded live data for the Exchange proxy `0x34B6552d57a35a1D042CcAe1951BD1C370112a6F` on Monad (`eip155:143`). Every number below was read from the chain, the Perpl public API or HyperSync on 2026-10-12. Raw files are what the wire returned. Expected files are the same data decoded by the Python reference in `tools/`, with every number as a decimal string.

## Pinned block

All `eth_call` and `eth_getStorageAt` fixtures were taken at one block, `head - 5`.

| Field | Value |
|---|---|
| block | 112777356 (`0x6b8d88c`) |
| time | 2026-10-12 14:21:57 UTC, see `chain.json` for the hash and the exact timestamp |
| head when pinned | block + 5 |
| collateral decimals | 6 |
| implementation (ERC-1967 slot `0x360894a1...82bbc`) | `0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a` (value for `config.expected_implementation`) |

`chain.json` holds these values plus the perp id list. `manifest.json` lists every call fixture with its function, perp id and whether it reverted.

## Layout

| Path | Content |
|---|---|
| `selectors.json` | 4-byte selector of each of the 10 functions, topic0 of `PositionLiquidated`, and the canonical signatures. Checked against `methodIdentifiers` of the SDK `Exchange.json`. |
| `../internal/perpl/exchange.abi.json` | The 11 ABI entries the engine uses, copied verbatim from the SDK `Exchange.json` (a JSON array). |
| `chain.json`, `manifest.json`, `deploy.json` | Pinned block facts, call index, deploy block facts. |
| `calls/<function>[.<tag>].json` | Raw call: `function`, `args` (decimal strings, ABI input names), `block`, `request` (`to`, `data`), and `result` (hex) or `error` (the JSON-RPC error as the router returned it). |
| `calls/<same name>.expected.json` | Decoded outputs, an object keyed by the ABI output names (always wrapped, so `getPerpetualInfoV2` is `{"perpetualInfo": {...}}`). Structs are objects, arrays are arrays, uints and ints are decimal strings, bools are JSON bools, addresses are lowercase hex, `uint8` enums are decimal strings. No expected file exists for a revert. |
| `calls/getStorageAt.implementation.json` | Raw `eth_getStorageAt` request and result, expected is `{"implementation": "0x..."}`. |
| `calls/eth_getBlockByNumber.pinned.json` | Raw block object of the pinned block (transaction hashes only, 25 in this block). |
| `derived/positions.perp<N>.json` | Exact per-position results for every market that has positions (see below). |
| `derived/sdk_goldens.json` | The vectors of the SDK unit tests (`state_position.rs`) with SDK and exact values. |
| `liquidations/` | 20 raw `PositionLiquidated` logs, their transactions and blocks, and the decoded expected rows. |
| `hypersync/` | One HyperSync request and response page, plus decoded expected rows. |
| `rest/` | Perpl public API samples. |
| `tools/` | The Python scripts that produced everything. Standard library only. |

### Call files

- `getExchangeInfo`, `getPerpetualExistsBitmap`, `getFundingInterval` (8571 blocks).
- Per perp id, for all 17 ids in the bitmap: `getPerpetualInfoV2.perp<N>`, `getMarginFractions.perp<N>.lot0`, `getLiquidationInfo.perp<N>`, `getPositionIds.perp<N>`.
- `getPositionsV2.perp<N>.page<NN>`: page size 100, first page starts at `startNodeId` from `getPositionIds`, the next page starts at the last real position's `nextNodeId`, and the list ends when that is 0. BTC (perp 1) has 3 pages (100, 100, 46 positions). MON (perp 10) has 1 page of 98. Every other market has fewer than 100 positions, so its first page is the whole market (checked: `nextNodeId` of the last real position is 0 and the lots sum to the open interest in every market).
- Extra edge fixtures: `getPositionsV2.perp1.start0` (page start 0 returns the same first page as `startNodeId`, verified equal) and `getPositionsV2.perp30.empty` (empty market, `positions` is `[]`, `numPositions` 0, `markPriceValid` false).
- `getAccountById.btc_top` (account 4848, largest BTC position), `.mon_top` (account 4159, largest MON position), `.id1`, and `.missing` (id 999999999, reverts). `getAccountByAddr.btc_top` (address of account 4848) and `.no_account` (address `0x...dEaD`, reverts).
- `getMarginFractions.perp{1,10,20}.lot<L>`: the lot ladder for the lot-size check below.

Reverts come back from the router as a JSON-RPC error with `code` 3, `message` "execution reverted" and the custom error in `data`. Unknown account id gives selector `0xb6b1e4a0` plus the id, unknown address gives `0x03a0e277` plus the address. The two names are not in the SDK ABI, so they stay unnamed here.

### Live markets at the pinned block

The bitmap returns 17 ids. The market table in the plan (section 3) is wrong about ids: MON is 10, ETH is 20, SOL_v2 is 31, LIT is 60 and PUMP is 90. Read ids and symbols from the chain.

| perp_id | symbol | status | price decimals | lot decimals | maint hdths | init hdths | positions |
|---|---|---|---|---|---|---|---|
| 1 | BTC | 4 | 1 | 5 | 2500 | 1500 | 246 |
| 10 | MON | 4 | 6 | 0 | 2000 | 1000 | 98 |
| 20 | ETH | 4 | 2 | 3 | 2000 | 1200 | 84 |
| 30 | SOL | 0 | 2 | 3 | 1000 | 500 | 0 |
| 31 | SOL_v2 | 4 | 3 | 3 | 2000 | 1200 | 55 |
| 40 | HYPE | 4 | 4 | 2 | 2000 | 1000 | 53 |
| 50 | ZEC | 4 | 2 | 4 | 1800 | 1000 | 35 |
| 60 | LIT | 4 | 5 | 1 | 1000 | 300 | 25 |
| 70 | VVV | 4 | 4 | 2 | 1000 | 300 | 13 |
| 80 | TAO | 4 | 3 | 3 | 1000 | 500 | 0 |
| 90 | PUMP | 4 | 6 | 0 | 1000 | 500 | 17 |
| 100 | NEAR | 4 | 4 | 2 | 1000 | 300 | 22 |
| 110 | UNI | 4 | 4 | 2 | 1000 | 300 | 17 |
| 120 | ARB | 0 | 5 | 1 | 1000 | 300 | 0 |
| 130 | AAVE | 0 | 3 | 3 | 1000 | 300 | 0 |
| 140 | MORPHO | 0 | 4 | 2 | 1000 | 300 | 0 |
| 150 | ENA | 0 | 5 | 1 | 1000 | 300 | 0 |

Status 4 is a trading market, status 0 appears on the retired SOL (30) and the four ids that have not launched. The enum names are not in the ABI. The public ticker lists only the 11 markets that have positions.

## Things the decoder must handle

- `getPositionsV2` always returns `positionsPerPage` entries. Entries after the first `numPositions` are zero filled. Trim by `numPositions`.
- `getPositionsV2` also returns `markPricePNS` and `markPriceValid`, identical on every page of one block and equal to `markPNS` of `getPerpetualInfoV2`.
- `getPerpetualInfoV2` string fields (`name`, `symbol`) are dynamic, so the struct is behind an offset. `fundingRatePct100k` is a signed `int16` sign-extended to 32 bytes, so negative rates must decode as negative.
- In every market the sum of long lots equals the sum of short lots equals the contract open interest (checked by `tools/verify.py`).

## Derived expected values

`derived/positions.perp<N>.json` holds, for each position, the raw inputs (`position_type`, `lot_lns`, `price_pns`, `price_residue_pnsq16`, `deposit_cns`, `premium_pnl_cns`) and the results, all computed with Python `fractions.Fraction` as plan section 4 defines:

- `entry`: `price_pns` with the residue rule (long and `price_pns >= 1`: `price_pns - 1 + residue/65536`, otherwise `price_pns + residue/65536`, no change when the residue is 0), divided by 10^price decimals.
- `size = lot / 10^lot decimals`, `deposit` and `premium_pnl` use 10^6.
- `mmr = entry * size / MMF` with `MMF = perpMaintMarginFracHdths / 100` (BTC 25, which is 4 percent).
- `liq_price = max(0, entry + s * (mmr - deposit - premium_pnl) / size)` and `bankruptcy_price = max(0, entry - s * (deposit + premium_pnl) / size)`, with `s = +1` for `position_type` 0 (long) and `-1` for 1 (short).
- `notional = size * mark`, `leverage = notional / deposit` (`null` when the deposit is 0), `distance = s * (mark - liq_price) / mark`.
- Mark is `markPricePNS` from the position pages at the same block.

Number format of every result: plain decimal string, 18 significant digits, round half to even, trailing zeros removed, `"0"` for zero. The file covers all 11 markets with positions (665 positions in total), which includes BTC and MON as asked. The `complete` flag is true for every file.

### Cross-check against the SDK (`state_position.rs`)

- The formulas are the same term by term: `effective_entry_price`, `MMR = entry * size / maintenance_margin`, `liquidation_price` and `bankruptcy_price`, both floored at 0, both using `premium_pnl` only. `delta_pnl` depends on the mark and does not enter either price.
- All 10 entry price vectors, 6 liquidation and bankruptcy vectors (plain, funding paid, funding received, long and short) and 4 MMR vectors of the SDK tests are reproduced exactly (`derived/sdk_goldens.json`, asserted when the file is built).
- Difference 1: the SDK uses fastnum fixed width decimals (`UD64`, `UD128`, `D256`) and its entry price test asserts `0.9999999984741210937`, while the exact value is `0.99999999847412109375`. The SDK truncates at 19 significant digits, the fixtures are exact. Relative gap 5e-20 on that vector. A 19 digit emulation of the SDK types over all 665 live positions differs from the exact values by at most 5.1e-18 relative (liquidation and bankruptcy). The emulation is mine, not the SDK binary, and assumes half-up rounding.
- Difference 2: the research script `positions2.py` computes in binary floats. Against the exact values its liquidation price differs by at most 4.4e-15 relative over the 665 positions, well inside the 1e-12 tolerance of task T4.
- Not checkable offline: how the SDK turns `perpMaintMarginFracHdths` into the `maintenance_margin` argument. The Perpl docs define `MMR = P_entry * L / MMF` with `MMF = maintMarginFracHdths`, and the live values (BTC 2500, docs say BTC maintenance margin 4 percent) fit `MMF = hdths / 100`. ZEC reads 1800 on chain (5.56 percent) while the docs table says about 6.7 percent, so the docs are stale and the chain is the source.

## Lot-size check

`getMarginFractions(perp, lot)` was called at the pinned block for BTC, MON and ETH with lots from 0 up to 10^12 LNS, including 0.1, 0.5, 1 and 2 times `oiMaxLNS` (9 to 10 lots per market, files `calls/getMarginFractions.perp<N>.lot<L>.json`).

Result: `perpMaintMarginFracHdths` does not depend on the lot. It stayed at 2500 for BTC and 2000 for MON and ETH over the whole range. `perpInitMarginFracHdths` also stayed constant (1500, 1000, 1200). Only `dynamicInitMarginFracHdths` moves: it equals the initial fraction below `oiMaxLNS` and drops to 1 from `oiMaxLNS` upward (BTC 30,000,000, MON 1,000,000,000, ETH 10,000,000). The engine may call `getMarginFractions(perp, 0)` once per market.

## Liquidation logs

`liquidations/`:

- `getlogs.raw.json`: array of `{method, params, result}`, one `eth_getLogs` per block (`fromBlock = toBlock`, the proxy address and topic0 filter) with the full result array. 18 blocks, 20 logs in total. One block (90076937) holds 3 logs from one transaction.
- `transactions.raw.json`: object keyed by transaction hash with the raw `eth_getTransactionByHash` result (18 transactions).
- `blocks.raw.json`: object keyed by the block number in hex with the raw `eth_getBlockByNumber` result (no transactions).
- `expected.json`: the 20 logs sorted by block and log index, each with `blockNumber`, `logIndex`, `transactionHash`, `blockTimestamp`, `transactionFrom` (lowercase `tx.from`) and `event` (the 14 decoded fields under their ABI names).

Selection (seed 143): from the 3,650 historical events of the research file `perpl_work/live/liquidations_all.json`, one single-event block for each of the 10 market ids that appear in history (1, 10, 20, 30, 31, 40, 50, 60, 70, 90), seven more single-event blocks spread over time, and one 3-event block. Blocks range from 68727576 to 111612151. Both sides occur (11 long, 9 short). Only two keepers appear in the 18 transactions: `0xb3b0f0a1...74a3` (12 events) and `0x8dea7a01...9f33` (8 events).

Facts about the event worth knowing:

- All 14 fields are non-indexed, so the log has only topic0 and 448 bytes of data.
- `onOrderBook` is true in all 20 samples and in all 3,650 historical events.
- `liqPricePNS` is the price the position was closed at, not the trigger price. Over the 3,650 events the mark is on the "crossed" side of `liqPricePNS` (long mark <= liq, short mark >= liq) in only 1,055 events (29 percent). The plan's go/no-go gate 2 and design doc 22 section 3.1 read `liqPricePNS` as the stored liquidation price of the position, which these data do not support. A usable replay compares the computed liquidation price of the position before the event with `markPricePNS` of the event.
- A partial liquidation exists in `hypersync/expected.json` (perp 50, `liqLotLNS` 448112, `posLotLNS` 122935, `posDepositCNS` nonzero). Of the 20 raw samples, 19 are full liquidations with `posLotLNS` 0 and `posDepositCNS` 0, and one is partial (perp 50, block 101294109).

## Deploy block

`deploy.json`: the proxy was created in block **54773010** (2026-02-11 23:02:27 UTC) by `0x141ace3283da8fd356b5f079b228146e783e50a6`, creation transaction `0x22d1d74e137c3a82ac4b702fd90024811b184fcdcf8fd4d1473097b4d8a619f3`. Found by a binary search on `eth_getCode` between block 0 and the pinned block through `http://127.0.0.1:4000/sona-history/monad` (27 steps), and the block of the creation transaction (taken from the explorer page in the research folder) is the same. The first indexed liquidation is at block 55493930.

## HyperSync page

`hypersync/request.json` is the POST body sent to `https://monad.hypersync.xyz/query`. The key went only in the header `Authorization: Bearer <ENVIO_HYPERSYNC_KEY>` and is not stored anywhere in these files. `response.json` is the response as received: 10 logs, 9 transactions, 9 blocks in one `data` item, blocks 102493417 to 102493439 (`to_block` 102493440 is exclusive, and `next_block` equals it). `expected.json` is the decoded rows in the same shape as `liquidations/expected.json`.

Type quirks: block `number`, `block_number`, `log_index` and `transaction_index` are JSON numbers, but the block `timestamp` is a hex string (`"0x6a9d7977"`). `rollback_guard` is null. The free key answered 429 four times before the first 200 (waits of 8, 16, 32 and 60 seconds), so the backfill client needs backoff.

## REST samples

`rest/leaderboard_day_vol.json` and `rest/leaderboard_all_vol.json` are `GET https://app.perpl.xyz/api/v1/trading/leaderboard/{day,all}/vol` trimmed to the first 50 rows of `d` (5,457 upstream), shape unchanged: `{"at": {"b": block, "t": ms}, "d": [{"i", "a", "p", "r", "v", "s"}]}` where `i` is the rank and `a` the wallet. The endpoint is undocumented, and `p`, `r`, `v`, `s` are not named anywhere. Doc 22 says each row carries wallet, PnL and volume, so `p` looks like PnL and `v` like volume in 6-decimal collateral units (the day leader shows `v` 8716023344081, about $8.7M). That mapping is an inference, and `r` and `s` are unidentified. 
`rest/ticker.json` is `GET https://app.perpl.xyz/api/v1/market-data/ticker` unchanged: `{"mt", "sn", "d": {"<perp id>": {...}}}`.

## Reproduce

Needs python3 (stdlib only), the local router, and for HyperSync the key in the environment. Run from `testdata/tools` with `python3 -B -I`:

1. `make_abi.py <path to SDK Exchange.json> ../../internal/perpl/exchange.abi.json`, then `make_selectors.py`.
2. `record_calls.py` (picks a new pinned block and rewrites `calls/`, `chain.json`, `manifest.json`), then `decode_calls.py`, `derive_positions.py`, `crosscheck.py`.
3. `record_logs.py <liquidations_all.json>`, `find_deploy.py <creation tx>`, `record_rest.py`, `record_hypersync.py <liquidations_all.json>`.
4. `verify.py` runs 544 offline consistency checks over all fixtures.

Chain reads went through the router. No call used a paid upstream except one: during a rerun the router answered one `eth_call` with the header `X-Erpc-Credits: drpc:eth_call=20;unknown-monad.xyz:eth_call=1`. The recorder now counts such answers (`router_paid_calls` in `chain.json`, 0 for the files here) and switches to the public node for the remaining calls. Fixture size is about 2.3 MB.

# Rules

Every number this engine returns is computed by the rules below from public Perpl data at a stated block. This file says what each metric means, which contract calls feed it, and where it stops being true, so that anyone can recompute a value or see why it differs. The code that implements each rule is named in brackets.

Read the limits as part of the definition. Several metrics are floors or snapshots and say so in the `coverage` field of the record the API returns, an array of limit codes.

## 0. Conventions

**Chain and contract.** Monad, `eip155:143`. The Perpl Exchange proxy is `0x34B6552d57a35a1D042CcAe1951BD1C370112a6F`. The ABI subset the engine uses is `internal/perpl/exchange.abi.json`, copied verbatim from the Perpl SDK (`PerplFoundation/dex-sdk`, `crates/sdk/abi/dex/Exchange.json`).

**One block per snapshot.** A refresh reads `eth_blockNumber`, takes `N = head - 2`, and issues every `eth_call` and `eth_getStorageAt` of that refresh at block `N`, with one exception. The inputs that change only by governance (`getMarginFractions`, `getLiquidationInfo`, `getExchangeInfo`, `getFundingInterval`, marked "every 10 min" in section 1) are read at the block of the last slow refresh and reused for up to 10 minutes. Failed slow reads keep the older copy, so the lag can be longer. The maintenance value `h` behind the liquidation price, bankruptcy price, distance, the liquidation map and the stress can therefore come from a block up to about 2,000 blocks (10 minutes at 0.3 s a block) before `N`. A governance change shows up in the numbers within that time. Two blocks of depth keep the read clear of a short reorg. The block number and its time are the `Sona-Block` and `Sona-Block-Time` headers of every answer that uses the snapshot.

**Batched reads.** With `multicall: true` (the default) the engine sends these reads through Multicall3 at `0xcA11bde05977b3631167028862bE2a173976CA11` with `aggregate3`, still by `eth_call` at block `N`, and decodes each returned `returnData` as it would a single call. One batch holds the bitmap, `getPerpetualInfoV2` and the first `getPositionsV2` page of every market the previous snapshot listed, then one batch per further page round follows `nextNodeId` exactly as before, and the slow reads of section 1 are one batch. A market the bitmap adds is read in one more batch and a market it drops is ignored. A failed sub-call fails the refresh like a failed `eth_call`. A batch holds at most 64 calls. With `multicall: false` every read is its own `eth_call`.

**Units.** The contract stores integers with a scale.

| Quantity | Stored as | Real value |
|---|---|---|
| price `P` | `PNS` | `PNS / 10^priceDecimals` |
| size `L` | `LNS` | `LNS / 10^lotDecimals` |
| money `C` | `CNS` | `CNS / 10^collateralDecimals` (6 on Perpl, so 1 unit is 1 USD) |

`priceDecimals` and `lotDecimals` are per market, from `getPerpetualInfoV2`. `collateralDecimals` is from `getExchangeInfo`. Nothing is hard-coded.

**Side convention.** The contract's `positionType` is `0` for a long and `1` for a short. In every formula `s = +1` for a long and `s = -1` for a short.

**Arithmetic.** Per-position values (entry, margin, liquidation and bankruptcy prices, notional, leverage, distance) are exact rational numbers (`math/big.Rat`) built from the stored integers. Sums and shares across positions use 64-bit floats, and set membership and bad debt are decided on the exact values before they are converted. Prices and amounts leave the API as decimal strings, and ratios and shares as JSON numbers. [`internal/risk`]

**Markets.** The set of markets is whatever `getPerpetualExistsBitmap` reports at block `N`. A market is priced when its mark price is above zero. A market can exist in the bitmap with no mark price and no positions (retired or not yet launched), and it is left out of the computed lists. The `status` field in the API is the raw number from the contract.

## 1. Inputs

All calls go to `Exchange` through `eth_call` at block `N`. Selectors are pinned by tests against the SDK.

| Call | Gives | Cadence |
|---|---|---|
| `getPerpetualExistsBitmap()` returns `uint256[4]` | which perp ids exist (bit `n` of word `i` is id `256*i + n`) | every refresh |
| `getPerpetualInfoV2(uint256 perpId)` | name, symbol, `priceDecimals`, `lotDecimals`, `markPNS`, `oraclePNS`, `oracleTimestampSec`, `longOpenInterestLNS`, `shortOpenInterestLNS`, `insuranceBalanceCNS`, `fundingRatePct100k`, `absFundingClampPctPer100K`, `status`, `refPriceMaxAgeSec` | every refresh, per market |
| `getPositionsV2(uint256 perpId, uint256 pageStartPositionId, uint256 positionsPerPage)` | positions of a market, 100 per page: account id, `positionType`, `lotLNS`, `pricePNS`, `priceResiduePNSQ16`, `depositCNS`, `premiumPnlCNS`, `deltaPnlCNS`, `nextNodeId` | every refresh, per market |
| `getMarginFractions(uint256 perpId, uint256 lotLNS)` | `perpMaintMarginFracHdths`, `perpInitMarginFracHdths` | every 10 min, per market, with `lotLNS = 0` |
| `getLiquidationInfo(uint256 perpId)` | the liquidation split of the market | every 10 min, per market |
| `getExchangeInfo()` | `collateralDecimals`, balances | every 10 min |
| `getFundingInterval()` | the funding interval | every 10 min |
| `getAccountByAddr(address)` and `getAccountById(uint256)` | account id, balances, frozen flag, address | on demand, cached 5 min |
| `eth_getStorageAt(exchange, 0x360894a1...382bbc)` | the ERC-1967 implementation address | every refresh |
| `eth_getBlockByNumber(N)` | block time | every refresh |

The positions of a market form a linked list. The engine asks for a page starting at node 0, then at the last returned `nextNodeId`, until that id is 0. It trims each page to the `numPositions` the contract reports, because the contract zero-fills the rest of a page. A repeated node id is an error and the refresh keeps the previous snapshot.

`getPositionsV2` also returns a mark price. The engine uses `markPNS` from `getPerpetualInfoV2` for every computation, so that a market's numbers are all against one mark.

**Maintenance margin does not depend on size.** Fixtures taken at lots from 0 to 10^12 LNS, including 0.1, 0.5, 1 and 2 times `oiMaxLNS`, show `perpMaintMarginFracHdths` constant, so one call with `lotLNS = 0` per market is enough. Only `dynamicInitMarginFracHdths` changes with size, and the engine does not use it. [`testdata/README.md`]

Other sources, both public:

- The Perpl REST API, `GET https://app.perpl.xyz/api/v1/trading/leaderboard/{day,all}/vol`, every 10 min, for volume and rank. Only the keys `i` (rank), `a` (address), `p` (PnL) and `v` (volume, 6 decimals) are used, and their meaning is inferred from the data, not documented by Perpl.
- The Perpl REST API, `GET https://app.perpl.xyz/api/v1/trading/leaderboard/{day,all}/pnl`, the same rows ranked by PnL, every 10 min together with the volume boards, one request at a time with a one second pause.
- The Perpl market data, `GET https://app.perpl.xyz/api/v1/market-data/{perpId}/candles/3600/{fromMs}-{toMs}` (keys `t` start in ms, `v` volume with 6 decimals, `n` trades) and `.../{perpId}/funding/{fromMs}-{toMs}` (keys `at.t` time in ms and `rate`), only when a history route asks, through one client that sends at most one request a second and keeps an answer 5 minutes.
- HyperSync (`https://monad.hypersync.xyz/query`) with an API key, for the history backfill. See section 8.

## 2. Per position

For a position with stored price `pricePNS`, residue `r = priceResiduePNSQ16`, size `lotLNS`, deposit `depositCNS` and premium PnL `premiumPnlCNS`, in a market with maintenance value `h = perpMaintMarginFracHdths`. [`risk.Assess`]

**Size, deposit, premium PnL.** `L = lotLNS / 10^lotDecimals`, `D = depositCNS / 10^6`, `F = premiumPnlCNS / 10^6` (signed).

**Entry price `E`.** The contract stores the entry price rounded to a tick (up for a long, down for a short) plus a 16-bit residue of the rounding. The SDK function `effective_entry_price` restores it:

- if `r = 0`: `E = pricePNS / 10^priceDecimals`
- otherwise: `E_pns = (pricePNS - 1 if long and pricePNS >= 1, else pricePNS) + r / 65536`, and `E = E_pns / 10^priceDecimals`

**Maintenance margin requirement.** The contract gives the fraction as a divisor in hundredths: `MMF = h / 100`, and `MMR = E * L / MMF`. BTC has `h = 2500`, so `MMF = 25` and `MMR` is 4% of the entry notional. The API shows `maint_margin_fraction = 100 / h` (0.04 for BTC) and `max_leverage = perpInitMarginFracHdths / 100` (15 for BTC at `1500`).

**Liquidation price.** From the SDK `Position::liquidation_price`:

```text
liq = max(0, E + s * (MMR - D - F) / L)
```

**Bankruptcy price.** From the SDK `Position::bankruptcy_price`:

```text
bk = max(0, E - s * (D + F) / L)
```

The unrealised PnL `deltaPnlCNS` is not an input. The entry price already carries the price part of the position's result, and `F` is the accrued funding and premium part.

**Derived per position.** With `mark` from `getPerpetualInfoV2`:

| Field | Definition |
|---|---|
| notional | `L * mark` |
| leverage | `notional / D`, absent when `D = 0` |
| distance | `s * (mark - liq) / mark`. Zero or negative means liquidatable at the mark |

**Check values from the SDK tests.** Entry 100, size 10, deposit 100, maintenance fraction 20 give `MMR = 100 * 10 / 20 = 50`. A long has `liq = 100 + (50 - 100) / 10 = 95` and `bk = 100 - 100 / 10 = 90`. A short has `liq = 105` and `bk = 110`. The engine reproduces every SDK vector (10 entry prices, 6 liquidation and bankruptcy prices, 4 margin requirements) exactly, and the 665 live positions of the recorded fixtures to 1e-12 relative or better. The SDK itself works in fixed-width decimals and truncates at 19 digits, so it can differ from the exact value by a relative 5e-18. [`testdata/derived`]

**One reading is not checked against the contract source.** The divisor reading of the maintenance value (`MMR = E * L * 100 / h`) is supported by the SDK test vectors and by live values (BTC `2500` is 4%), but the SDK code does not show how it derives its `maintenance_margin_requirement` from the number the contract returns.

## 3. Liquidation map

Parameters: `bucket_bps` (default 25, allowed 5 to 500) and `range_pct` (default 20, allowed 1 to 50). Anything else is a 400 `invalid_query`. The defaults are fixed in the code. [`risk.LiquidationMap`]

For every position of one market, take its liquidation price's offset from the mark in basis points: `o = (liq - mark) / mark * 10000`.

- **Buckets.** Edges sit at multiples of `bucket_bps` away from the mark and are clipped at plus and minus `range_pct * 100` bps, so the two outermost buckets can be narrower. A position adds its notional at mark and one account to the bucket whose range holds `floor(o)`. The lower edge is inclusive, and the top edge is inclusive only for the last bucket. Longs and shorts are summed apart. A liquidation price below the mark belongs to a long, and above to a short, in healthy books.
- **Beyond range.** A position whose offset lies outside plus or minus `range_pct` is counted in `beyond_range` with its notional and position count, apart from the buckets.
- **Cumulative curves.** For each whole percent `x` from 1 to `range_pct`:
  - longs: the notional and count of long positions with `liq >= mark * (1 - x/100)`;
  - shorts: those of short positions with `liq <= mark * (1 + x/100)`.

  The curves use all positions of the market, including positions beyond the range and positions already liquidatable, so the first point can be above zero. They never decrease.

Accounts equal positions here, because an account holds one position per market on Perpl. The engine relies on this for the account counts of the map, and it aggregates by account id elsewhere so a duplicate would not be double counted.

**Example.** Mark 100, a long with `liq = 95` sits at `o = -500` bps. With `bucket_bps = 25` it falls in the bucket from 95.00 (inclusive) to 95.25, and it first enters the long curve at `x = 5`.

## 4. Stress and the Shock Cover Ratio

For a market and a shock `x` that is nonzero and between -0.5 and +0.5 (default shocks are -20, -10, -5, -3, +3, +5, +10, +20 %). The shock is read through its shortest decimal form, so `0.15` means exactly 15%. [`risk.Stress`]

```text
P1 = mark * (1 + x)
```

**Liquidating set.** For a fall (`x < 0`), long positions with `liq >= P1`. For a rise (`x > 0`), short positions with `liq <= P1`. The comparison is inclusive.

**Liquidating notional.** `N(x) = sum of L * P1` over the set. The API also gives the number of positions and the number of distinct accounts.

**Gap bad debt.** The loss left when a position is closed at `P1` and its bankruptcy price has already been crossed:

- long: `L * max(0, bk - P1)`
- short: `L * max(0, P1 - bk)`

`D(x)` is their sum over the set.

**Insurance.** `I = insuranceBalanceCNS / 10^6`, the market's own insurance fund at the snapshot.

**Shock Cover Ratio.** `SCR = I / D` when `D > 0`. When `D = 0` the SCR is `null` and `no_bad_debt` is `true`, so a missing SCR always means no bad debt and never a failed computation. `uncovered = max(0, D - I)`.

**Top positions.** Up to 10 positions of the set ranked by notional at the mark, largest first, ties by account id, each with size, liquidation price, bankruptcy price and its own bad debt.

**Example.** The position of section 2 (long, `liq 95`, `bk 90`, size 10) in a market with mark 100. A fall of 15% gives `P1 = 85`, `liq >= 85` puts it in the set, and `D = 10 * (90 - 85) = 50`. With `I = 100`, `SCR = 2` and `uncovered = 0`. With `I = 20`, `SCR = 0.4` and `uncovered = 30`. A fall of 10% gives `P1 = 90`, the position is in the set and `D = 0`.

**Limits.**

- **It is a floor.** `D` assumes every liquidation fills at exactly `P1`. A real book has depth, so large positions fill at a worse price, and one liquidation can move the price that the next one meets. Cascades, slippage and order-book depth are not modelled, and the API says so with the coverage code `perpl_bad_debt_is_floor`. Read `D` as the least bad debt a gap to `P1` leaves, never as an estimate of what would happen.
- **A liquidating position is closed whole.** Partial liquidations appear in the event data (section 8) and are not modelled.
- **Nothing happens during the move.** Traders adding collateral, closing early or being liquidated at a price that already moved are not in the numbers, and funding that accrues after the snapshot is not included.
- **Insurance is the fund at the snapshot.** Money that the liquidations themselves would pay into it (the contract splits a liquidation between the insurance fund, the user and the protocol, see `getLiquidationInfo`) is not added to `I`.
- **One market at a time.** Each market has its own fund, and the engine does not net a market's bad debt against another's fund.

## 5. Concentration

All shares are numbers from 0 to 1. [`risk.Concentration`, `risk.VolumeShares`, `risk.Keepers`]

**Per market.** Accounts are ranked by the notional of their position in the market. `top1`, `top5` and `top10` are the summed notional of the 1, 5 or 10 largest accounts divided by the market's **two-sided** open notional, which is the long notional plus the short notional. The top accounts list gives up to 10 of them with their individual share.

*Why two-sided.* The first plan divided by the one-sided notional (the long side, which equals the short side in lots). On the recorded fixtures that gives shares above 1, for example BTC top-10 at 1.295 and UNI at 1.98, because the largest accounts can be on both sides of the book. Dividing by the sum of both sides keeps every share between 0 and 1 and matches the overall definition. The denominator is exactly `total_notional_usd` of the same market, which is `long_notional_usd` plus `short_notional_usd`, so a reader can recompute any share from the response. The response also gives `long_notional_usd` and `short_notional_usd`, so a one-sided share can be derived.

**Overall.** Accounts are ranked by their notional summed across all markets, and the share is over the total open notional of all markets, both sides.

**Account identity.** The lists hold account ids. After each snapshot the engine looks up the address of every listed account with `getAccountById`, stores it, and fills `address` from that table, so it is `null` only for an account whose lookup has not finished or failed. An account is not necessarily one person, and one person can control several addresses.

**Volume.** Taken from the public leaderboard (`day` and `all` periods). Rows are sorted by volume, and `top1`, `top5` and `top10` are the volume of the 1, 5 or 10 largest divided by the sum of volume over all rows the leaderboard lists. That total is the leaderboard's own, not an official exchange volume. When the leaderboard cannot be read, or the last good copy of a period is older than 30 minutes (three polls), the volume fields are `null` and `coverage` carries `perpl_leaderboard_unavailable`.

**Keepers.** Liquidation events are grouped by the `from` address of the transaction that emitted them, over all indexed events. The share of a keeper is its count over the total count. The address is the sender of the liquidation transaction. A contract or relay that sends transactions on behalf of someone else shows up as that sender. The result is only as complete as the history (section 8).

## 6. Wallet card

`GET .../accounts/{address}` calls `getAccountByAddr(address)` (cached 5 minutes), then takes that account's positions from the latest snapshot. [`risk.WalletCard`]

- Balance, locked balance and the frozen flag come from the account record.
- Each position shows the fields of section 2 and its **OI share**: the position's notional over the open notional of its own side of that market.
- `min_distance` is the smallest distance over the account's positions, `null` with none.
- The leaderboard rank, PnL and volume for the day and the all-time period are shown when the address is listed, and are `null` otherwise.
- An address with no Perpl account makes the contract revert. The engine treats any revert of `getAccountByAddr` as "no account" and answers 404 with the problem code `not_found` and the detail `This address has no Perpl account.`.

The card has no balance outside Perpl and no history of the wallet on other chains.

## 7. Market figures

| Field | Definition |
|---|---|
| `mark`, `oracle` | `markPNS` and `oraclePNS` over `10^priceDecimals` |
| `oracle_gap_bps` | `(mark - oracle) / oracle * 10000`, `null` when the oracle price is 0 |
| `oracle_age_s` | block time minus `oracleTimestampSec` |
| `long_oi`, `short_oi` | open interest in size units, and in USD at the mark |
| `insurance_fund_usd` | `insuranceBalanceCNS / 10^6` |
| `funding_rate` | `fundingRatePct100k / 100000`, the rate of one funding interval as a fraction of notional |
| `scr_m10`, `scr_p10` | the SCR of section 4 at -10% and +10%, with `no_bad_debt_m10` and `no_bad_debt_p10` |

Two readings are unconfirmed. The unit of `fundingRatePct100k` is taken from its name, with the field equal to 100,000 times a fraction, which matches Perpl's other per-100K figures. The unit of `getFundingInterval` is read as blocks (8,571 at the recorded block), which has not been checked against the contract source.

At the recorded block, long open interest equals short open interest in every market, and each equals the sum of the position sizes of its side. Positions and open interest are read at the same block, so a mismatch in a later snapshot would point at a read error.

**Funding series.** `GET .../funding` is built from the market snapshots this engine stored, one row per refresh. It starts when the engine first ran, and a gap means the engine was not running. The engine keeps market snapshots for 8 days and deletes older rows once an hour, a day more than the 168 hours the route can ask for (position snapshots are kept 7 days). Coverage code `perpl_funding_from_snapshots`.

## 7b. History and leaderboard routes

**Volume history.** `GET .../markets/{market}/history?metric=volume` serves the hourly candles of Perpl's market data. `volume_usd = v / 10^6`. The sum of the 24 hourly candles of BTC equals the `dva` (24 hour volume in collateral) of Perpl's ticker for BTC within the rounding of the moment (20.44 million USD at the check). The newest candle is still open. Perpl leaves out an hour without trades, so the series can have gaps.

**Funding history.** `metric=funding` serves the funding events Perpl applied, `rate = rate / 10^6` of notional per funding interval. Perpl's `rate` is in millionths, which the data shows: its `ppl` field (the payment per unit) equals `rate * idx / 10^6` for every event (rate 40 on index 829410 gives 33), and a stored contract reading of `fundingRatePct100k = -4` (a `funding_rate` of -0.00004) sits next to the Perpl events of -40 in the same hours. So the history and the `funding_rate` of the market list use the same unit, a fraction of notional. The unit of `rate` is inferred from the data, not documented by Perpl.

**Open interest history.** `metric=oi` is built from this engine's own snapshots. Every five minutes of block time the first snapshot of the bucket stores, per priced market, the long notional at the mark (it equals the short notional, section 7) and the number of positions in the table `market_oi`, kept 31 days. The route thins the rows to one per step (5 minutes for 24 hours, 30 minutes for 168 hours, 2 hours for 720 hours), the last row of each window. It starts when the engine began recording and a gap means it was not running. Coverage code `perpl_oi_from_snapshots`.

**Leaderboard.** `GET .../leaderboard` serves rows of Perpl's four boards (`day` and `all`, ranked by `pnl` or `vol`) unchanged: `rank` is Perpl's `i`, `pnl_usd = p / 10^6`, `volume_usd = v / 10^6`. `volume_share` is the row's volume over the volume of all rows of the same board (about 5,000 accounts), so it is a share of the leaderboard and not of the exchange. Perpl's volume counts both sides of a trade: at the check the volume board of the last day summed to 49.0 million USD against 26.3 million USD of 24 hour volume over all markets in the ticker, 1.9 times. Perpl does not say what its PnL figure includes, so the engine states no more than "Perpl's PnL". A board older than 30 minutes is not served and the route answers 503. Coverage code `perpl_leaderboard_figures`.

## 8. Liquidation history

The event is `PositionLiquidated(perpId, posAccountId, positionType, markPricePNS, liqPricePNS, liqLotLNS, posLotLNS, deltaPnlCNS, fundingCNS, posAmountCNS, posDepositCNS, accAmountCNS, accBalanceCNS, onOrderBook)`. [`perpl.DecodeLiquidation`]

| Field | Source |
|---|---|
| `side` | `positionType`, `0` long and `1` short |
| `mark` | `markPricePNS` over `10^priceDecimals` |
| `liq_price` | `liqPricePNS` over `10^priceDecimals` |
| `size` | `liqLotLNS` over `10^lotDecimals`, the part that was liquidated |
| `remaining_size` | `posLotLNS` over `10^lotDecimals`, `0` for a full liquidation |
| `notional_usd` | `size * mark` |
| `keeper` | `from` of the transaction |
| `time` | the block's timestamp |

**`liq_price` is the price the position was closed at, not its trigger price.** Over the 3,650 events in the research data, the mark is on the crossed side of `liqPricePNS` in only 29%, so the field cannot be compared with a stored liquidation price. To test a computed liquidation price against an event, compare the price computed for the position at the block before the event with the event's `markPricePNS`.

**Sources.**

- **Backfill.** With `ENVIO_HYPERSYNC_KEY` set, the engine reads all events from the exchange's deployment block (54773010, 2026-02-11 23:02:27 UTC, found by a binary search on `eth_getCode`) through HyperSync, filtered by contract address and topic `0x6fc9c0ea...58a9`, one request at a time with backoff on rate limits. It pages by `next_block` and persists its cursor. It ends at the tail cursor of the engine's first start, saved as the cursor `backfill_target`, because the tail reads everything after that block. A restart after a finished backfill therefore stays `complete` and asks HyperSync for nothing, and a restart during the backfill resumes it up to the saved target. A database from an earlier build has no saved target, so its first restart takes the live tail cursor once and saves it.
- **Tail.** Every 30 s the engine reads new events through the router with `eth_getLogs` over at most 1,000 blocks and an address filter, halving the range on JSON-RPC error `-32012`. The keeper comes from `eth_getTransactionByHash` and the time from `eth_getBlockByNumber`.
- **Without a key.** The history starts at `head - 9000` blocks, the status shows `backfill: "disabled"`, and every history answer carries the coverage code `perpl_history_partial`. The same code is present while the backfill is still running.

## 9. Upgrade watch and paid calls

**Upgrade watch.** The exchange is an ERC-1967 proxy. Every refresh reads the implementation slot `0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc` at block `N` and compares it with `expected_implementation` from `config/engine.yaml` (`0xa9ab97a404a0bca04d6a5b4a39995fea9e791b2a` at the recorded block). On a mismatch `implementation_changed` is `true` in the status. If the config value is empty, the first value read is the baseline. A changed implementation means the ABI, the formulas of section 2 or the units above may no longer hold, so treat every number as unconfirmed until the rules are checked again.

**Paid calls.** The engine reads chain data only through the RPC router, which prefers free public endpoints. A response whose `X-ERPC-Credits` header names `drpc:` was served by a paid provider. The engine counts those in `perps_paid_rpc_calls_total`, shows the count in the status and logs a warning. To keep that count at zero, every request carries the header `X-ERPC-Use-Upstream: !drpc-*` (`rpc_upstream_filter` in `config/engine.yaml`), which tells the router to skip its paid dRPC upstreams while it still fails over among the free ones.

## 10. What these numbers are not

- **Not live.** Every position figure is a snapshot of one block, 30 seconds apart. A trader can add collateral or close a position the second after. When refreshes keep failing, the last snapshot stays served. Once it is older than four refresh intervals (2 minutes) the records built from it carry `perpl_snapshot_stale` and `/readyz` answers 503. The block in `Sona-Block` always says which block the numbers describe.
- **Not a forecast.** The stress numbers are a mechanical "what if the price jumped", with the limits of section 4.
- **Not an exchange report.** Volume shares come from a public leaderboard of Perpl's own, whose key meanings are inferred. The volume and funding histories are Perpl's public market data, and the open interest history starts when this engine began recording.
- **Not complete history without the key.** See section 8.
- **No private data.** The engine reads public chain state and public Perpl market data. It holds no key, signs nothing and calls no signed per-account Perpl API.

## 11. Check a number yourself

1. Pick a block `B` and a market id from `getPerpetualExistsBitmap()`.
2. Call `getPerpetualInfoV2(perpId)`, `getMarginFractions(perpId, 0)` and `getExchangeInfo()` at `B` for the scales, mark, insurance and the maintenance value `h` (the engine may have read `h` up to 10 minutes earlier, see section 0, and it changes only by governance).
3. Walk `getPositionsV2(perpId, 0, 100)` and then from each last `nextNodeId`.
4. Apply section 2 to each position. Compare with `GET .../markets/{market}/liquidation-map` and `.../stress` for the snapshot block `B` (it is the `Sona-Block` header).

The recorded calls and the values computed from them are in `testdata/calls` and `testdata/derived`, and the SDK vectors are in `testdata/derived/sdk_goldens.json`. `testdata/README.md` says how each file was made.

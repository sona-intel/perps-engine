# Sona Intel perps engine

Sona's risk tool for [Perpl](https://perpl.xyz), the perpetuals exchange on Monad (`eip155:143`). It reads the exchange contract's public state and works out:

- every open position's liquidation and bankruptcy price, from the contract's own margin formula;
- a liquidation map per market, with long and short notional by price bucket;
- a stress test per market: the notional that liquidates and the bad debt left after a price shock, set against the market's insurance fund (the Shock Cover Ratio);
- how concentrated open interest, volume and liquidations are across accounts and keepers;
- the liquidation history and a wallet risk card;
- the oracle against the mark price, and the funding rate.

Every number is derived from public data at a stated block, and the rules are published so anyone can recompute them. [`RULES.md`](RULES.md) defines each metric, names the contract calls that feed it and lists its limits.

## Read-only

The engine only reads. It holds no wallet or private key, signs nothing, places no order and never calls Perpl's signed per-account API. Never commit real keys; copy `.env.example` to `.env`.

## Data sources

| Source | Used for |
|---|---|
| JSON-RPC (`PERPS_RPC_URL`, in the Sona stack the RPC router) | all contract reads at one block, the implementation slot, block times, the live tail of liquidation events, transaction senders |
| HyperSync (`ENVIO_HYPERSYNC_KEY`, optional) | the backfill of `PositionLiquidated` events up to the tail cursor of the first start (done once, a restart resumes it only if it was cut short) from the deployment block. Without the key the history starts 9,000 blocks back and the API says so |
| Perpl public REST (`/api/v1/trading/leaderboard/{day,all}/vol`) | volume shares and a wallet's rank. Optional, the API reports when it is unavailable |
| Postgres | market snapshots (kept 8 days), position snapshots (kept 7 days), liquidations, account addresses, backfill and tail cursors |

## API

Internal only. `sona-api` is the public face. The full contract is [`openapi.yaml`](openapi.yaml) (OpenAPI 3.1, shown in the stack's Swagger as "Perpl risk service (internal)"). Everything under `/internal/v1/chains/{chain}/perpl/` (written `...` in the table) needs `{chain}` to be `eip155:143`, otherwise it answers 404 `unsupported_chain`, or 400 `invalid_id` when the value is no CAIP-2 id. The problem codes are the ones of the other Sona engines (`invalid_query`, `invalid_id`, `unsupported_chain`, `not_found`, `upstream_unavailable`, `deadline_exceeded`, `internal`), and an unknown market, an address with no account and a missing route are all `not_found` with the cause in `detail`. `{market}` is a symbol such as `BTC` (any case) or a perp id. Answers are bare records with no `{data, meta}` envelope, the same shape as x402-engine and the EVM service. A record starts with `kind` and `chain` and ends with `coverage`, an array of limit codes. The snapshot block and time are in the `Sona-Block` and `Sona-Block-Time` headers, with `Sona-Finality`, which is always `confirmed`. A list is a record with `items`, and a paged one has `next_cursor`, left out on the last page. Errors are `application/problem+json`.

| Route | Answers |
|---|---|
| `GET .../status` | exchange, implementation and whether it changed, snapshot block and age, counts, backfill state, paid RPC calls |
| `GET .../markets` and `.../markets/{market}` | mark, oracle gap, open interest, insurance fund, funding, margin fractions, SCR at -10% and +10% |
| `GET .../markets/{market}/liquidation-map?bucket_bps=&range_pct=` | long and short notional by liquidation price bucket, beyond-range totals, cumulative curves |
| `GET .../markets/{market}/stress?shock=` | liquidating notional, gap bad debt, SCR, uncovered amount, top positions |
| `GET .../stress?shock=` | the same summary for every market |
| `GET .../concentration` | top 1, 5 and 10 shares of open interest per market and overall, top accounts, volume shares, keepers |
| `GET .../liquidations?market=&limit=&cursor=` | liquidation events, newest first, cursor paged |
| `GET .../liquidations/daily?market=&days=` | count and notional per UTC day |
| `GET .../funding?market=&hours=` | mark, oracle gap and funding from stored snapshots |
| `GET .../accounts/{address}` | wallet risk card, 404 `not_found` for an address with no account |
| `GET /healthz`, `GET /readyz` | liveness, and readiness after migrations and the first snapshot. Readiness turns 503 again when the snapshot is older than four refresh intervals (2 minutes), and the records built from it then carry `perpl_snapshot_stale` |

Metrics are on port 9464 (`/metrics`, not published by the compose file; Prometheus scrapes it as the job `perps-engine`).

## Configuration

`config/engine.yaml` holds the chain, exchange address, the expected implementation address, the deployment block, refresh intervals, `max_inflight_calls` (default 2), `multicall` (default `true`, batches the per-refresh reads through Multicall3, `false` sends one `eth_call` per read), `rpc_upstream_filter` (default `!drpc-*`, sent as the header `X-ERPC-Use-Upstream` on every JSON-RPC request so the router skips its paid dRPC upstreams and still fails over among the free ones, empty sends no header, otherwise 1 to 128 visible ASCII characters without spaces) and stress shocks, and is validated on start (a key it does not know is an error). The liquidation map defaults, 25 bps buckets over 20 %, are fixed in the code and documented in `openapi.yaml`. Settings from the environment (names in `.env.example`):

| Variable | Use |
|---|---|
| `PERPS_CONFIG_DIR` | folder holding `engine.yaml` (default `/etc/perps`) |
| `PERPS_RPC_URL` | JSON-RPC endpoint, overrides `rpc_url` |
| `ENVIO_HYPERSYNC_KEY` | optional, enables the liquidation history backfill |
| `PGHOST`, `PGPORT`, `PGUSER`, `PGDATABASE`, `PGPASSWORD` | Postgres, read by the libpq variables |
| `LOG_LEVEL` | `debug`, `info`, `warn` or `error` |

## Run

In the Sona stack, from `services/` (needs `PERPL_PG_PASSWORD` and `PERPL_PG_ENGINE_PASSWORD` in `services/.env`, see `.env.example`):

```sh
docker compose up -d --build perpl-postgres perps-engine
curl -s http://127.0.0.1:8201/internal/v1/chains/eip155:143/perpl/status
curl -s http://127.0.0.1:8201/internal/v1/chains/eip155:143/perpl/markets
```

The engine listens on host port 8201 and Postgres on 5435, both bound to `127.0.0.1`. The compose file is `services/platform/perpl/compose.yaml`. The engine connects as the least-privilege role `perpl_engine` (no superuser, createdb or createrole, default `search_path` `perpl`), so its tables live in the schema `perpl`, and `PERPL_PG_PASSWORD` is only the password of the superuser `perpl`. A fresh volume gets the role from `initdb/01-engine-role.sh`, which is safe to run again by hand (`docker compose exec -T perpl-postgres sh /docker-entrypoint-initdb.d/01-engine-role.sh`) on an existing volume, where the tables made earlier in `public` then need `ALTER TABLE ... SET SCHEMA perpl` and `OWNER TO perpl_engine`. A changed `config/engine.yaml` needs `docker compose up -d --force-recreate perps-engine`. The first snapshot is ready a few seconds after the start, and until then the routes that need one answer 503 `upstream_unavailable` with `Retry-After: 5`.

Outside Docker, with a Postgres reachable through the libpq variables:

```sh
PERPS_CONFIG_DIR=config PERPS_RPC_URL=http://127.0.0.1:4000/sona/monad go run . serve
```

The binary takes one subcommand: `serve` (the default), `healthcheck` (GET `http://127.0.0.1:8080/healthz`, exit 0 when it answers 200) and `migrate` (apply the embedded migrations and exit).

## Tests and lint

```sh
go build ./... && go vet ./...
go test ./...
golangci-lint run ./...
```

Tests need no network. The ones that need Postgres skip unless `PERPS_TEST_PG` names a database where they may create and drop their own schemas, for example `PERPS_TEST_PG="host=127.0.0.1 port=5435 user=perpl dbname=perpl"` with `PGPASSWORD` set. `testdata/` holds the recorded chain calls, event logs and API samples that the decoders and the risk math are tested against, and its README says how each file was made.

## Layout

| Path | Content |
|---|---|
| `main.go` | subcommands `serve`, `healthcheck`, `migrate` |
| `internal/config` | `engine.yaml` and environment, with secrets redacted |
| `internal/perpl` | domain types, ABI encode and decode, event decode |
| `internal/rpc` | JSON-RPC client with retries, log paging, an upstream filter header and a paid-call watch |
| `internal/risk` | the math of `RULES.md`, no I/O |
| `internal/sources` | Perpl REST and HyperSync clients |
| `internal/store`, `migrations` | Postgres access and embedded SQL |
| `internal/refresh` | the snapshot loop, persistence, liquidation backfill and tail |
| `internal/server` | HTTP handlers, parameters, metrics |
| `internal/app` | wiring between refresh, server and store |
| `internal/problem` | problem+json writer |
| `internal/testkit` | Postgres test helpers |

## License

Not chosen yet. The founder decides before the repository is made public.

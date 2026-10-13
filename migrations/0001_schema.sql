create table market_snapshots (
    perp_id              integer not null,
    block                bigint not null,
    ts                   timestamptz not null,
    mark                 numeric not null,
    oracle               numeric not null,
    oracle_ts            timestamptz not null,
    long_oi              numeric not null,
    short_oi             numeric not null,
    long_notional        numeric not null,
    short_notional       numeric not null,
    positions            integer not null,
    insurance            numeric not null,
    funding_rate_pct100k smallint not null,
    scr_m10              numeric,
    scr_p10              numeric,
    primary key (perp_id, block)
);

create index market_snapshots_ts on market_snapshots (perp_id, ts);

create table position_snapshots (
    block       bigint not null,
    ts          timestamptz not null,
    perp_id     integer not null,
    account_id  bigint not null,
    side        text not null check (side in ('long', 'short')),
    size        numeric not null,
    entry       numeric not null,
    deposit     numeric not null,
    premium_pnl numeric not null,
    delta_pnl   numeric not null,
    liq_price   numeric not null,
    bk_price    numeric not null,
    primary key (block, perp_id, account_id)
);

create index position_snapshots_ts on position_snapshots (ts);

create table liquidations (
    block         bigint not null,
    log_index     integer not null,
    tx            bytea not null check (octet_length(tx) = 32),
    ts            timestamptz not null,
    perp_id       integer not null,
    account_id    bigint not null,
    side          text not null check (side in ('long', 'short')),
    mark          numeric not null,
    liq_price     numeric not null,
    liq_lot       numeric not null,
    pos_lot       numeric not null,
    notional      numeric not null,
    on_order_book boolean not null,
    keeper        bytea not null check (octet_length(keeper) = 20),
    primary key (block, log_index)
);

create index liquidations_perp on liquidations (perp_id, block desc, log_index desc);
create index liquidations_ts on liquidations (ts);

create table accounts (
    account_id bigint primary key,
    address    bytea not null check (octet_length(address) = 20)
);

create table cursor (
    name  text primary key,
    block bigint not null
);

create table market_oi (
    market            integer not null,
    t                 timestamptz not null,
    open_interest_usd numeric not null,
    positions         integer not null,
    primary key (market, t)
);

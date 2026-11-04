-- Catalogue: replaced wholesale by `gw2tools seed`.

CREATE TABLE skins (
    id      INTEGER PRIMARY KEY,
    name    TEXT    NOT NULL,
    section TEXT    NOT NULL,
    type    TEXT    NOT NULL DEFAULT '',
    rarity  TEXT    NOT NULL DEFAULT '',
    gwu     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE items (
    id     INTEGER PRIMARY KEY,
    name   TEXT NOT NULL,
    rarity TEXT NOT NULL DEFAULT ''
);

CREATE TABLE item_skins (
    item_id INTEGER NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    skin_id INTEGER NOT NULL REFERENCES skins (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, skin_id)
);

CREATE INDEX item_skins_skin ON item_skins (skin_id);

-- Account and market state: written by the server's refreshes.

-- Unlocked skins are kept forever; first_seen is when a refresh first saw them.
CREATE TABLE account_skins (
    skin_id    INTEGER PRIMARY KEY,
    first_seen INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE buy_orders (
    id       INTEGER PRIMARY KEY,
    item_id  INTEGER NOT NULL,
    price    INTEGER NOT NULL,
    quantity INTEGER NOT NULL,
    created  INTEGER NOT NULL
);

-- Prices are refreshed per item on different schedules (orders every minute,
-- candidates hourly); updated_at is when each row was last written.
CREATE TABLE prices (
    item_id          INTEGER PRIMARY KEY,
    buy_price        INTEGER NOT NULL,
    buy_quantity     INTEGER NOT NULL,
    sell_price       INTEGER NOT NULL,
    sell_quantity    INTEGER NOT NULL,
    top_buy_quantity INTEGER NOT NULL DEFAULT 0,
    updated_at       INTEGER NOT NULL DEFAULT 0
);

-- Timestamps (seeded_at, orders_refreshed_at, prices_refreshed_at,
-- prices_attempted_at) and the last refresh errors.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

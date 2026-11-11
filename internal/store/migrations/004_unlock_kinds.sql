-- Finishers and minis join skins in the catalogue and account tables. Their API ids
-- overlap skin ids (there is a skin 24 and a finisher 24), so every skin id is now
-- paired with its kind: skin, finisher or mini. Existing rows are all skins.
-- SQLite can't change a primary key in place, so the tables are rebuilt, without
-- rowids so each is a single b-tree on its composite key.

CREATE TABLE skins_new (
    kind    TEXT    NOT NULL,
    id      INTEGER NOT NULL,
    name    TEXT    NOT NULL,
    section TEXT    NOT NULL,
    type    TEXT    NOT NULL DEFAULT '',
    rarity  TEXT    NOT NULL DEFAULT '',
    gwu     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, id)
) WITHOUT ROWID;
INSERT INTO skins_new (kind, id, name, section, type, rarity, gwu)
    SELECT 'skin', id, name, section, type, rarity, gwu FROM skins;

CREATE TABLE item_skins_new (
    item_id INTEGER NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    kind    TEXT    NOT NULL,
    skin_id INTEGER NOT NULL,
    PRIMARY KEY (item_id, kind, skin_id),
    FOREIGN KEY (kind, skin_id) REFERENCES skins_new (kind, id) ON DELETE CASCADE
) WITHOUT ROWID;
INSERT INTO item_skins_new (item_id, kind, skin_id) SELECT item_id, 'skin', skin_id FROM item_skins;

-- Weapon sets only ever hold skins; kind exists for the foreign key.
CREATE TABLE weapon_set_skins_new (
    set_id  INTEGER NOT NULL REFERENCES weapon_sets (id) ON DELETE CASCADE,
    kind    TEXT    NOT NULL DEFAULT 'skin' CHECK (kind = 'skin'),
    skin_id INTEGER NOT NULL,
    PRIMARY KEY (set_id, skin_id),
    FOREIGN KEY (kind, skin_id) REFERENCES skins_new (kind, id)
) WITHOUT ROWID;
INSERT INTO weapon_set_skins_new (set_id, skin_id) SELECT set_id, skin_id FROM weapon_set_skins;

CREATE TABLE account_skins_new (
    kind       TEXT    NOT NULL,
    skin_id    INTEGER NOT NULL,
    first_seen INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, skin_id)
) WITHOUT ROWID;
INSERT INTO account_skins_new (kind, skin_id, first_seen) SELECT 'skin', skin_id, first_seen FROM account_skins;

-- Children first, so dropping skins breaks no foreign keys. Renaming a table updates
-- the foreign keys that point at it.
DROP TABLE weapon_set_skins;
DROP TABLE item_skins;
DROP TABLE account_skins;
DROP TABLE skins;
ALTER TABLE skins_new RENAME TO skins;
ALTER TABLE item_skins_new RENAME TO item_skins;
ALTER TABLE weapon_set_skins_new RENAME TO weapon_set_skins;
ALTER TABLE account_skins_new RENAME TO account_skins;

CREATE INDEX item_skins_skin ON item_skins (kind, skin_id);

-- Black Lion weapon sets (catalogue, replaced by `gw2tools seed`): each set's skins,
-- from its collection achievement in the "Black Lion Collections" category.

CREATE TABLE weapon_sets (
    id   INTEGER PRIMARY KEY, -- the collection achievement id
    name TEXT NOT NULL
);

CREATE TABLE weapon_set_skins (
    set_id  INTEGER NOT NULL REFERENCES weapon_sets (id) ON DELETE CASCADE,
    skin_id INTEGER NOT NULL REFERENCES skins (id),
    PRIMARY KEY (set_id, skin_id)
);

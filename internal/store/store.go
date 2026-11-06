// Package store persists gw2tools data in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/bbck/gw2tools/internal/wardrobe"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	metaSeededAt    = "seeded_at"
	metaOrdersAt    = "orders_refreshed_at"
	metaPricesAt    = "prices_refreshed_at"
	metaPricesTried = "prices_attempted_at"
	metaOrdersError = "orders_error"
	metaPricesError = "prices_error"
	metaChestSets   = "chest_sets" // JSON list of set names
	metaChestTried  = "chest_attempted_at"
	metaChestError  = "chest_error"
	upsertMetaSQL   = "INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value"
	upsertPriceSQL  = `INSERT INTO prices
		(item_id, buy_price, buy_quantity, sell_price, sell_quantity, top_buy_quantity, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET
			buy_price = excluded.buy_price, buy_quantity = excluded.buy_quantity,
			sell_price = excluded.sell_price, sell_quantity = excluded.sell_quantity,
			top_buy_quantity = excluded.top_buy_quantity, updated_at = excluded.updated_at`
)

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	// Temp tables and sorts stay in memory; the scratch image has no /tmp.
	q.Add("_pragma", "temp_store(MEMORY)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection keeps SQLite writes simple; the workload is tiny.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate applies migrations/NNN_*.sql in order, tracking progress in user_version.
func migrate(db *sql.DB) error {
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i, name := range names {
		n := i + 1
		if n <= version {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// ReplaceCatalog swaps the seeded catalogue (skins, items and weapon sets) for a new one.
func (s *Store) ReplaceCatalog(ctx context.Context, c wardrobe.Catalog) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"DELETE FROM weapon_set_skins", "DELETE FROM weapon_sets", "DELETE FROM item_skins", "DELETE FROM items", "DELETE FROM skins"} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		skinStmt, err := tx.PrepareContext(ctx, "INSERT INTO skins (id, name, section, type, rarity, gwu) VALUES (?, ?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		for _, sk := range c.Skins {
			if _, err := skinStmt.ExecContext(ctx, sk.ID, sk.Name, sk.Section, sk.Type, sk.Rarity, sk.GWU); err != nil {
				return err
			}
		}
		itemStmt, err := tx.PrepareContext(ctx, "INSERT INTO items (id, name, rarity) VALUES (?, ?, ?)")
		if err != nil {
			return err
		}
		linkStmt, err := tx.PrepareContext(ctx, "INSERT INTO item_skins (item_id, skin_id) VALUES (?, ?)")
		if err != nil {
			return err
		}
		for _, it := range c.Items {
			if _, err := itemStmt.ExecContext(ctx, it.ID, it.Name, it.Rarity); err != nil {
				return err
			}
			for _, sk := range it.Skins {
				if _, err := linkStmt.ExecContext(ctx, it.ID, sk); err != nil {
					return fmt.Errorf("linking item %d to skin %d: %w", it.ID, sk, err)
				}
			}
		}
		setStmt, err := tx.PrepareContext(ctx, "INSERT INTO weapon_sets (id, name) VALUES (?, ?)")
		if err != nil {
			return err
		}
		setSkinStmt, err := tx.PrepareContext(ctx, "INSERT INTO weapon_set_skins (set_id, skin_id) VALUES (?, ?)")
		if err != nil {
			return err
		}
		for _, set := range c.Sets {
			if _, err := setStmt.ExecContext(ctx, set.ID, set.Name); err != nil {
				return err
			}
			for _, sk := range set.Skins {
				if _, err := setSkinStmt.ExecContext(ctx, set.ID, sk); err != nil {
					return fmt.Errorf("adding skin %d to set %q: %w", sk, set.Name, err)
				}
			}
		}
		return setMetaTime(ctx, tx, metaSeededAt, time.Now())
	})
}

// WeaponSetCount is the number of stored Black Lion weapon sets.
func (s *Store) WeaponSetCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM weapon_sets").Scan(&n)
	return n, err
}

// LoadCatalog reads the seeded skins, items and weapon sets.
func (s *Store) LoadCatalog(ctx context.Context) (wardrobe.Catalog, error) {
	c := wardrobe.Catalog{Skins: map[int]wardrobe.Skin{}, Items: map[int]wardrobe.Item{}}
	err := each(ctx, s.db, "SELECT id, name, section, type, rarity, gwu FROM skins", nil, func(rows *sql.Rows) error {
		var sk wardrobe.Skin
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Section, &sk.Type, &sk.Rarity, &sk.GWU); err != nil {
			return err
		}
		c.Skins[sk.ID] = sk
		return nil
	})
	if err != nil {
		return c, err
	}
	err = each(ctx, s.db, "SELECT id, name, rarity FROM items", nil, func(rows *sql.Rows) error {
		var it wardrobe.Item
		if err := rows.Scan(&it.ID, &it.Name, &it.Rarity); err != nil {
			return err
		}
		c.Items[it.ID] = it
		return nil
	})
	if err != nil {
		return c, err
	}
	err = each(ctx, s.db, "SELECT item_id, skin_id FROM item_skins ORDER BY item_id, skin_id", nil, func(rows *sql.Rows) error {
		var itemID, skinID int
		if err := rows.Scan(&itemID, &skinID); err != nil {
			return err
		}
		it := c.Items[itemID]
		it.Skins = append(it.Skins, skinID)
		c.Items[itemID] = it
		return nil
	})
	if err != nil {
		return c, err
	}
	err = each(ctx, s.db, `SELECT ws.id, ws.name, wss.skin_id FROM weapon_sets ws
		JOIN weapon_set_skins wss ON wss.set_id = ws.id
		ORDER BY ws.name, ws.id, wss.skin_id`, nil, func(rows *sql.Rows) error {
		var id, skinID int
		var name string
		if err := rows.Scan(&id, &name, &skinID); err != nil {
			return err
		}
		if n := len(c.Sets); n == 0 || c.Sets[n-1].ID != id {
			c.Sets = append(c.Sets, wardrobe.Set{ID: id, Name: name})
		}
		last := &c.Sets[len(c.Sets)-1]
		last.Skins = append(last.Skins, skinID)
		return nil
	})
	return c, err
}

// SaveOrders records the result of an orders refresh: newly unlocked skins are added
// (unlocks are never removed), buy orders are replaced, and the given prices (for the
// items we have orders on) are upserted.
func (s *Store) SaveOrders(ctx context.Context, u wardrobe.OrdersUpdate) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		// One statement for the whole unlock list; only new skins are inserted.
		owned, err := jsonIDs(u.Owned)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_skins (skin_id, first_seen)
			SELECT value, ? FROM json_each(?) WHERE true
			ON CONFLICT (skin_id) DO NOTHING`, u.At.Unix(), owned); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM buy_orders"); err != nil {
			return err
		}
		orders, err := tx.PrepareContext(ctx, "INSERT INTO buy_orders (id, item_id, price, quantity, created) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			return err
		}
		for _, o := range u.Orders {
			if _, err := orders.ExecContext(ctx, o.ID, o.ItemID, o.Price, o.Quantity, o.Created.Unix()); err != nil {
				return err
			}
		}
		if err := upsertPrices(ctx, tx, u.Prices, u.At); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM delivery"); err != nil {
			return err
		}
		delivery, err := tx.PrepareContext(ctx, "INSERT INTO delivery (item_id, count) VALUES (?, ?)")
		if err != nil {
			return err
		}
		for id, count := range u.Delivery {
			if _, err := delivery.ExecContext(ctx, id, count); err != nil {
				return err
			}
		}
		if err := setMetaTime(ctx, tx, metaOrdersAt, u.At); err != nil {
			return err
		}
		return deleteMeta(ctx, tx, metaOrdersError)
	})
}

// SavePrices records the result of a prices refresh: prices are upserted, and stored
// prices for items not in keep are dropped. Items that were wanted but missing from
// the response keep their previous price.
func (s *Store) SavePrices(ctx context.Context, keep []int, prices map[int]wardrobe.Price, at time.Time) error {
	keepJSON, err := jsonIDs(keep)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM prices WHERE item_id NOT IN (SELECT value FROM json_each(?))", keepJSON); err != nil {
			return err
		}
		if err := upsertPrices(ctx, tx, prices, at); err != nil {
			return err
		}
		if err := setMetaTime(ctx, tx, metaPricesAt, at); err != nil {
			return err
		}
		return deleteMeta(ctx, tx, metaPricesError)
	})
}

// LoadSnapshot reads the stored account and market state. The reads share one
// transaction, so a refresh saved meanwhile is seen either entirely or not at all.
func (s *Store) LoadSnapshot(ctx context.Context) (wardrobe.Snapshot, error) {
	var snap wardrobe.Snapshot
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		snap, err = loadSnapshot(ctx, tx)
		return err
	})
	return snap, err
}

func loadSnapshot(ctx context.Context, tx *sql.Tx) (wardrobe.Snapshot, error) {
	snap := wardrobe.Snapshot{Owned: map[int]bool{}, Prices: map[int]wardrobe.Price{}, Delivery: map[int]int{}}
	var err error
	if snap.OrdersAt, err = metaTime(ctx, tx, metaOrdersAt); err != nil {
		return snap, err
	}
	if snap.PricesAt, err = metaTime(ctx, tx, metaPricesAt); err != nil {
		return snap, err
	}
	var chest string
	err = tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", metaChestSets).Scan(&chest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return snap, err
	default:
		if err := json.Unmarshal([]byte(chest), &snap.Chest); err != nil {
			return snap, fmt.Errorf("meta %s: %w", metaChestSets, err)
		}
	}
	err = each(ctx, tx, "SELECT skin_id FROM account_skins", nil, func(rows *sql.Rows) error {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		snap.Owned[id] = true
		return nil
	})
	if err != nil {
		return snap, err
	}
	err = each(ctx, tx, "SELECT id, item_id, price, quantity, created FROM buy_orders ORDER BY id", nil, func(rows *sql.Rows) error {
		var o wardrobe.Order
		var created int64
		if err := rows.Scan(&o.ID, &o.ItemID, &o.Price, &o.Quantity, &created); err != nil {
			return err
		}
		o.Created = time.Unix(created, 0).UTC()
		snap.Orders = append(snap.Orders, o)
		return nil
	})
	if err != nil {
		return snap, err
	}
	err = each(ctx, tx, "SELECT item_id, count FROM delivery", nil, func(rows *sql.Rows) error {
		var id, count int
		if err := rows.Scan(&id, &count); err != nil {
			return err
		}
		snap.Delivery[id] = count
		return nil
	})
	if err != nil {
		return snap, err
	}
	err = each(ctx, tx, "SELECT item_id, buy_price, buy_quantity, sell_price, sell_quantity, top_buy_quantity FROM prices", nil, func(rows *sql.Rows) error {
		var p wardrobe.Price
		if err := rows.Scan(&p.ItemID, &p.BuyPrice, &p.BuyQuantity, &p.SellPrice, &p.SellQuantity, &p.TopBuyQuantity); err != nil {
			return err
		}
		snap.Prices[p.ItemID] = p
		return nil
	})
	return snap, err
}

// SaveChestSets records the weapon sets in the current Black Lion Chest.
func (s *Store) SaveChestSets(ctx context.Context, names []string) error {
	b, err := json.Marshal(names)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := setMeta(ctx, tx, metaChestSets, string(b)); err != nil {
			return err
		}
		return deleteMeta(ctx, tx, metaChestError)
	})
}

// SetChestAttempt records when a Black Lion Chest check started. Like
// SetPricesAttempt, it is that check's schedule clock.
func (s *Store) SetChestAttempt(ctx context.Context, at time.Time) error {
	return setMetaTime(ctx, s.db, metaChestTried, at)
}

// ChestAttemptedAt is when the last Black Lion Chest check started, zero if never.
func (s *Store) ChestAttemptedAt(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, metaChestTried)
}

func (s *Store) SetChestError(ctx context.Context, msg string) error {
	return setMeta(ctx, s.db, metaChestError, msg)
}

// SetPricesAttempt records when a prices refresh started. It is the prices schedule
// clock: a failing refresh is not retried early, even across restarts.
func (s *Store) SetPricesAttempt(ctx context.Context, at time.Time) error {
	return setMetaTime(ctx, s.db, metaPricesTried, at)
}

// PricesAttemptedAt is when the last prices refresh started, zero if never.
func (s *Store) PricesAttemptedAt(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, metaPricesTried)
}

// SeededAt is when the catalogue was last seeded, zero if never.
func (s *Store) SeededAt(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, metaSeededAt)
}

// Errors from the last failed refreshes; each is cleared by the next good run.
type Errors struct {
	Orders string
	Prices string
	Chest  string
}

func (s *Store) SetOrdersError(ctx context.Context, msg string) error {
	return setMeta(ctx, s.db, metaOrdersError, msg)
}

func (s *Store) SetPricesError(ctx context.Context, msg string) error {
	return setMeta(ctx, s.db, metaPricesError, msg)
}

func (s *Store) Errors(ctx context.Context) (Errors, error) {
	var e Errors
	dst := map[string]*string{metaOrdersError: &e.Orders, metaPricesError: &e.Prices, metaChestError: &e.Chest}
	err := each(ctx, s.db, "SELECT key, value FROM meta WHERE key IN (?, ?, ?)", []any{metaOrdersError, metaPricesError, metaChestError}, func(rows *sql.Rows) error {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		*dst[k] = v
		return nil
	})
	return e, err
}

func upsertPrices(ctx context.Context, tx *sql.Tx, prices map[int]wardrobe.Price, at time.Time) error {
	stmt, err := tx.PrepareContext(ctx, upsertPriceSQL)
	if err != nil {
		return err
	}
	for _, p := range prices {
		if _, err := stmt.ExecContext(ctx, p.ItemID, p.BuyPrice, p.BuyQuantity, p.SellPrice, p.SellQuantity, p.TopBuyQuantity, at.Unix()); err != nil {
			return err
		}
	}
	return nil
}

func each(ctx context.Context, q querier, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func metaTime(ctx context.Context, q querier, key string) (time.Time, error) {
	var v string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("meta %s: %w", key, err)
	}
	return time.Unix(sec, 0).UTC(), nil
}

// execer and querier are satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func setMeta(ctx context.Context, ex execer, key, value string) error {
	_, err := ex.ExecContext(ctx, upsertMetaSQL, key, value)
	return err
}

func setMetaTime(ctx context.Context, ex execer, key string, t time.Time) error {
	return setMeta(ctx, ex, key, strconv.FormatInt(t.Unix(), 10))
}

func deleteMeta(ctx context.Context, ex execer, key string) error {
	_, err := ex.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", key)
	return err
}

// jsonIDs encodes ids as a JSON array for use with json_each.
func jsonIDs(ids []int) (string, error) {
	if ids == nil {
		ids = []int{}
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

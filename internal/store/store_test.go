package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bbck/gw2tools/internal/wardrobe"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	if at, err := s.SeededAt(ctx); err != nil || !at.IsZero() {
		t.Fatalf("SeededAt before seed = %v, %v", at, err)
	}

	want := wardrobe.Catalog{
		Skins: map[wardrobe.Key]wardrobe.Skin{
			wardrobe.SkinKey(1):     {ID: wardrobe.SkinKey(1), Name: "Flame Blade", Section: "weapon", Type: "Sword", Rarity: "Rare", GWU: true},
			wardrobe.SkinKey(2):     {ID: wardrobe.SkinKey(2), Name: "Cape", Section: "back", Type: "Back", Rarity: "Exotic"},
			wardrobe.FinisherKey(1): {ID: wardrobe.FinisherKey(1), Name: "Finisher 1", Section: "finisher", Rarity: "Rare", GWU: true},
			wardrobe.MiniKey(1):     {ID: wardrobe.MiniKey(1), Name: "Mini 1", Section: "mini", Rarity: "Rare"},
		},
		Items: map[int]wardrobe.Item{
			10: {ID: 10, Name: "Carrion Flame Blade", Rarity: "Rare", Skins: []wardrobe.Key{wardrobe.SkinKey(1)}},
			11: {ID: 11, Name: "Skin Box", Rarity: "Exotic", Skins: []wardrobe.Key{wardrobe.SkinKey(1), wardrobe.SkinKey(2)}},
			// Loaded sorted by kind, then id.
			12: {ID: 12, Name: "Odd Box", Skins: []wardrobe.Key{wardrobe.FinisherKey(1), wardrobe.MiniKey(1), wardrobe.SkinKey(1)}},
		},
		// Loaded by name, with skins by id.
		Sets: []wardrobe.Set{
			{ID: 200, Name: "Alpha", Skins: []int{1, 2}},
			{ID: 100, Name: "Bravo", Skins: []int{1}},
		},
	}
	if err := s.ReplaceCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	// Seeding again replaces rather than duplicates.
	if err := s.ReplaceCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %+v\nwant %+v", got, want)
	}
	if at, _ := s.SeededAt(ctx); at.IsZero() {
		t.Fatal("SeededAt not set")
	}
}

func TestReplaceCatalogRejectsUnknownSkin(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	err := s.ReplaceCatalog(ctx, wardrobe.Catalog{
		Skins: map[wardrobe.Key]wardrobe.Skin{wardrobe.SkinKey(99): {ID: wardrobe.SkinKey(99), Name: "Skin 99", Section: "weapon"}},
		Items: map[int]wardrobe.Item{10: {ID: 10, Name: "x", Skins: []wardrobe.Key{wardrobe.FinisherKey(99)}}},
	})
	if err == nil {
		t.Fatal("expected foreign key error: there is a skin 99 but no finisher 99")
	}
	err = s.ReplaceCatalog(ctx, wardrobe.Catalog{Sets: []wardrobe.Set{{ID: 1, Name: "x", Skins: []int{99}}}})
	if err == nil {
		t.Fatal("expected foreign key error for a set skin")
	}
}

func TestOrdersAndPrices(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	t1 := time.Unix(1_800_000_000, 0).UTC()
	t2 := t1.Add(time.Minute)

	if err := s.SetOrdersError(ctx, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPricesError(ctx, "bang"); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Errors(ctx); e.Orders != "boom" || e.Prices != "bang" {
		t.Fatalf("errors = %+v", e)
	}

	err := s.SaveOrders(ctx, wardrobe.OrdersUpdate{
		At:       t1,
		Owned:    []wardrobe.Key{wardrobe.SkinKey(1), wardrobe.SkinKey(5)},
		Orders:   []wardrobe.Order{{ID: 100, ItemID: 10, Price: 55, Quantity: 1, Created: t1}},
		Prices:   map[int]wardrobe.Price{10: {ItemID: 10, BuyPrice: 55, BuyQuantity: 3, SellPrice: 90, SellQuantity: 2, TopBuyQuantity: 1}},
		Delivery: map[int]int{20: 2, 30: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap, _ := s.LoadSnapshot(ctx); len(snap.Delivery) != 2 || snap.Delivery[20] != 2 {
		t.Fatalf("delivery = %v", snap.Delivery)
	}
	if err := s.SavePrices(ctx, []int{10, 20, 30}, map[int]wardrobe.Price{
		20: {ItemID: 20, BuyPrice: 7, SellPrice: 9},
		30: {ItemID: 30, BuyPrice: 8, SellPrice: 10},
	}, t1); err != nil {
		t.Fatal(err)
	}

	// Next minute: skin 6 and finisher 1 newly unlocked; the API response is the full
	// list again.
	owned := []wardrobe.Key{wardrobe.SkinKey(1), wardrobe.SkinKey(5), wardrobe.SkinKey(6), wardrobe.FinisherKey(1)}
	if err := s.SaveOrders(ctx, wardrobe.OrdersUpdate{At: t2, Owned: owned}); err != nil {
		t.Fatal(err)
	}
	// Next prices run wants only item 20: item 30's skin was unlocked and item 10's
	// order closed.
	if err := s.SavePrices(ctx, []int{20}, map[int]wardrobe.Price{20: {ItemID: 20, BuyPrice: 6, SellPrice: 9}}, t2); err != nil {
		t.Fatal(err)
	}

	snap, err := s.LoadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.OrdersAt.Equal(t2) || !snap.PricesAt.Equal(t2) {
		t.Errorf("times = %v %v", snap.OrdersAt, snap.PricesAt)
	}
	if len(snap.Delivery) != 0 {
		t.Errorf("delivery not replaced: %v", snap.Delivery)
	}
	if len(snap.Owned) != 4 || !snap.Owned[wardrobe.FinisherKey(1)] || snap.Owned[wardrobe.MiniKey(1)] || len(snap.Orders) != 0 {
		t.Errorf("owned = %v orders = %v", snap.Owned, snap.Orders)
	}
	if _, ok := snap.Prices[30]; ok {
		t.Error("unwanted price for item 30 kept")
	}
	if snap.Prices[20].BuyPrice != 6 {
		t.Errorf("price 20 = %+v", snap.Prices[20])
	}
	// Item 10 was not kept, so the prices run dropped it too.
	if _, ok := snap.Prices[10]; ok {
		t.Error("price for item 10 kept after its order went away")
	}
	if e, _ := s.Errors(ctx); e != (Errors{}) {
		t.Errorf("errors not cleared: %+v", e)
	}

	seen := firstSeen(t, s)
	if !seen[wardrobe.SkinKey(1)].Equal(t1) || !seen[wardrobe.SkinKey(6)].Equal(t2) || !seen[wardrobe.FinisherKey(1)].Equal(t2) {
		t.Errorf("first seen = %v", seen)
	}
}

func firstSeen(t *testing.T, s *Store) map[wardrobe.Key]time.Time {
	t.Helper()
	out := map[wardrobe.Key]time.Time{}
	err := each(context.Background(), s.db, "SELECT kind, skin_id, first_seen FROM account_skins", nil, func(rows *sql.Rows) error {
		var k wardrobe.Key
		var at int64
		if err := rows.Scan(&k.Kind, &k.ID, &at); err != nil {
			return err
		}
		out[k] = time.Unix(at, 0).UTC()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPricesAttempt(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Unix(1_800_000_000, 0).UTC()
	if err := s.SetPricesAttempt(ctx, at); err != nil {
		t.Fatal(err)
	}
	got, err := s.PricesAttemptedAt(ctx)
	if err != nil || !got.Equal(at) {
		t.Errorf("attempted = %v, %v", got, err)
	}
}

func TestSavePricesKeepsMissingWantedPrices(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Unix(1_800_000_000, 0).UTC()
	err := s.SaveOrders(ctx, wardrobe.OrdersUpdate{
		At:     at,
		Orders: []wardrobe.Order{{ID: 1, ItemID: 10, Price: 5, Quantity: 1, Created: at}},
		Prices: map[int]wardrobe.Price{10: {ItemID: 10, BuyPrice: 5, TopBuyQuantity: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// An empty response (e.g. an API glitch) must not wipe prices that are still wanted.
	if err := s.SavePrices(ctx, []int{10}, nil, at); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.LoadSnapshot(ctx)
	if p := snap.Prices[10]; p.BuyPrice != 5 || p.TopBuyQuantity != 1 {
		t.Errorf("kept price = %+v", p)
	}
}

func TestReopenKeepsDataAndSkipsMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "reopen.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCatalog(ctx, wardrobe.Catalog{Skins: map[wardrobe.Key]wardrobe.Skin{wardrobe.SkinKey(1): {ID: wardrobe.SkinKey(1), Name: "Kept", Section: "weapon"}}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Applied migrations are not re-run (their plain CREATE TABLEs would fail).
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.LoadCatalog(ctx)
	if err != nil || c.Skins[wardrobe.SkinKey(1)].Name != "Kept" {
		t.Fatalf("catalogue after reopen = %+v, %v", c, err)
	}
	migrations, _ := fs.Glob(migrationFS, "migrations/*.sql")
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version = %d, %v, want %d", version, err, len(migrations))
	}
}

func TestChestSets(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if snap, err := s.LoadSnapshot(ctx); err != nil || snap.Chest != nil {
		t.Fatalf("snapshot before any check = %+v, %v", snap, err)
	}
	at := time.Unix(1_800_000_000, 0).UTC()
	if err := s.SetChestAttempt(ctx, at); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ChestAttemptedAt(ctx); err != nil || !got.Equal(at) {
		t.Errorf("attempted = %v, %v", got, err)
	}
	if err := s.SetChestError(ctx, "wiki down"); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Errors(ctx); e.Chest != "wiki down" || e.Orders != "" {
		t.Errorf("errors = %+v", e)
	}
	if err := s.SaveChestSets(ctx, []string{"Foefire", "Calligrapher's"}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.LoadSnapshot(ctx)
	if err != nil || !reflect.DeepEqual(snap.Chest, []string{"Foefire", "Calligrapher's"}) {
		t.Errorf("chest = %q, %v", snap.Chest, err)
	}
	if e, _ := s.Errors(ctx); e.Chest != "" {
		t.Errorf("chest error not cleared: %q", e.Chest)
	}
}

// Migration 004 pairs skin ids with their kind; existing rows are all skins.
func TestMigrateUnlockKinds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(db, 3); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO skins (id, name, section, type, rarity, gwu) VALUES (1, 'Blade', 'weapon', 'Sword', 'Rare', 1);
		INSERT INTO items (id, name, rarity) VALUES (10, 'Blade Item', 'Rare');
		INSERT INTO item_skins (item_id, skin_id) VALUES (10, 1);
		INSERT INTO weapon_sets (id, name) VALUES (500, 'Set');
		INSERT INTO weapon_set_skins (set_id, skin_id) VALUES (500, 1);
		INSERT INTO account_skins (skin_id, first_seen) VALUES (1, 1800000000), (7, 1800000060);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := wardrobe.Catalog{
		Skins: map[wardrobe.Key]wardrobe.Skin{wardrobe.SkinKey(1): {ID: wardrobe.SkinKey(1), Name: "Blade", Section: "weapon", Type: "Sword", Rarity: "Rare", GWU: true}},
		Items: map[int]wardrobe.Item{10: {ID: 10, Name: "Blade Item", Rarity: "Rare", Skins: []wardrobe.Key{wardrobe.SkinKey(1)}}},
		Sets:  []wardrobe.Set{{ID: 500, Name: "Set", Skins: []int{1}}},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("catalog = %+v\nwant %+v", c, want)
	}
	seen := firstSeen(t, s)
	if len(seen) != 2 || seen[wardrobe.SkinKey(7)].Unix() != 1800000060 {
		t.Errorf("first seen = %v", seen)
	}
	var violations int
	if err := s.db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
		t.Errorf("foreign key violations = %d, %v", violations, err)
	}
	// The rebuilt tables still enforce their foreign keys.
	if err := s.ReplaceCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO item_skins (item_id, kind, skin_id) VALUES (10, 'mini', 1)"); err == nil {
		t.Error("item_skins accepted a mini that is not in the catalogue")
	}
	if _, err := s.db.Exec("INSERT INTO skins (kind, id, name, section) VALUES ('mini', 2, 'Mini', 'mini')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO weapon_set_skins (set_id, kind, skin_id) VALUES (500, 'mini', 2)"); err == nil {
		t.Error("weapon_set_skins accepted a mini")
	}
}

package store

import (
	"context"
	"database/sql"
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
		Skins: map[int]wardrobe.Skin{
			1: {ID: 1, Name: "Flame Blade", Section: "weapon", Type: "Sword", Rarity: "Rare", GWU: true},
			2: {ID: 2, Name: "Cape", Section: "back", Type: "Back", Rarity: "Exotic"},
		},
		Items: map[int]wardrobe.Item{
			10: {ID: 10, Name: "Carrion Flame Blade", Rarity: "Rare", Skins: []int{1}},
			11: {ID: 11, Name: "Skin Box", Rarity: "Exotic", Skins: []int{1, 2}},
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
	if !reflect.DeepEqual(got.Skins, want.Skins) || !reflect.DeepEqual(got.Items, want.Items) {
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
		Items: map[int]wardrobe.Item{10: {ID: 10, Name: "x", Skins: []int{99}}},
	})
	if err == nil {
		t.Fatal("expected foreign key error")
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
		At:     t1,
		Owned:  []int{1, 5},
		Orders: []wardrobe.Order{{ID: 100, ItemID: 10, Price: 55, Quantity: 1, Created: t1}},
		Prices: map[int]wardrobe.Price{10: {ItemID: 10, BuyPrice: 55, BuyQuantity: 3, SellPrice: 90, SellQuantity: 2, TopBuyQuantity: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SavePrices(ctx, []int{10, 20, 30}, map[int]wardrobe.Price{
		20: {ItemID: 20, BuyPrice: 7, SellPrice: 9},
		30: {ItemID: 30, BuyPrice: 8, SellPrice: 10},
	}, t1); err != nil {
		t.Fatal(err)
	}

	// Next minute: skin 6 newly unlocked; the API response is the full list again.
	if err := s.SaveOrders(ctx, wardrobe.OrdersUpdate{At: t2, Owned: []int{1, 5, 6}}); err != nil {
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
	if len(snap.Owned) != 3 || len(snap.Orders) != 0 {
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
	if !seen[1].Equal(t1) || !seen[6].Equal(t2) {
		t.Errorf("first seen = %v", seen)
	}
}

func firstSeen(t *testing.T, s *Store) map[int]time.Time {
	t.Helper()
	out := map[int]time.Time{}
	err := s.each(context.Background(), "SELECT skin_id, first_seen FROM account_skins", nil, func(rows *sql.Rows) error {
		var id int
		var at int64
		if err := rows.Scan(&id, &at); err != nil {
			return err
		}
		out[id] = time.Unix(at, 0).UTC()
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
	if err := s.ReplaceCatalog(ctx, wardrobe.Catalog{Skins: map[int]wardrobe.Skin{1: {ID: 1, Name: "Kept", Section: "weapon"}}}); err != nil {
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
	if err != nil || c.Skins[1].Name != "Kept" {
		t.Fatalf("catalogue after reopen = %+v, %v", c, err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("user_version = %d, %v", version, err)
	}
}

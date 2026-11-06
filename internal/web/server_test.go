package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
	"github.com/bbck/gw2tools/internal/store"
	"github.com/bbck/gw2tools/internal/wardrobe"
)

type fakeAPI struct {
	skinCalls  int
	priceCalls int
	priceIDs   []int
	ownedSkins []int
	failPrices bool
	delivery   []gw2.DeliveryItem
	chestCalls int
	failChest  bool
}

func (f *fakeAPI) ChestSets(context.Context) ([]string, error) {
	f.chestCalls++
	if f.failChest {
		return nil, errors.New("wiki down")
	}
	return []string{"Flame & Cape"}, nil
}

var (
	fakePrices = map[int]gw2.Price{
		20: {ID: 20, Buys: gw2.PriceSide{UnitPrice: 20, Quantity: 4}, Sells: gw2.PriceSide{UnitPrice: 30, Quantity: 1}},
	}
	fakeListings = map[int]gw2.Listing{
		10: {ID: 10, Buys: []gw2.ListingEntry{{UnitPrice: 150, Quantity: 4}}, Sells: []gw2.ListingEntry{{UnitPrice: 12345, Quantity: 1}}},
		30: {ID: 30, Buys: []gw2.ListingEntry{{UnitPrice: 99, Quantity: 1}}, Sells: []gw2.ListingEntry{{UnitPrice: 200, Quantity: 1}}},
	}
)

// pick returns the entries of m for the requested ids, like a batched API lookup.
func pick[T any](m map[int]T, ids []int) []T {
	var out []T
	for _, id := range ids {
		if v, ok := m[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

func (f *fakeAPI) AccountSkins(context.Context) ([]int, error) {
	f.skinCalls++
	return f.ownedSkins, nil
}

func (f *fakeAPI) CurrentBuys(context.Context) ([]gw2.Transaction, error) {
	return []gw2.Transaction{
		{ID: 1, ItemID: 10, Price: 140, Quantity: 1}, // outbid on Flame Blade
		{ID: 2, ItemID: 30, Price: 99, Quantity: 1},  // skin 3 already owned
	}, nil
}

func (f *fakeAPI) Delivery(context.Context) ([]gw2.DeliveryItem, error) { return f.delivery, nil }

func (f *fakeAPI) Prices(_ context.Context, ids []int) ([]gw2.Price, error) {
	f.priceCalls++
	f.priceIDs = ids
	if f.failPrices {
		return nil, errors.New("api down")
	}
	return pick(fakePrices, ids), nil
}

func (f *fakeAPI) Listings(_ context.Context, ids []int) ([]gw2.Listing, error) {
	return pick(fakeListings, ids), nil
}

func openStore(t *testing.T, seed bool) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if seed {
		err := st.ReplaceCatalog(context.Background(), wardrobe.Catalog{
			Skins: map[int]wardrobe.Skin{
				1: {ID: 1, Name: "Flame Blade", Section: "weapon", Type: "Sword", GWU: true},
				2: {ID: 2, Name: "Cheap Helm", Section: "armor", Type: "Helm", GWU: true},
				3: {ID: 3, Name: "Owned Cape", Section: "back", Type: "Back", GWU: true},
			},
			Items: map[int]wardrobe.Item{
				10: {ID: 10, Name: "Carrion Flame Blade", Skins: []int{1}},
				20: {ID: 20, Name: "Cheap <Helm>", Skins: []int{2}},
				30: {ID: 30, Name: "Owned Cape Item", Skins: []int{3}},
			},
			Sets: []wardrobe.Set{{ID: 500, Name: "Flame & Cape", Skins: []int{1, 3}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return st
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestServer(t *testing.T, st *store.Store) (*Server, *fakeAPI, *clock) {
	t.Helper()
	api := &fakeAPI{ownedSkins: []int{3}}
	srv, err := New(st, api, api, slog.New(slog.DiscardHandler), Intervals{})
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	srv.now = clk.now
	return srv, api, clk
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestWardrobePage(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, true))
	if err := srv.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv.Handler(), "/wardrobe")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		"Flame Blade",
		"Cheap &lt;Helm&gt;", // names are escaped
		`<span class="badge outbid">Outbid</span>`,
		`<span class="g">1</span><span class="s">23</span><span class="c">45</span>`, // 12345 copper
		"Owned Cape Item", // redundant order
		"unlocked: <strong>1</strong> / 3",
		`data-copy="Carrion Flame Blade"`, // copy button holds the item name
		`data-copy="Cheap &lt;Helm&gt;"`,  // escaped in the attribute too
		`<meta http-equiv="refresh" content="60">`,
		`<title>Wardrobe Unlocks · gw2tools</title>`,
		`<a href="/wardrobe" class="active">Wardrobe Unlocks</a>`,
		"next in 1h 0m",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Cheapest first: the helm (20c) ranks above the blade (150c).
	if strings.Index(body, "Cheap &lt;Helm&gt;") > strings.Index(body, "Carrion Flame Blade") {
		t.Error("cheapest skin should be listed first")
	}
	if strings.Contains(body, "No skin catalogue") || strings.Contains(body, "failed") {
		t.Error("unexpected banner")
	}
}

func TestWardrobeLimit(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, true))
	if err := srv.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, srv.Handler(), "/wardrobe?n=1")
	if !strings.Contains(body, "Cheapest 1 missing") {
		t.Error("limit not applied to heading")
	}
	// Flame Blade drops out of the cheapest list but its order shows under others.
	others := body[strings.Index(body, "Other orders on missing skins"):]
	// The blade's cheapest item has a 150c top bid.
	if !strings.Contains(others, `total <span class="coins"><span class="s">1</span><span class="c">50</span></span>`) {
		t.Error("other orders heading should show its total")
	}
	if !strings.Contains(others, "Carrion Flame Blade") {
		t.Error("outbid order outside the limit should be listed under other orders")
	}
}

func TestTickSchedulesPricesHourly(t *testing.T) {
	ctx := context.Background()
	srv, api, clk := newTestServer(t, openStore(t, true))

	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if api.skinCalls != 1 || api.priceCalls != 1 {
		t.Fatalf("first tick: skins %d prices %d, want 1 and 1", api.skinCalls, api.priceCalls)
	}
	// Owned skin 3 and the order item 10 are not priced in bulk.
	if !slices.Equal(api.priceIDs, []int{20}) {
		t.Errorf("priced ids = %v, want [20]", api.priceIDs)
	}

	for i := 0; i < 59; i++ {
		clk.t = clk.t.Add(time.Minute)
		if err := srv.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if api.skinCalls != 60 || api.priceCalls != 1 {
		t.Fatalf("after 59m: skins %d prices %d, want 60 and 1", api.skinCalls, api.priceCalls)
	}

	// A restart keeps the prices schedule because it is stored.
	srv2, api2, clk2 := newTestServer(t, srv.store)
	clk2.t = clk.t
	if err := srv2.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if api2.priceCalls != 0 {
		t.Error("restart triggered an early prices refresh")
	}

	clk.t = clk.t.Add(time.Minute)
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if api.priceCalls != 2 {
		t.Errorf("after 60m: prices %d, want 2", api.priceCalls)
	}
}

func TestNewSkinUnlockStopsPricing(t *testing.T) {
	ctx := context.Background()
	srv, api, clk := newTestServer(t, openStore(t, true))
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	api.ownedSkins = []int{2, 3}
	clk.t = clk.t.Add(time.Hour)
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(api.priceIDs) != 0 {
		t.Errorf("priced ids = %v after unlocking skin 2", api.priceIDs)
	}
	// Unlocks are kept even if a later response omits them.
	api.ownedSkins = nil
	clk.t = clk.t.Add(time.Minute)
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ := srv.store.LoadSnapshot(ctx)
	if !snap.Owned[2] || !snap.Owned[3] {
		t.Errorf("owned = %v", snap.Owned)
	}
}

func TestIntervalsHaveMinimums(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, false))
	if iv := srv.Intervals(); iv.Orders != time.Minute || iv.Prices != time.Hour {
		t.Errorf("intervals = %+v", iv)
	}
	srv, err := New(srv.store, &fakeAPI{}, &fakeAPI{}, srv.log, Intervals{Orders: 5 * time.Minute, Prices: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if iv := srv.Intervals(); iv.Orders != 5*time.Minute || iv.Prices != 2*time.Hour {
		t.Errorf("intervals = %+v", iv)
	}
}

func TestUnseededPage(t *testing.T) {
	srv, api, _ := newTestServer(t, openStore(t, false))
	if err := srv.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if api.skinCalls != 0 {
		t.Error("refresh should not call the API before seeding")
	}
	code, body := get(t, srv.Handler(), "/wardrobe")
	if code != http.StatusOK || !strings.Contains(body, "No skin catalogue") {
		t.Fatalf("status %d, body missing seed banner", code)
	}
}

func TestStaticAndHealth(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, false))
	h := srv.Handler()
	for _, path := range []string{"/static/style.css", "/static/app.js", "/healthz"} {
		if code, _ := get(t, h, path); code != http.StatusOK {
			t.Errorf("%s status = %d", path, code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/wardrobe" {
		t.Errorf("/ = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCoins(t *testing.T) {
	cases := map[int]string{
		0:     `<span class="coins none">—</span>`,
		5:     `<span class="coins"><span class="c">5</span></span>`,
		105:   `<span class="coins"><span class="s">1</span><span class="c">5</span></span>`,
		10005: `<span class="coins"><span class="g">1</span><span class="s">0</span><span class="c">5</span></span>`,
	}
	for in, want := range cases {
		if got := string(coins(in)); got != want {
			t.Errorf("coins(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestFailedPricesNotRetriedEarly(t *testing.T) {
	ctx := context.Background()
	srv, api, clk := newTestServer(t, openStore(t, true))
	api.failPrices = true
	if err := srv.Tick(ctx); err == nil {
		t.Fatal("expected prices error")
	}
	if api.priceCalls != 1 {
		t.Fatalf("price calls = %d", api.priceCalls)
	}

	// Restarting 30 minutes later must not retry the failed run.
	srv2, api2, clk2 := newTestServer(t, srv.store)
	api2.failPrices = true
	clk2.t = clk.t.Add(30 * time.Minute)
	srv2.Tick(ctx)
	if api2.priceCalls != 0 {
		t.Fatal("restart retried a failed prices refresh early")
	}

	// The page shows the real next attempt and the error.
	_, body := get(t, srv2.Handler(), "/wardrobe")
	if !strings.Contains(body, "next in 30m") {
		t.Error("countdown should count from the failed attempt")
	}
	if !strings.Contains(body, "Last prices refresh failed") {
		t.Error("missing prices error banner")
	}

	clk2.t = clk.t.Add(time.Hour)
	srv2.Tick(ctx)
	if api2.priceCalls != 1 {
		t.Errorf("price calls after an hour = %d, want 1", api2.priceCalls)
	}
}

func TestLimitSelectIncludesCurrent(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, true))
	_, body := get(t, srv.Handler(), "/wardrobe?n=30")
	if !strings.Contains(body, `<option value="30" selected>30</option>`) {
		t.Error("select should show the current limit")
	}
	if strings.Index(body, `value="25"`) > strings.Index(body, `value="30"`) || strings.Index(body, `value="30"`) > strings.Index(body, `value="50"`) {
		t.Error("options should stay sorted")
	}
	_, body = get(t, srv.Handler(), "/wardrobe")
	if !strings.Contains(body, `<option value="25" selected>25</option>`) || strings.Count(body, "<option") != 4 {
		t.Error("default limit options wrong")
	}
}

func TestDeliveryBoxSection(t *testing.T) {
	ctx := context.Background()
	srv, api, _ := newTestServer(t, openStore(t, true))
	api.delivery = []gw2.DeliveryItem{{ID: 20, Count: 1}} // bought the cheap helm
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// The bought helm is not priced.
	if len(api.priceIDs) != 0 {
		t.Errorf("priced ids = %v, want none", api.priceIDs)
	}
	_, body := get(t, srv.Handler(), "/wardrobe")
	box := strings.Index(body, "<h2>Delivery box</h2>")
	cheapest := strings.Index(body, "Cheapest 25 missing")
	if box < 0 || box > cheapest {
		t.Fatal("delivery section should appear above the cheapest list")
	}
	if !strings.Contains(body[box:cheapest], "Cheap &lt;Helm&gt;") {
		t.Error("delivery section should list the helm")
	}
	if strings.Contains(body[cheapest:], "Cheap &lt;Helm&gt;") {
		t.Error("a bought skin should not be in the cheapest list")
	}
}

func TestSetsPage(t *testing.T) {
	ctx := context.Background()
	srv, _, _ := newTestServer(t, openStore(t, true))
	if err := srv.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, srv.Handler(), "/black-lion")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, want := range []string{
		`<a href="/black-lion" class="active">`,
		`<details class="set" id="set-500">`,
		"Flame &amp; Cape",
		"1/2",                 // the cape is owned
		"Carrion Flame Blade", // the missing skin's item, with its copy button
		`data-copy="Carrion Flame Blade"`,
		`badge outbid`, // the fake's 140c order is under the 150c top bid
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestSetsPageNeedsSeed(t *testing.T) {
	srv, _, _ := newTestServer(t, openStore(t, false))
	_, body := get(t, srv.Handler(), "/black-lion")
	if !strings.Contains(body, "No skin catalogue yet") {
		t.Error("unseeded page should ask for a seed")
	}

	// A catalogue seeded before sets existed asks for a re-seed.
	st := openStore(t, false)
	if err := st.ReplaceCatalog(context.Background(), wardrobe.Catalog{Skins: map[int]wardrobe.Skin{1: {ID: 1, Name: "x", Section: "weapon"}}}); err != nil {
		t.Fatal(err)
	}
	srv, _, _ = newTestServer(t, st)
	if _, body := get(t, srv.Handler(), "/black-lion"); !strings.Contains(body, "No Black Lion weapon sets") {
		t.Error("a catalogue without sets should ask for a re-seed")
	}
}

func TestSetsPageWarnsAboutUnpricedSkins(t *testing.T) {
	st := openStore(t, false)
	err := st.ReplaceCatalog(context.Background(), wardrobe.Catalog{
		Skins: map[int]wardrobe.Skin{1: {ID: 1, Name: "Priced Sword", Section: "weapon"}, 2: {ID: 2, Name: "New Axe", Section: "weapon"}},
		Items: map[int]wardrobe.Item{
			20: {ID: 20, Name: "Priced Sword Skin", Skins: []int{1}}, // in fakePrices
			40: {ID: 40, Name: "New Axe Skin", Skins: []int{2}},      // not priced
		},
		Sets: []wardrobe.Set{{ID: 1, Name: "Half Priced", Skins: []int{1, 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, _, _ := newTestServer(t, st)
	if err := srv.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body := get(t, srv.Handler(), "/black-lion")
	if !strings.Contains(body, `title="1 missing skin has no price yet, so the total is too low">⚠</span>`) {
		t.Error("a set with an unpriced skin should show the warning icon")
	}
	if !strings.Contains(body, `data-copy="New Axe Skin"`) {
		t.Error("an unpriced skin should still name its item")
	}
}

func TestChestCheckedDailyAfterMidnightUTC(t *testing.T) {
	ctx := context.Background()
	srv, api, clk := newTestServer(t, openStore(t, true))
	// The first check runs straight away (it has never run).
	api.failChest = true
	if err := srv.Tick(ctx); err == nil {
		t.Fatal("expected chest error")
	}
	if api.chestCalls != 1 {
		t.Fatalf("chest calls = %d, want 1", api.chestCalls)
	}
	_, body := get(t, srv.Handler(), "/black-lion")
	if !strings.Contains(body, "Last Black Lion Chest check failed: wiki down") {
		t.Error("missing chest error banner")
	}

	// Not again that day, even after a failure and a restart.
	midnight := lastMidnightUTC(clk.t).Add(24 * time.Hour)
	srv2, api2, clk2 := newTestServer(t, srv.store)
	for _, at := range []time.Time{clk.t.Add(time.Minute), clk.t.Add(time.Hour), midnight.Add(-time.Minute)} {
		clk2.t = at
		srv2.Tick(ctx)
	}
	if api2.chestCalls != 0 {
		t.Fatalf("chest calls before midnight UTC = %d, want 0", api2.chestCalls)
	}
	// The first tick after midnight UTC checks, once.
	for _, at := range []time.Time{midnight.Add(30 * time.Second), midnight.Add(2 * time.Minute), midnight.Add(23 * time.Hour)} {
		clk2.t = at
		if err := srv2.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if api2.chestCalls != 1 {
		t.Fatalf("chest calls the next day = %d, want 1", api2.chestCalls)
	}

	_, body = get(t, srv2.Handler(), "/black-lion")
	if !strings.Contains(body, `<span class="name">Flame &amp; Cape <img class="chest" src="/static/black-lion-chest.png"`) {
		t.Error("the chest set should have the chest icon")
	}
	if strings.Contains(body, "check failed") {
		t.Error("a good check should clear the error")
	}
}

func TestLastMidnightUTC(t *testing.T) {
	pst := time.FixedZone("PST", -8*3600)
	at := time.Date(2026, 11, 5, 19, 30, 0, 0, pst) // 03:30 UTC on the 6th
	if got, want := lastMidnightUTC(at), time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("lastMidnightUTC = %v, want %v", got, want)
	}
}

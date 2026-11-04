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
	srv, err := New(st, api, slog.New(slog.DiscardHandler), Intervals{})
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
	srv, err := New(srv.store, &fakeAPI{}, srv.log, Intervals{Orders: 5 * time.Minute, Prices: 2 * time.Hour})
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

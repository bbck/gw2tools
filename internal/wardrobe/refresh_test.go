package wardrobe

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
)

type fakeAPI struct {
	owned      []int
	buys       []gw2.Transaction
	prices     map[int]gw2.Price
	listings   map[int]gw2.Listing
	err        error
	priceIDs   []int
	listingIDs []int
}

func (f *fakeAPI) AccountSkins(context.Context) ([]int, error) { return f.owned, f.err }
func (f *fakeAPI) CurrentBuys(context.Context) ([]gw2.Transaction, error) {
	return f.buys, nil
}
func (f *fakeAPI) Prices(_ context.Context, ids []int) ([]gw2.Price, error) {
	f.priceIDs = ids
	return pick(f.prices, ids), nil
}
func (f *fakeAPI) Listings(_ context.Context, ids []int) ([]gw2.Listing, error) {
	f.listingIDs = ids
	return pick(f.listings, ids), nil
}

func TestRefreshOrders(t *testing.T) {
	api := &fakeAPI{
		owned: []int{1, 2},
		buys: []gw2.Transaction{
			{ID: 1, ItemID: 20, Price: 100, Quantity: 1},
			{ID: 2, ItemID: 50, Price: 100, Quantity: 1},
			{ID: 3, ItemID: 999, Price: 100, Quantity: 1}, // not in the catalogue
		},
		listings: map[int]gw2.Listing{
			50: {ID: 50,
				Buys:  []gw2.ListingEntry{{UnitPrice: 110, Quantity: 2}, {UnitPrice: 90, Quantity: 9}},
				Sells: []gw2.ListingEntry{{UnitPrice: 200, Quantity: 1}, {UnitPrice: 250, Quantity: 4}},
			},
		},
	}
	now := time.Unix(1_800_000_000, 0)
	u, err := RefreshOrders(context.Background(), api, testCatalog(), now)
	if err != nil {
		t.Fatal(err)
	}
	if api.priceIDs != nil {
		t.Error("orders refresh must not call /v2/commerce/prices")
	}
	if !slices.Equal(api.listingIDs, []int{20, 50}) {
		t.Errorf("listing ids = %v, want only catalogue items with orders", api.listingIDs)
	}
	if len(u.Orders) != 3 || !slices.Equal(u.Owned, []int{1, 2}) || !u.At.Equal(now) {
		t.Errorf("update = %+v", u)
	}
	want := Price{ItemID: 50, BuyPrice: 110, BuyQuantity: 11, SellPrice: 200, SellQuantity: 5, TopBuyQuantity: 2}
	if p := u.Prices[50]; p != want {
		t.Errorf("price 50 = %+v, want %+v", p, want)
	}
}

func TestPriceCandidates(t *testing.T) {
	// Skin 1 owned; item 40 has an order; item 50 is a non-GWU skin.
	got := PriceCandidates(testCatalog(), map[int]bool{1: true}, []Order{{ItemID: 40}})
	if want := []int{20, 30}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

func TestRefreshPrices(t *testing.T) {
	api := &fakeAPI{prices: map[int]gw2.Price{
		20: {ID: 20, Buys: gw2.PriceSide{UnitPrice: 5, Quantity: 1}, Sells: gw2.PriceSide{UnitPrice: 9, Quantity: 2}},
	}}
	got, err := RefreshPrices(context.Background(), api, []int{20, 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[20].BuyPrice != 5 || got[20].SellQuantity != 2 {
		t.Errorf("prices = %+v", got)
	}
}

func TestRefreshOrdersError(t *testing.T) {
	api := &fakeAPI{err: errors.New("invalid key")}
	if _, err := RefreshOrders(context.Background(), api, testCatalog(), time.Now()); err == nil {
		t.Fatal("expected error")
	}
}

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

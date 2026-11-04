package wardrobe

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
)

// API is the subset of the GW2 API a refresh uses.
type API interface {
	AccountSkins(ctx context.Context) ([]int, error)
	CurrentBuys(ctx context.Context) ([]gw2.Transaction, error)
	Prices(ctx context.Context, ids []int) ([]gw2.Price, error)
	Listings(ctx context.Context, ids []int) ([]gw2.Listing, error)
}

// RefreshOrders is the frequent, cheap refresh: unlocked skins, open buy orders, and
// the order book for the skin items we have orders on. A handful of requests.
func RefreshOrders(ctx context.Context, api API, c Catalog, now time.Time) (OrdersUpdate, error) {
	u := OrdersUpdate{At: now, Prices: map[int]Price{}}
	var err error
	if u.Owned, err = api.AccountSkins(ctx); err != nil {
		return u, fmt.Errorf("account skins: %w", err)
	}
	buys, err := api.CurrentBuys(ctx)
	if err != nil {
		return u, fmt.Errorf("buy orders: %w", err)
	}
	orderItems := map[int]bool{}
	for _, b := range buys {
		u.Orders = append(u.Orders, Order{ID: b.ID, ItemID: b.ItemID, Price: b.Price, Quantity: b.Quantity, Created: b.Created})
		if _, ok := c.Items[b.ItemID]; ok {
			orderItems[b.ItemID] = true
		}
	}
	listings, err := api.Listings(ctx, slices.Sorted(maps.Keys(orderItems)))
	if err != nil {
		return u, fmt.Errorf("listings: %w", err)
	}
	for _, l := range listings {
		u.Prices[l.ID] = priceFromListing(l)
	}
	return u, nil
}

// PriceCandidates are the items worth pricing: those that unlock a Guaranteed Wardrobe
// Unlock skin we don't have. Items we have orders on are left out because
// RefreshOrders keeps them current.
func PriceCandidates(c Catalog, owned map[int]bool, orders []Order) []int {
	skip := map[int]bool{}
	for _, o := range orders {
		skip[o.ItemID] = true
	}
	var ids []int
	for _, it := range c.Items {
		if skip[it.ID] {
			continue
		}
		for _, s := range it.Skins {
			if c.Skins[s].GWU && !owned[s] {
				ids = append(ids, it.ID)
				break
			}
		}
	}
	slices.Sort(ids)
	return ids
}

// RefreshPrices is the infrequent, bulk refresh of prices for ids.
func RefreshPrices(ctx context.Context, api API, ids []int) (map[int]Price, error) {
	prices, err := api.Prices(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("prices: %w", err)
	}
	out := make(map[int]Price, len(prices))
	for _, p := range prices {
		out[p.ID] = Price{
			ItemID:       p.ID,
			BuyPrice:     p.Buys.UnitPrice,
			BuyQuantity:  p.Buys.Quantity,
			SellPrice:    p.Sells.UnitPrice,
			SellQuantity: p.Sells.Quantity,
		}
	}
	return out, nil
}

func priceFromListing(l gw2.Listing) Price {
	p := Price{ItemID: l.ID}
	for _, b := range l.Buys {
		p.BuyQuantity += b.Quantity
		if b.UnitPrice > p.BuyPrice {
			p.BuyPrice, p.TopBuyQuantity = b.UnitPrice, b.Quantity
		}
	}
	for _, s := range l.Sells {
		p.SellQuantity += s.Quantity
		if p.SellPrice == 0 || s.UnitPrice < p.SellPrice {
			p.SellPrice = s.UnitPrice
		}
	}
	return p
}

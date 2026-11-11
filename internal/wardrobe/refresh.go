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
	AccountFinishers(ctx context.Context) ([]gw2.AccountFinisher, error)
	AccountMinis(ctx context.Context) ([]int, error)
	CurrentBuys(ctx context.Context) ([]gw2.Transaction, error)
	Delivery(ctx context.Context) ([]gw2.DeliveryItem, error)
	Prices(ctx context.Context, ids []int) ([]gw2.Price, error)
	Listings(ctx context.Context, ids []int) ([]gw2.Listing, error)
}

// RefreshOrders is the frequent, cheap refresh: unlocked skins, finishers and minis,
// open buy orders, the delivery box, and the order book for the skin items we have
// orders on. A handful of requests.
func RefreshOrders(ctx context.Context, api API, c Catalog, now time.Time) (OrdersUpdate, error) {
	u := OrdersUpdate{At: now, Prices: map[int]Price{}, Delivery: map[int]int{}}
	var err error
	if u.Owned, err = ownedUnlocks(ctx, api); err != nil {
		return u, err
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
	// The delivery box is read after the orders, so an order that fills in between is
	// seen twice (open, and delivered) rather than not at all. The leftover order then
	// shows as redundant until the next refresh, instead of the skin briefly showing as
	// missing with no order.
	box, err := api.Delivery(ctx)
	if err != nil {
		return u, fmt.Errorf("delivery box: %w", err)
	}
	for _, it := range box {
		u.Delivery[it.ID] += it.Count
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

// ownedUnlocks returns every skin, finisher and mini the account has unlocked.
func ownedUnlocks(ctx context.Context, api API) ([]Key, error) {
	skins, err := api.AccountSkins(ctx)
	if err != nil {
		return nil, fmt.Errorf("account skins: %w", err)
	}
	finishers, err := api.AccountFinishers(ctx)
	if err != nil {
		return nil, fmt.Errorf("account finishers: %w", err)
	}
	minis, err := api.AccountMinis(ctx)
	if err != nil {
		return nil, fmt.Errorf("account minis: %w", err)
	}
	owned := make([]Key, 0, len(skins)+len(finishers)+len(minis))
	for _, id := range skins {
		owned = append(owned, SkinKey(id))
	}
	// A finisher with only charges left is not unlocked.
	for _, f := range finishers {
		if f.Permanent {
			owned = append(owned, FinisherKey(f.ID))
		}
	}
	for _, id := range minis {
		owned = append(owned, MiniKey(id))
	}
	return owned, nil
}

// PriceCandidates returns the items worth pricing: those that unlock a Guaranteed
// Wardrobe Unlock skin, finisher or mini, or a Black Lion weapon set skin, we don't
// have and haven't bought (an item for it in the delivery box). Items we have orders on
// are left out because RefreshOrders keeps them current. keep is the items whose
// stored prices a prices refresh keeps: the candidates and the items with orders.
func PriceCandidates(c Catalog, snap Snapshot) (ids, keep []int) {
	skip := map[int]bool{}
	for _, o := range snap.Orders {
		skip[o.ItemID] = true
	}
	locked := c.locked(snap)
	tracked := c.tracked()
	for _, it := range c.Items {
		if skip[it.ID] {
			continue
		}
		for _, s := range it.Skins {
			if tracked[s] && locked(s) {
				ids = append(ids, it.ID)
				break
			}
		}
	}
	slices.Sort(ids)
	keep = append(slices.Clone(ids), slices.Collect(maps.Keys(skip))...)
	return ids, keep
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

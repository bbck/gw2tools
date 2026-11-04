// Package wardrobe tracks trading post buy orders for items that unlock wardrobe skins.
package wardrobe

import "time"

// Skin is a wardrobe skin from the seeded catalogue.
type Skin struct {
	ID      int
	Name    string
	Section string // armor, weapon or back
	Type    string // e.g. Sword, Coat
	Rarity  string
	// GWU marks skins that can drop from a Guaranteed Wardrobe Unlock.
	GWU bool
}

// Item is a tradeable item that unlocks one or more skins.
type Item struct {
	ID     int
	Name   string
	Rarity string
	Skins  []int
}

// Catalog is the static, seeded data: skins and the tradeable items that unlock them.
type Catalog struct {
	Skins map[int]Skin
	Items map[int]Item
}

// skinItems indexes the catalogue by skin.
func (c Catalog) skinItems() map[int][]int {
	out := make(map[int][]int)
	for _, it := range c.Items {
		for _, s := range it.Skins {
			out[s] = append(out[s], it.ID)
		}
	}
	return out
}

// Price is the trading post state for an item.
type Price struct {
	ItemID       int
	BuyPrice     int // highest buy order, 0 if none
	BuyQuantity  int
	SellPrice    int // lowest sell listing, 0 if none
	SellQuantity int
	// TopBuyQuantity is the quantity wanted at BuyPrice, from the order book. Only
	// fetched for items with one of our orders; 0 when unknown.
	TopBuyQuantity int
}

// Order is one of the account's open buy orders.
type Order struct {
	ID       int
	ItemID   int
	Price    int
	Quantity int
	Created  time.Time
}

// Snapshot is the stored account and market state.
type Snapshot struct {
	OrdersAt time.Time // last orders refresh
	PricesAt time.Time // start of the last successful prices refresh
	Owned    map[int]bool
	Orders   []Order
	Prices   map[int]Price
	// Delivery is the trading post delivery box: item id to count.
	Delivery map[int]int
}

// OrdersUpdate is the result of an orders refresh.
type OrdersUpdate struct {
	At     time.Time
	Owned  []int // every skin the account has unlocked
	Orders []Order
	// Prices for items we have orders on, from the order book.
	Prices map[int]Price
	// Delivery is the trading post delivery box: item id to count.
	Delivery map[int]int
}

// Package wardrobe tracks trading post buy orders for items that unlock wardrobe skins,
// finishers and minis.
package wardrobe

import (
	"cmp"
	"time"
)

// Kind is the API id space an unlock belongs to.
type Kind string

const (
	KindSkin     Kind = "skin"     // /v2/skins, /v2/account/skins
	KindFinisher Kind = "finisher" // /v2/finishers, /v2/account/finishers
	KindMini     Kind = "mini"     // /v2/minis, /v2/account/minis
)

// Key identifies an unlock. API ids are only unique within a kind: there is a skin 24
// and a finisher 24.
type Key struct {
	Kind Kind
	ID   int
}

func SkinKey(id int) Key     { return Key{KindSkin, id} }
func FinisherKey(id int) Key { return Key{KindFinisher, id} }
func MiniKey(id int) Key     { return Key{KindMini, id} }

func compareKeys(a, b Key) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ID, b.ID)) }

// Skin is an unlock from the seeded catalogue: a wardrobe skin, a finisher or a mini.
// The code calls them all skins; Key.Kind tells them apart.
type Skin struct {
	ID      Key
	Name    string
	Section string // armor, weapon, back, finisher or mini
	Type    string // e.g. Sword, Coat; empty for finishers and minis
	Rarity  string
	// GWU marks unlocks that can drop from a Guaranteed Wardrobe Unlock.
	GWU bool
}

// Item is a tradeable item that unlocks one or more skins, finishers or minis.
type Item struct {
	ID     int
	Name   string
	Rarity string
	Skins  []Key
}

// Set is a Black Lion weapon set: the skins asked for by its collection achievement.
type Set struct {
	ID    int // the collection achievement id
	Name  string
	Skins []int // skin ids (KindSkin)
}

// Catalog is the static, seeded data: skins, the tradeable items that unlock them, and
// the Black Lion weapon sets.
type Catalog struct {
	Skins map[Key]Skin
	Items map[int]Item
	Sets  []Set // by name

	itemsBySkin map[Key][]int // set by Index
}

// Index precomputes the skin index of a catalogue that is reused across requests, so
// pages don't rebuild it from every item on each render.
func (c *Catalog) Index() {
	c.itemsBySkin = nil
	c.itemsBySkin = c.skinItems()
}

// skinItems indexes the catalogue by skin.
func (c Catalog) skinItems() map[Key][]int {
	if c.itemsBySkin != nil {
		return c.itemsBySkin
	}
	out := make(map[Key][]int)
	for _, it := range c.Items {
		for _, s := range it.Skins {
			out[s] = append(out[s], it.ID)
		}
	}
	return out
}

// tracked are the unlocks the pages track: Guaranteed Wardrobe Unlock skins, finishers
// and minis, and the skins in Black Lion weapon sets.
func (c Catalog) tracked() map[Key]bool {
	out := map[Key]bool{}
	for _, sk := range c.Skins {
		if sk.GWU {
			out[sk.ID] = true
		}
	}
	for _, set := range c.Sets {
		for _, s := range set.Skins {
			out[SkinKey(s)] = true
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
	Owned    map[Key]bool
	Orders   []Order
	Prices   map[int]Price
	// Delivery is the trading post delivery box: item id to count.
	Delivery map[int]int
	// Chest names the weapon sets in the current Black Lion Chest, from the wiki.
	Chest []string
}

// OrdersUpdate is the result of an orders refresh.
type OrdersUpdate struct {
	At time.Time
	// Owned is every skin, permanent finisher and mini the account has unlocked.
	Owned  []Key
	Orders []Order
	// Prices for items we have orders on, from the order book.
	Prices map[int]Price
	// Delivery is the trading post delivery box: item id to count.
	Delivery map[int]int
}

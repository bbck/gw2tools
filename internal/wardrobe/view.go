package wardrobe

import (
	"cmp"
	"slices"
)

// Status is where one of our buy orders stands against the order book.
type Status int

const (
	StatusNoOrder Status = iota
	StatusUnknown        // no price data for the item
	StatusOutbid
	StatusTied // at the top price, but someone else is bidding the same
	StatusTop
)

// statuses holds each Status's label, CSS class and attention rank (lower sorts
// first because it needs action sooner).
var statuses = [...]struct {
	label, class string
	attention    int
}{
	StatusNoOrder: {"No order", "none", 3},
	StatusUnknown: {"Unknown", "unknown", 2},
	StatusOutbid:  {"Outbid", "outbid", 0},
	StatusTied:    {"Tied", "tied", 1},
	StatusTop:     {"Top bid", "top", 3},
}

func (s Status) String() string { return statuses[s].label }

// Class is the CSS class used to render the status.
func (s Status) Class() string { return statuses[s].class }

func attention(s Status) int { return statuses[s].attention }

// OrderRow is one of our buy orders with its standing.
type OrderRow struct {
	Order
	Item     Item
	Market   Price // trading post state for Item
	Status   Status
	OutbidBy int // copper above our price, when outbid
	// SuggestedBid is the price that would retake the top spot, when outbid.
	SuggestedBid int
}

// Row is one skin.
type Row struct {
	Rank int // position in the cheapest list, 0 when not in it
	Skin Skin
	// Item is the cheapest priced item that unlocks the skin, else any item for it.
	Item  Item
	Price Price
	// Cost is what the skin costs: the top buy order, or the lowest sell listing if
	// nobody is bidding.
	Cost int
	// SuggestedBid outbids the current top buy order by one copper.
	SuggestedBid int
	Orders       []OrderRow
	// Status is the standing of whichever of our orders on the skin most needs
	// attention: an outbid order shows even if another order is top.
	Status Status
}

// DeliveryRow is an item in the delivery box that unlocks GWU skins we don't have.
type DeliveryRow struct {
	Item  Item
	Count int
	Skins []Skin // the locked GWU skins it unlocks
}

type View struct {
	Limit int
	// Delivery are delivery box items that unlock a locked GWU skin, by skin name.
	Delivery []DeliveryRow
	// Cheapest is the Limit cheapest unowned Guaranteed Wardrobe Unlock skins.
	Cheapest []Row
	// CheapestCost is the sum of Cost over Cheapest.
	CheapestCost int
	// Others are unowned skins we have orders on that are not in Cheapest, e.g.
	// because the price rose. Outbid orders sort first.
	Others []Row
	// OthersCost is the sum of Cost over Others, the same measure as CheapestCost.
	OthersCost int
	// Redundant are orders on items whose skins are all already unlocked or bought.
	Redundant []OrderRow
	// OtherOrders counts orders on items that do not unlock a skin; they are ignored.
	OtherOrders int
	// GWUTotal and GWUOwned count GWU skins that have a tradeable item.
	GWUTotal int
	GWUOwned int
}

// BuildView combines the catalogue and the latest snapshot into the wardrobe page.
func BuildView(c Catalog, snap Snapshot, limit int) View {
	v := View{Limit: limit}
	skinItems := c.skinItems()
	locked, delivery := c.locked(snap)
	v.Delivery = delivery

	var ordersBySkin map[Key][]OrderRow
	ordersBySkin, v.Redundant, v.OtherOrders = groupOrders(c, snap, locked)

	var candidates []Row
	for _, sk := range c.Skins {
		// Only skins with a tradeable item count; the rest can't be bought.
		if !sk.GWU || len(skinItems[sk.ID]) == 0 {
			continue
		}
		v.GWUTotal++
		if snap.Owned[sk.ID] {
			v.GWUOwned++
			continue
		}
		if !locked(sk.ID) { // bought
			continue
		}
		if r, ok := skinRow(sk, skinItems[sk.ID], c, snap.Prices, ordersBySkin[sk.ID]); ok {
			candidates = append(candidates, r)
		}
	}
	slices.SortFunc(candidates, byCost)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	inCheapest := map[Key]bool{}
	for i := range candidates {
		candidates[i].Rank = i + 1
		v.CheapestCost += candidates[i].Cost
		inCheapest[candidates[i].Skin.ID] = true
	}
	v.Cheapest = candidates

	for skinID, orders := range ordersBySkin {
		if inCheapest[skinID] {
			continue
		}
		r, _ := skinRow(c.Skins[skinID], skinItems[skinID], c, snap.Prices, orders)
		v.Others = append(v.Others, r)
		v.OthersCost += r.Cost
	}
	// Orders needing attention first, then by price.
	slices.SortFunc(v.Others, func(a, b Row) int {
		return cmp.Or(cmp.Compare(attention(a.Status), attention(b.Status)), byCost(a, b))
	})
	return v
}

// groupOrders rates our orders and groups them by the locked skins their items
// unlock. redundant are orders whose skins are all unlocked or bought; other counts
// orders on items that unlock no skin.
func groupOrders(c Catalog, snap Snapshot, locked func(Key) bool) (bySkin map[Key][]OrderRow, redundant []OrderRow, other int) {
	// Quantity we are bidding per item and price, to tell "top" from "tied".
	type itemPrice struct{ item, price int }
	ourQty := map[itemPrice]int{}
	for _, o := range snap.Orders {
		ourQty[itemPrice{o.ItemID, o.Price}] += o.Quantity
	}
	bySkin = map[Key][]OrderRow{}
	for _, o := range snap.Orders {
		it, ok := c.Items[o.ItemID]
		if !ok {
			other++
			continue
		}
		or := orderRow(o, it, snap.Prices, ourQty[itemPrice{o.ItemID, o.Price}])
		var wanted []Key
		for _, s := range it.Skins {
			if _, known := c.Skins[s]; known && locked(s) {
				wanted = append(wanted, s)
			}
		}
		if len(wanted) == 0 {
			redundant = append(redundant, or)
			continue
		}
		for _, s := range wanted {
			bySkin[s] = append(bySkin[s], or)
		}
	}
	slices.SortFunc(redundant, func(a, b OrderRow) int {
		return cmp.Or(cmp.Compare(a.Item.Name, b.Item.Name), cmp.Compare(a.ID, b.ID))
	})
	return bySkin, redundant, other
}

// locked reports which skins the account still needs: not unlocked, and not bought.
// A skin with an item in the delivery box counts as bought, because collecting the
// item unlocks it. Every "is this skin still wanted" check goes through locked.
// delivery lists the delivery box items that unlock a locked GWU skin.
func (c Catalog) locked(snap Snapshot) (locked func(Key) bool, delivery []DeliveryRow) {
	bought := map[Key]bool{}
	for itemID, count := range snap.Delivery {
		it, ok := c.Items[itemID]
		if !ok {
			continue
		}
		r := DeliveryRow{Item: it, Count: count}
		for _, id := range it.Skins {
			if snap.Owned[id] {
				continue
			}
			bought[id] = true
			if sk := c.Skins[id]; sk.GWU {
				r.Skins = append(r.Skins, sk)
			}
		}
		if len(r.Skins) > 0 {
			slices.SortFunc(r.Skins, func(a, b Skin) int { return cmp.Compare(a.Name, b.Name) })
			delivery = append(delivery, r)
		}
	}
	slices.SortFunc(delivery, func(a, b DeliveryRow) int {
		return cmp.Or(cmp.Compare(a.Skins[0].Name, b.Skins[0].Name), cmp.Compare(a.Item.ID, b.Item.ID))
	})
	return func(skin Key) bool { return !snap.Owned[skin] && !bought[skin] }, delivery
}

func byCost(a, b Row) int {
	return cmp.Or(cmp.Compare(a.Cost, b.Cost), cmp.Compare(a.Skin.Name, b.Skin.Name), compareKeys(a.Skin.ID, b.Skin.ID))
}

// skinRow picks the cheapest priced item for the skin. ok is false when no item
// has any buy orders or sell listings; Item is then the lowest-id item, if any.
func skinRow(sk Skin, itemIDs []int, c Catalog, prices map[int]Price, orders []OrderRow) (Row, bool) {
	r := Row{Skin: sk, Orders: orders, Status: mostUrgent(orders)}
	found := false
	for _, id := range itemIDs {
		p, ok := prices[id]
		if !ok {
			continue
		}
		cost := cmp.Or(p.BuyPrice, p.SellPrice)
		if cost == 0 {
			continue
		}
		it := c.Items[id]
		if !found || cost < r.Cost || (cost == r.Cost && it.ID < r.Item.ID) {
			r.Item, r.Price, r.Cost, found = it, p, cost, true
		}
	}
	if !found && len(itemIDs) > 0 {
		r.Item = c.Items[slices.Min(itemIDs)] // still searchable on the trading post
	}
	if found && r.Price.BuyPrice > 0 {
		r.SuggestedBid = r.Price.BuyPrice + 1
	}
	return r, found
}

// mostUrgent is the status of whichever order needs action soonest, StatusNoOrder
// when there are none.
func mostUrgent(orders []OrderRow) Status {
	s := StatusNoOrder
	for i, o := range orders {
		if i == 0 || attention(o.Status) < attention(s) {
			s = o.Status
		}
	}
	return s
}

func orderRow(o Order, it Item, prices map[int]Price, ourQtyAtPrice int) OrderRow {
	r := OrderRow{Order: o, Item: it}
	p, ok := prices[o.ItemID]
	if !ok {
		r.Status = StatusUnknown
		return r
	}
	r.Market = p
	switch {
	case p.BuyPrice > o.Price:
		r.Status = StatusOutbid
		r.OutbidBy = p.BuyPrice - o.Price
		r.SuggestedBid = p.BuyPrice + 1
	case p.BuyPrice == o.Price && p.TopBuyQuantity > ourQtyAtPrice:
		r.Status = StatusTied
		r.SuggestedBid = p.BuyPrice + 1
	default:
		r.Status = StatusTop
	}
	return r
}

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
	StatusBehindOwn // below the top bid, but the top bid is one of our own orders
)

// statuses holds each Status's label, CSS class, attention rank (lower sorts first
// because it needs action sooner) and standing (lower is closer to filling).
var statuses = [...]struct {
	label, class        string
	attention, standing int
}{
	StatusNoOrder:   {"No order", "none", 3, 5},
	StatusUnknown:   {"Unknown", "unknown", 2, 4},
	StatusOutbid:    {"Outbid", "outbid", 0, 3},
	StatusTied:      {"Tied", "tied", 1, 2},
	StatusTop:       {"Top bid", "top", 3, 0},
	StatusBehindOwn: {"Behind own bid", "own", 3, 1}, // raising it would only outbid ourselves
}

func (s Status) String() string { return statuses[s].label }

// Class is the CSS class used to render the status.
func (s Status) Class() string { return statuses[s].class }

func attention(s Status) int { return statuses[s].attention }

func standing(s Status) int { return statuses[s].standing }

// mostUrgent is whichever status needs action sooner. Any order's status beats
// StatusNoOrder.
func mostUrgent(a, b Status) Status {
	if a == StatusNoOrder || attention(b) < attention(a) {
		return b
	}
	return a
}

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
	// Orders are our orders on the skin, highest bid first. The pages show only the
	// highest, the one closest to winning, and count the rest.
	Orders []OrderRow
	// Status is the standing of our highest bid on the skin.
	Status Status
}

// Order is our highest bid on the skin, nil when there are none.
func (r Row) Order() *OrderRow {
	if len(r.Orders) == 0 {
		return nil
	}
	return &r.Orders[0]
}

// SameBids counts our orders on the skin besides the highest at the same price.
func (r Row) SameBids() int {
	n := 0
	for n+1 < len(r.Orders) && r.Orders[n+1].Order.Price == r.Orders[0].Order.Price {
		n++
	}
	return n
}

// LowerBids counts our orders on the skin below the highest bid.
func (r Row) LowerBids() int { return max(len(r.Orders)-1, 0) - r.SameBids() }

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
	locked := c.locked(snap)
	v.Delivery = c.deliveryRows(snap)

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
		if r := skinRow(sk, skinItems[sk.ID], c, snap.Prices, ordersBySkin[sk.ID]); r.Cost > 0 {
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
		r := skinRow(c.Skins[skinID], skinItems[skinID], c, snap.Prices, orders)
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
// unlock, highest bid first; between equal bids, the one closest to filling.
// redundant are orders whose skins are all unlocked or bought; other counts orders
// on items that unlock no skin.
func groupOrders(c Catalog, snap Snapshot, locked func(Key) bool) (bySkin map[Key][]OrderRow, redundant []OrderRow, other int) {
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
		or := orderRow(o, it, snap.Prices, ourQty)
		var wanted []Key
		for _, s := range it.Skins {
			if locked(s) {
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
	for _, orders := range bySkin {
		slices.SortFunc(orders, func(a, b OrderRow) int {
			return cmp.Or(cmp.Compare(b.Order.Price, a.Order.Price),
				cmp.Compare(standing(a.Status), standing(b.Status)), cmp.Compare(a.ID, b.ID))
		})
	}
	slices.SortFunc(redundant, func(a, b OrderRow) int {
		return cmp.Or(cmp.Compare(a.Item.Name, b.Item.Name), cmp.Compare(a.ID, b.ID))
	})
	return bySkin, redundant, other
}

// locked reports which skins the account still needs: not unlocked, and not bought.
// A skin with an item in the delivery box counts as bought, because collecting the
// item unlocks it. Every "is this skin still wanted" check goes through locked.
func (c Catalog) locked(snap Snapshot) func(Key) bool {
	bought := map[Key]bool{}
	for itemID := range snap.Delivery {
		for _, id := range c.Items[itemID].Skins {
			bought[id] = true
		}
	}
	return func(skin Key) bool { return !snap.Owned[skin] && !bought[skin] }
}

// deliveryRows lists the delivery box items that unlock a GWU skin we don't have, by
// skin name.
func (c Catalog) deliveryRows(snap Snapshot) []DeliveryRow {
	var delivery []DeliveryRow
	for itemID, count := range snap.Delivery {
		it, ok := c.Items[itemID]
		if !ok {
			continue
		}
		r := DeliveryRow{Item: it, Count: count}
		for _, id := range it.Skins {
			if sk := c.Skins[id]; sk.GWU && !snap.Owned[id] {
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
	return delivery
}

func byCost(a, b Row) int {
	return cmp.Or(cmp.Compare(a.Cost, b.Cost), cmp.Compare(a.Skin.Name, b.Skin.Name), compareKeys(a.Skin.ID, b.Skin.ID))
}

// skinRow picks the cheapest priced item for the skin. Cost is 0 when no item has
// any buy orders or sell listings; Item is then the lowest-id item, if any.
func skinRow(sk Skin, itemIDs []int, c Catalog, prices map[int]Price, orders []OrderRow) Row {
	r := Row{Skin: sk, Orders: orders}
	if o := r.Order(); o != nil {
		r.Status = o.Status
	}
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
		if r.Cost == 0 || cost < r.Cost || (cost == r.Cost && it.ID < r.Item.ID) {
			r.Item, r.Price, r.Cost = it, p, cost
		}
	}
	if r.Cost == 0 && len(itemIDs) > 0 {
		r.Item = c.Items[slices.Min(itemIDs)] // still searchable on the trading post
	}
	if r.Price.BuyPrice > 0 {
		r.SuggestedBid = r.Price.BuyPrice + 1
	}
	return r
}

// itemPrice keys the quantity we are bidding per item and price, to tell "top" from
// "tied", and our own orders from other bidders'.
type itemPrice struct{ item, price int }

func orderRow(o Order, it Item, prices map[int]Price, ourQty map[itemPrice]int) OrderRow {
	r := OrderRow{Order: o, Item: it}
	p, ok := prices[o.ItemID]
	if !ok {
		r.Status = StatusUnknown
		return r
	}
	r.Market = p
	switch {
	case p.BuyPrice > o.Price && ourQty[itemPrice{o.ItemID, p.BuyPrice}] > 0:
		r.Status = StatusBehindOwn
	case p.BuyPrice > o.Price:
		r.Status = StatusOutbid
		r.OutbidBy = p.BuyPrice - o.Price
		r.SuggestedBid = p.BuyPrice + 1
	case p.BuyPrice == o.Price && p.TopBuyQuantity > ourQty[itemPrice{o.ItemID, o.Price}]:
		r.Status = StatusTied
		r.SuggestedBid = p.BuyPrice + 1
	default:
		r.Status = StatusTop
	}
	return r
}

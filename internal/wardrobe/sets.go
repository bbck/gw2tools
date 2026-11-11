package wardrobe

import (
	"cmp"
	"slices"
	"strings"
)

// SetRow is one Black Lion weapon set and what it takes to complete it.
type SetRow struct {
	Set Set
	// InChest marks a set in the current Black Lion Chest.
	InChest bool
	Owned   int // unlocked
	Bought  int // waiting in the delivery box
	// Missing are the skins neither unlocked nor bought: priced cheapest first, then
	// unpriced, then untradeable.
	Missing []Row
	// Cost is the sum of Cost over the priced Missing skins.
	Cost int
	// Unpriced counts Missing skins with a trading post item but no price yet, so Cost
	// is a lower bound. Untradeable counts Missing skins no trading post item unlocks.
	Unpriced    int
	Untradeable int
	// Orders counts our orders on Missing skins; Status is the most urgent of them.
	Orders int
	Status Status
}

// Complete reports whether every skin is unlocked or bought.
func (r SetRow) Complete() bool { return len(r.Missing) == 0 }

// Buyable reports whether every missing skin can be bought on the trading post.
func (r SetRow) Buyable() bool { return r.Untradeable == 0 }

type SetsView struct {
	// Sets are sorted by the cost to complete. Sets with skins that can't be bought
	// come after the rest, and complete sets last.
	Sets     []SetRow
	Complete int
}

// Availability of a missing skin on the trading post, in sort order.
const (
	priced = iota
	unpriced
	untradeable
)

func availability(r Row) int {
	switch {
	case r.Cost > 0:
		return priced
	case r.Item.ID != 0:
		return unpriced
	default:
		return untradeable
	}
}

// BuildSets combines the catalogue and the latest snapshot into the Black Lion weapon
// sets page.
func BuildSets(c Catalog, snap Snapshot) SetsView {
	var v SetsView
	inChest := map[string]bool{}
	for _, name := range snap.Chest {
		inChest[setKey(name)] = true
	}
	skinItems := c.skinItems()
	locked := c.locked(snap)
	bySkin, _, _ := groupOrders(c, snap, locked)

	for _, set := range c.Sets {
		r := SetRow{Set: set, InChest: inChest[setKey(set.Name)]}
		var orders []OrderRow
		for _, skinID := range set.Skins {
			id := SkinKey(skinID)
			switch {
			case snap.Owned[id]:
				r.Owned++
				continue
			case !locked(id):
				r.Bought++
				continue
			}
			row := skinRow(c.Skins[id], skinItems[id], c, snap.Prices, bySkin[id])
			switch availability(row) {
			case unpriced:
				r.Unpriced++
			case untradeable:
				r.Untradeable++
			}
			r.Cost += row.Cost
			orders = append(orders, row.Orders...)
			r.Missing = append(r.Missing, row)
		}
		r.Orders, r.Status = len(orders), mostUrgent(orders)
		slices.SortFunc(r.Missing, func(a, b Row) int {
			return cmp.Or(cmp.Compare(availability(a), availability(b)), byCost(a, b))
		})
		if r.Complete() {
			v.Complete++
		}
		v.Sets = append(v.Sets, r)
	}
	slices.SortFunc(v.Sets, func(a, b SetRow) int {
		return cmp.Or(cmp.Compare(setGroup(a), setGroup(b)), cmp.Compare(a.Cost, b.Cost),
			cmp.Compare(a.Set.Name, b.Set.Name), cmp.Compare(a.Set.ID, b.Set.ID))
	})
	return v
}

// setKey matches set names from the wiki to those from the API, which may differ in
// case or apostrophe style.
func setKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "’", "'"))
}

func setGroup(r SetRow) int {
	switch {
	case r.Complete():
		return 2
	case !r.Buyable():
		return 1
	default:
		return 0
	}
}

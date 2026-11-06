package wardrobe

import (
	"slices"
	"testing"
)

// setsCatalog extends testCatalog with sets:
//   - "Cheap": skins 1 (two items) and 2
//   - "Pricey": skins 3 and 4 (the box, item 30, unlocks both; item 40 unlocks 4)
//   - "Gem": skin 6, which no trading post item unlocks
//   - "Done": skin 5
func setsCatalog() Catalog {
	c := testCatalog()
	c.Sets = []Set{
		{ID: 104, Name: "Cheap", Skins: []int{1, 2}},
		{ID: 103, Name: "Done", Skins: []int{5}},
		{ID: 102, Name: "Gem", Skins: []int{6}},
		{ID: 101, Name: "Pricey", Skins: []int{3, 4}},
	}
	return c
}

func setNames(v SetsView) []string {
	var out []string
	for _, r := range v.Sets {
		out = append(out, r.Set.Name)
	}
	return out
}

func TestSetsSortedByCostToComplete(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{5: true},
		Prices: map[int]Price{
			10: price(10, 300, 400),
			11: price(11, 200, 250),
			20: price(20, 0, 900), // no bids: costs the lowest sell
			30: price(30, 5000, 6000),
			40: price(40, 4000, 4500),
		},
	}
	v := BuildSets(setsCatalog(), snap)

	if want := []string{"Cheap", "Pricey", "Gem", "Done"}; !slices.Equal(setNames(v), want) {
		t.Fatalf("sets = %v, want %v", setNames(v), want)
	}
	if v.Complete != 1 || !v.Sets[3].Complete() || v.Sets[3].Owned != 1 {
		t.Errorf("complete = %d, done row = %+v", v.Complete, v.Sets[3])
	}
	cheap := v.Sets[0]
	if cheap.Cost != 200+900 || len(cheap.Missing) != 2 {
		t.Fatalf("cheap = %+v", cheap)
	}
	// Cheapest skin first, each through its cheapest item.
	if m := cheap.Missing; m[0].Skin.ID != 1 || m[0].Item.ID != 11 || m[1].Skin.ID != 2 {
		t.Errorf("cheap missing = %+v", m)
	}
	if pricey := v.Sets[1]; pricey.Cost != 5000+4000 {
		t.Errorf("pricey cost = %d", pricey.Cost)
	}
	if gem := v.Sets[2]; gem.Buyable() || gem.Untradeable != 1 || gem.Cost != 0 {
		t.Errorf("gem = %+v", gem)
	}
}

func TestSetsOrdersBoughtAndUnpriced(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		Orders: []Order{
			{ID: 1, ItemID: 10, Price: 300, Quantity: 1}, // skin 1: top
			{ID: 2, ItemID: 20, Price: 50, Quantity: 1},  // skin 2: outbid
		},
		Prices: map[int]Price{
			10: {ItemID: 10, BuyPrice: 300, SellPrice: 400, TopBuyQuantity: 1},
			20: {ItemID: 20, BuyPrice: 80, SellPrice: 100, TopBuyQuantity: 1},
			// Item 30 (skins 3 and 4) and item 40 are not priced yet.
		},
		Delivery: map[int]int{40: 1}, // skin 4 bought
	}
	v := BuildSets(setsCatalog(), snap)

	byName := map[string]SetRow{}
	for _, r := range v.Sets {
		byName[r.Set.Name] = r
	}
	cheap := byName["Cheap"]
	if cheap.Orders != 2 || cheap.Status != StatusOutbid {
		t.Errorf("cheap orders = %d, status = %v, want 2 and outbid", cheap.Orders, cheap.Status)
	}
	pricey := byName["Pricey"]
	if pricey.Bought != 1 || len(pricey.Missing) != 1 || pricey.Unpriced != 1 || pricey.Cost != 0 {
		t.Fatalf("pricey = %+v", pricey)
	}
	// An unpriced skin still names an item to search for.
	if m := pricey.Missing[0]; m.Skin.ID != 3 || m.Item.ID != 30 {
		t.Errorf("unpriced row = %+v", m)
	}
	if pricey.Status != StatusNoOrder {
		t.Errorf("pricey status = %v", pricey.Status)
	}
}

func TestPriceCandidatesIncludeSetSkins(t *testing.T) {
	c := setsCatalog()
	c.Skins[5] = Skin{ID: 5, Name: "Echo"} // non-GWU, but in the "Done" set
	ids := PriceCandidates(c, Snapshot{Owned: map[int]bool{}})
	if !slices.Contains(ids, 50) {
		t.Errorf("candidates = %v, want item 50 for set skin 5", ids)
	}
	c.Sets = nil
	if ids := PriceCandidates(c, Snapshot{Owned: map[int]bool{}}); slices.Contains(ids, 50) {
		t.Errorf("candidates = %v, item 50 unlocks no GWU or set skin", ids)
	}
}

func TestSetsInChest(t *testing.T) {
	snap := Snapshot{Owned: map[int]bool{}, Chest: []string{"cheap", "Gem’s Not A Set"}}
	c := setsCatalog()
	c.Sets[2].Name = "Gem's" // straight apostrophe in the catalogue
	snap.Chest = append(snap.Chest, "Gem’s")
	v := BuildSets(c, snap)
	var in []string
	for _, r := range v.Sets {
		if r.InChest {
			in = append(in, r.Set.Name)
		}
	}
	slices.Sort(in)
	if want := []string{"Cheap", "Gem's"}; !slices.Equal(in, want) {
		t.Errorf("in chest = %v, want %v", in, want)
	}
}

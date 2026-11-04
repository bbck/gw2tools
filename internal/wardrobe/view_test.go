package wardrobe

import (
	"slices"
	"testing"
)

// testCatalog has four tradeable GWU skins (1-4), one non-GWU skin (5) with items for
// each, and a GWU skin (6) with no tradeable item.
// Skin 1 is unlocked by two items; item 30 is a box unlocking skins 3 and 4.
func testCatalog() Catalog {
	return Catalog{
		Skins: map[int]Skin{
			1: {ID: 1, Name: "Alpha", Section: "weapon", GWU: true},
			2: {ID: 2, Name: "Bravo", Section: "armor", GWU: true},
			3: {ID: 3, Name: "Charlie", Section: "back", GWU: true},
			4: {ID: 4, Name: "Delta", Section: "weapon", GWU: true},
			5: {ID: 5, Name: "Echo", Section: "weapon"},
			6: {ID: 6, Name: "Gem Store Only", Section: "armor", GWU: true}, // no tradeable item
		},
		Items: map[int]Item{
			10: {ID: 10, Name: "Alpha Sword", Skins: []int{1}},
			11: {ID: 11, Name: "Alpha Sword of Air", Skins: []int{1}},
			20: {ID: 20, Name: "Bravo Coat", Skins: []int{2}},
			30: {ID: 30, Name: "Box", Skins: []int{3, 4}},
			40: {ID: 40, Name: "Delta Axe", Skins: []int{4}},
			50: {ID: 50, Name: "Echo Staff", Skins: []int{5}},
		},
	}
}

func price(id, buy, sell int) Price {
	return Price{ItemID: id, BuyPrice: buy, BuyQuantity: 10, SellPrice: sell, SellQuantity: 10}
}

func TestCheapestPicksCheapestItemPerSkinAndSorts(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		Prices: map[int]Price{
			10: price(10, 500, 600),
			11: price(11, 300, 900), // cheaper buy price wins for skin 1
			20: price(20, 0, 200),   // no bids: falls back to sell price
			30: price(30, 1000, 2000),
			40: price(40, 50, 60),
		},
	}
	v := BuildView(testCatalog(), snap, 25)

	var got []int
	for _, r := range v.Cheapest {
		got = append(got, r.Skin.ID)
	}
	// Delta (50) < Bravo (200) < Alpha (300) < Charlie (1000). Echo is not GWU.
	if want := []int{4, 2, 1, 3}; !slices.Equal(got, want) {
		t.Fatalf("cheapest skins = %v, want %v", got, want)
	}
	if v.Cheapest[2].Item.ID != 11 {
		t.Errorf("Alpha item = %d, want 11", v.Cheapest[2].Item.ID)
	}
	if v.Cheapest[0].Rank != 1 || v.Cheapest[3].Rank != 4 {
		t.Errorf("ranks = %d..%d", v.Cheapest[0].Rank, v.Cheapest[3].Rank)
	}
	if v.Cheapest[0].SuggestedBid != 51 {
		t.Errorf("suggested bid = %d, want 51", v.Cheapest[0].SuggestedBid)
	}
	if v.Cheapest[1].SuggestedBid != 0 {
		t.Errorf("no-bid item suggested bid = %d, want 0", v.Cheapest[1].SuggestedBid)
	}
	if v.CheapestCost != 50+200+300+1000 {
		t.Errorf("CheapestCost = %d", v.CheapestCost)
	}
	if v.GWUTotal != 4 || v.GWUOwned != 0 {
		t.Errorf("GWU counts = %d/%d", v.GWUOwned, v.GWUTotal)
	}
}

func TestCheapestExcludesOwnedAndUnpricedAndLimits(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{4: true},
		Prices: map[int]Price{
			10: price(10, 300, 400),
			20: price(20, 0, 0), // nothing on the market
			30: price(30, 1000, 2000),
			40: price(40, 50, 60),
		},
	}
	v := BuildView(testCatalog(), snap, 1)
	if len(v.Cheapest) != 1 || v.Cheapest[0].Skin.ID != 1 {
		t.Fatalf("cheapest = %+v", v.Cheapest)
	}
	if v.GWUOwned != 1 {
		t.Errorf("GWUOwned = %d", v.GWUOwned)
	}
}

func TestOrderStatuses(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		Orders: []Order{
			{ID: 1, ItemID: 40, Price: 50, Quantity: 1},  // top
			{ID: 2, ItemID: 10, Price: 300, Quantity: 1}, // tied: 3 wanted at 300, we want 1
			{ID: 3, ItemID: 20, Price: 150, Quantity: 1}, // outbid by 50
		},
		Prices: map[int]Price{
			10: {ItemID: 10, BuyPrice: 300, SellPrice: 400, TopBuyQuantity: 3},
			20: {ItemID: 20, BuyPrice: 200, SellPrice: 250, TopBuyQuantity: 1},
			30: price(30, 1000, 2000),
			40: {ItemID: 40, BuyPrice: 50, SellPrice: 60, TopBuyQuantity: 1},
		},
	}
	v := BuildView(testCatalog(), snap, 25)
	status := map[int]Row{}
	for _, r := range v.Cheapest {
		status[r.Skin.ID] = r
	}
	if s := status[4].Status; s != StatusTop {
		t.Errorf("skin 4 status = %v, want top", s)
	}
	if s := status[1].Status; s != StatusTied {
		t.Errorf("skin 1 status = %v, want tied", s)
	}
	r := status[2]
	if r.Status != StatusOutbid || r.Orders[0].OutbidBy != 50 || r.Orders[0].SuggestedBid != 201 {
		t.Errorf("skin 2 = %v by %d bid %d, want outbid by 50 bid 201", r.Status, r.Orders[0].OutbidBy, r.Orders[0].SuggestedBid)
	}
	if s := status[3].Status; s != StatusNoOrder {
		t.Errorf("skin 3 status = %v, want no order", s)
	}
}

func TestTopWhenAllTopQuantityIsOurs(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		// Two of our own orders at the same price make up the whole top of the book.
		Orders: []Order{
			{ID: 1, ItemID: 40, Price: 50, Quantity: 1},
			{ID: 2, ItemID: 40, Price: 50, Quantity: 2},
		},
		Prices: map[int]Price{40: {ItemID: 40, BuyPrice: 50, SellPrice: 60, TopBuyQuantity: 3}},
	}
	v := BuildView(testCatalog(), snap, 25)
	for _, o := range v.Cheapest[0].Orders {
		if o.Status != StatusTop {
			t.Errorf("order %d status = %v, want top", o.ID, o.Status)
		}
	}
}

func TestOthersRedundantAndIgnoredOrders(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{2: true},
		Orders: []Order{
			{ID: 1, ItemID: 30, Price: 900, Quantity: 1},  // skin 3/4: outside limit, outbid
			{ID: 2, ItemID: 50, Price: 100, Quantity: 1},  // non-GWU skin 5: top
			{ID: 3, ItemID: 20, Price: 100, Quantity: 1},  // skin 2 owned: redundant
			{ID: 4, ItemID: 999, Price: 100, Quantity: 1}, // not a skin item
			{ID: 5, ItemID: 11, Price: 10, Quantity: 1},   // skin 1: in cheapest
		},
		Prices: map[int]Price{
			10: price(10, 10, 20),
			11: {ItemID: 11, BuyPrice: 10, SellPrice: 20, TopBuyQuantity: 1},
			20: price(20, 100, 200),
			30: {ItemID: 30, BuyPrice: 1000, SellPrice: 2000, TopBuyQuantity: 1},
			40: price(40, 5000, 6000),
			50: {ItemID: 50, BuyPrice: 100, SellPrice: 200, TopBuyQuantity: 1},
		},
	}
	v := BuildView(testCatalog(), snap, 1)

	if len(v.Cheapest) != 1 || v.Cheapest[0].Skin.ID != 1 || v.Cheapest[0].Status != StatusTop {
		t.Fatalf("cheapest = %+v", v.Cheapest)
	}
	// Box order 1 covers skins 3 and 4, both outside the limit; Echo order 2 is top.
	var others []int
	for _, r := range v.Others {
		others = append(others, r.Skin.ID)
	}
	// Outbid rows first (3 then 4 by cost), then the top bid on skin 5.
	if want := []int{3, 4, 5}; !slices.Equal(others, want) {
		t.Fatalf("others = %v, want %v", others, want)
	}
	// Skin 3 and 4 are both cheapest via the box (1000c); Echo costs 100c.
	if v.OthersCost != 1000+1000+100 {
		t.Errorf("OthersCost = %d, want 2100", v.OthersCost)
	}
	if v.Others[0].Status != StatusOutbid || v.Others[2].Status != StatusTop {
		t.Errorf("others statuses = %v, %v", v.Others[0].Status, v.Others[2].Status)
	}
	if len(v.Redundant) != 1 || v.Redundant[0].ID != 3 {
		t.Errorf("redundant = %+v", v.Redundant)
	}
	if v.OtherOrders != 1 {
		t.Errorf("OtherOrders = %d", v.OtherOrders)
	}
}

func TestUnknownPrice(t *testing.T) {
	snap := Snapshot{
		Owned:  map[int]bool{},
		Orders: []Order{{ID: 1, ItemID: 50, Price: 100, Quantity: 1}},
		Prices: map[int]Price{},
	}
	v := BuildView(testCatalog(), snap, 25)
	if len(v.Others) != 1 || v.Others[0].Status != StatusUnknown {
		t.Fatalf("others = %+v", v.Others)
	}
}

func TestRowStatusShowsMostUrgentOrder(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		Orders: []Order{
			{ID: 1, ItemID: 10, Price: 500, Quantity: 1}, // top
			{ID: 2, ItemID: 11, Price: 100, Quantity: 1}, // outbid
		},
		Prices: map[int]Price{
			10: {ItemID: 10, BuyPrice: 500, SellPrice: 600, TopBuyQuantity: 1},
			11: {ItemID: 11, BuyPrice: 300, SellPrice: 900, TopBuyQuantity: 1},
			40: price(40, 50, 60),
		},
	}
	v := BuildView(testCatalog(), snap, 1)
	if len(v.Others) != 1 || v.Others[0].Skin.ID != 1 || v.Others[0].Status != StatusOutbid {
		t.Fatalf("others = %+v, want skin 1 outbid", v.Others)
	}
}

func TestDeliveryBoxSkins(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{3: true},
		Prices: map[int]Price{
			10: price(10, 300, 400),
			20: price(20, 50, 60),
			30: price(30, 1000, 2000),
			40: price(40, 70, 80),
		},
		// Bought the helm (skin 2) and the box (skins 3, owned, and 4); item 999 is
		// not a skin item; item 50 unlocks a non-GWU skin.
		Delivery: map[int]int{20: 2, 30: 1, 999: 5, 50: 1},
	}
	v := BuildView(testCatalog(), snap, 25)

	if len(v.Delivery) != 2 {
		t.Fatalf("delivery = %+v", v.Delivery)
	}
	if d := v.Delivery[0]; d.Item.ID != 20 || d.Count != 2 || len(d.Skins) != 1 || d.Skins[0].ID != 2 {
		t.Errorf("first delivery row = %+v, want the helm", d)
	}
	if d := v.Delivery[1]; d.Item.ID != 30 || len(d.Skins) != 1 || d.Skins[0].ID != 4 {
		t.Errorf("box row = %+v, want only the locked skin 4", d)
	}
	// Bought skins leave the cheapest list; only Alpha (skin 1) remains.
	var cheapest []int
	for _, r := range v.Cheapest {
		cheapest = append(cheapest, r.Skin.ID)
	}
	if !slices.Equal(cheapest, []int{1}) {
		t.Errorf("cheapest = %v, want [1]", cheapest)
	}
}

func TestOrdersOnBoughtSkinsAreRedundant(t *testing.T) {
	snap := Snapshot{
		Owned: map[int]bool{},
		Orders: []Order{
			{ID: 1, ItemID: 11, Price: 5, Quantity: 1},   // skin 1, bought via item 10: redundant
			{ID: 2, ItemID: 20, Price: 50, Quantity: 1},  // leftover of a part-filled order: redundant
			{ID: 3, ItemID: 30, Price: 900, Quantity: 1}, // box: skin 4 bought, skin 3 still wanted
		},
		Prices: map[int]Price{
			10: price(10, 10, 20),
			11: price(11, 10, 20),
			20: price(20, 50, 60),
			30: price(30, 1000, 2000),
			40: price(40, 70, 80),
		},
		Delivery: map[int]int{10: 1, 20: 1, 40: 1},
	}
	v := BuildView(testCatalog(), snap, 0)

	var redundant []int
	for _, o := range v.Redundant {
		redundant = append(redundant, o.ID)
	}
	if !slices.Equal(redundant, []int{1, 2}) {
		t.Errorf("redundant orders = %v, want [1 2]", redundant)
	}
	if len(v.Others) != 1 || v.Others[0].Skin.ID != 3 {
		t.Fatalf("others = %+v, want only skin 3", v.Others)
	}
	if v.OthersCost != 1000 {
		t.Errorf("OthersCost = %d, want 1000", v.OthersCost)
	}
}

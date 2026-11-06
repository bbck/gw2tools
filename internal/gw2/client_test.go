package gw2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-key")
	c.MinInterval = 0
	c.RetryBackoff = time.Millisecond
	return c
}

func TestAccountSkinsSendsKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/v2/account/skins" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`[1,2,3]`))
	})
	ids, err := c.AccountSkins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestAuthRequiresKey(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not be sent without a key")
	})
	c.APIKey = ""
	if _, err := c.AccountSkins(context.Background()); err == nil {
		t.Fatal("expected error without API key")
	}
}

func TestCurrentBuysPaginates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Page-Total", "2")
		switch r.URL.Query().Get("page") {
		case "0":
			w.Write([]byte(`[{"id":1,"item_id":10,"price":100,"quantity":1,"created":"2026-01-01T00:00:00Z"}]`))
		case "1":
			w.Write([]byte(`[{"id":2,"item_id":11,"price":200,"quantity":2,"created":"2026-01-02T00:00:00Z"}]`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"text":"page out of range"}`))
		}
	})
	orders, err := c.CurrentBuys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 2 || orders[1].ItemID != 11 || orders[1].Quantity != 2 {
		t.Fatalf("orders = %+v", orders)
	}
}

func TestCurrentBuysEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Page-Total", "0")
		w.Write([]byte(`[]`))
	})
	orders, err := c.CurrentBuys(context.Background())
	if err != nil || len(orders) != 0 {
		t.Fatalf("orders = %v, err = %v", orders, err)
	}
}

func TestPricesBatchesAndSkipsInvalid(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		ids := strings.Split(r.URL.Query().Get("ids"), ",")
		if len(ids) > MaxIDs {
			t.Errorf("batch of %d ids", len(ids))
		}
		switch ids[0] {
		case "1":
			// Partial content: only some ids valid.
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte(`[{"id":1,"buys":{"quantity":5,"unit_price":10},"sells":{"quantity":3,"unit_price":20}}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"text":"all ids provided are invalid"}`))
		}
	})
	ids := make([]int, MaxIDs+5)
	for i := range ids {
		ids[i] = i + 1
	}
	prices, err := c.Prices(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(prices) != 1 || prices[0].Buys.UnitPrice != 10 || prices[0].Sells.UnitPrice != 20 {
		t.Fatalf("prices = %+v", prices)
	}
}

func TestErrorText(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"text":"requires scope tradingpost"}`))
	})
	_, err := c.CurrentBuys(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires scope tradingpost") {
		t.Fatalf("err = %v", err)
	}
}

func TestItemsDecodesSkins(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[
			{"id":1,"name":"Sword","type":"Weapon","rarity":"Rare","default_skin":5232,"details":{"type":"Sword"}},
			{"id":2,"name":"Unlock","type":"Consumable","rarity":"Exotic","details":{"type":"Transmutation","skins":[7,8]}}
		]`))
	})
	items, err := c.Items(context.Background(), []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].DefaultSkin != 5232 || len(items[1].Details.Skins) != 2 {
		t.Fatalf("items = %+v", items)
	}
}

func TestRetriesRateLimit(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"text":"too many requests"}`))
			return
		}
		w.Write([]byte(`[1]`))
	})
	ids, err := c.CommerceItemIDs(context.Background())
	if err != nil || len(ids) != 1 || calls != 3 {
		t.Fatalf("ids = %v, err = %v, calls = %d", ids, err, calls)
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := c.CommerceItemIDs(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if calls != maxRetries+1 {
		t.Errorf("calls = %d, want %d", calls, maxRetries+1)
	}
}

func TestCurrentBuysEmptyOutOfRange(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"text":"page out of range. Use page values 0 - -1."}`))
	})
	orders, err := c.CurrentBuys(context.Background())
	if err != nil || len(orders) != 0 {
		t.Fatalf("orders = %v, err = %v", orders, err)
	}
}

func TestBatchedOther404IsError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`not found`))
	})
	if _, err := c.Prices(context.Background(), []int{1, 2}); err == nil {
		t.Fatal("a 404 that isn't 'all ids invalid' should be an error")
	}
}

func TestCurrentBuysPageVanishes(t *testing.T) {
	// Orders closed while paging: page 1 is out of range by the time it's fetched.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Page-Total", "2")
		if r.URL.Query().Get("page") == "0" {
			w.Write([]byte(`[{"id":1,"item_id":10,"price":100,"quantity":1,"created":"2026-01-01T00:00:00Z"}]`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"text":"page out of range. Use page values 0 - 0."}`))
	})
	orders, err := c.CurrentBuys(context.Background())
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders = %v, err = %v", orders, err)
	}
}

func TestRequests(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 1, MaxIDs: 1, MaxIDs + 1: 2} {
		if got := Requests(n); got != want {
			t.Errorf("Requests(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestDelivery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/commerce/delivery" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request = %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"coins":1234,"items":[{"id":10,"count":2},{"id":11,"count":1}]}`))
	})
	d, err := c.Delivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 || d[0].ID != 10 || d[0].Count != 2 {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestNonJSONErrorIsSummarised(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("<html><body><p>Page not found</p><dd>203.0.113.7</dd></body></html>"))
	})
	_, err := c.AccountSkins(context.Background())
	if err == nil || err.Error() != "gw2 api: status 404: Not Found (non-JSON response)" {
		t.Fatalf("err = %v", err)
	}
}

func TestAchievementsAndSkins(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("%s sent the API key", r.URL.Path)
		}
		switch r.URL.Path {
		case "/v2/achievements/categories/76":
			w.Write([]byte(`{"id":76,"name":"Black Lion Collections","achievements":[1732,5066]}`))
		case "/v2/achievements":
			if got := r.URL.Query().Get("ids"); got != "1732,5066" {
				t.Errorf("achievement ids = %q", got)
			}
			w.Write([]byte(`[{"id":1732,"name":"Chaos Weapon Collection","bits":[{"type":"Skin","id":4672},{"type":"Skin","id":4673}]},
				{"id":5066,"name":"Skyscale Toy Training","bits":[{"type":"Item","id":12}]}]`))
		case "/v2/skins":
			w.Write([]byte(`[{"id":4672,"name":"Chaos Longbow","type":"Weapon","rarity":"Exotic","details":{"type":"LongBow"}}]`))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	ids, err := c.AchievementCategory(ctx, 76)
	if err != nil || len(ids) != 2 {
		t.Fatalf("category = %v, %v", ids, err)
	}
	as, err := c.Achievements(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 || as[0].Name != "Chaos Weapon Collection" || len(as[0].Bits) != 2 || as[0].Bits[1] != (AchievementBit{Type: "Skin", ID: 4673}) {
		t.Fatalf("achievements = %+v", as)
	}
	skins, err := c.Skins(ctx, []int{4672})
	if err != nil {
		t.Fatal(err)
	}
	if len(skins) != 1 || skins[0].Type != "Weapon" || skins[0].Details.Type != "LongBow" {
		t.Fatalf("skins = %+v", skins)
	}
}

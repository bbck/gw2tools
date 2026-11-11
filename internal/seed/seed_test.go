package seed

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/bbck/gw2tools/internal/gw2"
	"github.com/bbck/gw2tools/internal/store"
	"github.com/bbck/gw2tools/internal/wardrobe"
)

func TestParseContent(t *testing.T) {
	f, err := os.Open("testdata/content.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	skins, err := ParseContent(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(skins) != 5 {
		t.Fatalf("skins = %+v, want armor, weapon, finisher and mini", skins)
	}
	if s := skins[wardrobe.SkinKey(100)]; !s.GWU || s.Section != "armor" || s.Type != "Helm" || s.Name != "Test Helm" {
		t.Errorf("skin 100 = %+v", s)
	}
	if skins[wardrobe.SkinKey(101)].GWU {
		t.Error("skin 101 should not be GWU")
	}
	if f := skins[wardrobe.FinisherKey(24)]; f != (wardrobe.Skin{ID: wardrobe.FinisherKey(24), Name: "Twisted Watchwork Finisher", Section: "finisher", Rarity: "Rare", GWU: true}) {
		t.Errorf("finisher 24 = %+v", f)
	}
	if m := skins[wardrobe.MiniKey(300)]; m != (wardrobe.Skin{ID: wardrobe.MiniKey(300), Name: "Mini Test", Section: "mini", Rarity: "Rare", GWU: true}) {
		t.Errorf("mini 300 = %+v", m)
	}
}

func TestMapItems(t *testing.T) {
	keys := []wardrobe.Key{wardrobe.SkinKey(100), wardrobe.SkinKey(200), wardrobe.FinisherKey(24), wardrobe.MiniKey(300)}
	skins := map[wardrobe.Key]wardrobe.Skin{}
	for _, k := range keys {
		skins[k] = wardrobe.Skin{ID: k}
	}
	items := MapItems([]gw2.Item{
		{ID: 1, Name: "Helm", DefaultSkin: 100},
		{ID: 2, Name: "Unknown skin", DefaultSkin: 999},
		{ID: 3, Name: "Box", Details: gw2.ItemDetails{Skins: []int{100, 200, 999, 200}}},
		{ID: 4, Name: "Material"},
		{ID: 5, Name: "Permanent Twisted Watchwork Finisher"},
		{ID: 6, Name: "Unknown Finisher"},
		{ID: 7, Name: "Mini Test", Details: gw2.ItemDetails{MinipetID: 300}},
		{ID: 8, Name: "Mini Unknown", Details: gw2.ItemDetails{MinipetID: 301}},
		// Default skin 24 is a skin, not finisher 24.
		{ID: 9, Name: "Skin 24", DefaultSkin: 24},
	}, skins, []gw2.Finisher{{ID: 24, UnlockItems: []int{5}}, {ID: 99, UnlockItems: []int{6}}})
	if len(items) != 4 {
		t.Fatalf("items = %+v", items)
	}
	if !slices.Equal(items[1].Skins, []wardrobe.Key{wardrobe.SkinKey(100)}) {
		t.Errorf("item 1 skins = %v", items[1].Skins)
	}
	if !slices.Equal(items[3].Skins, []wardrobe.Key{wardrobe.SkinKey(100), wardrobe.SkinKey(200)}) {
		t.Errorf("item 3 skins = %v", items[3].Skins)
	}
	if !slices.Equal(items[5].Skins, []wardrobe.Key{wardrobe.FinisherKey(24)}) {
		t.Errorf("finisher item skins = %v", items[5].Skins)
	}
	if !slices.Equal(items[7].Skins, []wardrobe.Key{wardrobe.MiniKey(300)}) {
		t.Errorf("mini item skins = %v", items[7].Skins)
	}
}

func TestRunAgainstFakeAPI(t *testing.T) {
	setsGone := false // the API stops returning the weapon set
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/commerce/prices":
			w.Write([]byte(`[1,2,3,4,5,6]`))
		case "/v2/finishers":
			w.Write([]byte(`[{"id":24,"name":"Twisted Watchwork Finisher","unlock_items":[5]}]`))
		case "/v2/items":
			if r.URL.Query().Get("ids") != "1,2,3,4,5,6" {
				t.Errorf("ids = %q", r.URL.Query().Get("ids"))
			}
			w.Write([]byte(`[
				{"id":1,"name":"Test Helm Item","type":"Armor","rarity":"Rare","default_skin":100},
				{"id":2,"name":"Test Sword Item","type":"Weapon","rarity":"Masterwork","default_skin":200},
				{"id":3,"name":"Ore","type":"CraftingMaterial","rarity":"Basic"},
				{"id":4,"name":"New Longbow Skin","type":"Consumable","rarity":"Exotic","details":{"skins":[400]}},
				{"id":5,"name":"Permanent Twisted Watchwork Finisher","type":"Consumable","rarity":"Rare","details":{"type":"Unlock"}},
				{"id":6,"name":"Mini Test","type":"MiniPet","rarity":"Rare","details":{"minipet_id":300}}
			]`))
		case "/v2/achievements/categories/76":
			w.Write([]byte(`{"id":76,"achievements":[1,2,3]}`))
		case "/v2/achievements":
			if setsGone {
				w.Write([]byte(`[{"id":2,"name":"Mini Collection","bits":[{"type":"Minipet","id":1}]}]`))
				return
			}
			// A weapon set (skin 400 is newer than the analyser data, 401 is unknown),
			// a mini collection and a mixed collection.
			w.Write([]byte(`[
				{"id":1,"name":"New Weapon Collection","bits":[{"type":"Skin","id":400},{"type":"Skin","id":200},{"type":"Skin","id":401}]},
				{"id":2,"name":"Mini Collection","bits":[{"type":"Minipet","id":1}]},
				{"id":3,"name":"Toybox","bits":[{"type":"Skin","id":200},{"type":"Item","id":5}]}
			]`))
		case "/v2/skins":
			if got := r.URL.Query().Get("ids"); got != "400,401" {
				t.Errorf("skin ids = %q, want only the ones missing from the analyser", got)
			}
			w.Write([]byte(`[{"id":400,"name":"New Longbow","type":"Weapon","rarity":"Exotic","details":{"type":"LongBow"}}]`))
		case "/content.json":
			f, _ := os.Open("testdata/content.json")
			defer f.Close()
			io.Copy(w, f)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	api := gw2.New(srv.URL, "")
	api.MinInterval = 0
	st, err := store.Open(filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()
	if err := Run(ctx, api, st, srv.URL+"/content.json", log); err != nil {
		t.Fatal(err)
	}
	c, err := st.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Skins) != 6 || len(c.Items) != 5 {
		t.Fatalf("catalog = %+v", c)
	}
	if want := []wardrobe.Set{{ID: 1, Name: "New", Skins: []int{200, 400}}}; !reflect.DeepEqual(c.Sets, want) {
		t.Errorf("sets = %+v, want %+v", c.Sets, want)
	}
	if sk := c.Skins[wardrobe.SkinKey(400)]; sk != (wardrobe.Skin{ID: wardrobe.SkinKey(400), Name: "New Longbow", Section: "weapon", Type: "Longbow", Rarity: "Exotic"}) {
		t.Errorf("skin from /v2/skins = %+v", sk)
	}
	if !slices.Equal(c.Items[4].Skins, []wardrobe.Key{wardrobe.SkinKey(400)}) {
		t.Errorf("item for the new set skin = %+v", c.Items[4])
	}
	if c.Items[5].Name != "Permanent Twisted Watchwork Finisher" || !slices.Equal(c.Items[5].Skins, []wardrobe.Key{wardrobe.FinisherKey(24)}) {
		t.Errorf("finisher item = %+v", c.Items[5])
	}
	if !slices.Equal(c.Items[6].Skins, []wardrobe.Key{wardrobe.MiniKey(300)}) {
		t.Errorf("mini item = %+v", c.Items[6])
	}
	if f := c.Skins[wardrobe.FinisherKey(24)]; f.Name != "Twisted Watchwork Finisher" || !f.GWU {
		t.Errorf("finisher = %+v", f)
	}
	if c.Items[2].Name != "Test Sword Item" || !slices.Equal(c.Items[2].Skins, []wardrobe.Key{wardrobe.SkinKey(200)}) {
		t.Errorf("item 2 = %+v", c.Items[2])
	}

	// A local file works as the content source too.
	if err := Run(ctx, api, st, "testdata/content.json", log); err != nil {
		t.Fatal(err)
	}

	// A seed that finds fewer sets than are stored fails and keeps the old catalogue.
	setsGone = true
	if err := Run(ctx, api, st, "testdata/content.json", log); err == nil {
		t.Fatal("seed with fewer sets should fail")
	}
	if c, _ := st.LoadCatalog(ctx); len(c.Sets) != 1 {
		t.Errorf("sets after failed seed = %+v, want the stored one kept", c.Sets)
	}
}

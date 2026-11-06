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
	if len(skins) != 3 {
		t.Fatalf("skins = %+v, want armor and weapon only", skins)
	}
	if s := skins[100]; !s.GWU || s.Section != "armor" || s.Type != "Helm" || s.Name != "Test Helm" {
		t.Errorf("skin 100 = %+v", s)
	}
	if skins[101].GWU {
		t.Error("skin 101 should not be GWU")
	}
	if _, ok := skins[300]; ok {
		t.Error("minis are not wardrobe skins")
	}
}

func TestMapItems(t *testing.T) {
	skins := map[int]wardrobe.Skin{100: {ID: 100}, 200: {ID: 200}}
	items := MapItems([]gw2.Item{
		{ID: 1, Name: "Helm", DefaultSkin: 100},
		{ID: 2, Name: "Unknown skin", DefaultSkin: 999},
		{ID: 3, Name: "Box", Details: gw2.ItemDetails{Skins: []int{100, 200, 999, 200}}},
		{ID: 4, Name: "Material"},
	}, skins)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if !slices.Equal(items[1].Skins, []int{100}) {
		t.Errorf("item 1 skins = %v", items[1].Skins)
	}
	if !slices.Equal(items[3].Skins, []int{100, 200}) {
		t.Errorf("item 3 skins = %v", items[3].Skins)
	}
}

func TestRunAgainstFakeAPI(t *testing.T) {
	setsGone := false // the API stops returning the weapon set
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/commerce/prices":
			w.Write([]byte(`[1,2,3,4]`))
		case "/v2/items":
			if r.URL.Query().Get("ids") != "1,2,3,4" {
				t.Errorf("ids = %q", r.URL.Query().Get("ids"))
			}
			w.Write([]byte(`[
				{"id":1,"name":"Test Helm Item","type":"Armor","rarity":"Rare","default_skin":100},
				{"id":2,"name":"Test Sword Item","type":"Weapon","rarity":"Masterwork","default_skin":200},
				{"id":3,"name":"Ore","type":"CraftingMaterial","rarity":"Basic"},
				{"id":4,"name":"New Longbow Skin","type":"Consumable","rarity":"Exotic","details":{"skins":[400]}}
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
	if len(c.Skins) != 4 || len(c.Items) != 3 {
		t.Fatalf("catalog = %+v", c)
	}
	if want := []wardrobe.Set{{ID: 1, Name: "New", Skins: []int{200, 400}}}; !reflect.DeepEqual(c.Sets, want) {
		t.Errorf("sets = %+v, want %+v", c.Sets, want)
	}
	if sk := c.Skins[400]; sk != (wardrobe.Skin{ID: 400, Name: "New Longbow", Section: "weapon", Type: "Longbow", Rarity: "Exotic"}) {
		t.Errorf("skin from /v2/skins = %+v", sk)
	}
	if !slices.Equal(c.Items[4].Skins, []int{400}) {
		t.Errorf("item for the new set skin = %+v", c.Items[4])
	}
	if c.Items[2].Name != "Test Sword Item" || !slices.Equal(c.Items[2].Skins, []int{200}) {
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

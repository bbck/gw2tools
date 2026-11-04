package seed

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/commerce/prices":
			w.Write([]byte(`[1,2,3]`))
		case "/v2/items":
			if r.URL.Query().Get("ids") != "1,2,3" {
				t.Errorf("ids = %q", r.URL.Query().Get("ids"))
			}
			w.Write([]byte(`[
				{"id":1,"name":"Test Helm Item","type":"Armor","rarity":"Rare","default_skin":100},
				{"id":2,"name":"Test Sword Item","type":"Weapon","rarity":"Masterwork","default_skin":200},
				{"id":3,"name":"Ore","type":"CraftingMaterial","rarity":"Basic"}
			]`))
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
	if len(c.Skins) != 3 || len(c.Items) != 2 {
		t.Fatalf("catalog = %+v", c)
	}
	if c.Items[2].Name != "Test Sword Item" || !slices.Equal(c.Items[2].Skins, []int{200}) {
		t.Errorf("item 2 = %+v", c.Items[2])
	}

	// A local file works as the content source too.
	if err := Run(ctx, api, st, "testdata/content.json", log); err != nil {
		t.Fatal(err)
	}
}

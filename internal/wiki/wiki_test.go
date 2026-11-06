package wiki

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// chestText mimics the structure of the wiki's Black Lion Chest page.
const chestText = `{{Item infobox}}
Intro mentioning [[Old weapon skins]] outside the contents.

== Contents ==
=== New Exclusive (~1.9%) ===

=== Uncommon (~5.0%) ===
* [[Foefire weapon skins]] <small>(0.22% per weapon)</small>
** {{contains|Foefire Sword Skin}}

=== Rare (~1.8%) ===
* [[Calligrapher's weapon skins|Calligrapher's]] <small>(0.06% per weapon)</small>
** {{contains|Calligrapher's Sword Skin}}
* [[Foefire weapon skins]]
** [[Not a set weapon skins]] (nested, ignored)

== Notes ==
* [[Retired weapon skins]] were removed.
`

func TestParseChestSets(t *testing.T) {
	if got, want := ParseChestSets(chestText), []string{"Foefire", "Calligrapher's"}; !slices.Equal(got, want) {
		t.Errorf("sets = %q, want %q", got, want)
	}
	if got := ParseChestSets("no contents here"); got != nil {
		t.Errorf("sets without a contents section = %q", got)
	}
}

func TestChestSets(t *testing.T) {
	text := chestText
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api.php" || q.Get("page") != "Black Lion Chest" || q.Get("prop") != "wikitext" {
			t.Errorf("request = %s", r.URL)
		}
		if r.Header.Get("User-Agent") != "gw2tools" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		json.NewEncoder(w).Encode(map[string]any{"parse": map[string]any{"wikitext": text}})
	}))
	defer srv.Close()
	c := New(srv.URL)

	sets, err := c.ChestSets(context.Background())
	if err != nil || len(sets) != 2 {
		t.Fatalf("sets = %q, %v", sets, err)
	}
	// A page that no longer lists any sets is an error, not an empty chest.
	text = "== Contents ==\nnothing"
	if _, err := c.ChestSets(context.Background()); err == nil {
		t.Error("expected an error when no sets are found")
	}
}

// Package seed builds the static skin catalogue. It does bulk API work and is only
// ever run by hand (gw2tools seed), never by the server.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
	"github.com/bbck/gw2tools/internal/wardrobe"
)

// DefaultContentURL is the data file behind immortius' Wardrobe Unlock Analyser, which
// records which skins the Guaranteed Wardrobe Unlock can give.
const DefaultContentURL = "https://immortius.xyz/wardrobe-unlock-analyser/data/content.json"

// sections are the analyser sections that are wardrobe skins (/v2/account/skins).
var sections = []string{"armor", "weapon", "back"}

type content struct {
	Items []struct {
		ID         string `json:"id"`
		Categories []struct {
			Groups []struct {
				Content []struct {
					ID      string   `json:"id"`
					Name    string   `json:"name"`
					Type    string   `json:"type"`
					Rarity  string   `json:"rarity"`
					Sources []string `json:"sources"`
				} `json:"content"`
			} `json:"groups"`
		} `json:"categories"`
	} `json:"items"`
}

// ParseContent reads the analyser's content.json and returns the skins in sections,
// flagging those it marks as Guaranteed Wardrobe Unlock drops.
func ParseContent(r io.Reader) (map[int]wardrobe.Skin, error) {
	var c content
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("decoding content: %w", err)
	}
	skins := map[int]wardrobe.Skin{}
	for _, sec := range c.Items {
		if !slices.Contains(sections, sec.ID) {
			continue
		}
		for _, cat := range sec.Categories {
			for _, g := range cat.Groups {
				for _, u := range g.Content {
					id, err := strconv.Atoi(u.ID)
					if err != nil {
						return nil, fmt.Errorf("%s skin id %q: %w", sec.ID, u.ID, err)
					}
					skins[id] = wardrobe.Skin{
						ID:      id,
						Name:    u.Name,
						Section: sec.ID,
						Type:    u.Type,
						Rarity:  u.Rarity,
						GWU:     slices.Contains(u.Sources, "gwu"),
					}
				}
			}
		}
	}
	if len(skins) == 0 {
		return nil, fmt.Errorf("no skins found in content")
	}
	return skins, nil
}

// MapItems keeps the items that unlock a known skin, either as their default skin or
// through a skin list (e.g. transmutation and unlock consumables).
func MapItems(items []gw2.Item, skins map[int]wardrobe.Skin) map[int]wardrobe.Item {
	out := map[int]wardrobe.Item{}
	for _, it := range items {
		var unlocks []int
		for _, id := range append([]int{it.DefaultSkin}, it.Details.Skins...) {
			if _, ok := skins[id]; ok && !slices.Contains(unlocks, id) {
				unlocks = append(unlocks, id)
			}
		}
		if len(unlocks) > 0 {
			out[it.ID] = wardrobe.Item{ID: it.ID, Name: it.Name, Rarity: it.Rarity, Skins: unlocks}
		}
	}
	return out
}

// API is the subset of the GW2 API seeding uses.
type API interface {
	CommerceItemIDs(ctx context.Context) ([]int, error)
	Items(ctx context.Context, ids []int) ([]gw2.Item, error)
}

type Catalog interface {
	ReplaceCatalog(ctx context.Context, c wardrobe.Catalog) error
}

// Run builds the catalogue from the analyser content at contentSrc (a URL or file
// path) and every tradeable item's details, then replaces the stored catalogue.
func Run(ctx context.Context, api API, dst Catalog, contentSrc string, log *slog.Logger) error {
	log.Info("loading skins", "source", contentSrc)
	rc, err := open(ctx, contentSrc)
	if err != nil {
		return err
	}
	skins, err := ParseContent(rc)
	rc.Close()
	if err != nil {
		return err
	}
	gwu := 0
	for _, s := range skins {
		if s.GWU {
			gwu++
		}
	}
	log.Info("loaded skins", "skins", len(skins), "gwu", gwu)

	ids, err := api.CommerceItemIDs(ctx)
	if err != nil {
		return fmt.Errorf("listing tradeable items: %w", err)
	}
	log.Info("fetching tradeable item details", "items", len(ids), "requests", gw2.Requests(len(ids)))

	// Fetched in chunks of 20 requests so progress can be logged.
	var all []gw2.Item
	done := 0
	for chunk := range slices.Chunk(ids, gw2.MaxIDs*20) {
		items, err := api.Items(ctx, chunk)
		if err != nil {
			return fmt.Errorf("fetching items: %w", err)
		}
		all = append(all, items...)
		done += len(chunk)
		log.Info("progress", "done", done, "of", len(ids))
	}

	c := wardrobe.Catalog{Skins: skins, Items: MapItems(all, skins)}
	log.Info("saving catalogue", "skins", len(c.Skins), "items", len(c.Items))
	return dst.ReplaceCatalog(ctx, c)
}

func open(ctx context.Context, src string) (io.ReadCloser, error) {
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		return os.Open(src)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", gw2.UserAgent)
	// The timeout covers reading the body too.
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetching %s: status %d", src, resp.StatusCode)
	}
	return resp.Body, nil
}

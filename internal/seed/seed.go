// Package seed builds the static skin, finisher and mini catalogue. It does bulk API work and is only
// ever run by hand (gw2tools seed), never by the server.
package seed

import (
	"cmp"
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

// sections maps the analyser sections the catalogue holds to their kind of unlock.
var sections = map[string]wardrobe.Kind{
	"armor":    wardrobe.KindSkin,
	"weapon":   wardrobe.KindSkin,
	"back":     wardrobe.KindSkin,
	"finisher": wardrobe.KindFinisher,
	"mini":     wardrobe.KindMini,
}

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

// ParseContent reads the analyser's content.json and returns the skins, finishers and
// minis in sections, flagging those it marks as Guaranteed Wardrobe Unlock drops.
func ParseContent(r io.Reader) (map[wardrobe.Key]wardrobe.Skin, error) {
	var c content
	if err := json.NewDecoder(r).Decode(&c); err != nil {
		return nil, fmt.Errorf("decoding content: %w", err)
	}
	skins := map[wardrobe.Key]wardrobe.Skin{}
	for _, sec := range c.Items {
		kind, ok := sections[sec.ID]
		if !ok {
			continue
		}
		for _, cat := range sec.Categories {
			for _, g := range cat.Groups {
				for _, u := range g.Content {
					id, err := strconv.Atoi(u.ID)
					if err != nil {
						return nil, fmt.Errorf("%s skin id %q: %w", sec.ID, u.ID, err)
					}
					key := wardrobe.Key{Kind: kind, ID: id}
					skins[key] = wardrobe.Skin{
						ID:      key,
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

// MapItems keeps the items that unlock a known skin (their default skin, or a skin list
// as on transmutation and unlock consumables), mini (a MiniPet item's minipet_id), or
// finisher (which names its unlock items, as items don't name their finisher).
func MapItems(items []gw2.Item, skins map[wardrobe.Key]wardrobe.Skin, finishers []gw2.Finisher) map[int]wardrobe.Item {
	finisherItems := map[int][]wardrobe.Key{}
	for _, f := range finishers {
		for _, it := range f.UnlockItems {
			finisherItems[it] = append(finisherItems[it], wardrobe.FinisherKey(f.ID))
		}
	}
	out := map[int]wardrobe.Item{}
	for _, it := range items {
		var unlocks []wardrobe.Key
		ids := append([]wardrobe.Key{wardrobe.SkinKey(it.DefaultSkin), wardrobe.MiniKey(it.Details.MinipetID)}, finisherItems[it.ID]...)
		for _, id := range it.Details.Skins {
			ids = append(ids, wardrobe.SkinKey(id))
		}
		for _, id := range ids {
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

// BlackLionCategory is the "Black Lion Collections" achievement category. Each Black
// Lion weapon set has a collection achievement there whose bits are the set's skins,
// so a newly released set is picked up by seeding again.
const BlackLionCategory = 76

// weaponTypes maps /v2/skins weapon types to the analyser's names, where they differ.
var weaponTypes = map[string]string{"LongBow": "Longbow", "ShortBow": "Short bow", "Speargun": "Harpoon gun"}

// API is the subset of the GW2 API seeding uses.
type API interface {
	CommerceItemIDs(ctx context.Context) ([]int, error)
	Items(ctx context.Context, ids []int) ([]gw2.Item, error)
	AchievementCategory(ctx context.Context, id int) ([]int, error)
	Achievements(ctx context.Context, ids []int) ([]gw2.Achievement, error)
	Skins(ctx context.Context, ids []int) ([]gw2.Skin, error)
	Finishers(ctx context.Context) ([]gw2.Finisher, error)
}

// Sets reads the Black Lion weapon sets: the achievements in BlackLionCategory whose
// bits are all skins. The category also holds mini and toy collections, which are
// skipped and logged, so a new set with an unexpected shape is noticed.
func Sets(ctx context.Context, api API, log *slog.Logger) ([]wardrobe.Set, error) {
	ids, err := api.AchievementCategory(ctx, BlackLionCategory)
	if err != nil {
		return nil, fmt.Errorf("black lion collections: %w", err)
	}
	achievements, err := api.Achievements(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("black lion collections: %w", err)
	}
	var sets []wardrobe.Set
	for _, a := range achievements {
		set := wardrobe.Set{ID: a.ID, Name: strings.TrimSuffix(a.Name, " Weapon Collection")}
		for _, b := range a.Bits {
			if b.Type != "Skin" {
				set.Skins = nil
				break
			}
			set.Skins = append(set.Skins, b.ID)
		}
		if len(set.Skins) == 0 {
			log.Info("skipping black lion collection, not a weapon set", "id", a.ID, "name", a.Name)
			continue
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// addSetSkins adds set skins the analyser does not list (e.g. a set newer than its
// data) to skins, from /v2/skins. Skins the API does not know are dropped from their set.
func addSetSkins(ctx context.Context, api API, sets []wardrobe.Set, skins map[wardrobe.Key]wardrobe.Skin, log *slog.Logger) error {
	var missing []int
	for _, set := range sets {
		for _, id := range set.Skins {
			if _, ok := skins[wardrobe.SkinKey(id)]; !ok {
				missing = append(missing, id)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	log.Info("fetching set skins missing from the analyser", "skins", len(missing))
	found, err := api.Skins(ctx, missing)
	if err != nil {
		return fmt.Errorf("fetching set skins: %w", err)
	}
	for _, sk := range found {
		skins[wardrobe.SkinKey(sk.ID)] = wardrobe.Skin{
			ID:      wardrobe.SkinKey(sk.ID),
			Name:    sk.Name,
			Section: strings.ToLower(sk.Type),
			Type:    cmp.Or(weaponTypes[sk.Details.Type], sk.Details.Type),
			Rarity:  sk.Rarity,
		}
	}
	for i := range sets {
		sets[i].Skins = slices.DeleteFunc(sets[i].Skins, func(id int) bool {
			_, ok := skins[wardrobe.SkinKey(id)]
			if !ok {
				log.Warn("dropping unknown skin from set", "set", sets[i].Name, "skin", id)
			}
			return !ok
		})
	}
	return nil
}

type Catalog interface {
	ReplaceCatalog(ctx context.Context, c wardrobe.Catalog) error
	WeaponSetCount(ctx context.Context) (int, error)
}

// Run builds the catalogue from the analyser content at contentSrc (a URL or file
// path), the Black Lion weapon sets, the finishers, and every tradeable item's
// details, then replaces the stored catalogue.
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
	kinds := map[wardrobe.Kind]int{}
	gwu := 0
	for _, s := range skins {
		kinds[s.ID.Kind]++
		if s.GWU {
			gwu++
		}
	}
	log.Info("loaded skins", "kinds", kinds, "gwu", gwu)

	sets, err := Sets(ctx, api, log)
	if err != nil {
		return err
	}
	// Sets are only ever added, so fewer than before means the API or the filter
	// changed. Stop before replacing (and losing) the stored sets.
	stored, err := dst.WeaponSetCount(ctx)
	if err != nil {
		return err
	}
	if len(sets) == 0 || len(sets) < stored {
		return fmt.Errorf("found %d black lion weapon sets, fewer than the %d stored; not replacing the catalogue", len(sets), stored)
	}
	if err := addSetSkins(ctx, api, sets, skins, log); err != nil {
		return err
	}
	log.Info("loaded black lion weapon sets", "sets", len(sets))

	// One request; items don't name the finisher they unlock, so finishers name their items.
	finishers, err := api.Finishers(ctx)
	if err != nil {
		return fmt.Errorf("fetching finishers: %w", err)
	}

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

	c := wardrobe.Catalog{Skins: skins, Items: MapItems(all, skins, finishers), Sets: sets}
	log.Info("saving catalogue", "skins", len(c.Skins), "items", len(c.Items), "sets", len(c.Sets))
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

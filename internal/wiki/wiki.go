// Package wiki reads the few facts gw2tools needs from the Guild Wars 2 wiki that the
// API does not expose. It makes one targeted request per lookup, and the server calls
// it once a day.
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
)

const DefaultBaseURL = "https://wiki.guildwars2.com"

// chestPage is the wiki page listing the current Black Lion Chest's contents.
const chestPage = "Black Lion Chest"

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// ChestSets returns the names of the weapon sets in the current Black Lion Chest.
func (c *Client) ChestSets(ctx context.Context) ([]string, error) {
	text, err := c.wikitext(ctx, chestPage)
	if err != nil {
		return nil, err
	}
	sets := ParseChestSets(text)
	if len(sets) == 0 {
		return nil, fmt.Errorf("wiki: no weapon sets found on the %q page", chestPage)
	}
	return sets, nil
}

var (
	// A level-2 heading, e.g. "== Contents ==".
	heading = regexp.MustCompile(`(?m)^==[^=].*?==\s*$`)
	// A top-level list entry linking a set's page, e.g. "* [[Foefire weapon skins]]".
	setLink = regexp.MustCompile(`(?m)^\*\s*\[\[([^\]|]+?) weapon skins(?:\|[^\]]*)?\]\]`)
)

// ParseChestSets finds the weapon sets listed in the Contents section of the Black
// Lion Chest page's wikitext.
func ParseChestSets(text string) []string {
	start := strings.Index(text, "== Contents ==")
	if start < 0 {
		return nil
	}
	section := text[start+len("== Contents =="):]
	if next := heading.FindStringIndex(section); next != nil {
		section = section[:next[0]]
	}
	var sets []string
	for _, m := range setLink.FindAllStringSubmatch(section, -1) {
		if name := strings.TrimSpace(m[1]); !slices.Contains(sets, name) {
			sets = append(sets, name)
		}
	}
	return sets
}

// wikitext fetches a page's source through the MediaWiki API.
func (c *Client) wikitext(ctx context.Context, page string) (string, error) {
	q := url.Values{"action": {"parse"}, "page": {page}, "prop": {"wikitext"}, "format": {"json"}, "formatversion": {"2"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api.php?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", gw2.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wiki: %s: status %d", page, resp.StatusCode)
	}
	var body struct {
		Parse struct {
			Wikitext string `json:"wikitext"`
		} `json:"parse"`
		Error *struct {
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("wiki: decoding %s: %w", page, err)
	}
	if body.Error != nil {
		return "", fmt.Errorf("wiki: %s: %s", page, body.Error.Info)
	}
	if body.Parse.Wikitext == "" {
		return "", errors.New("wiki: " + page + ": empty page")
	}
	return body.Parse.Wikitext, nil
}

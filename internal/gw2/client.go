// Package gw2 is a minimal client for the parts of the Guild Wars 2 API used by gw2tools.
package gw2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://api.guildwars2.com"
	// MaxIDs is the largest number of ids the API accepts in one ?ids= request.
	MaxIDs = 200
	// UserAgent identifies gw2tools in every outgoing request.
	UserAgent = "gw2tools"
	// maxRetries is how many times a rate-limited (429) request is retried.
	maxRetries = 3
)

// Requests is how many API requests a batched lookup of n ids takes.
func Requests(n int) int { return (n + MaxIDs - 1) / MaxIDs }

// Client talks to the GW2 API. Requests are serialised and spaced by MinInterval so
// batch work stays under the API rate limit; rate-limited requests are retried with
// exponential backoff starting at RetryBackoff.
type Client struct {
	BaseURL      string
	APIKey       string
	HTTP         *http.Client
	MinInterval  time.Duration
	RetryBackoff time.Duration

	mu   sync.Mutex
	last time.Time
}

func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		APIKey:       apiKey,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		MinInterval:  200 * time.Millisecond,
		RetryBackoff: 5 * time.Second,
	}
}

// APIError is a non-success response from the API.
type APIError struct {
	Status     int
	Text       string
	retryAfter string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gw2 api: status %d: %s", e.Status, e.Text)
}

// isAPIError reports whether err is an API error with the given status whose text
// contains text (case-insensitively; empty matches anything).
func isAPIError(err error, status int, text string) bool {
	e, ok := errors.AsType[*APIError](err)
	return ok && e.Status == status && strings.Contains(strings.ToLower(e.Text), text)
}

type Price struct {
	ID    int       `json:"id"`
	Buys  PriceSide `json:"buys"`
	Sells PriceSide `json:"sells"`
}

type PriceSide struct {
	Quantity  int `json:"quantity"`
	UnitPrice int `json:"unit_price"`
}

type Listing struct {
	ID    int            `json:"id"`
	Buys  []ListingEntry `json:"buys"`
	Sells []ListingEntry `json:"sells"`
}

type ListingEntry struct {
	UnitPrice int `json:"unit_price"`
	Quantity  int `json:"quantity"`
}

type Transaction struct {
	ID       int       `json:"id"`
	ItemID   int       `json:"item_id"`
	Price    int       `json:"price"`
	Quantity int       `json:"quantity"`
	Created  time.Time `json:"created"`
}

type Item struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Rarity      string      `json:"rarity"`
	DefaultSkin int         `json:"default_skin"`
	Details     ItemDetails `json:"details"`
}

type ItemDetails struct {
	Skins     []int `json:"skins"`
	MinipetID int   `json:"minipet_id"` // the mini a MiniPet item unlocks
}

// AccountSkins returns the skin ids unlocked on the account. Needs the "unlocks" permission.
func (c *Client) AccountSkins(ctx context.Context) ([]int, error) {
	var ids []int
	_, err := c.get(ctx, "/v2/account/skins", nil, true, &ids)
	return ids, err
}

// AccountFinisher is a finisher on the account. Permanent is false for finishers
// that only have charges left.
type AccountFinisher struct {
	ID        int  `json:"id"`
	Permanent bool `json:"permanent"`
}

// AccountFinishers returns the finishers on the account. Needs the "unlocks" permission.
func (c *Client) AccountFinishers(ctx context.Context) ([]AccountFinisher, error) {
	var fs []AccountFinisher
	_, err := c.get(ctx, "/v2/account/finishers", nil, true, &fs)
	return fs, err
}

// Finisher is a finisher from /v2/finishers, with the items that unlock it.
type Finisher struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	UnlockItems []int  `json:"unlock_items"`
}

// Finishers returns every finisher, in one request.
func (c *Client) Finishers(ctx context.Context) ([]Finisher, error) {
	var fs []Finisher
	_, err := c.get(ctx, "/v2/finishers", url.Values{"ids": {"all"}}, false, &fs)
	return fs, err
}

// AccountMinis returns the ids of the minis unlocked on the account. Needs the
// "unlocks" permission.
func (c *Client) AccountMinis(ctx context.Context) ([]int, error) {
	var ids []int
	_, err := c.get(ctx, "/v2/account/minis", nil, true, &ids)
	return ids, err
}

// CurrentBuys returns every open buy order on the account. Needs the "tradingpost" permission.
func (c *Client) CurrentBuys(ctx context.Context) ([]Transaction, error) {
	var all []Transaction
	for page := 0; ; page++ {
		var batch []Transaction
		q := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(MaxIDs)}}
		h, err := c.get(ctx, "/v2/commerce/transactions/current/buys", q, true, &batch)
		if isAPIError(err, http.StatusBadRequest, "page out of range") {
			// Past the last page: an empty list, or orders closed while paging.
			return all, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		total, err := strconv.Atoi(h.Get("X-Page-Total"))
		if err != nil || page+1 >= total {
			return all, nil
		}
	}
}

// DeliveryItem is an item waiting in the trading post delivery box.
type DeliveryItem struct {
	ID    int `json:"id"`
	Count int `json:"count"`
}

// Delivery returns the items in the trading post delivery box. Needs the
// "tradingpost" permission.
func (c *Client) Delivery(ctx context.Context) ([]DeliveryItem, error) {
	var d struct {
		Items []DeliveryItem `json:"items"`
	}
	_, err := c.get(ctx, "/v2/commerce/delivery", nil, true, &d)
	return d.Items, err
}

// Achievement is an achievement and its bits (the steps of a collection).
type Achievement struct {
	ID   int              `json:"id"`
	Name string           `json:"name"`
	Bits []AchievementBit `json:"bits"`
}

// AchievementBit is one step of an achievement. For Type "Skin", ID is a skin id.
type AchievementBit struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
}

// Skin is a wardrobe skin from /v2/skins.
type Skin struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // Armor, Weapon, Back, ...
	Rarity  string `json:"rarity"`
	Details struct {
		Type string `json:"type"` // e.g. Sword, LongBow, Coat
	} `json:"details"`
}

// AchievementCategory returns the ids of the achievements in a category.
func (c *Client) AchievementCategory(ctx context.Context, id int) ([]int, error) {
	var cat struct {
		Achievements []int `json:"achievements"`
	}
	_, err := c.get(ctx, "/v2/achievements/categories/"+strconv.Itoa(id), nil, false, &cat)
	return cat.Achievements, err
}

// Achievements returns achievement details. Ids the API does not know are skipped.
func (c *Client) Achievements(ctx context.Context, ids []int) ([]Achievement, error) {
	return getBatched[Achievement](ctx, c, "/v2/achievements", ids)
}

// Skins returns skin details. Ids the API does not know are skipped.
func (c *Client) Skins(ctx context.Context, ids []int) ([]Skin, error) {
	return getBatched[Skin](ctx, c, "/v2/skins", ids)
}

// CommerceItemIDs returns the ids of every item that can be traded on the trading post.
func (c *Client) CommerceItemIDs(ctx context.Context) ([]int, error) {
	var ids []int
	_, err := c.get(ctx, "/v2/commerce/prices", nil, false, &ids)
	return ids, err
}

// Prices returns trading post prices. Ids the API does not know are skipped.
func (c *Client) Prices(ctx context.Context, ids []int) ([]Price, error) {
	return getBatched[Price](ctx, c, "/v2/commerce/prices", ids)
}

// Listings returns the full order book. Ids the API does not know are skipped.
func (c *Client) Listings(ctx context.Context, ids []int) ([]Listing, error) {
	return getBatched[Listing](ctx, c, "/v2/commerce/listings", ids)
}

// Items returns item details. Ids the API does not know are skipped.
func (c *Client) Items(ctx context.Context, ids []int) ([]Item, error) {
	return getBatched[Item](ctx, c, "/v2/items", ids)
}

func getBatched[T any](ctx context.Context, c *Client, path string, ids []int) ([]T, error) {
	var all []T
	for chunk := range slices.Chunk(ids, MaxIDs) {
		parts := make([]string, len(chunk))
		for i, id := range chunk {
			parts[i] = strconv.Itoa(id)
		}
		var batch []T
		_, err := c.get(ctx, path, url.Values{"ids": {strings.Join(parts, ",")}}, false, &batch)
		if isAPIError(err, http.StatusNotFound, "all ids provided are invalid") {
			// Every id in the batch was invalid, e.g. items no longer on the trading post.
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
	}
	return all, nil
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.MinInterval - time.Since(c.last); d > 0 {
		if err := sleep(ctx, d); err != nil {
			return err
		}
	}
	c.last = time.Now()
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, auth bool, out any) (http.Header, error) {
	for attempt := 0; ; attempt++ {
		h, err := c.getOnce(ctx, path, q, auth, out)
		if !isAPIError(err, http.StatusTooManyRequests, "") || attempt >= maxRetries {
			return h, err
		}
		backoff := c.RetryBackoff * time.Duration(1<<attempt)
		if e, _ := errors.AsType[*APIError](err); e != nil {
			if s, err := strconv.Atoi(e.retryAfter); err == nil && s > 0 {
				backoff = time.Duration(s) * time.Second
			}
		}
		if err := sleep(ctx, backoff); err != nil {
			return nil, err
		}
	}
}

func (c *Client) getOnce(ctx context.Context, path string, q url.Values, auth bool, out any) (http.Header, error) {
	if auth && c.APIKey == "" {
		return nil, errors.New("gw2 api: an API key is required for " + path)
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 206 means some of the requested ids were invalid; the body holds the valid ones.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var e struct {
			Text string `json:"text"`
		}
		// Non-JSON bodies (e.g. the HTML page served while the API is down) are not
		// repeated: they are noisy and include the caller's IP address.
		text := http.StatusText(resp.StatusCode) + " (non-JSON response)"
		if json.Unmarshal(body, &e) == nil && e.Text != "" {
			text = e.Text
		}
		return nil, &APIError{Status: resp.StatusCode, Text: text, retryAfter: resp.Header.Get("Retry-After")}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("gw2 api: decoding %s: %w", path, err)
	}
	return resp.Header, nil
}

// Package web serves the gw2tools UI.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
	"github.com/bbck/gw2tools/internal/store"
	"github.com/bbck/gw2tools/internal/wardrobe"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	defaultLimit = 25
	maxLimit     = 500

	// MinOrdersInterval and MinPricesInterval cap how often the server calls the API.
	MinOrdersInterval = time.Minute
	MinPricesInterval = time.Hour
)

// ChestSource names the weapon sets in the current Black Lion Chest.
type ChestSource interface {
	ChestSets(ctx context.Context) ([]string, error)
}

// Intervals are how often each refresh runs. Values below the minimums are raised.
type Intervals struct {
	Orders time.Duration
	Prices time.Duration
}

func (i Intervals) clamp() Intervals {
	return Intervals{Orders: max(i.Orders, MinOrdersInterval), Prices: max(i.Prices, MinPricesInterval)}
}

type Server struct {
	store     *store.Store
	api       wardrobe.API
	chest     ChestSource
	log       *slog.Logger
	tmpl      *template.Template
	now       func() time.Time
	intervals Intervals

	mu      sync.Mutex // serialises refreshes
	pricing atomic.Bool

	catMu       sync.Mutex
	catalog     wardrobe.Catalog
	catalogSeed time.Time
}

func New(st *store.Store, api wardrobe.API, chest ChestSource, log *slog.Logger, iv Intervals) (*Server, error) {
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: st, api: api, chest: chest, log: log, tmpl: tmpl, now: time.Now, intervals: iv.clamp()}, nil
}

func (s *Server) Intervals() Intervals { return s.intervals }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/wardrobe", http.StatusFound)
	})
	mux.HandleFunc("GET /wardrobe", s.wardrobe)
	mux.HandleFunc("GET /black-lion", s.sets)
	return mux
}

// RunScheduler runs Tick now and then every orders interval until ctx is done.
func (s *Server) RunScheduler(ctx context.Context) {
	t := time.NewTicker(s.intervals.Orders)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick refreshes orders, then prices if the last prices refresh attempt, successful
// or not, is at least the prices interval old, then the Black Lion Chest sets if they
// were last attempted before the latest midnight UTC. Attempts are stored, so restarts
// do not cause extra refreshes.
func (s *Server) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cat, seeded, err := s.loadCatalog(ctx)
	if err != nil {
		return err
	}
	if seeded.IsZero() {
		s.log.Warn("skipping refresh: catalogue not seeded, run `mise run seed`")
		return nil
	}
	u, ordersErr := s.refreshOrders(ctx, cat)
	return errors.Join(ordersErr, s.maybeRefreshPrices(ctx, cat, u, ordersErr), s.maybeRefreshChest(ctx))
}

func (s *Server) maybeRefreshPrices(ctx context.Context, cat wardrobe.Catalog, u wardrobe.OrdersUpdate, ordersErr error) error {
	tried, err := s.store.PricesAttemptedAt(ctx)
	if err != nil || s.now().Sub(tried) < s.intervals.Prices {
		return err
	}
	snap := wardrobe.Snapshot{Owned: map[int]bool{}, Orders: u.Orders, Delivery: u.Delivery}
	for _, id := range u.Owned {
		snap.Owned[id] = true
	}
	if ordersErr != nil {
		// Fall back to the stored account state.
		if snap, err = s.store.LoadSnapshot(ctx); err != nil {
			return err
		}
	}
	return s.refreshPrices(ctx, cat, snap)
}

// maybeRefreshChest asks the wiki which weapon sets are in the Black Lion Chest, once
// a day: on the first tick after midnight UTC, which leaves the wiki time to catch up
// with a game update. A server that was down at midnight checks when it starts.
func (s *Server) maybeRefreshChest(ctx context.Context) error {
	tried, err := s.store.ChestAttemptedAt(ctx)
	if err != nil || !tried.Before(lastMidnightUTC(s.now())) {
		return err
	}
	start := s.now()
	// Recorded before the request, so a failing check waits a full interval.
	if err := s.store.SetChestAttempt(ctx, start); err != nil {
		return fmt.Errorf("chest: recording attempt: %w", err)
	}
	sets, err := s.chest.ChestSets(ctx)
	if err == nil {
		err = s.store.SaveChestSets(ctx, sets)
	}
	if err != nil {
		s.recordError(ctx, s.store.SetChestError, err)
		return fmt.Errorf("chest: %w", err)
	}
	s.log.Info("checked black lion chest", "sets", sets)
	return nil
}

func (s *Server) refreshOrders(ctx context.Context, cat wardrobe.Catalog) (wardrobe.OrdersUpdate, error) {
	start := s.now()
	u, err := wardrobe.RefreshOrders(ctx, s.api, cat, start)
	if err == nil {
		err = s.store.SaveOrders(ctx, u)
	}
	if err != nil {
		s.recordError(ctx, s.store.SetOrdersError, err)
		return u, fmt.Errorf("orders: %w", err)
	}
	s.log.Info("refreshed orders", "unlocked_skins", len(u.Owned), "orders", len(u.Orders), "delivery_items", len(u.Delivery), "took", s.now().Sub(start).Round(time.Millisecond))
	return u, nil
}

func (s *Server) refreshPrices(ctx context.Context, cat wardrobe.Catalog, snap wardrobe.Snapshot) error {
	s.pricing.Store(true)
	defer s.pricing.Store(false)
	start := s.now()
	// Recorded before any request, so a failing refresh waits a full interval.
	if err := s.store.SetPricesAttempt(ctx, start); err != nil {
		return fmt.Errorf("prices: recording attempt: %w", err)
	}
	ids := wardrobe.PriceCandidates(cat, snap)
	s.log.Info("refreshing prices", "items", len(ids), "requests", gw2.Requests(len(ids)))
	prices, err := wardrobe.RefreshPrices(ctx, s.api, ids)
	if err == nil {
		// Keep the candidates and the order items (kept current by the orders refresh).
		keep := slices.Clone(ids)
		for _, o := range snap.Orders {
			keep = append(keep, o.ItemID)
		}
		err = s.store.SavePrices(ctx, keep, prices, start)
	}
	if err != nil {
		s.recordError(ctx, s.store.SetPricesError, err)
		return fmt.Errorf("prices: %w", err)
	}
	s.log.Info("refreshed prices", "prices", len(prices), "took", s.now().Sub(start).Round(time.Millisecond))
	return nil
}

// recordError stores a refresh error for the page banner. Errors caused by shutting
// down are not recorded.
func (s *Server) recordError(ctx context.Context, set func(context.Context, string) error, err error) {
	if ctx.Err() != nil {
		return
	}
	if serr := set(ctx, err.Error()); serr != nil {
		s.log.Error("recording refresh error", "err", serr)
	}
}

func (s *Server) loadCatalog(ctx context.Context) (wardrobe.Catalog, time.Time, error) {
	seeded, err := s.store.SeededAt(ctx)
	if err != nil {
		return wardrobe.Catalog{}, seeded, err
	}
	s.catMu.Lock()
	defer s.catMu.Unlock()
	if s.catalog.Skins == nil || !seeded.Equal(s.catalogSeed) {
		cat, err := s.store.LoadCatalog(ctx)
		if err != nil {
			return cat, seeded, err
		}
		s.catalog, s.catalogSeed = cat, seeded
	}
	return s.catalog, seeded, nil
}

// navLinks are the pages, in nav order. Each page's title is its label.
var navLinks = []struct{ Path, Label string }{
	{"/wardrobe", "Wardrobe Unlocks"},
	{"/black-lion", "Black Lion Sets"},
}

// pageState is what every page's layout, toolbar and banners show.
type pageState struct {
	Path       string
	Title      string
	OrdersAt   time.Time // last orders refresh
	PricesAt   time.Time // start of the last successful prices refresh
	Seeded     bool
	Pricing    bool
	Errors     store.Errors
	Now        time.Time
	NextPrices time.Time
}

// load reads the catalogue, the stored snapshot and the state for the page at r's path.
func (s *Server) load(r *http.Request) (wardrobe.Catalog, wardrobe.Snapshot, pageState, error) {
	ctx := r.Context()
	ps := pageState{Path: r.URL.Path}
	for _, l := range navLinks {
		if l.Path == ps.Path {
			ps.Title = l.Label
		}
	}
	cat, seeded, err := s.loadCatalog(ctx)
	if err != nil {
		return cat, wardrobe.Snapshot{}, ps, err
	}
	snap, err := s.store.LoadSnapshot(ctx)
	if err != nil {
		return cat, snap, ps, err
	}
	if ps.Errors, err = s.store.Errors(ctx); err != nil {
		return cat, snap, ps, err
	}
	tried, err := s.store.PricesAttemptedAt(ctx)
	if err != nil {
		return cat, snap, ps, err
	}
	ps.OrdersAt, ps.PricesAt = snap.OrdersAt, snap.PricesAt
	ps.Seeded, ps.Pricing, ps.Now = !seeded.IsZero(), s.pricing.Load(), s.now()
	if !tried.IsZero() {
		ps.NextPrices = tried.Add(s.intervals.Prices)
	}
	return cat, snap, ps, nil
}

func (s *Server) render(w http.ResponseWriter, name string, page any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, page); err != nil {
		s.log.Error("rendering page", "template", name, "err", err)
	}
}

type wardrobePage struct {
	pageState
	View   wardrobe.View
	Limits []int
}

func (s *Server) wardrobe(w http.ResponseWriter, r *http.Request) {
	limit := defaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		limit = min(n, maxLimit)
	}
	cat, snap, ps, err := s.load(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "wardrobe.html", wardrobePage{pageState: ps, View: wardrobe.BuildView(cat, snap, limit), Limits: limitOptions(limit)})
}

type setsPage struct {
	pageState
	View wardrobe.SetsView
}

func (s *Server) sets(w http.ResponseWriter, r *http.Request) {
	cat, snap, ps, err := s.load(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "sets.html", setsPage{pageState: ps, View: wardrobe.BuildSets(cat, snap)})
}

// limitOptions are the "Show" choices, including the current limit so the select
// always matches the page.
func limitOptions(current int) []int {
	opts := []int{10, 25, 50, 100}
	if !slices.Contains(opts, current) {
		opts = append(opts, current)
		slices.Sort(opts)
	}
	return opts
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func lastMidnightUTC(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

var funcs = template.FuncMap{
	"nav":      func() any { return navLinks },
	"coins":    coins,
	"ago":      ago,
	"until":    until,
	"datetime": func(t time.Time) string { return t.Format(time.RFC3339) },
}

// coins renders a copper amount as gold, silver and copper.
func coins(c int) template.HTML {
	if c <= 0 {
		return `<span class="coins none">—</span>`
	}
	g, sv, cp := c/10000, c/100%100, c%100
	out := `<span class="coins">`
	if g > 0 {
		out += fmt.Sprintf(`<span class="g">%d</span>`, g)
	}
	if g > 0 || sv > 0 {
		out += fmt.Sprintf(`<span class="s">%d</span>`, sv)
	}
	out += fmt.Sprintf(`<span class="c">%d</span></span>`, cp)
	return template.HTML(out)
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func until(t, now time.Time) string {
	if t.IsZero() {
		return "soon"
	}
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "within a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("in %dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

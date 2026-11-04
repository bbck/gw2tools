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

func New(st *store.Store, api wardrobe.API, log *slog.Logger, iv Intervals) (*Server, error) {
	tmpl, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: st, api: api, log: log, tmpl: tmpl, now: time.Now, intervals: iv.clamp()}, nil
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
// or not, is at least the prices interval old. Attempts are stored, so restarts do
// not cause extra price refreshes.
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

	tried, err := s.store.PricesAttemptedAt(ctx)
	if err != nil {
		return errors.Join(ordersErr, err)
	}
	if s.now().Sub(tried) < s.intervals.Prices {
		return ordersErr
	}
	owned := map[int]bool{}
	for _, id := range u.Owned {
		owned[id] = true
	}
	orders := u.Orders
	if ordersErr != nil {
		// Fall back to the stored unlocks and orders.
		snap, err := s.store.LoadSnapshot(ctx)
		if err != nil {
			return errors.Join(ordersErr, err)
		}
		owned, orders = snap.Owned, snap.Orders
	}
	return errors.Join(ordersErr, s.refreshPrices(ctx, cat, owned, orders))
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
	s.log.Info("refreshed orders", "unlocked_skins", len(u.Owned), "orders", len(u.Orders), "took", s.now().Sub(start).Round(time.Millisecond))
	return u, nil
}

func (s *Server) refreshPrices(ctx context.Context, cat wardrobe.Catalog, owned map[int]bool, orders []wardrobe.Order) error {
	s.pricing.Store(true)
	defer s.pricing.Store(false)
	start := s.now()
	// Recorded before any request, so a failing refresh waits a full interval.
	if err := s.store.SetPricesAttempt(ctx, start); err != nil {
		return fmt.Errorf("prices: recording attempt: %w", err)
	}
	ids := wardrobe.PriceCandidates(cat, owned, orders)
	s.log.Info("refreshing prices", "items", len(ids), "requests", gw2.Requests(len(ids)))
	prices, err := wardrobe.RefreshPrices(ctx, s.api, ids)
	if err == nil {
		// Keep the candidates and the order items (kept current by the orders refresh).
		keep := slices.Clone(ids)
		for _, o := range orders {
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

type wardrobePage struct {
	View       wardrobe.View
	Seeded     bool
	Pricing    bool
	Errors     store.Errors
	Now        time.Time
	NextPrices time.Time
	Limits     []int
}

func (wardrobePage) Title() string { return "Wardrobe Unlocks" }

func (s *Server) wardrobe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := defaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		limit = min(n, maxLimit)
	}
	cat, seeded, err := s.loadCatalog(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	snap, err := s.store.LoadSnapshot(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	errs, err := s.store.Errors(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	tried, err := s.store.PricesAttemptedAt(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	page := wardrobePage{
		View:    wardrobe.BuildView(cat, snap, limit),
		Seeded:  !seeded.IsZero(),
		Pricing: s.pricing.Load(),
		Errors:  errs,
		Now:     s.now(),
		Limits:  limitOptions(limit),
	}
	if !tried.IsZero() {
		page.NextPrices = tried.Add(s.intervals.Prices)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "wardrobe.html", page); err != nil {
		s.log.Error("rendering wardrobe", "err", err)
	}
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

var funcs = template.FuncMap{
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

// Command gw2tools serves a small Guild Wars 2 tracking site.
//
//	gw2tools serve   run the web server (default)
//	gw2tools seed    load the skin catalogue (bulk API work, run by hand)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/bbck/gw2tools/internal/gw2"
	"github.com/bbck/gw2tools/internal/seed"
	"github.com/bbck/gw2tools/internal/store"
	"github.com/bbck/gw2tools/internal/web"
	"github.com/bbck/gw2tools/internal/wiki"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, log, args)
	case "seed":
		err = runSeed(ctx, log, args)
	default:
		err = fmt.Errorf("unknown command %q (want serve or seed)", cmd)
	}
	if err != nil {
		log.Error(cmd+" failed", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func commonFlags(fs *flag.FlagSet) (db, apiURL *string) {
	db = fs.String("db", env("GW2TOOLS_DB", "gw2tools.db"), "SQLite database path (GW2TOOLS_DB)")
	apiURL = fs.String("api-url", env("GW2_API_URL", gw2.DefaultBaseURL), "GW2 API base URL (GW2_API_URL)")
	return
}

func openStore(path string) (*store.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return store.Open(path)
}

func serve(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	db, apiURL := commonFlags(fs)
	addr := fs.String("listen", env("GW2TOOLS_LISTEN", ":8080"), "listen address (GW2TOOLS_LISTEN)")
	ordersEvery := fs.Duration("orders-interval", durEnv("GW2TOOLS_ORDERS_INTERVAL", web.MinOrdersInterval), "how often to refresh unlocks and buy orders, at least 1m (GW2TOOLS_ORDERS_INTERVAL)")
	wikiURL := fs.String("wiki-url", env("GW2TOOLS_WIKI_URL", wiki.DefaultBaseURL), "GW2 wiki base URL, checked daily after midnight UTC for the Black Lion Chest sets (GW2TOOLS_WIKI_URL)")
	pricesEvery := fs.Duration("prices-interval", durEnv("GW2TOOLS_PRICES_INTERVAL", web.MinPricesInterval), "how often to refresh candidate item prices, at least 1h (GW2TOOLS_PRICES_INTERVAL)")
	fs.Parse(args)

	key := os.Getenv("GW2_API_KEY")
	if key == "" {
		log.Warn("GW2_API_KEY is not set; refreshes will fail until it is")
	}
	st, err := openStore(*db)
	if err != nil {
		return err
	}
	defer st.Close()

	srv, err := web.New(st, gw2.New(*apiURL, key), wiki.New(*wikiURL), log, web.Intervals{Orders: *ordersEvery, Prices: *pricesEvery})
	if err != nil {
		return err
	}
	// Everything that uses the store stops before the deferred st.Close runs.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	if key != "" {
		wg.Go(func() { srv.RunScheduler(ctx) })
	}

	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	wg.Go(func() {
		<-ctx.Done()
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		hs.Shutdown(shutdown)
	})
	iv := srv.Intervals()
	log.Info("listening", "addr", *addr, "db", *db, "orders_interval", iv.Orders, "prices_interval", iv.Prices)
	err = hs.ListenAndServe()
	cancel()
	wg.Wait() // drains HTTP requests and lets an in-progress refresh finish its write
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runSeed(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	db, apiURL := commonFlags(fs)
	content := fs.String("content", seed.DefaultContentURL, "Wardrobe Unlock Analyser content.json URL or file path")
	fs.Parse(args)

	st, err := openStore(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	return seed.Run(ctx, gw2.New(*apiURL, ""), st, *content, log)
}

func durEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

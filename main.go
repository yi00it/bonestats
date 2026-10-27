// bonestats is a tiny, cookieless, privacy-friendly web analytics server.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"bonestats/internal/collect"
	"bonestats/internal/demo"
	"bonestats/internal/store"
	"bonestats/internal/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "demo" {
		runDemo(os.Args[2:])
		return
	}
	retentionDefault, _ := strconv.Atoi(env("BONESTATS_RETENTION_DAYS", "365"))
	var (
		addr       = flag.String("addr", env("BONESTATS_ADDR", ":8080"), "listen address")
		dbPath     = flag.String("db", env("BONESTATS_DB", "bonestats.db"), "SQLite database file")
		geoPath    = flag.String("geoip", env("BONESTATS_GEOIP", ""), "optional DB-IP/MaxMind country .mmdb file")
		retention  = flag.Int("retention", retentionDefault, "delete data older than this many days (0 = keep forever)")
		tz         = flag.String("tz", env("BONESTATS_TZ", "Local"), "timezone for days in the dashboard, e.g. Europe/Berlin")
		trustProxy = flag.Bool("trust-proxy", env("BONESTATS_TRUST_PROXY", "") == "true", "read client IP from X-Forwarded-For")
	)
	flag.Parse()

	password := os.Getenv("BONESTATS_PASSWORD")
	if len(password) < 8 {
		log.Fatal("set BONESTATS_PASSWORD to an admin password of at least 8 characters")
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		log.Fatalf("timezone: %v", err)
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	geo, err := collect.OpenGeo(*geoPath)
	if err != nil {
		log.Fatalf("geoip: %v", err)
	}
	defer geo.Close()

	secret, err := st.Secret(context.Background(), "cookie_secret")
	if err != nil {
		log.Fatalf("secret: %v", err)
	}

	srv := &web.Server{
		Store:      st,
		Password:   password,
		Secret:     secret,
		Location:   loc,
		Retention:  *retention,
		GeoEnabled: geo.Enabled(),
		Collector:  &collect.Collector{Store: st, Geo: geo, Location: loc, TrustProxy: *trustProxy},
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *retention > 0 {
		go pruneLoop(ctx, st, time.Duration(*retention)*24*time.Hour)
	}

	go func() {
		log.Printf("bonestats listening on %s (geoip: %v, retention: %d days)", *addr, geo.Enabled(), *retention)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)
}

// pruneLoop deletes expired data at startup and then every six hours.
func pruneLoop(ctx context.Context, st *store.Store, keep time.Duration) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if n, err := st.Prune(ctx, time.Now().Add(-keep)); err != nil {
			log.Printf("prune: %v", err)
		} else if n > 0 {
			log.Printf("prune: deleted %d expired events", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runDemo implements "bonestats demo": it adds a site filled with fake
// traffic. Deleting that site on the Sites page removes all of it again.
func runDemo(args []string) {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	dbPath := fs.String("db", env("BONESTATS_DB", "bonestats.db"), "SQLite database file")
	domain := fs.String("site", "demo.example.com", "domain of the demo site to create")
	days := fs.Int("days", 90, "days of history to generate")
	visitors := fs.Int("visitors", 120, "average visitors per day")
	tz := fs.String("tz", env("BONESTATS_TZ", "Local"), "timezone for the daily traffic curve")
	replace := fs.Bool("replace", false, "delete an existing site with the same domain (and all its data) first")
	fs.Parse(args)

	loc, err := time.LoadLocation(*tz)
	if err != nil {
		log.Fatalf("timezone: %v", err)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	log.Printf("generating %d days of demo traffic for %s ...", *days, *domain)
	start := time.Now()
	res, err := demo.Seed(context.Background(), st, demo.Options{Domain: *domain, Days: *days, Visitors: *visitors, Location: loc, Replace: *replace})
	if err != nil {
		log.Fatalf("demo: %v (if the site already exists, pass -replace or another -site)", err)
	}
	log.Printf("done in %s: %d visits, %d pageviews, %d custom events", time.Since(start).Round(time.Millisecond), res.Visits, res.Pageviews, res.Events)
}

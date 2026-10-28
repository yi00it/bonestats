// SPDX-License-Identifier: AGPL-3.0-only

// bonestats is a tiny, cookieless, privacy-friendly web analytics server.
package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yi00it/bonestats/collect"
	"github.com/yi00it/bonestats/demo"
	"github.com/yi00it/bonestats/store"
	"github.com/yi00it/bonestats/web"
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
	retentionDefault, err := strconv.Atoi(env("BONESTATS_RETENTION_DAYS", "365"))
	if err != nil {
		log.Fatalf("BONESTATS_RETENTION_DAYS must be a whole number of days, got %q", os.Getenv("BONESTATS_RETENTION_DAYS"))
	}
	eventRateDefault, err := strconv.Atoi(env("BONESTATS_EVENT_RATE", "120"))
	if err != nil {
		log.Fatalf("BONESTATS_EVENT_RATE must be a whole number of beacons per minute, got %q", os.Getenv("BONESTATS_EVENT_RATE"))
	}
	trustProxyDefault := boolEnv("BONESTATS_TRUST_PROXY")
	var (
		addr         = flag.String("addr", env("BONESTATS_ADDR", ":8080"), "listen address")
		dbPath       = flag.String("db", env("BONESTATS_DB", "bonestats.db"), "SQLite database file")
		geoPath      = flag.String("geoip", env("BONESTATS_GEOIP", ""), "optional DB-IP/MaxMind country .mmdb file")
		retention    = flag.Int("retention", retentionDefault, "delete data older than this many days (0 = keep forever)")
		tz           = flag.String("tz", env("BONESTATS_TZ", "Local"), "timezone for days in the dashboard, e.g. Europe/Berlin")
		proxyHeader  = flag.String("proxy-header", env("BONESTATS_PROXY_HEADER", ""), "header your reverse proxy sets to the client IP, e.g. X-Forwarded-For or X-Real-IP")
		trustProxy   = flag.Bool("trust-proxy", trustProxyDefault, "deprecated: same as -proxy-header X-Forwarded-For")
		allowHTTP    = flag.Bool("allow-http", boolEnv("BONESTATS_ALLOW_HTTP"), "allow logging in over plain HTTP from hosts other than localhost")
		eventRate    = flag.Int("event-rate", eventRateDefault, "beacons per minute accepted from one IP (0 = unlimited)")
		passwordFile = flag.String("password-file", env("BONESTATS_PASSWORD_FILE", ""), "file holding the admin password, instead of BONESTATS_PASSWORD")
		sourceURL    = flag.String("source-url", env("BONESTATS_SOURCE_URL", "https://github.com/yi00it/bonestats"), "where users can get the source code of this version (AGPL-3.0)")
	)
	flag.Parse()
	if *retention < 0 {
		log.Fatal("retention must be 0 (keep forever) or a positive number of days")
	}
	if *eventRate < 0 {
		log.Fatal("event rate must be 0 (unlimited) or a positive number of beacons per minute")
	}
	if *trustProxy && *proxyHeader == "" {
		*proxyHeader = "X-Forwarded-For"
	}

	password, err := readPassword(*passwordFile)
	if err != nil {
		log.Fatal(err)
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

	// Changing the password ends every existing login. Only a slow hash of
	// it is stored, to notice the change.
	salt, err := st.Secret(context.Background(), "cookie_secret")
	if err != nil {
		log.Fatalf("secret: %v", err)
	}
	fingerprint, err := pbkdf2.Key(sha256.New, password, salt, 600_000, 32)
	if err != nil {
		log.Fatalf("password: %v", err)
	}
	if changed, err := st.CheckPassword(context.Background(), fingerprint); err != nil {
		log.Fatalf("password: %v", err)
	} else if changed {
		log.Print("admin password changed: logged out all sessions")
	}

	collector := &collect.Collector{Store: st, Geo: geo, Location: loc, ProxyHeader: *proxyHeader, RateLimit: *eventRate}
	srv := &web.Server{
		Store:       st,
		Password:    password,
		ProxyHeader: *proxyHeader,
		AllowHTTP:   *allowHTTP,
		Location:    loc,
		Retention:   *retention,
		GeoEnabled:  geo.Enabled(),
		Collector:   collector,
		SourceURL:   *sourceURL,
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
	go saltLoop(ctx, collector)

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

func boolEnv(key string) bool {
	v, err := strconv.ParseBool(env(key, "false"))
	if err != nil {
		log.Fatalf("%s must be true or false, got %q", key, os.Getenv(key))
	}
	return v
}

// minPassword is the shortest admin password accepted. The login is public,
// so it has to withstand guessing.
const minPassword = 12

// readPassword returns the admin password from the file, if given, or from
// BONESTATS_PASSWORD.
func readPassword(file string) (string, error) {
	password := os.Getenv("BONESTATS_PASSWORD")
	if file != "" {
		if password != "" {
			return "", errors.New("set either BONESTATS_PASSWORD or a password file, not both")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("password file: %w", err)
		}
		password = strings.TrimRight(string(b), "\r\n")
	}
	if len(password) < minPassword {
		return "", fmt.Errorf("set BONESTATS_PASSWORD (or BONESTATS_PASSWORD_FILE) to an admin password of at least %d characters", minPassword)
	}
	return password, nil
}

// saltLoop deletes the previous day's salt soon after midnight, even when no
// beacon arrives to rotate it.
func saltLoop(ctx context.Context, c *collect.Collector) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := c.RotateSalt(ctx, time.Now()); err != nil {
			log.Printf("salt: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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

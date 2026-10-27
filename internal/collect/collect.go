// Package collect implements the public endpoint that receives beacons from
// the tracking script and turns them into anonymous events.
package collect

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bonestats/internal/store"
)

const maxBody = 4 << 10

type Collector struct {
	Store      *store.Store
	Geo        *Geo
	Location   *time.Location
	TrustProxy bool // read client IP from X-Forwarded-For (behind a reverse proxy)

	mu      sync.Mutex
	saltDay string
	salt    []byte
}

// payload is what bs.js sends. Short keys keep beacons small.
type payload struct {
	Domain   string         `json:"d"`
	Name     string         `json:"n"` // "pageview", "leave" or a custom event name
	Path     string         `json:"p"`
	Referrer string         `json:"r"`
	Width    int            `json:"w"`
	Source   string         `json:"source"`
	Medium   string         `json:"medium"`
	Campaign string         `json:"campaign"`
	Term     string         `json:"term"`
	Content  string         `json:"content"`
	Props    map[string]any `json:"pr"` // custom event properties
	Revenue  *float64       `json:"rv"` // custom event revenue amount
	Scroll   *int64         `json:"sd"` // leave: max scroll depth %
	Engaged  *int64         `json:"et"` // leave: visible ms since last report
	Vitals   *struct {
		LCP  *float64 `json:"lcp"`
		CLS  *float64 `json:"cls"`
		INP  *float64 `json:"inp"`
		FCP  *float64 `json:"fcp"`
		TTFB *float64 `json:"ttfb"`
	} `json:"v"`
}

func (c *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Always answer the same way so the endpoint reveals nothing about
	// whether an event was stored.
	defer w.WriteHeader(http.StatusAccepted)

	// Honor the visitor's privacy signals, even if the script was modified.
	if r.Header.Get("Sec-GPC") == "1" || r.Header.Get("DNT") == "1" {
		return
	}
	ua := r.UserAgent()
	if isBot(ua) {
		return
	}

	var p payload
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&p); err != nil {
		return
	}
	if err := c.handle(r.Context(), r, ua, p); err != nil && !errors.Is(err, errDropped) {
		log.Printf("collect: %v", err)
	}
}

var errDropped = errors.New("dropped")

func (c *Collector) handle(ctx context.Context, r *http.Request, ua string, p payload) error {
	domain := store.NormalizeDomain(p.Domain)
	site, err := c.Store.SiteByDomain(ctx, domain)
	if errors.Is(err, sql.ErrNoRows) {
		return errDropped
	} else if err != nil {
		return err
	}
	if !originAllowed(r.Header.Get("Origin"), domain) {
		return errDropped
	}

	now := time.Now()
	visitor, err := c.visitorHash(ctx, now, domain, clientIP(r, c.TrustProxy), ua)
	if err != nil {
		return err
	}
	path := cleanPath(p.Path)
	device := deviceFromWidth(p.Width)

	if p.Name == "leave" {
		var scroll, engaged *int64
		if p.Scroll != nil && *p.Scroll >= 0 && *p.Scroll <= 100 {
			scroll = p.Scroll
		}
		if p.Engaged != nil && *p.Engaged >= 0 && *p.Engaged <= int64(store.SessionTimeout/time.Millisecond) {
			engaged = p.Engaged
		}
		if err := c.Store.RecordLeave(ctx, store.Leave{
			SiteID: site.ID, Visitor: visitor, Time: now, Path: path, Scroll: scroll, Engaged: engaged,
		}); err != nil {
			return err
		}
		if v := p.Vitals; v != nil {
			return c.Store.RecordVitals(ctx, store.Vitals{
				SiteID: site.ID, Time: now, Path: path, Device: device,
				LCP: clamp(v.LCP, 0, 120000), CLS: clamp(v.CLS, 0, 100), INP: clamp(v.INP, 0, 120000),
				FCP: clamp(v.FCP, 0, 120000), TTFB: clamp(v.TTFB, 0, 120000),
			})
		}
		return nil
	}

	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 64 {
		return errDropped
	}
	e := store.Event{SiteID: site.ID, Time: now, Visitor: visitor, Name: name, Path: path}
	if name != "pageview" {
		e.Props = cleanProps(p.Props)
		if p.Revenue != nil && !math.IsNaN(*p.Revenue) && math.Abs(*p.Revenue) < 1e9 {
			e.Revenue = p.Revenue
		}
	}

	// Visit attributes are only stored when this event starts a new visit.
	refHost, refURL := referrer(p.Referrer, domain)
	browser, os := parseBrowser(ua), parseOS(ua)
	loc := c.Geo.Lookup(clientIP(r, c.TrustProxy))
	e.Visit = store.Visit{
		Referrer:       refHost,
		ReferrerURL:    refURL,
		UTMSource:      limit(p.Source, 100),
		UTMMedium:      limit(p.Medium, 100),
		UTMCampaign:    limit(p.Campaign, 100),
		UTMTerm:        limit(p.Term, 100),
		UTMContent:     limit(p.Content, 100),
		Country:        loc.Country,
		Region:         limit(loc.Region, 100),
		City:           limit(loc.City, 100),
		Browser:        browser,
		BrowserVersion: browserVersion(ua, browser),
		OS:             os,
		OSVersion:      osVersion(ua, os),
		Device:         device,
		Screen:         screenBucket(p.Width),
	}
	e.Visit.Channel = store.Channel(e.Visit.Referrer, e.Visit.UTMSource, e.Visit.UTMMedium)
	return c.Store.RecordEvent(ctx, e)
}

// cleanProps keeps at most 10 properties with simple names and short scalar
// values, all stored as strings.
func cleanProps(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if len(out) == 10 || !store.ValidPropKey(k) {
			continue
		}
		var s string
		switch v := v.(type) {
		case string:
			s = v
		case float64:
			s = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			s = strconv.FormatBool(v)
		default:
			continue
		}
		if s = limit(s, 256); s != "" {
			out[k] = s
		}
	}
	return out
}

// visitorHash = sha256(dailySalt + domain + ip + userAgent), truncated to 64
// bits. Neither the IP nor the user agent is stored, and since the salt is
// deleted at the end of each day the hash cannot be reversed or linked to
// the same person on another day.
func (c *Collector) visitorHash(ctx context.Context, now time.Time, domain string, ip netip.Addr, ua string) (int64, error) {
	salt, err := c.dailySalt(ctx, now)
	if err != nil {
		return 0, err
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(ip.AsSlice())
	h.Write([]byte(ua))
	return int64(binary.BigEndian.Uint64(h.Sum(nil)[:8])), nil
}

func (c *Collector) dailySalt(ctx context.Context, now time.Time) ([]byte, error) {
	day := now.In(c.Location).Format("2006-01-02")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saltDay == day {
		return c.salt, nil
	}
	salt, err := c.Store.DailySalt(ctx, day)
	if err != nil {
		return nil, err
	}
	c.saltDay, c.salt = day, salt
	return salt, nil
}

func clientIP(r *http.Request, trustProxy bool) netip.Addr {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
				return ip.Unmap()
			}
		}
		if ip, err := netip.ParseAddr(r.Header.Get("X-Real-IP")); err == nil {
			return ip.Unmap()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

// originAllowed rejects beacons sent by browsers from other websites. A
// missing Origin is accepted (some browsers omit it for same-origin beacons).
func originAllowed(origin, domain string) bool {
	if origin == "" {
		return true
	}
	host := store.NormalizeDomain(origin)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// referrer returns the referring host and host+path (the query string is
// never kept, as it can contain personal data). Internal navigation is
// dropped.
func referrer(ref, domain string) (host, hostPath string) {
	if ref == "" {
		return "", ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	host = store.NormalizeDomain(u.Hostname())
	if host == domain || strings.HasSuffix(host, "."+domain) {
		return "", ""
	}
	hostPath = host
	if p := strings.TrimSuffix(u.EscapedPath(), "/"); p != "" {
		hostPath += p
	}
	return limit(host, 253), limit(hostPath, 512)
}

func cleanPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return limit(p, 512)
}

func limit(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func clamp(v *float64, lo, hi float64) *float64 {
	if v == nil || *v < lo || *v > hi {
		return nil
	}
	return v
}

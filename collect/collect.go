// SPDX-License-Identifier: AGPL-3.0-only

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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yi00it/bonestats/store"
)

const maxBody = 4 << 10

type Collector struct {
	// Store holds the sites, their events and the daily salt. A host that
	// keeps one store per customer sets Sites and Salts instead.
	Store       *store.Store
	Sites       Sites
	Salts       Salts
	Geo         *Geo
	Location    *time.Location
	ProxyHeader string // header the reverse proxy puts the client IP in; "" = none
	RateLimit   int    // beacons per minute per client; 0 = unlimited

	// IgnorePrivacySignals counts visitors who send Global Privacy Control
	// or Do Not Track, instead of dropping their events.
	IgnorePrivacySignals bool

	limitOnce sync.Once
	limiter   *rateLimiter

	mu      sync.Mutex
	saltDay string
	salt    []byte
	// The salt of the day that just ended, kept in memory only and for one
	// session timeout, so a visit in progress at midnight isn't cut in two.
	prevSalt  []byte
	prevUntil time.Time
}

// Sites finds the store that holds a domain's site.
type Sites interface {
	// Site returns sql.ErrNoRows for a domain that isn't tracked.
	Site(ctx context.Context, domain string) (*store.Store, store.Site, error)
}

// Salts keeps the daily salt for visitor hashes; *store.Store is one.
type Salts interface {
	DailySalt(ctx context.Context, day string) ([]byte, error)
	PurgeSalts(ctx context.Context, keepDay string) error
}

// oneStore puts every site in the same store.
type oneStore struct{ *store.Store }

func (o oneStore) Site(ctx context.Context, domain string) (*store.Store, store.Site, error) {
	site, err := o.SiteByDomain(ctx, domain)
	return o.Store, site, err
}

func (c *Collector) sites() Sites {
	if c.Sites != nil {
		return c.Sites
	}
	return oneStore{c.Store}
}

func (c *Collector) salts() Salts {
	if c.Salts != nil {
		return c.Salts
	}
	return c.Store
}

// payload is what bs.js sends. Short keys keep beacons small.
type payload struct {
	Domain   string         `json:"d"`
	Name     string         `json:"n"` // "pageview", "leave" or a custom event name
	Path     string         `json:"p"`
	Referrer string         `json:"r"`
	Width    int            `json:"w"`
	Touch    int            `json:"t"` // touch points of the screen
	ClickID  string         `json:"c"` // name of an ad click ID in the URL, e.g. "gclid"
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
	if !c.IgnorePrivacySignals && (r.Header.Get("Sec-GPC") == "1" || r.Header.Get("DNT") == "1") {
		return
	}
	ua := r.UserAgent()
	if isBot(ua) {
		return
	}
	ip := ClientIP(r, c.ProxyHeader)
	c.limitOnce.Do(func() { c.limiter = newRateLimiter(c.RateLimit) })
	if !c.limiter.allow(ip, time.Now()) {
		return
	}

	var p payload
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&p); err != nil {
		return
	}
	// Beacons sent while a page unloads often disconnect before the reply;
	// that must not cancel the write.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	if err := c.handle(ctx, r, ip, ua, p); err != nil && !errors.Is(err, errDropped) {
		log.Printf("collect: %v", err)
	}
}

var errDropped = errors.New("dropped")

func (c *Collector) handle(ctx context.Context, r *http.Request, ip netip.Addr, ua string, p payload) error {
	domain := store.NormalizeDomain(p.Domain)
	st, site, err := c.sites().Site(ctx, domain)
	if errors.Is(err, sql.ErrNoRows) {
		return errDropped
	} else if err != nil {
		return err
	}
	if !originAllowed(r.Header.Get("Origin"), domain) {
		return errDropped
	}

	now := time.Now()
	visitor, err := c.visitor(ctx, st, site.ID, now, domain, ip, ua)
	if err != nil {
		return err
	}
	path := cleanPath(p.Path)
	device := deviceClass(ua, p.Width, p.Touch)

	if p.Name == "leave" {
		var scroll, engaged *int64
		if p.Scroll != nil && *p.Scroll >= 0 && *p.Scroll <= 100 {
			scroll = p.Scroll
		}
		if p.Engaged != nil && *p.Engaged >= 0 && *p.Engaged <= int64(store.SessionTimeout/time.Millisecond) {
			engaged = p.Engaged
		}
		l := store.Leave{SiteID: site.ID, Visitor: visitor, Time: now, Path: path, Scroll: scroll, Engaged: engaged}
		if v := p.Vitals; v != nil {
			l.Vitals = &store.Vitals{
				Device: device,
				LCP:    clamp(v.LCP, 0, 120000), CLS: clamp(v.CLS, 0, 100), INP: clamp(v.INP, 0, 120000),
				FCP: clamp(v.FCP, 0, 120000), TTFB: clamp(v.TTFB, 0, 120000),
			}
		}
		_, err := st.RecordLeave(ctx, l)
		return err
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
	if os == "macOS" && p.Touch > 1 {
		os = "iOS" // an iPad asking for desktop sites calls itself a Mac
	}
	loc := c.Geo.Lookup(ip)
	// An ad click ID in the URL marks a paid visit even without UTM tags.
	chSource, chMedium := limit(p.Source, 100), limit(p.Medium, 100)
	if network := store.ClickIDs[p.ClickID]; network != "" && chMedium == "" {
		chMedium = "cpc"
		if chSource == "" {
			chSource = network
		}
	}
	e.Visit = store.Visit{
		Referrer:       store.Source(refHost, p.Source),
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
	e.Visit.Referrer = limit(e.Visit.Referrer, 253)
	e.Visit.Channel = store.Channel(refHost, chSource, chMedium)
	return st.RecordEvent(ctx, e)
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

// visitor returns the visitor's hash for today. For one session timeout
// after midnight, a visitor whose visit began before midnight keeps
// yesterday's hash, so the visit continues instead of starting again as a
// new visitor.
func (c *Collector) visitor(ctx context.Context, st *store.Store, siteID int64, now time.Time, domain string, ip netip.Addr, ua string) (int64, error) {
	salt, prev, err := c.dailySalt(ctx, now)
	if err != nil {
		return 0, err
	}
	if prev != nil {
		old := visitorHash(prev, domain, ip, ua)
		if current, err := st.HasCurrentVisit(ctx, siteID, old, now); err != nil {
			return 0, err
		} else if current {
			return old, nil
		}
	}
	return visitorHash(salt, domain, ip, ua), nil
}

// visitorHash = sha256(dailySalt + domain + ip + userAgent), truncated to 64
// bits. Neither the IP nor the user agent is stored, and since the salt is
// deleted at the end of each day the hash cannot be reversed or linked to
// the same person on another day. Of an IPv6 address only the /64 network
// counts: devices change the rest of their address at will, often several
// times a day.
func visitorHash(salt []byte, domain string, ip netip.Addr, ua string) int64 {
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			ip = p.Addr()
		}
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(ip.AsSlice())
	h.Write([]byte(ua))
	return int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
}

// dailySalt returns today's salt and, shortly after midnight, yesterday's.
func (c *Collector) dailySalt(ctx context.Context, now time.Time) (salt, prev []byte, err error) {
	local := now.In(c.Location)
	day := local.Format("2006-01-02")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saltDay != day {
		salt, err := c.salts().DailySalt(ctx, day)
		if err != nil {
			return nil, nil, err
		}
		if c.saltDay != "" {
			c.forget(local)
		}
		c.saltDay, c.salt = day, salt
	}
	if now.Before(c.prevUntil) {
		prev = c.prevSalt
	}
	return c.salt, prev, nil
}

// forget drops the salt of a day that has ended, keeping it in memory as
// the previous one if that day was yesterday. The caller holds c.mu.
func (c *Collector) forget(local time.Time) {
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	c.prevSalt, c.prevUntil = nil, time.Time{}
	if c.salt != nil && c.saltDay == midnight.AddDate(0, 0, -1).Format("2006-01-02") {
		c.prevSalt, c.prevUntil = c.salt, midnight.Add(store.SessionTimeout)
	}
	c.saltDay, c.salt = "", nil
}

// RotateSalt forgets yesterday's salt once the day has changed: in the
// database at once, in memory one session timeout after midnight. Run it
// periodically: otherwise the old salt would survive until the first beacon
// of the new day.
func (c *Collector) RotateSalt(ctx context.Context, now time.Time) error {
	local := now.In(c.Location)
	day := local.Format("2006-01-02")
	c.mu.Lock()
	if c.saltDay != day && c.saltDay != "" {
		c.forget(local)
	}
	if !now.Before(c.prevUntil) {
		c.prevSalt, c.prevUntil = nil, time.Time{}
	}
	c.mu.Unlock()
	return c.salts().PurgeSalts(ctx, day)
}

// ClientIP returns the visitor's IP. Behind a reverse proxy, proxyHeader
// names the one header the proxy sets to the client's IP; only that header
// is read, because any other one may come straight from the client. For
// X-Forwarded-For it is the last entry, the one the proxy itself appended:
// earlier entries come from the client and can be forged.
func ClientIP(r *http.Request, proxyHeader string) netip.Addr {
	if proxyHeader != "" {
		if vs := r.Header.Values(proxyHeader); len(vs) > 0 {
			last := vs[len(vs)-1]
			if i := strings.LastIndexByte(last, ','); i >= 0 {
				last = last[i+1:]
			}
			if ip, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
				return ip.Unmap()
			}
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
	if p := strings.TrimSuffix(scrubPath(store.UnescapePath(u.EscapedPath())), "/"); p != "" {
		hostPath += p
	}
	return limit(host, 253), limit(hostPath, 512)
}

// cleanPath returns the page path as it is stored: without query string or
// fragment, readable (percent-decoded), without a trailing slash, scrubbed.
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
	return limit(scrubPath(store.UnescapePath(p)), 512)
}

var (
	hexIDRe    = regexp.MustCompile(`^[0-9a-fA-F-]+$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9_.=~-]{20,}$`)
	upperRe    = regexp.MustCompile(`[A-Z]`)
	lowerRe    = regexp.MustCompile(`[a-z]`)
	hasDigitRe = regexp.MustCompile(`[0-9]`)
	// A word as it appears in a readable slug or file name: "top", "Pro",
	// "JavaScript", "iPhone", "USB" or "2026", also with a number after it
	// ("M4") or a unit after a number ("4K").
	wordRe = regexp.MustCompile(`^(?:(?:[a-z]+|[A-Z][a-z]+)(?:[A-Z][a-z]+)*|[A-Z]+)?[0-9]*$|^[0-9]+[A-Za-z]{1,2}$|^.{1,3}$`)
)

// scrubPath replaces path segments that look like personal data or secrets,
// such as /reset/<token> or /users/jane@example.com, so they are never
// stored. It is a best-effort guard: email addresses become [email], and
// UUIDs, long hex IDs and mixed-case tokens become [id].
func scrubPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		lower := strings.ToLower(s)
		switch {
		case s == "":
		case strings.Contains(s, "@") || strings.Contains(lower, "%40"):
			segs[i] = "[email]"
		case hexIDRe.MatchString(s) && strings.Count(s, "-") < len(s)-15:
			segs[i] = "[id]" // 16+ hex digits, e.g. a UUID
		case tokenRe.MatchString(s) && upperRe.MatchString(s) && lowerRe.MatchString(s) && hasDigitRe.MatchString(s) && !isSlug(s):
			segs[i] = "[id]" // base64-like token
		}
	}
	return strings.Join(segs, "/")
}

// isSlug reports whether s is several words joined by separators, like
// "iPhone-17-Pro-Max-review" or "Annual_Report_2025.pdf". A random token
// practically never splits into words only.
func isSlug(s string) bool {
	parts := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("-_.~=", r) })
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !wordRe.MatchString(p) {
			return false
		}
	}
	return true
}

func limit(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "") // don't end in half a character
	}
	return s
}

func clamp(v *float64, lo, hi float64) *float64 {
	if v == nil || *v < lo || *v > hi {
		return nil
	}
	return v
}

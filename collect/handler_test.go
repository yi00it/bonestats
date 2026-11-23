// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

// A beacon whose connection is already gone (the page was closing) must
// still be stored.
func TestBeaconSurvivesDisconnect(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	geo, _ := OpenGeo("")
	c := &Collector{Store: st, Geo: geo, Location: time.UTC}

	gone, cancel := context.WithCancel(ctx)
	cancel()
	for _, body := range []string{`{"d":"example.com","n":"pageview","p":"/","w":1200}`, `{"d":"example.com","n":"leave","p":"/","et":5000}`} {
		r := httptest.NewRequest("POST", "/api/event", strings.NewReader(body)).WithContext(gone)
		r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0")
		r.Header.Set("Origin", "https://example.com")
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatalf("status = %d", w.Code)
		}
	}

	now := time.Now()
	sum, err := st.Summary(ctx, store.Query{SiteID: site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Pageviews != 1 {
		t.Errorf("pageviews = %d, want 1", sum.Pageviews)
	}
}

func beacon(c *Collector, body string) {
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(body))
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0")
	r.RemoteAddr = "203.0.113.9:5000"
	c.ServeHTTP(httptest.NewRecorder(), r)
}

// A flood from one client is cut off, and vitals without a visit are not
// stored.
func TestFloodAndOrphanVitals(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	geo, _ := OpenGeo("")
	c := &Collector{Store: st, Geo: geo, Location: time.UTC, RateLimit: 30}

	beacon(c, `{"d":"example.com","n":"leave","p":"/","v":{"lcp":1200}}`)
	for i := 0; i < 100; i++ {
		beacon(c, `{"d":"example.com","n":"pageview","p":"/","w":1200}`)
	}
	c.limiter = nil // let the leave beacon through, so the visit counts
	beacon(c, `{"d":"example.com","n":"leave","p":"/","et":5000}`)

	now := time.Now()
	q := store.Query{SiteID: site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	sum, err := st.Summary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Pageviews != 29 { // burst of 30, one used by the leave beacon
		t.Errorf("pageviews = %d, want 29", sum.Pageviews)
	}
	vitals, err := st.VitalsSummary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if n := vitals["lcp"].Samples; n != 0 {
		t.Errorf("stored %d vitals without a visit", n)
	}
}

// tenants routes each domain to its own store, as a multi-tenant host would.
type tenants map[string]*store.Store

func (t tenants) Site(ctx context.Context, domain string) (*store.Store, store.Site, error) {
	st, ok := t[domain]
	if !ok {
		return nil, store.Site{}, sql.ErrNoRows
	}
	site, err := st.SiteByDomain(ctx, domain)
	return st, site, err
}

func TestBeaconsGoToTheSitesStore(t *testing.T) {
	ctx := context.Background()
	open := func(name string) *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	a, b, salts := open("a.db"), open("b.db"), open("salts.db")
	a.AddSite(ctx, "a.example")
	b.AddSite(ctx, "b.example")
	geo, _ := OpenGeo("")
	c := &Collector{Sites: tenants{"a.example": a, "b.example": b}, Salts: salts, Geo: geo, Location: time.UTC}

	beacon(c, `{"d":"a.example","n":"pageview","p":"/a"}`)
	beacon(c, `{"d":"b.example","n":"pageview","p":"/b1"}`)
	beacon(c, `{"d":"b.example","n":"pageview","p":"/b2"}`)
	beacon(c, `{"d":"unknown.example","n":"pageview","p":"/"}`)
	beacon(c, `{"d":"a.example","n":"leave","p":"/a","et":5000}`)
	beacon(c, `{"d":"b.example","n":"leave","p":"/b2","et":5000}`)

	now := time.Now()
	for _, tc := range []struct {
		st     *store.Store
		domain string
		want   int64
	}{{a, "a.example", 1}, {b, "b.example", 2}} {
		site, _ := tc.st.SiteByDomain(ctx, tc.domain)
		sum, err := tc.st.Summary(ctx, store.Query{SiteID: site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Pageviews != tc.want {
			t.Errorf("%s: %d pageviews, want %d", tc.domain, sum.Pageviews, tc.want)
		}
	}
	// Store is nil, so hashing and rotation can only have used Salts.
	if err := c.RotateSalt(ctx, now); err != nil {
		t.Fatal(err)
	}
}

// Visitors who send Global Privacy Control or Do Not Track are dropped,
// unless the collector is told to ignore those signals.
func TestPrivacySignals(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		ctx := context.Background()
		st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		st.AddSite(ctx, "example.com")
		site, _ := st.SiteByDomain(ctx, "example.com")
		geo, _ := OpenGeo("")
		c := &Collector{Store: st, Geo: geo, Location: time.UTC, IgnorePrivacySignals: ignore}

		for _, h := range []string{"Sec-GPC", "DNT"} {
			for _, body := range []string{`{"d":"example.com","n":"pageview","p":"/","w":1200}`, `{"d":"example.com","n":"leave","p":"/","et":5000}`} {
				r := httptest.NewRequest("POST", "/api/event", strings.NewReader(body))
				r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0")
				r.Header.Set(h, "1")
				r.RemoteAddr = "203.0.113.9:5000"
				c.ServeHTTP(httptest.NewRecorder(), r)
			}
		}

		now := time.Now()
		sum, err := st.Summary(ctx, store.Query{SiteID: site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if ignore {
			want = 2
		}
		if sum.Pageviews != want {
			t.Errorf("ignore=%v: pageviews = %d, want %d", ignore, sum.Pageviews, want)
		}
		st.Close()
	}
}

// Visits whose pages were never visible for a second, as with bots that load
// a page and leave, are hidden from the dashboard, and so are their vitals.
func TestVisitsWithoutEngagementAreHidden(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	geo, _ := OpenGeo("")
	c := &Collector{Store: st, Geo: geo, Location: time.UTC}

	send := func(ip, body string) {
		r := httptest.NewRequest("POST", "/api/event", strings.NewReader(body))
		r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0")
		r.RemoteAddr = ip + ":5000"
		c.ServeHTTP(httptest.NewRecorder(), r)
	}
	// no leave beacon at all
	send("203.0.113.1", `{"d":"example.com","n":"pageview","p":"/a","w":1200}`)
	// gone in half a second
	send("203.0.113.2", `{"d":"example.com","n":"pageview","p":"/b","w":1200}`)
	send("203.0.113.2", `{"d":"example.com","n":"leave","p":"/b","et":500,"v":{"lcp":900}}`)
	// a reader: two reports that add up to more than a second
	send("203.0.113.3", `{"d":"example.com","n":"pageview","p":"/c","w":1200}`)
	send("203.0.113.3", `{"d":"example.com","n":"leave","p":"/c","et":600}`)
	send("203.0.113.3", `{"d":"example.com","n":"leave","p":"/c","et":600,"v":{"lcp":1200}}`)

	now := time.Now()
	q := store.Query{SiteID: site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
	sum, err := st.Summary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Visitors != 1 || sum.Pageviews != 1 {
		t.Errorf("visitors = %d, pageviews = %d, want 1/1", sum.Visitors, sum.Pageviews)
	}
	pages, err := st.Breakdown(ctx, q, "page", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].Label != "/c" {
		t.Errorf("pages = %+v, want only /c", pages)
	}
	if n, _ := st.CurrentVisitors(ctx, site.ID); n != 1 {
		t.Errorf("current visitors = %d, want 1", n)
	}
	vitals, err := st.VitalsSummary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if n := vitals["lcp"].Samples; n != 1 {
		t.Errorf("lcp samples = %d, want 1", n)
	}
}

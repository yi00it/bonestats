// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
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
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(`{"d":"example.com","n":"pageview","p":"/","w":1200}`)).WithContext(gone)
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0")
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	c.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("status = %d", w.Code)
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

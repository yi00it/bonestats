package collect

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bonestats/internal/store"
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

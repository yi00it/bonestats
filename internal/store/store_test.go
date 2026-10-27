package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionsAndSummary(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddSite(ctx, "https://www.Example.com/"); err != nil {
		t.Fatal(err)
	}
	site, err := s.SiteByDomain(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	pv := func(visitor int64, at time.Time, path string) {
		t.Helper()
		if err := s.RecordEvent(ctx, Event{SiteID: site.ID, Time: at, Visitor: visitor, Name: "pageview", Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	// Visitor 1: two pages in one visit, then returns after the timeout.
	pv(1, t0, "/")
	pv(1, t0.Add(2*time.Minute), "/pricing")
	pv(1, t0.Add(2*time.Hour), "/")
	// Visitor 2: a single bounce.
	pv(2, t0, "/blog")

	sum, err := s.Summary(ctx, Query{SiteID: site.ID, From: t0.Add(-time.Hour), To: t0.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Visitors != 2 || sum.Pageviews != 4 || sum.Visits != 3 {
		t.Fatalf("got %+v", sum)
	}
	if want := 200.0 / 3; sum.BounceRate < want-0.01 || sum.BounceRate > want+0.01 {
		t.Errorf("bounce rate = %.2f, want %.2f", sum.BounceRate, want)
	}
	if sum.AvgDuration != 40*time.Second {
		t.Errorf("avg duration = %v, want 40s", sum.AvgDuration)
	}
}

func TestVitalsP75(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")
	now := time.Now()
	for _, v := range []float64{100, 200, 300, 400} {
		v := v
		if err := s.RecordVitals(ctx, Vitals{SiteID: site.ID, Time: now, Path: "/", LCP: &v}); err != nil {
			t.Fatal(err)
		}
	}
	q := Query{SiteID: site.ID, From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	sum, err := s.VitalsSummary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum["lcp"].P75 != 300 || sum["lcp"].Samples != 4 {
		t.Errorf("lcp = %+v, want p75 300 of 4", sum["lcp"])
	}
	pages, err := s.VitalsByPage(ctx, q, 10)
	if err != nil || len(pages) != 1 || pages[0].P75["lcp"] == nil || *pages[0].P75["lcp"] != 300 {
		t.Errorf("by page = %+v, %v", pages, err)
	}
}

// A dashboard query in progress must not hold up writes from the collector.
func TestReadsDoNotBlockWrites(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")

	rows, err := s.rdb.QueryContext(ctx, `SELECT id FROM sites`) // left open, like a slow query
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.RecordEvent(wctx, Event{SiteID: site.ID, Time: time.Now(), Visitor: 1, Name: "pageview", Path: "/"}); err != nil {
		t.Fatalf("write blocked by an open read: %v", err)
	}
	if _, err := s.rdb.ExecContext(ctx, `DELETE FROM sites`); err == nil {
		t.Error("read-only pool accepted a write")
	}
}

func TestDatabaseIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	os.WriteFile(path, nil, 0o644) // an existing, world-readable file is tightened too
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DailySalt(context.Background(), "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := fi.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(p), mode)
		}
	}
}

func TestAddSiteValidatesDomain(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ok := range []string{"example.com", "https://www.shop.example.co.uk/path", "localhost", "xn--bcher-kva.example", "192.168.1.5:8080"} {
		if err := s.AddSite(ctx, ok); err != nil {
			t.Errorf("AddSite(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "exa mple.com", `"><script>`, "-example.com", "example..com", "bücher.example", "*.example.com"} {
		if err := s.AddSite(ctx, bad); !errors.Is(err, ErrInvalidDomain) {
			t.Errorf("AddSite(%q) = %v, want ErrInvalidDomain", bad, err)
		}
	}
}

func TestPurgeSalts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, _ := s.DailySalt(ctx, "2026-01-01")
	if err := s.PurgeSalts(ctx, "2026-01-02"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM salts`).Scan(&n)
	if n != 0 {
		t.Errorf("%d salts left after the day changed", n)
	}
	if again, _ := s.DailySalt(ctx, "2026-01-01"); string(again) == string(old) {
		t.Error("purged salt came back")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestChannelAndSource(t *testing.T) {
	cases := []struct{ ref, src, med, channel, source string }{
		{"google.com.tr", "", "", "Organic Search", "Google"},
		{"com.google.android.googlequicksearchbox", "", "", "Organic Search", "Google"},
		{"mail.google.com", "", "", "Email", "Gmail"},
		{"com.google.android.gm", "", "", "Email", "Gmail"},
		{"mail.yandex.com.tr", "", "", "Email", "mail.yandex.com.tr"},
		{"outlook.live.com", "", "", "Email", "outlook.live.com"},
		{"news.google.com", "", "", "Referral", "Google News"},
		{"docs.google.com", "", "", "Referral", "docs.google.com"},
		{"gemini.google.com", "", "", "AI", "Gemini"},
		{"chatgpt.com", "", "", "AI", "ChatGPT"},
		{"claude.ai", "", "", "AI", "Claude"},
		{"ask.metafilter.com", "", "", "Referral", "ask.metafilter.com"},
		{"t.co", "", "", "Organic Social", "X"},
		{"l.facebook.com", "", "", "Organic Social", "Facebook"},
		{"google.com", "google", "cpc", "Paid Search", "Google"},
		{"", "tiktok", "cpc", "Paid Social", "TikTok"},
		{"", "Spring Newsletter", "email", "Email", "Spring Newsletter"},
		{"blog.example.org", "", "", "Referral", "blog.example.org"},
		{"", "", "", "Direct", ""},
	}
	for _, c := range cases {
		if got := Channel(c.ref, c.src, c.med); got != c.channel {
			t.Errorf("Channel(%q, %q, %q) = %q, want %q", c.ref, c.src, c.med, got, c.channel)
		}
		if got := Source(c.ref, c.src); got != c.source {
			t.Errorf("Source(%q, %q) = %q, want %q", c.ref, c.src, got, c.source)
		}
	}
}

func TestUnescapePath(t *testing.T) {
	cases := map[string]string{
		"/yaz%C4%B1l%C4%B1m/haber": "/yazılım/haber",
		"/a%2Fb":                   "/a%2Fb", // would change the path
		"/a%3Fb":                   "/a%3Fb",
		"/bad%ZZ":                  "/bad%ZZ",
		"/bin%FF":                  "/bin%FF", // not text
		"/100%25":                  "/100%25",
		"/plain":                   "/plain",
	}
	for in, want := range cases {
		if got := UnescapePath(in); got != want {
			t.Errorf("UnescapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func open(t *testing.T) (*Store, Site) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")
	return s, site
}

// A report that arrives after the visit's 30 minutes of inactivity (a long
// read, a tab opened again) still goes to its pageview, without bringing
// the visit back to life.
func TestLateLeave(t *testing.T) {
	ctx := context.Background()
	s, site := open(t)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	if err := s.RecordEvent(ctx, Event{SiteID: site.ID, Time: t0, Visitor: 1, Name: "pageview", Path: "/long"}); err != nil {
		t.Fatal(err)
	}
	leave := func(at time.Time, ms, scroll int64) {
		t.Helper()
		if counts, err := s.RecordLeave(ctx, Leave{SiteID: site.ID, Visitor: 1, Time: at, Path: "/long", Engaged: &ms, Scroll: &scroll}); err != nil || !counts {
			t.Fatalf("leave: counts = %v, err = %v", counts, err)
		}
	}
	leave(t0.Add(5*time.Second), 5000, 20)
	leave(t0.Add(45*time.Minute), 600000, 90)

	q := Query{SiteID: site.ID, From: t0.Add(-time.Hour), To: t0.Add(24 * time.Hour)}
	pages, err := s.Breakdown(ctx, q, "page", 10)
	if err != nil || len(pages) != 1 {
		t.Fatalf("pages = %+v, %v", pages, err)
	}
	if got := *pages[0].TimeOnPage; got != 605*time.Second {
		t.Errorf("time on page = %v, want 10m5s", got)
	}
	if got := *pages[0].Scroll; got != 90 {
		t.Errorf("scroll = %v, want 90", got)
	}
	sum, _ := s.Summary(ctx, q)
	if sum.AvgDuration != 605*time.Second {
		t.Errorf("visit duration = %v, want 10m5s", sum.AvgDuration)
	}
	// The next pageview starts a new visit.
	view(t, s, Event{SiteID: site.ID, Time: t0.Add(46 * time.Minute), Visitor: 1, Name: "pageview", Path: "/next"})
	if sum, _ = s.Summary(ctx, q); sum.Visits != 2 {
		t.Errorf("visits = %d, want 2", sum.Visits)
	}
	// A report for a page the visitor never loaded stores nothing.
	ms := int64(5000)
	if counts, err := s.RecordLeave(ctx, Leave{SiteID: site.ID, Visitor: 1, Time: t0, Path: "/never", Engaged: &ms}); err != nil || counts {
		t.Errorf("unknown page: counts = %v, err = %v", counts, err)
	}
}

// Vitals reported again by the same page load replace the first report, and
// can be filtered by the visit they belong to.
func TestVitalsPerPageLoad(t *testing.T) {
	ctx := context.Background()
	s, site := open(t)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	load := func(visitor int64, country string, cls ...float64) {
		t.Helper()
		if err := s.RecordEvent(ctx, Event{SiteID: site.ID, Time: t0, Visitor: visitor, Name: "pageview", Path: "/", Visit: Visit{Country: country}}); err != nil {
			t.Fatal(err)
		}
		for _, v := range cls {
			ms, v, lcp := int64(2000), v, 1000.0
			l := Leave{SiteID: site.ID, Visitor: visitor, Time: t0, Path: "/", Engaged: &ms, Vitals: &Vitals{CLS: &v}}
			if v == cls[0] {
				l.Vitals.LCP = &lcp
			}
			if _, err := s.RecordLeave(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
	}
	load(1, "DE", 0.05, 0.3)
	load(2, "FR", 0.01)

	q := Query{SiteID: site.ID, From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}
	sum, err := s.VitalsSummary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum["cls"].Samples != 2 || sum["cls"].P75 != 0.3 || sum["lcp"].Samples != 2 {
		t.Errorf("cls = %+v, lcp = %+v, want 2 samples each and cls p75 0.3", sum["cls"], sum["lcp"])
	}
	q.Filters = []Filter{{Dim: "country", Op: OpIs, Value: "FR"}}
	if sum, err = s.VitalsSummary(ctx, q); err != nil || sum["cls"].Samples != 1 || sum["cls"].P75 != 0.01 {
		t.Errorf("FR: cls = %+v, %v", sum["cls"], err)
	}
	if pages, err := s.VitalsByPage(ctx, q, 10); err != nil || len(pages) != 1 || pages[0].Samples != 1 {
		t.Errorf("FR by page = %+v, %v", pages, err)
	}
}

// Direct visits are a row of the sources and can be filtered on.
func TestDirectSource(t *testing.T) {
	s, q := fixture(t)
	ctx := context.Background()
	rows, err := s.Breakdown(ctx, q, "source", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range rows {
		got[r.Label] = r.Visitors
	}
	if len(got) != 3 || got[DirectSource] != 1 || got["google.com"] != 1 || got["reddit.com"] != 1 {
		t.Errorf("sources = %v", got)
	}
	for filter, want := range map[string]int64{"source|is|" + DirectSource: 1, "source|is_not|" + DirectSource: 2} {
		f, _ := ParseFilter(filter)
		q.Filters = []Filter{f}
		if sum, err := s.Summary(ctx, q); err != nil || sum.Visits != want {
			t.Errorf("%q: visits = %d, %v, want %d", filter, sum.Visits, err, want)
		}
	}
}

// Upgrading a version 4 database decodes stored paths, names the sources,
// derives the channels again and adds up each visit's visible time.
func TestMigrateV4(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	view(t, s, Event{SiteID: site.ID, Time: t0, Visitor: 1, Name: "pageview", Path: "/yaz%C4%B1l%C4%B1m",
		Visit: Visit{Referrer: "mail.google.com", Channel: "Organic Search"}})
	view(t, s, Event{SiteID: site.ID, Time: t0, Visitor: 2, Name: "pageview", Path: "/",
		Visit: Visit{Referrer: "google.com.tr", Channel: "Organic Search"}})
	view(t, s, Event{SiteID: site.ID, Time: t0, Visitor: 3, Name: "pageview", Path: "/",
		Visit: Visit{UTMSource: "newsletter", UTMMedium: "email", Channel: "Email"}})
	for _, q := range []string{`UPDATE sessions SET entry_path = '/yaz%C4%B1l%C4%B1m' WHERE visitor = 1`,
		`ALTER TABLE sessions DROP COLUMN engaged`, `DROP INDEX vitals_event`, `ALTER TABLE vitals DROP COLUMN event_id`,
		`PRAGMA user_version = 4`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := Query{SiteID: site.ID, From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}
	labels := func(dim string) map[string]bool {
		rows, err := s.Breakdown(ctx, q, dim, 10)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, r := range rows {
			out[r.Label] = true
		}
		return out
	}
	if got := labels("page"); !got["/yazılım"] || len(got) != 2 {
		t.Errorf("pages = %v", got)
	}
	if got := labels("entry_page"); !got["/yazılım"] {
		t.Errorf("entry pages = %v", got)
	}
	if got := labels("source"); !got["Gmail"] || !got["Google"] || !got["newsletter"] || len(got) != 3 {
		t.Errorf("sources = %v", got)
	}
	if got := labels("channel"); !got["Email"] || !got["Organic Search"] || len(got) != 2 {
		t.Errorf("channels = %v", got)
	}
	if sum, _ := s.Summary(ctx, q); sum.Visits != 3 || sum.AvgDuration != MinEngaged {
		t.Errorf("visits = %d, duration = %v, want 3 and %v", sum.Visits, sum.AvgDuration, MinEngaged)
	}
}

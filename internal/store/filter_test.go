package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestChannel(t *testing.T) {
	cases := []struct{ ref, src, med, want string }{
		{"", "", "", "Direct"},
		{"google.com", "", "", "Organic Search"},
		{"www.google.co.uk", "", "", "Organic Search"},
		{"", "google", "cpc", "Paid Search"},
		{"news.ycombinator.com", "", "", "Organic Social"},
		{"", "newsletter", "email", "Email"},
		{"youtube.com", "", "", "Organic Video"},
		{"blog.example.org", "", "", "Referral"},
		{"", "facebook", "paid-social", "Paid Social"},
	}
	for _, c := range cases {
		if got := Channel(c.ref, c.src, c.med); got != c.want {
			t.Errorf("Channel(%q, %q, %q) = %q, want %q", c.ref, c.src, c.med, got, c.want)
		}
	}
}

// fixture: three visits on one day.
//
//	v1: google, DE, / -> /pricing -> /signup, signup{plan: pro}, purchase 29
//	v2: direct, US, /blog/hello (bounce)
//	v3: reddit, DE, /pricing (bounce)
func fixture(t *testing.T) (*Store, Query) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.AddSite(ctx, "example.com")
	site, _ := s.SiteByDomain(ctx, "example.com")
	for _, g := range []Goal{{EventName: "signup"}, {EventName: "purchase", Currency: "EUR"}, {PagePath: "/pricing"}, {PagePath: "/sign*"}} {
		g.SiteID = site.ID
		if err := s.AddGoal(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	rec := func(visitor int64, min int, name, path string, v Visit, props map[string]string, rev float64) {
		t.Helper()
		e := Event{SiteID: site.ID, Time: t0.Add(time.Duration(min) * time.Minute), Visitor: visitor, Name: name, Path: path, Props: props, Visit: v}
		if rev != 0 {
			e.Revenue = &rev
		}
		if err := s.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	google := Visit{Referrer: "google.com", Channel: "Organic Search", Country: "DE"}
	rec(1, 0, "pageview", "/", google, nil, 0)
	rec(1, 1, "pageview", "/pricing", google, nil, 0)
	rec(1, 2, "pageview", "/signup", google, nil, 0)
	rec(1, 3, "signup", "/signup", google, map[string]string{"plan": "pro"}, 0)
	rec(1, 4, "purchase", "/signup", google, map[string]string{"plan": "pro"}, 29)
	rec(2, 0, "pageview", "/blog/hello", Visit{Channel: "Direct", Country: "US"}, nil, 0)
	rec(3, 0, "pageview", "/pricing", Visit{Referrer: "reddit.com", Channel: "Organic Social", Country: "DE"}, nil, 0)

	goals, _ := s.Goals(ctx, site.ID)
	return s, Query{SiteID: site.ID, From: t0.Add(-time.Hour), To: t0.Add(24 * time.Hour), Goals: goals}
}

func TestFilters(t *testing.T) {
	s, q := fixture(t)
	ctx := context.Background()
	cases := []struct {
		filter            string
		visits, pageviews int64
	}{
		{"", 3, 5},
		{"source|is|google.com", 1, 3},
		{"country|is|DE", 2, 4},
		{"country|is_not|DE", 1, 1},
		{"page|is|/pricing", 2, 2}, // visits that saw /pricing; only /pricing pageviews
		{"page|is_not|/pricing", 1, 1},
		{"page|contains|blog", 1, 1},
		{"goal|is|signup", 1, 3},
		{"goal|is|Visit /pricing", 2, 4},
		{"goal|is_not|signup", 2, 2},
		{"prop:plan|is|pro", 1, 3},
		{"channel|is|Organic Social", 1, 1},
	}
	for _, c := range cases {
		fq := q
		if c.filter != "" {
			f, ok := ParseFilter(c.filter)
			if !ok {
				t.Fatalf("ParseFilter(%q) failed", c.filter)
			}
			fq.Filters = []Filter{f}
		}
		sum, err := s.Summary(ctx, fq)
		if err != nil {
			t.Fatalf("%s: %v", c.filter, err)
		}
		if sum.Visits != c.visits || sum.Pageviews != c.pageviews {
			t.Errorf("%q: visits=%d pageviews=%d, want %d/%d", c.filter, sum.Visits, sum.Pageviews, c.visits, c.pageviews)
		}
	}
}

func TestEntryExitAndConversionRate(t *testing.T) {
	s, q := fixture(t)
	ctx := context.Background()
	exits, err := s.Breakdown(ctx, q, "exit_page", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range exits {
		got[r.Label] = r.Count
	}
	if got["/signup"] != 1 || got["/pricing"] != 1 || got["/blog/hello"] != 1 {
		t.Errorf("exit pages = %v", got)
	}

	f, _ := ParseFilter("goal|is|signup")
	q.Filters = []Filter{f}
	rows, err := s.Breakdown(ctx, q, "country", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "DE" || rows[0].CR == nil || *rows[0].CR != 50 {
		t.Errorf("country breakdown with goal filter = %+v", rows)
	}
}

func TestGoalsPropsFunnel(t *testing.T) {
	s, q := fixture(t)
	ctx := context.Background()
	stats, err := s.GoalStats(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]GoalStat{}
	for _, st := range stats {
		byName[st.Goal.Name()] = st
	}
	if st := byName["Visit /pricing"]; st.Visitors != 2 || int(st.CR) != 66 {
		t.Errorf("pricing goal = %+v", st)
	}
	if st := byName["purchase"]; st.Revenue != 29 || st.Visitors != 1 {
		t.Errorf("purchase goal = %+v", st)
	}
	if st := byName["Visit /sign*"]; st.Visitors != 1 {
		t.Errorf("wildcard goal = %+v", st)
	}

	keys, err := s.PropKeys(ctx, q)
	if err != nil || len(keys) != 1 || keys[0] != "plan" {
		t.Errorf("prop keys = %v, %v", keys, err)
	}
	vals, err := s.PropValues(ctx, q, "plan", 10)
	if err != nil || len(vals) != 1 || vals[0].Label != "pro" || vals[0].Count != 2 {
		t.Errorf("prop values = %+v, %v", vals, err)
	}

	id := map[string]int64{}
	for _, g := range q.Goals {
		id[g.Name()] = g.ID
	}
	steps, err := s.FunnelStats(ctx, q, Funnel{Steps: []int64{id["Visit /pricing"], id["signup"], id["purchase"]}})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 || steps[0].Visitors != 2 || steps[1].Visitors != 1 || steps[2].Visitors != 1 || steps[1].DropOff != 50 {
		t.Errorf("funnel = %+v", steps)
	}
	// order matters: purchase before pricing never happens
	steps, _ = s.FunnelStats(ctx, q, Funnel{Steps: []int64{id["purchase"], id["Visit /pricing"]}})
	if steps[1].Visitors != 0 {
		t.Errorf("reversed funnel = %+v", steps)
	}
}

func TestTimeseriesBuckets(t *testing.T) {
	s, q := fixture(t)
	for _, iv := range []Interval{Hour, Day, Week, Month} {
		series, err := s.Timeseries(context.Background(), q, iv)
		if err != nil {
			t.Fatal(err)
		}
		var visits, pageviews float64
		for i := range series.Times {
			visits += series.Values["visits"][i]
			pageviews += series.Values["pageviews"][i]
		}
		if visits != 3 || pageviews != 5 {
			t.Errorf("%s: visits=%v pageviews=%v", iv, visits, pageviews)
		}
	}
}

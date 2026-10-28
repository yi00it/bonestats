// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

func TestWindow(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	now := time.Date(2026, 3, 15, 14, 30, 0, 0, berlin)
	first := time.Date(2025, 11, 3, 22, 0, 0, 0, time.UTC) // Nov 3 23:00 in Berlin
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, berlin) }
	tomorrow := day(2026, 3, 16)
	for _, c := range []struct {
		key, from, to    string
		wantFrom, wantTo time.Time
		label            string
	}{
		{"today", "", "", day(2026, 3, 15), tomorrow, "Today"},
		{"yesterday", "", "", day(2026, 3, 14), day(2026, 3, 15), "Yesterday"},
		{"7d", "", "", day(2026, 3, 9), tomorrow, "Last 7 days"},
		{"28d", "", "", day(2026, 2, 16), tomorrow, "Last 28 days"},
		{"30d", "", "", day(2026, 2, 14), tomorrow, "Last 30 days"},
		{"91d", "", "", day(2025, 12, 15), tomorrow, "Last 91 days"},
		{"month", "", "", day(2026, 3, 1), tomorrow, "Month to date"},
		{"lastmonth", "", "", day(2026, 2, 1), day(2026, 3, 1), "Last month"},
		{"year", "", "", day(2026, 1, 1), tomorrow, "Year to date"},
		{"12mo", "", "", day(2025, 4, 1), tomorrow, "Last 12 months"},
		{"all", "", "", day(2025, 11, 3), tomorrow, "All time"},
		{"custom", "2026-01-05", "2026-01-07", day(2026, 1, 5), day(2026, 1, 8), "Jan 5, 2026 – Jan 7, 2026"},
		{"custom", "2026-01-05", "2026-01-05", day(2026, 1, 5), day(2026, 1, 6), "Jan 5, 2026 – Jan 5, 2026"},
		// Invalid input falls back to today.
		{"custom", "2026-01-07", "2026-01-05", day(2026, 3, 15), tomorrow, "Today"},
		{"custom", "2020-01-01", "2026-01-01", day(2026, 3, 15), tomorrow, "Today"},
		{"custom", "yesterday", "2026-01-01", day(2026, 3, 15), tomorrow, "Today"},
		{"custom", "", "", day(2026, 3, 15), tomorrow, "Today"},
		{"bogus", "", "", day(2026, 3, 15), tomorrow, "Today"},
		{"", "", "", day(2026, 3, 15), tomorrow, "Today"},
	} {
		from, to, label := window(c.key, c.from, c.to, now, first)
		if !from.Equal(c.wantFrom) || !to.Equal(c.wantTo) || label != c.label {
			t.Errorf("window(%q, %q, %q) = %v, %v, %q; want %v, %v, %q",
				c.key, c.from, c.to, from, to, label, c.wantFrom, c.wantTo, c.label)
		}
	}
}

// In December, "last 12 months" starts on January 1 of the same year.
func TestWindow12MonthsInDecember(t *testing.T) {
	now := time.Date(2026, 12, 10, 9, 0, 0, 0, time.UTC)
	from, _, _ := window("12mo", "", "", now, now)
	if want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC); !from.Equal(want) {
		t.Errorf("from = %v, want %v", from, want)
	}
}

func TestInterval(t *testing.T) {
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		days int
		want store.Interval
	}{
		{1, store.Hour}, {2, store.Hour}, {3, store.Day}, {30, store.Day},
		{91, store.Day}, {92, store.Day}, {93, store.Month}, {365, store.Month},
	} {
		if got := interval(d, d.AddDate(0, 0, c.days)); got != c.want {
			t.Errorf("%d days: interval %v, want %v", c.days, got, c.want)
		}
	}
}

func TestPrevious(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, berlin) }

	// March 29 is 23 hours long in Berlin; the period is still 7 days.
	from, to, label, ok := previous("", day(3, 26), day(4, 2))
	if !ok || !from.Equal(day(3, 19)) || !to.Equal(day(3, 26)) || label != "Previous period" {
		t.Errorf("previous period = %v, %v, %q, %v", from, to, label, ok)
	}
	from, to, label, ok = previous("year", day(3, 1), day(4, 1))
	if !ok || !from.Equal(day(3, 1).AddDate(-1, 0, 0)) || !to.Equal(day(4, 1).AddDate(-1, 0, 0)) || label != "Previous year" {
		t.Errorf("previous year = %v, %v, %q, %v", from, to, label, ok)
	}
	if _, _, _, ok := previous("off", day(3, 1), day(4, 1)); ok {
		t.Error("compare=off still compares")
	}
}

func TestChange(t *testing.T) {
	for _, c := range []struct {
		key        string
		cur, prev  float64
		text, mood string
	}{
		{"visitors", 150, 100, "↑ 50%", "good"},
		{"visitors", 50, 100, "↓ 50%", "bad"},
		{"visitors", 100, 100, "0%", ""},
		{"visitors", 1001, 1000, "0%", ""}, // rounds to zero
		{"visitors", 5, 0, "new", "good"},
		{"visitors", 0, 0, "0%", ""},
		{"visit_duration", 30, 60, "↓ 50%", "bad"},
		{"bounce_rate", 40, 50, "↓ 10 pp", "good"},
		{"bounce_rate", 55, 50, "↑ 5 pp", "bad"},
		{"bounce_rate", 50, 50, "0 pp", ""},
		{"bounce_rate", 30, 0, "↑ 30 pp", "bad"},
	} {
		text, mood := change(c.key, c.cur, c.prev)
		if text != c.text || mood != c.mood {
			t.Errorf("change(%s, %v, %v) = %q, %q; want %q, %q", c.key, c.cur, c.prev, text, mood, c.text, c.mood)
		}
	}
}

func TestFormatMetric(t *testing.T) {
	for _, c := range []struct {
		kind string
		v    float64
		want string
	}{
		{"number", 42, "42"}, {"number", 12345, "12.3k"}, {"number", 2500000, "2.5M"},
		{"decimal", 2.345, "2.3"}, {"percent", 66.6, "67%"},
		{"duration", 45, "45s"}, {"duration", 125, "2m 05s"}, {"unknown", 7, "7"},
	} {
		if got := formatMetric(c.kind, c.v); got != c.want {
			t.Errorf("formatMetric(%s, %v) = %q, want %q", c.kind, c.v, got, c.want)
		}
	}
	if metricLabel("bounce_rate") != "Bounce rate" || metricLabel("nope") != "nope" {
		t.Error("metricLabel")
	}
	if metricKind("visit_duration") != "duration" || metricKind("nope") != "number" {
		t.Error("metricKind")
	}
}

func TestMoney(t *testing.T) {
	for _, c := range []struct {
		amount   float64
		currency string
		want     string
	}{
		{29.9, "USD", "$29.90"},
		{1234567.5, "EUR", "€1,234,567.50"},
		{-12, "GBP", "-£12.00"},
		{999, "CHF", "CHF 999.00"},
		{1000, "SEK", "1,000.00 SEK"},
		{0, "", "0.00 "},
	} {
		if got := money(c.amount, c.currency); got != c.want {
			t.Errorf("money(%v, %q) = %q, want %q", c.amount, c.currency, got, c.want)
		}
	}
}

func TestVitalPos(t *testing.T) {
	near := func(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }
	for _, c := range []struct {
		key  string
		v    float64
		want float64
	}{
		{"lcp", 0, 0},
		{"lcp", 1250, 100.0 / 6},
		{"lcp", 2500, 100.0 / 3},
		{"lcp", 3250, 50},
		{"lcp", 4000, 200.0 / 3},
		{"lcp", 8000, 100},
		{"lcp", 1e9, 100}, // capped
		{"cls", 0.1, 100.0 / 3},
		{"cls", 0.25, 200.0 / 3},
	} {
		if got := vitalPos(c.key, c.v); !near(got, c.want) {
			t.Errorf("vitalPos(%s, %v) = %.2f, want %.2f", c.key, c.v, got, c.want)
		}
	}
}

func TestNav(t *testing.T) {
	v := url.Values{"site": {"example.com"}, "range": {"7d"}, "f": {"country|is|DE", "page|contains|/blog"}}
	n := nav{v}
	parse := func(link string) url.Values {
		t.Helper()
		if !strings.HasPrefix(link, "/?") {
			t.Fatalf("link %q does not start with /?", link)
		}
		q, err := url.ParseQuery(link[2:])
		if err != nil {
			t.Fatal(err)
		}
		return q
	}

	if q := parse(n.Set("range", "30d")); q.Get("range") != "30d" || q.Get("site") != "example.com" || len(q["f"]) != 2 {
		t.Errorf("Set: %v", q)
	}
	if q := parse(n.Set("range", "")); q.Has("range") {
		t.Errorf("Set empty kept the key: %v", q)
	}
	q := parse(n.AddFilter("country", "FR"))
	if got := strings.Join(q["f"], ","); got != "page|contains|/blog,country|is|FR" {
		t.Errorf("AddFilter replaced to %q", got)
	}
	if q := parse(n.AddFilter("browser", "Firefox")); len(q["f"]) != 3 {
		t.Errorf("AddFilter on a new dimension: %v", q["f"])
	}
	if q := parse(n.RemoveFilter(0)); strings.Join(q["f"], ",") != "page|contains|/blog" {
		t.Errorf("RemoveFilter(0): %v", q["f"])
	}
	if q := parse(n.RemoveFilter(5)); len(q["f"]) != 2 {
		t.Errorf("RemoveFilter out of range: %v", q["f"])
	}
	// None of the above may change the current state.
	if len(v["f"]) != 2 || v.Get("range") != "7d" || v["f"][0] != "country|is|DE" {
		t.Errorf("nav mutated its values: %v", v)
	}

	got := map[string]int{}
	for _, h := range n.Hidden("range", "site") {
		got[h.Name]++
	}
	if got["f"] != 2 || got["range"] != 0 || got["site"] != 0 || len(got) != 1 {
		t.Errorf("Hidden: %v", got)
	}
}

// ---- handler ----

func dashboardClient(t *testing.T) (*Server, func(path string) *httptest.ResponseRecorder) {
	t.Helper()
	s, h := testServer(t)
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	return s, func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}

func addSite(t *testing.T, s *Server, domain string) store.Site {
	t.Helper()
	ctx := context.Background()
	if err := s.Store.AddSite(ctx, domain); err != nil {
		t.Fatal(err)
	}
	site, err := s.Store.SiteByDomain(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}
	return site
}

func record(t *testing.T, s *Server, e store.Event) {
	t.Helper()
	if err := s.Store.RecordEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func chartData(t *testing.T, body string) map[string]any {
	t.Helper()
	const open = `<script type="application/json" id="chart-data">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("no chart data in page")
	}
	raw := body[i+len(open):]
	raw = raw[:strings.Index(raw, "</script>")]
	var chart map[string]any
	if err := json.Unmarshal([]byte(raw), &chart); err != nil {
		t.Fatalf("chart data %q: %v", raw, err)
	}
	return chart
}

func TestDashboardWithoutSites(t *testing.T) {
	_, get := dashboardClient(t)
	w := get("/")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/sites" {
		t.Errorf("status %d, location %q", w.Code, w.Header().Get("Location"))
	}
}

func TestDashboardNeedsLogin(t *testing.T) {
	s, h := testServer(t)
	addSite(t, s, "example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code == http.StatusOK {
		t.Error("dashboard served without a session")
	}
}

// The "+ Filter" form submits fdim/fop/fval, which become one f parameter.
func TestDashboardFilterForm(t *testing.T) {
	s, get := dashboardClient(t)
	addSite(t, s, "example.com")

	w := get("/?site=example.com&range=7d&fdim=country&fop=is_not&fval=+DE+")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if q.Get("f") != "country|is_not|DE" || q.Has("fdim") || q.Has("fop") || q.Has("fval") || q.Get("range") != "7d" {
		t.Errorf("redirected to %s", loc)
	}

	// An invalid filter is dropped, but the form fields still go away.
	w = get("/?fdim=nope&fop=is&fval=x")
	loc, _ = url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Query().Has("f") || loc.Query().Has("fdim") {
		t.Errorf("invalid filter: status %d, location %s", w.Code, loc)
	}
}

func TestDashboard(t *testing.T) {
	s, get := dashboardClient(t)
	ctx := context.Background()
	addSite(t, s, "a.example")
	site := addSite(t, s, "b.example")

	for _, g := range []store.Goal{{SiteID: site.ID, EventName: "signup"}, {SiteID: site.ID, PagePath: "/pricing"}} {
		if err := s.Store.AddGoal(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	goals, _ := s.Store.Goals(ctx, site.ID)
	if err := s.Store.AddFunnel(ctx, store.Funnel{SiteID: site.ID, Name: "Pricing to signup", Steps: []int64{goals[1].ID, goals[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.AddFunnel(ctx, store.Funnel{SiteID: site.ID, Name: "Signup only", Steps: []int64{goals[0].ID, goals[0].ID}}); err != nil {
		t.Fatal(err)
	}
	funnels, _ := s.Store.Funnels(ctx, site.ID)

	now := time.Now()
	de := store.Visit{Country: "DE", Browser: "Firefox", Channel: "Direct"}
	fr := store.Visit{Country: "FR", Browser: "Chrome", Channel: "Direct"}
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-3 * time.Minute), Visitor: 1, Name: "pageview", Path: "/", Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-2 * time.Minute), Visitor: 1, Name: "pageview", Path: "/pricing", Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-time.Minute), Visitor: 1, Name: "signup", Path: "/pricing",
		Props: map[string]string{"plan": "pro"}, Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-time.Minute), Visitor: 2, Name: "pageview", Path: "/<b>blog</b>", Visit: fr})

	w := get("/?site=b.example")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"/pricing", "Firefox", "Pricing to signup", "Signup only", "signup", "pro", "Visit /pricing"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "<b>blog</b>") {
		t.Error("page path rendered as HTML")
	}

	chart := chartData(t, body)
	if chart["kind"] != "number" || chart["label"] != "Unique visitors" || chart["prevLabel"] != "Previous period" {
		t.Errorf("chart = %v", chart)
	}
	cur, _ := chart["cur"].([]any)
	prev, _ := chart["prev"].([]any)
	times, _ := chart["t"].([]any)
	if len(cur) != 30 || len(prev) != len(cur) || len(times) != len(cur) {
		t.Errorf("30 days: %d values, %d previous, %d times", len(cur), len(prev), len(times))
	}
	total := 0.0
	for _, v := range cur {
		total += v.(float64)
	}
	if total != 2 {
		t.Errorf("chart counts %v visitors, want 2", total)
	}

	// The KPI and metric choice.
	w = get("/?site=b.example&range=today&metric=pageviews&compare=year")
	chart = chartData(t, w.Body.String())
	if chart["label"] != "Pageviews" || chart["prevLabel"] != "Previous year" || chart["interval"] != string(store.Hour) {
		t.Errorf("today/pageviews chart = %v", chart)
	}
	if chart := chartData(t, get("/?site=b.example&metric=nope").Body.String()); chart["label"] != "Unique visitors" {
		t.Errorf("unknown metric charted %v", chart["label"])
	}

	// All time and compare=off show no comparison.
	for _, path := range []string{"/?site=b.example&range=all&compare=year", "/?site=b.example&compare=off"} {
		if chart := chartData(t, get(path).Body.String()); chart["prev"] != nil {
			t.Errorf("%s compares with %v", path, chart["prevLabel"])
		}
	}

	// A filter narrows the numbers and shows a chip.
	body = get("/?site=b.example&f=" + url.QueryEscape("country|is|FR")).Body.String()
	if !strings.Contains(body, `data-country="FR"`) {
		t.Error("no chip for the country filter")
	}
	if strings.Contains(body, "Firefox") {
		t.Error("filtered page still shows the DE visitor's browser")
	}
	// Invalid filters are ignored.
	if w := get("/?site=b.example&f=bogus"); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `class="chip"`) {
		t.Errorf("invalid filter: status %d", w.Code)
	}

	// The selected funnel is marked active.
	body = get("/?site=b.example&funnel=" + strconv.FormatInt(funnels[1].ID, 10)).Body.String()
	if !strings.Contains(body, `class="active">Signup only</a>`) {
		t.Error("requested funnel not active")
	}

	// An unknown site falls back to the first one.
	body = get("/?site=nope.example").Body.String()
	if !strings.Contains(body, "<title>a.example") {
		t.Error("unknown site did not fall back to the first site")
	}
	if strings.Contains(body, "Pricing to signup") {
		t.Error("fallback site shows another site's funnel")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/store"
)

// ---- links that keep the dashboard state ----

// nav builds dashboard URLs that keep the current site, range, filters, etc.
type nav struct{ v url.Values }

func (n nav) with(mod func(url.Values)) string {
	v := url.Values{}
	for k, vs := range n.v {
		v[k] = append([]string{}, vs...)
	}
	mod(v)
	return "/?" + v.Encode()
}

// Set returns the current URL with one parameter replaced ("" removes it).
func (n nav) Set(key, value string) string {
	return n.with(func(v url.Values) {
		if value == "" {
			v.Del(key)
		} else {
			v.Set(key, value)
		}
	})
}

// AddFilter adds "dim is value", replacing other filters on the same dimension.
func (n nav) AddFilter(dim, value string) string {
	return n.with(func(v url.Values) {
		var keep []string
		for _, f := range v["f"] {
			if pf, ok := store.ParseFilter(f); ok && pf.Dim != dim {
				keep = append(keep, f)
			}
		}
		v["f"] = append(keep, store.Filter{Dim: dim, Op: store.OpIs, Value: value}.String())
	})
}

// RemoveFilter drops the i-th filter.
func (n nav) RemoveFilter(i int) string {
	return n.with(func(v url.Values) {
		fs := v["f"]
		if i < len(fs) {
			v["f"] = append(append([]string{}, fs[:i]...), fs[i+1:]...)
		}
	})
}

type hidden struct{ Name, Value string }

// Hidden lists the current state as form fields, minus the given keys, so
// GET forms (range picker, filter form) keep everything else.
func (n nav) Hidden(skip ...string) []hidden {
	var out []hidden
	for k, vs := range n.v {
		skipped := false
		for _, s := range skip {
			skipped = skipped || s == k
		}
		if skipped {
			continue
		}
		for _, v := range vs {
			out = append(out, hidden{k, v})
		}
	}
	return out
}

// ---- date ranges ----

type rangeOpt struct{ Key, Label string }

var ranges = []rangeOpt{
	{"today", "Today"}, {"yesterday", "Yesterday"}, {"7d", "Last 7 days"}, {"28d", "Last 28 days"},
	{"30d", "Last 30 days"}, {"91d", "Last 91 days"}, {"month", "Month to date"}, {"lastmonth", "Last month"},
	{"year", "Year to date"}, {"12mo", "Last 12 months"}, {"all", "All time"}, {"custom", "Custom range"},
}

const dateLayout = "2006-01-02"

// window resolves the range parameters into [from, to) aligned to local
// midnight. For "custom", fromS and toS are inclusive dates.
func window(key, fromS, toS string, now, firstVisit time.Time) (from, to time.Time, label string) {
	loc := now.Location()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	tomorrow := midnight.AddDate(0, 0, 1)
	days := func(n int) time.Time { return midnight.AddDate(0, 0, -(n - 1)) }
	for _, r := range ranges {
		if r.Key == key {
			label = r.Label
		}
	}
	switch key {
	case "yesterday":
		return midnight.AddDate(0, 0, -1), midnight, label
	case "7d":
		return days(7), tomorrow, label
	case "28d":
		return days(28), tomorrow, label
	case "30d":
		return days(30), tomorrow, label
	case "91d":
		return days(91), tomorrow, label
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc), tomorrow, label
	case "lastmonth":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return first.AddDate(0, -1, 0), first, label
	case "year":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, loc), tomorrow, label
	case "12mo":
		return time.Date(now.Year()-1, now.Month()+1, 1, 0, 0, 0, 0, loc), tomorrow, label
	case "all":
		f := firstVisit.In(loc)
		return time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc), tomorrow, label
	case "custom":
		f, err1 := time.ParseInLocation(dateLayout, fromS, loc)
		t, err2 := time.ParseInLocation(dateLayout, toS, loc)
		if err1 == nil && err2 == nil && !t.Before(f) && t.Sub(f) < 5*366*24*time.Hour {
			return f, t.AddDate(0, 0, 1), f.Format("Jan 2, 2006") + " – " + t.Format("Jan 2, 2006")
		}
	}
	return midnight, tomorrow, "Today"
}

func interval(from, to time.Time) store.Interval {
	switch d := to.Sub(from); {
	case d <= 49*time.Hour:
		return store.Hour
	case d <= 92*24*time.Hour:
		return store.Day
	default:
		return store.Month
	}
}

// previous returns the comparison window for a compare mode.
func previous(mode string, from, to time.Time) (time.Time, time.Time, string, bool) {
	switch mode {
	case "year":
		return from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0), "Previous year", true
	case "off":
		return time.Time{}, time.Time{}, "", false
	default:
		days := int(math.Round(to.Sub(from).Hours() / 24))
		return from.AddDate(0, 0, -days), from, "Previous period", true
	}
}

// ---- view models ----

type card struct {
	Title, Dim, Head string // Head names the count column
	Rows             []store.Row
	Pages            bool // show time on page and scroll depth
	HasCR            bool
}

type tabs struct {
	Name, Title string
	Cards       []card
}

type kpi struct {
	Key, Label, Value string
	Change, Trend     string // Trend: "good", "bad" or ""
	Link              string
	Active            bool
}

type metricMeta struct{ Key, Label, Kind string }

var metricInfo = []metricMeta{
	{"visitors", "Unique visitors", "number"}, {"visits", "Visits", "number"},
	{"pageviews", "Pageviews", "number"}, {"views_per_visit", "Views / visit", "decimal"},
	{"bounce_rate", "Bounce rate", "percent"}, {"visit_duration", "Visit duration", "duration"},
}

type chip struct {
	Key, Dim, Op, Value, Remove string
}

var opLabels = map[string]string{store.OpIs: "is", store.OpIsNot: "is not", store.OpContains: "contains"}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	qs := r.URL.Query()

	// The "add filter" form submits fdim/fop/fval; fold them into f and redirect.
	if dim := qs.Get("fdim"); dim != "" {
		if f, ok := store.ParseFilter(dim + "|" + qs.Get("fop") + "|" + strings.TrimSpace(qs.Get("fval"))); ok {
			qs.Add("f", f.String())
		}
		qs.Del("fdim")
		qs.Del("fop")
		qs.Del("fval")
		http.Redirect(w, r, "/?"+qs.Encode(), http.StatusSeeOther)
		return
	}

	sites, err := s.Store.Sites(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	if len(sites) == 0 {
		http.Redirect(w, r, "/sites", http.StatusSeeOther)
		return
	}
	site := sites[0]
	for _, st := range sites {
		if st.Domain == qs.Get("site") {
			site = st
		}
	}
	qs.Set("site", site.Domain)
	if qs.Get("range") == "" {
		qs.Set("range", "30d")
	}
	n := nav{qs}

	goals, err := s.Store.Goals(ctx, site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	funnels, err := s.Store.Funnels(ctx, site.ID)
	if err != nil {
		serverError(w, err)
		return
	}

	now := time.Now().In(s.Location)
	first, ok := s.Store.FirstVisit(ctx, site.ID)
	if !ok {
		first = now
	}
	from, to, label := window(qs.Get("range"), qs.Get("from"), qs.Get("to"), now, first)
	iv := interval(from, to)

	var filters []store.Filter
	var chips []chip
	for i, raw := range qs["f"] {
		f, ok := store.ParseFilter(raw)
		if !ok {
			continue
		}
		filters = append(filters, f)
		d, _ := store.DimensionByKey(f.Dim)
		chips = append(chips, chip{f.Dim, d.Label, opLabels[f.Op], f.Value, n.RemoveFilter(i)})
	}
	q := store.Query{SiteID: site.ID, From: from, To: to, Location: s.Location, Filters: filters, Goals: goals}

	d := &dashData{ctx: ctx, s: s, q: q, n: n}

	// KPIs, compared with the previous period up to the same point in time.
	summary := d.summary(q)
	compare := qs.Get("compare")
	if qs.Get("range") == "all" {
		compare = "off" // nothing exists before the first visit
	}
	pFrom, pTo, prevLabel, comparing := previous(compare, from, to)
	var prevSummary store.Summary
	pq := q
	if comparing {
		pq.From, pq.To = pFrom, pTo
		kq := pq
		if to.After(now) {
			kq.To = pFrom.Add(now.Sub(from))
		}
		prevSummary = d.summary(kq)
	}
	metric := qs.Get("metric")
	if !contains(store.Metrics, metric) {
		metric = "visitors"
	}
	var kpis []kpi
	for _, m := range metricInfo {
		k := kpi{Key: m.Key, Label: m.Label, Value: formatMetric(m.Kind, summary.Metric(m.Key)),
			Link: n.Set("metric", m.Key), Active: m.Key == metric}
		if comparing {
			k.Change, k.Trend = change(m.Key, summary.Metric(m.Key), prevSummary.Metric(m.Key))
		}
		kpis = append(kpis, k)
	}

	// Chart: the selected metric, plus the comparison period.
	series := d.series(q, iv)
	chart := map[string]any{"t": series.Times, "cur": series.Values[metric], "interval": iv,
		"label": metricLabel(metric), "kind": metricKind(metric)}
	if comparing {
		ps := d.series(pq, iv)
		prev := make([]float64, len(series.Times))
		for i := range prev {
			if i < len(ps.Values[metric]) {
				prev[i] = ps.Values[metric][i]
			}
		}
		chart["prev"], chart["prevLabel"] = prev, prevLabel
	}
	chartJSON, _ := json.Marshal(chart)
	spark := map[string][]float64{}
	for _, m := range metricInfo {
		spark[m.Key] = series.Values[m.Key]
	}
	sparkJSON, _ := json.Marshal(spark)

	// Breakdowns.
	pages := tabs{"pages", "Pages", []card{
		d.card("Top pages", "page", "Views", 15),
		d.card("Entry pages", "entry_page", "Entrances", 15),
		d.card("Exit pages", "exit_page", "Exits", 15),
	}}
	pages.Cards[0].Pages = true
	sources := tabs{"sources", "Sources", []card{
		d.card("Channels", "channel", "Visits", 15),
		d.card("Sources", "source", "Visits", 15),
		d.card("Referrer URLs", "referrer", "Visits", 15),
	}}
	campaigns := tabs{"utm", "Campaigns", []card{
		d.card("UTM source", "utm_source", "Visits", 10), d.card("UTM medium", "utm_medium", "Visits", 10),
		d.card("UTM campaign", "utm_campaign", "Visits", 10), d.card("UTM term", "utm_term", "Visits", 10),
		d.card("UTM content", "utm_content", "Visits", 10),
	}}
	locations := tabs{"geo", "Locations", []card{
		d.card("Countries", "country", "Visits", 15), d.card("Regions", "region", "Visits", 15),
		d.card("Cities", "city", "Visits", 15),
	}}
	tech := tabs{"tech", "Devices", []card{
		d.card("Browser", "browser", "Visits", 10), d.card("Browser version", "browser_version", "Visits", 10),
		d.card("OS", "os", "Visits", 10), d.card("OS version", "os_version", "Visits", 10),
		d.card("Device", "device", "Visits", 10), d.card("Screen size", "screen", "Visits", 10),
	}}
	mapRows := d.rows(s.Store.Breakdown(ctx, q, "country", 300))
	mapData := map[string]int64{}
	for _, row := range mapRows {
		mapData[row.Label] = row.Visitors
	}
	mapJSON, _ := json.Marshal(mapData)

	// Goals, events, properties, funnels.
	goalStats, err := s.Store.GoalStats(ctx, q)
	d.check(err)
	for i := range goalStats {
		goalStats[i].Link = n.AddFilter("goal", goalStats[i].Goal.Name())
	}
	events := d.rows(s.Store.CustomEvents(ctx, q, 20))
	for i := range events {
		events[i].Link = n.AddFilter("goal", events[i].Label)
	}
	propKeys, err := s.Store.PropKeys(ctx, q)
	d.check(err)
	propKey := qs.Get("prop")
	if len(propKeys) > 0 && !contains(propKeys, propKey) {
		propKey = propKeys[0]
	}
	var propRows []store.Row
	if propKey != "" {
		propRows = d.rows(s.Store.PropValues(ctx, q, propKey, 15))
		for i := range propRows {
			propRows[i].Link = n.AddFilter("prop:"+propKey, propRows[i].Label)
		}
	}
	type propTab struct {
		Key, Link string
		Active    bool
	}
	var propTabs []propTab
	for _, k := range propKeys {
		propTabs = append(propTabs, propTab{k, n.Set("prop", k), k == propKey})
	}
	var funnel *store.Funnel
	type funnelTab struct {
		Name, Link string
		Active     bool
	}
	var funnelTabs []funnelTab
	for i := range funnels {
		f := &funnels[i]
		if funnel == nil || strconv.FormatInt(f.ID, 10) == qs.Get("funnel") {
			funnel = f
		}
	}
	for _, f := range funnels {
		funnelTabs = append(funnelTabs, funnelTab{f.Name, n.Set("funnel", strconv.FormatInt(f.ID, 10)), funnel.ID == f.ID})
	}
	var funnelSteps []store.FunnelStep
	if funnel != nil {
		funnelSteps, err = s.Store.FunnelStats(ctx, q, *funnel)
		d.check(err)
	}

	current, err := s.Store.CurrentVisitors(ctx, site.ID)
	d.check(err)
	vitals, err := s.Store.VitalsSummary(ctx, q)
	d.check(err)
	vitalPages, err := s.Store.VitalsByPage(ctx, q, 10)
	d.check(err)
	if d.err != nil {
		serverError(w, d.err)
		return
	}

	goalFilter, hasGoalFilter := q.GoalFilter()
	currency := ""
	if hasGoalFilter {
		currency = goalFilter.Currency
	}
	hasRevenue := false
	for _, g := range goals {
		hasRevenue = hasRevenue || g.Currency != ""
	}

	s.render(w, "dashboard", map[string]any{
		"Title":       site.Domain,
		"Nav":         n,
		"Sites":       sites,
		"Site":        site,
		"Ranges":      ranges,
		"Range":       qs.Get("range"),
		"RangeLabel":  label,
		"From":        from.Format(dateLayout),
		"To":          to.AddDate(0, 0, -1).Format(dateLayout),
		"Compare":     compare,
		"Chips":       chips,
		"Dimensions":  store.Dimensions,
		"KPIs":        kpis,
		"Current":     current,
		"Chart":       template.JS(chartJSON), // json.Marshal escapes <, > and &
		"Spark":       template.JS(sparkJSON),
		"Map":         template.JS(mapJSON),
		"Pages":       pages,
		"Sources":     sources,
		"Campaigns":   campaigns,
		"Locations":   locations,
		"Tech":        tech,
		"Goals":       goalStats,
		"HasRevenue":  hasRevenue,
		"Events":      events,
		"PropKey":     propKey,
		"PropTabs":    propTabs,
		"PropRows":    propRows,
		"Currency":    currency,
		"Funnel":      funnel,
		"FunnelTabs":  funnelTabs,
		"FunnelSteps": funnelSteps,
		"Vitals":      vitals,
		"VitalPages":  vitalPages,
		"VitalInfo":   vitalInfo,
		"GeoEnabled":  s.GeoEnabled,
		"MultiDay":    to.Sub(from) > 24*time.Hour,
	})
}

// dashData collects the first error so the handler can run many queries
// without checking each one.
type dashData struct {
	ctx context.Context
	s   *Server
	q   store.Query
	n   nav
	err error
}

func (d *dashData) check(err error) {
	if d.err == nil {
		d.err = err
	}
}

func (d *dashData) rows(rows []store.Row, err error) []store.Row {
	d.check(err)
	return rows
}

func (d *dashData) summary(q store.Query) store.Summary {
	s, err := d.s.Store.Summary(d.ctx, q)
	d.check(err)
	return s
}

func (d *dashData) series(q store.Query, iv store.Interval) store.Series {
	s, err := d.s.Store.Timeseries(d.ctx, q, iv)
	d.check(err)
	return s
}

func (d *dashData) card(title, dim, head string, limit int) card {
	c := card{Title: title, Dim: dim, Head: head, Rows: d.rows(d.s.Store.Breakdown(d.ctx, d.q, dim, limit))}
	for i := range c.Rows {
		c.Rows[i].Link = d.n.AddFilter(dim, c.Rows[i].Label)
		c.HasCR = c.HasCR || c.Rows[i].CR != nil
	}
	return c
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func metricLabel(key string) string {
	for _, m := range metricInfo {
		if m.Key == key {
			return m.Label
		}
	}
	return key
}

func metricKind(key string) string {
	for _, m := range metricInfo {
		if m.Key == key {
			return m.Kind
		}
	}
	return "number"
}

func formatMetric(kind string, v float64) string {
	switch kind {
	case "decimal":
		return strconv.FormatFloat(v, 'f', 1, 64)
	case "percent":
		return strconv.FormatFloat(v, 'f', 0, 64) + "%"
	case "duration":
		return formatDur(time.Duration(v) * time.Second)
	default:
		return formatNum(int64(v))
	}
}

// change formats the difference to the comparison period. Bounce rate is
// compared in percentage points and going down is good.
func change(key string, cur, prev float64) (string, string) {
	var diff float64
	unit := "%"
	if key == "bounce_rate" {
		diff, unit = cur-prev, " pp"
	} else {
		if prev == 0 {
			if cur == 0 {
				return "0%", ""
			}
			return "new", "good"
		}
		diff = (cur - prev) / prev * 100
	}
	r := math.Round(diff)
	if r == 0 {
		return "0" + unit, ""
	}
	good := r > 0
	if key == "bounce_rate" {
		good = !good
	}
	trend := "bad"
	if good {
		trend = "good"
	}
	arrow := "↑"
	if r < 0 {
		arrow = "↓"
	}
	return fmt.Sprintf("%s %.0f%s", arrow, math.Abs(r), unit), trend
}

// money formats an amount with its currency symbol where it is well known.
func money(amount float64, currency string) string {
	sym := map[string]string{"EUR": "€", "USD": "$", "GBP": "£", "TRY": "₺", "JPY": "¥", "CHF": "CHF "}[currency]
	n := strconv.FormatFloat(math.Abs(amount), 'f', 2, 64)
	// thousands separators
	intPart, frac, _ := strings.Cut(n, ".")
	for i := len(intPart) - 3; i > 0; i -= 3 {
		intPart = intPart[:i] + "," + intPart[i:]
	}
	sign := ""
	if amount < 0 {
		sign = "-"
	}
	if sym == "" {
		return sign + intPart + "." + frac + " " + currency
	}
	return sign + sym + intPart + "." + frac
}

// ---- web vitals display ----

type vitalMeta struct {
	Key, Name, Desc string
	Good, Poor      float64 // thresholds from web.dev
	Unitless        bool
}

var vitalInfo = []vitalMeta{
	{"lcp", "LCP", "Largest Contentful Paint: loading speed", 2500, 4000, false},
	{"inp", "INP", "Interaction to Next Paint: responsiveness", 200, 500, false},
	{"cls", "CLS", "Cumulative Layout Shift: visual stability", 0.1, 0.25, true},
	{"fcp", "FCP", "First Contentful Paint", 1800, 3000, false},
	{"ttfb", "TTFB", "Time to First Byte: server response", 800, 1800, false},
}

// vitalPos places a value on a gauge whose thirds are good, needs
// improvement and poor, for the marker on each web vitals tile.
func vitalPos(key string, v float64) float64 {
	m := metaFor(key)
	switch {
	case v <= m.Good:
		return v / m.Good * 100 / 3
	case v <= m.Poor:
		return 100.0/3 + (v-m.Good)/(m.Poor-m.Good)*100/3
	default:
		return 200.0/3 + math.Min(1, (v-m.Poor)/m.Poor)*100/3
	}
}

func metaFor(key string) vitalMeta {
	for _, m := range vitalInfo {
		if m.Key == key {
			return m
		}
	}
	return vitalMeta{}
}

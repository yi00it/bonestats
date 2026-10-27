package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Summary struct {
	Visitors      int64
	Visits        int64
	Pageviews     int64
	BounceRate    float64 // 0..100
	AvgDuration   time.Duration
	ViewsPerVisit float64
}

// Metric returns a summary value by metric key (see Metrics).
func (s Summary) Metric(key string) float64 {
	switch key {
	case "visits":
		return float64(s.Visits)
	case "pageviews":
		return float64(s.Pageviews)
	case "views_per_visit":
		return s.ViewsPerVisit
	case "bounce_rate":
		return s.BounceRate
	case "visit_duration":
		return s.AvgDuration.Seconds()
	default:
		return float64(s.Visitors)
	}
}

// Metrics that can be charted, in display order.
var Metrics = []string{"visitors", "visits", "pageviews", "views_per_visit", "bounce_rate", "visit_duration"}

func (s *Store) Summary(ctx context.Context, q Query) (Summary, error) {
	var sum Summary
	from, args := q.sessionScope()
	var bounces int64
	var dur sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT s.visitor), COUNT(*), COALESCE(SUM(s.pageviews = 1), 0), AVG(s.last_seen - s.started_at) `+from,
		args...).Scan(&sum.Visitors, &sum.Visits, &bounces, &dur)
	if err != nil {
		return sum, err
	}
	from, args = q.eventScope()
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) `+from+` AND e.name = 'pageview'`, args...).Scan(&sum.Pageviews); err != nil {
		return sum, err
	}
	if sum.Visits > 0 {
		sum.BounceRate = float64(bounces) * 100 / float64(sum.Visits)
		sum.ViewsPerVisit = float64(sum.Pageviews) / float64(sum.Visits)
	}
	sum.AvgDuration = time.Duration(dur.Float64) * time.Second
	return sum, nil
}

// CurrentVisitors counts visitors seen in the last five minutes.
func (s *Store) CurrentVisitors(ctx context.Context, siteID int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT visitor) FROM sessions WHERE site_id = ? AND last_seen >= ?`,
		siteID, time.Now().Add(-5*time.Minute).Unix()).Scan(&n)
	return n, err
}

// ---- time series ----

type Interval string

const (
	Hour  Interval = "hour"
	Day   Interval = "day"
	Week  Interval = "week"
	Month Interval = "month"
)

// starts returns the start of every bucket overlapping [from, to). Buckets
// follow the local calendar of loc, so days stay aligned across DST changes.
func (iv Interval) starts(from, to time.Time, loc *time.Location) []time.Time {
	if loc == nil {
		loc = time.UTC
	}
	f := from.In(loc)
	midnight := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
	var t time.Time
	var next func(time.Time) time.Time
	switch iv {
	case Hour:
		t = time.Date(f.Year(), f.Month(), f.Day(), f.Hour(), 0, 0, 0, loc)
		next = func(t time.Time) time.Time { return t.Add(time.Hour) }
	case Week: // weeks start on Monday
		t = midnight.AddDate(0, 0, -((int(midnight.Weekday()) + 6) % 7))
		next = func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	case Month:
		t = time.Date(f.Year(), f.Month(), 1, 0, 0, 0, 0, loc)
		next = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
	default:
		t = midnight
		next = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	}
	var out []time.Time
	for ; t.Before(to); t = next(t) {
		out = append(out, t)
	}
	return out
}

// bucketTable is the CTE b(k, lo, hi) listing the buckets as [lo, hi) unix
// ranges; k is the bucket's index in starts.
const bucketTable = `WITH b AS (SELECT CAST(key AS INTEGER) AS k, json_extract(value, '$[0]') AS lo,
	json_extract(value, '$[1]') AS hi FROM json_each(?)) `

func bucketRanges(starts []time.Time, to time.Time) string {
	r := make([][2]int64, len(starts))
	for i, t := range starts {
		end := to
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		r[i] = [2]int64{t.Unix(), end.Unix()}
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// Series holds chart data: bucket start times and one value list per metric.
type Series struct {
	Times  []int64
	Values map[string][]float64
}

func (s *Store) Timeseries(ctx context.Context, q Query, iv Interval) (Series, error) {
	type agg struct{ visitors, visits, bounces, dur, pageviews float64 }
	starts := iv.starts(q.From, q.To, q.Location)
	got := make([]agg, len(starts))
	ranges := bucketRanges(starts, q.To)

	from, args := q.sessionScope()
	from = strings.Replace(from, "FROM sessions s WHERE ",
		"FROM b CROSS JOIN sessions s WHERE s.started_at >= b.lo AND s.started_at < b.hi AND ", 1)
	rows, err := s.db.QueryContext(ctx, bucketTable+`
		SELECT b.k, COUNT(DISTINCT s.visitor), COUNT(*), COALESCE(SUM(s.pageviews = 1), 0),
			COALESCE(SUM(s.last_seen - s.started_at), 0) `+from+` GROUP BY b.k`, append([]any{ranges}, args...)...)
	if err != nil {
		return Series{}, err
	}
	for rows.Next() {
		var k int
		var a agg
		if err := rows.Scan(&k, &a.visitors, &a.visits, &a.bounces, &a.dur); err != nil {
			rows.Close()
			return Series{}, err
		}
		got[k] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Series{}, err
	}

	from, args = q.eventScope()
	from = strings.Replace(from, "FROM events e ", "FROM b CROSS JOIN events e ", 1)
	from = strings.Replace(from, " WHERE ", " WHERE e.ts >= b.lo AND e.ts < b.hi AND ", 1)
	rows, err = s.db.QueryContext(ctx, bucketTable+`SELECT b.k, COUNT(*) `+from+` AND e.name = 'pageview' GROUP BY b.k`,
		append([]any{ranges}, args...)...)
	if err != nil {
		return Series{}, err
	}
	for rows.Next() {
		var k int
		var n float64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return Series{}, err
		}
		got[k].pageviews = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Series{}, err
	}

	ratio := func(x, y float64) float64 {
		if y == 0 {
			return 0
		}
		return x / y
	}
	out := Series{Values: map[string][]float64{}}
	for i, a := range got {
		out.Times = append(out.Times, starts[i].Unix())
		out.Values["visitors"] = append(out.Values["visitors"], a.visitors)
		out.Values["visits"] = append(out.Values["visits"], a.visits)
		out.Values["pageviews"] = append(out.Values["pageviews"], a.pageviews)
		out.Values["views_per_visit"] = append(out.Values["views_per_visit"], ratio(a.pageviews, a.visits))
		out.Values["bounce_rate"] = append(out.Values["bounce_rate"], 100*ratio(a.bounces, a.visits))
		out.Values["visit_duration"] = append(out.Values["visit_duration"], ratio(a.dur, a.visits))
	}
	return out, nil
}

// ---- breakdowns ----

type Row struct {
	Label      string
	Hint       string // e.g. the country of a city
	Visitors   int64
	Count      int64 // visits, pageviews, entrances, exits or events
	Percent    float64
	CR         *float64 // conversion rate, when a goal filter is active
	TimeOnPage *time.Duration
	Scroll     *float64
	Revenue    *float64
	Link       string // filled in by the web layer
}

// Breakdown groups by a dimension, ordered by visitors (pages by pageviews).
// When a goal filter is active, rows get a conversion rate.
func (s *Store) Breakdown(ctx context.Context, q Query, dim string, limit int) ([]Row, error) {
	rows, err := s.breakdown(ctx, q, dim, limit)
	if err != nil {
		return nil, err
	}
	if _, ok := q.GoalFilter(); ok && len(rows) > 0 {
		base, err := s.breakdown(ctx, q.WithoutGoalFilters(), dim, 1000)
		if err != nil {
			return nil, err
		}
		totals := map[string]int64{}
		for _, r := range base {
			totals[r.Label] = r.Visitors
		}
		for i := range rows {
			if t := totals[rows[i].Label]; t > 0 {
				cr := float64(rows[i].Visitors) * 100 / float64(t)
				rows[i].CR = &cr
			}
		}
	}
	return rows, nil
}

func (s *Store) breakdown(ctx context.Context, q Query, dim string, limit int) ([]Row, error) {
	if dim == "page" {
		from, args := q.eventScope()
		rs, err := s.db.QueryContext(ctx, `
			SELECT e.path, COUNT(DISTINCT e.visitor), COUNT(*), AVG(e.engaged), AVG(e.scroll) `+from+`
			AND e.name = 'pageview' GROUP BY e.path ORDER BY 3 DESC, 2 DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		var out []Row
		for rs.Next() {
			var r Row
			var engaged, scroll sql.NullFloat64
			if err := rs.Scan(&r.Label, &r.Visitors, &r.Count, &engaged, &scroll); err != nil {
				return nil, err
			}
			if engaged.Valid {
				d := time.Duration(engaged.Float64) * time.Millisecond
				r.TimeOnPage = &d
			}
			if scroll.Valid {
				r.Scroll = &scroll.Float64
			}
			out = append(out, r)
		}
		return withPercent(out), rs.Err()
	}

	d, ok := DimensionByKey(dim)
	if !ok || d.Col == "" {
		return nil, fmt.Errorf("invalid breakdown dimension %q", dim)
	}
	hint := "''"
	if dim == "region" || dim == "city" {
		hint = "MAX(s.country)"
	}
	from, args := q.sessionScope()
	rs, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT s.%[1]s, %[2]s, COUNT(DISTINCT s.visitor), COUNT(*) %[3]s AND s.%[1]s <> ''
		GROUP BY s.%[1]s ORDER BY 3 DESC, 4 DESC LIMIT ?`, d.Col, hint, from), append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.Label, &r.Hint, &r.Visitors, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return withPercent(out), rs.Err()
}

func withPercent(rows []Row) []Row {
	var max int64
	for _, r := range rows {
		max = maxInt(max, r.Visitors)
	}
	for i := range rows {
		if max > 0 {
			rows[i].Percent = float64(rows[i].Visitors) * 100 / float64(max)
		}
	}
	return rows
}

// ---- goals, events, properties, funnels ----

type GoalStat struct {
	Goal       Goal
	Visitors   int64 // unique conversions
	Count      int64 // total conversions
	CR         float64
	Revenue    float64
	AvgRevenue float64
	Percent    float64
	Link       string
}

// GoalStats returns conversions for every configured goal. The conversion
// rate is relative to all visitors matching the filters except goal filters.
func (s *Store) GoalStats(ctx context.Context, q Query) ([]GoalStat, error) {
	base, err := s.Summary(ctx, q.WithoutGoalFilters())
	if err != nil {
		return nil, err
	}
	from, args := q.eventScope()
	var out []GoalStat
	for _, g := range q.Goals {
		cond, cargs := GoalCond(g, "e")
		st := GoalStat{Goal: g}
		var rev sql.NullFloat64
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT e.visitor), COUNT(*), SUM(e.revenue) `+from+` AND `+cond,
			append(append([]any{}, args...), cargs...)...).Scan(&st.Visitors, &st.Count, &rev)
		if err != nil {
			return nil, err
		}
		if base.Visitors > 0 {
			st.CR = float64(st.Visitors) * 100 / float64(base.Visitors)
		}
		st.Revenue = rev.Float64
		if st.Count > 0 {
			st.AvgRevenue = st.Revenue / float64(st.Count)
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Visitors > out[j].Visitors })
	var max int64
	for _, st := range out {
		max = maxInt(max, st.Visitors)
	}
	for i := range out {
		if max > 0 {
			out[i].Percent = float64(out[i].Visitors) * 100 / float64(max)
		}
	}
	return out, nil
}

// CustomEvents lists all custom events, configured as goals or not.
func (s *Store) CustomEvents(ctx context.Context, q Query, limit int) ([]Row, error) {
	from, args := q.eventScope()
	return s.rows(ctx, `SELECT e.name, COUNT(DISTINCT e.visitor), COUNT(*) `+from+`
		AND e.name <> 'pageview' GROUP BY e.name ORDER BY 3 DESC LIMIT ?`, append(args, limit)...)
}

// propScope restricts property queries to the filtered goal's events.
func (q Query) propScope() (string, []any) {
	from, args := q.eventScope()
	from += " AND e.props IS NOT NULL"
	if g, ok := q.GoalFilter(); ok {
		c, a := GoalCond(g, "e")
		from += " AND " + c
		args = append(args, a...)
	}
	return from, args
}

// PropKeys lists custom property names seen in the window, most used first.
func (s *Store) PropKeys(ctx context.Context, q Query) ([]string, error) {
	from, args := q.propScope()
	from = strings.Replace(from, " WHERE ", ", json_each(e.props) j WHERE ", 1)
	rs, err := s.db.QueryContext(ctx, `SELECT j.key, COUNT(*) `+from+` GROUP BY j.key ORDER BY 2 DESC LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var keys []string
	for rs.Next() {
		var k string
		var n int64
		if err := rs.Scan(&k, &n); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rs.Err()
}

// PropValues breaks a property down by value; Count is the number of events.
func (s *Store) PropValues(ctx context.Context, q Query, key string, limit int) ([]Row, error) {
	if !ValidPropKey(key) {
		return nil, fmt.Errorf("invalid property %q", key)
	}
	path := `$."` + key + `"`
	from, args := q.propScope()
	rows, err := s.rows(ctx, `SELECT CAST(json_extract(e.props, ?) AS TEXT) AS v, COUNT(DISTINCT e.visitor), COUNT(*) `+from+`
		AND v IS NOT NULL GROUP BY v ORDER BY 2 DESC, 3 DESC LIMIT ?`,
		append(append([]any{path}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	// revenue per value, when the filtered goal is a revenue goal
	if g, ok := q.GoalFilter(); ok && g.Currency != "" {
		rs, err := s.db.QueryContext(ctx, `SELECT CAST(json_extract(e.props, ?) AS TEXT) AS v, SUM(e.revenue) `+from+`
			AND v IS NOT NULL GROUP BY v`, append([]any{path}, args...)...)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		rev := map[string]float64{}
		for rs.Next() {
			var v string
			var r sql.NullFloat64
			if err := rs.Scan(&v, &r); err != nil {
				return nil, err
			}
			rev[v] = r.Float64
		}
		for i := range rows {
			r := rev[rows[i].Label]
			rows[i].Revenue = &r
		}
	}
	return rows, nil
}

type FunnelStep struct {
	Name     string
	Visitors int64
	Percent  float64 // of the first step
	DropOff  float64 // percent lost since the previous step
}

// FunnelStats counts visitors who completed each step in order within a
// single visit.
func (s *Store) FunnelStats(ctx context.Context, q Query, f Funnel) ([]FunnelStep, error) {
	byID := map[int64]Goal{}
	for _, g := range q.Goals {
		byID[g.ID] = g
	}
	var match []func(name, path string) bool
	var steps []FunnelStep
	for _, id := range f.Steps {
		g, ok := byID[id]
		if !ok {
			continue
		}
		match = append(match, GoalMatcher(g))
		steps = append(steps, FunnelStep{Name: g.Name()})
	}
	if len(match) == 0 {
		return nil, nil
	}

	where, args := q.sessionScope()
	where = where[len("FROM sessions s "):]
	rs, err := s.db.QueryContext(ctx, `SELECT e.session_id, s.visitor, e.name, e.path
		FROM sessions s JOIN events e ON e.session_id = s.id `+where+` ORDER BY e.session_id, e.ts, e.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	reached := make([]map[int64]bool, len(match))
	for i := range reached {
		reached[i] = map[int64]bool{}
	}
	var cur int64 = -1
	step := 0
	for rs.Next() {
		var sid, visitor int64
		var name, path string
		if err := rs.Scan(&sid, &visitor, &name, &path); err != nil {
			return nil, err
		}
		if sid != cur {
			cur, step = sid, 0
		}
		if step < len(match) && match[step](name, path) {
			reached[step][visitor] = true
			step++
		}
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for i := range steps {
		steps[i].Visitors = int64(len(reached[i]))
		if first := len(reached[0]); first > 0 {
			steps[i].Percent = float64(steps[i].Visitors) * 100 / float64(first)
		}
		if i > 0 && steps[i-1].Visitors > 0 {
			steps[i].DropOff = 100 - float64(steps[i].Visitors)*100/float64(steps[i-1].Visitors)
		}
	}
	return steps, nil
}

func (s *Store) rows(ctx context.Context, query string, args ...any) ([]Row, error) {
	rs, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		if err := rs.Scan(&r.Label, &r.Visitors, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return withPercent(out), rs.Err()
}

func maxInt(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---- web vitals ----

// VitalMetrics are the vitals columns, in display order.
var VitalMetrics = []string{"lcp", "inp", "cls", "fcp", "ttfb"}

type VitalStat struct {
	P75     float64
	Samples int64
}

type PageVitals struct {
	Path    string
	Samples int64
	P75     map[string]*float64
}

// vitalsScope applies the filters vitals can honor (page and device).
func (q Query) vitalsScope() (string, []any) {
	where := `FROM vitals WHERE site_id = ? AND ts >= ? AND ts < ?`
	args := []any{q.SiteID, q.From.Unix(), q.To.Unix()}
	for _, f := range q.Filters {
		switch f.Dim {
		case "page":
			where += " AND " + opCond("path", f.Op)
			args = append(args, f.Value)
		case "device":
			where += " AND " + opCond("device", f.Op)
			args = append(args, f.Value)
		}
	}
	return where, args
}

// VitalsSummary returns the 75th percentile of each metric (the value Google
// uses to assess Core Web Vitals).
func (s *Store) VitalsSummary(ctx context.Context, q Query) (map[string]VitalStat, error) {
	from, args := q.vitalsScope()
	out := map[string]VitalStat{}
	for _, m := range VitalMetrics {
		var st VitalStat
		var p75 sql.NullFloat64
		err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT MIN(CASE WHEN pr >= 0.75 THEN %[1]s END), COUNT(*) FROM (
				SELECT %[1]s, cume_dist() OVER (ORDER BY %[1]s) AS pr %[2]s AND %[1]s IS NOT NULL)`, m, from),
			args...).Scan(&p75, &st.Samples)
		if err != nil {
			return nil, err
		}
		st.P75 = p75.Float64
		out[m] = st
	}
	return out, nil
}

// VitalsByPage returns p75 of every metric for the pages with the most samples.
func (s *Store) VitalsByPage(ctx context.Context, q Query, limit int) ([]PageVitals, error) {
	from, args := q.vitalsScope()
	rows, err := s.db.QueryContext(ctx, `SELECT path, COUNT(*) `+from+` GROUP BY path ORDER BY 2 DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var pages []PageVitals
	index := map[string]int{}
	for rows.Next() {
		p := PageVitals{P75: map[string]*float64{}}
		if err := rows.Scan(&p.Path, &p.Samples); err != nil {
			rows.Close()
			return nil, err
		}
		index[p.Path] = len(pages)
		pages = append(pages, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, m := range VitalMetrics {
		rs, err := s.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT path, MIN(%[1]s) FROM (
				SELECT path, %[1]s, cume_dist() OVER (PARTITION BY path ORDER BY %[1]s) AS pr %[2]s AND %[1]s IS NOT NULL)
			WHERE pr >= 0.75 GROUP BY path`, m, from), args...)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var path string
			var v float64
			if err := rs.Scan(&path, &v); err != nil {
				rs.Close()
				return nil, err
			}
			if i, ok := index[path]; ok {
				pages[i].P75[m] = &v
			}
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Samples > pages[j].Samples })
	return pages, nil
}

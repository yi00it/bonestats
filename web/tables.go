// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/store"
)

// table is one result of the stats API and one file of the CSV export, so
// both always carry the same columns.
type table struct {
	cols []string
	rows [][]any // nil means no value: null in JSON, empty in CSV
}

func (t *table) add(row ...any) { t.rows = append(t.rows, row) }

// objects turns the rows into JSON objects keyed by column.
func (t table) objects() []map[string]any {
	out := make([]map[string]any, 0, len(t.rows))
	for _, row := range t.rows {
		m := make(map[string]any, len(t.cols))
		for i, c := range t.cols {
			m[c] = row[i]
		}
		out = append(out, m)
	}
	return out
}

func (t table) writeCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	cw.Write(t.cols)
	for _, row := range t.rows {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = csvCell(v)
		}
		cw.Write(rec)
	}
	cw.Flush()
	return cw.Error()
}

// csvCell formats a value for CSV. Paths, referrers and event names come
// from visitors' browsers, so text that a spreadsheet would run as a formula
// gets a leading apostrophe.
func csvCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if x != "" && strings.ContainsRune("=+-@\t\r", rune(x[0])) {
			return "'" + x
		}
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// round keeps two decimals, which is plenty for rates and averages.
func round(f float64) float64 { return math.Round(f*100) / 100 }

func roundPtr(f *float64) any {
	if f == nil {
		return nil
	}
	return round(*f)
}

func seconds(d *time.Duration) any {
	if d == nil {
		return nil
	}
	return round(d.Seconds())
}

// countColumn names what a breakdown's Count counts.
func countColumn(dim string) string {
	switch dim {
	case "page":
		return "pageviews"
	case "entry_page":
		return "entrances"
	case "exit_page":
		return "exits"
	case "event":
		return "events"
	}
	if strings.HasPrefix(dim, "prop:") {
		return "events"
	}
	return "visits"
}

// breakdownDims are the dimensions the API and the export break down by:
// the filter dimensions except goal, plus custom events. Properties go by
// "prop:<name>".
func breakdownDims() []string {
	var out []string
	for _, d := range store.Dimensions {
		if d.Key != "goal" {
			out = append(out, d.Key)
		}
	}
	return append(out, "event")
}

func validBreakdownDim(dim string) bool {
	if strings.HasPrefix(dim, "prop:") {
		_, ok := store.DimensionByKey(dim)
		return ok
	}
	return contains(breakdownDims(), dim)
}

func summaryTable(sum store.Summary) table {
	t := table{cols: []string{"metric", "value"}}
	for _, m := range store.Metrics {
		t.add(m, round(sum.Metric(m)))
	}
	return t
}

func summaryObject(sum store.Summary) map[string]any {
	m := map[string]any{}
	for _, k := range store.Metrics {
		v := round(sum.Metric(k))
		if k == "visitors" || k == "visits" || k == "pageviews" {
			m[k] = int64(v)
		} else {
			m[k] = v
		}
	}
	return m
}

func timeseriesTable(ctx context.Context, st *store.Store, q store.Query, iv store.Interval) (table, error) {
	series, err := st.Timeseries(ctx, q, iv)
	if err != nil {
		return table{}, err
	}
	t := table{cols: append([]string{"date"}, store.Metrics...)}
	for i, ts := range series.Times {
		at := time.Unix(ts, 0).In(q.Location)
		date := at.Format(dateLayout)
		if iv == store.Hour {
			date = at.Format(time.RFC3339)
		}
		row := []any{date}
		for _, m := range store.Metrics {
			v := series.Values[m][i]
			if m == "visitors" || m == "visits" || m == "pageviews" {
				row = append(row, int64(v))
			} else {
				row = append(row, round(v))
			}
		}
		t.add(row...)
	}
	return t, nil
}

func breakdownTable(ctx context.Context, st *store.Store, q store.Query, dim string, limit int) (table, error) {
	var rows []store.Row
	var err error
	switch {
	case dim == "event":
		rows, err = st.CustomEvents(ctx, q, limit)
	case strings.HasPrefix(dim, "prop:"):
		rows, err = st.PropValues(ctx, q, strings.TrimPrefix(dim, "prop:"), limit)
	default:
		rows, err = st.Breakdown(ctx, q, dim, limit)
	}
	if err != nil {
		return table{}, err
	}
	_, goal := q.GoalFilter()
	cols := []string{"value", "visitors", countColumn(dim)}
	hint := dim == "region" || dim == "city"
	if hint {
		cols = append(cols, "country")
	}
	if dim == "page" {
		cols = append(cols, "time_on_page", "scroll_depth")
	}
	cr := goal && dim != "event" && !strings.HasPrefix(dim, "prop:")
	if cr {
		cols = append(cols, "conversion_rate")
	}
	revenue := false
	if g, ok := q.GoalFilter(); ok && g.Currency != "" && strings.HasPrefix(dim, "prop:") {
		revenue = true
		cols = append(cols, "revenue")
	}
	t := table{cols: cols}
	for _, r := range rows {
		row := []any{r.Label, r.Visitors, r.Count}
		if hint {
			row = append(row, r.Hint)
		}
		if dim == "page" {
			row = append(row, seconds(r.TimeOnPage), roundPtr(r.Scroll))
		}
		if cr {
			row = append(row, roundPtr(r.CR))
		}
		if revenue {
			row = append(row, roundPtr(r.Revenue))
		}
		t.add(row...)
	}
	return t, nil
}

func goalsTable(ctx context.Context, st *store.Store, q store.Query) (table, error) {
	stats, err := st.GoalStats(ctx, q)
	if err != nil {
		return table{}, err
	}
	t := table{cols: []string{"goal", "type", "visitors", "conversions", "conversion_rate", "revenue", "currency"}}
	for _, g := range stats {
		kind := "event"
		if g.Goal.PagePath != "" {
			kind = "page"
		}
		var rev, cur any
		if g.Goal.Currency != "" {
			rev, cur = round(g.Revenue), g.Goal.Currency
		}
		t.add(g.Goal.Name(), kind, g.Visitors, g.Count, round(g.CR), rev, cur)
	}
	return t, nil
}

// propertiesTable lists every property's values, for the export.
func propertiesTable(ctx context.Context, st *store.Store, q store.Query, limit int) (table, error) {
	keys, err := st.PropKeys(ctx, q)
	if err != nil {
		return table{}, err
	}
	t := table{cols: []string{"property", "value", "visitors", "events"}}
	for _, k := range keys {
		rows, err := st.PropValues(ctx, q, k, limit)
		if err != nil {
			return table{}, err
		}
		for _, r := range rows {
			t.add(k, r.Label, r.Visitors, r.Count)
		}
	}
	return t, nil
}

func funnelsTable(ctx context.Context, st *store.Store, q store.Query, funnels []store.Funnel) (table, error) {
	t := table{cols: []string{"funnel", "step", "goal", "visitors", "percent", "drop_off"}}
	for _, f := range funnels {
		steps, err := st.FunnelStats(ctx, q, f)
		if err != nil {
			return table{}, err
		}
		for i, s := range steps {
			t.add(f.Name, int64(i+1), s.Name, s.Visitors, round(s.Percent), round(s.DropOff))
		}
	}
	return t, nil
}

func vitalsTable(ctx context.Context, st *store.Store, q store.Query, limit int) (table, error) {
	pages, err := st.VitalsByPage(ctx, q, limit)
	if err != nil {
		return table{}, err
	}
	t := table{cols: append(append([]string{"page"}, store.VitalMetrics...), "samples")}
	for _, p := range pages {
		row := []any{p.Path}
		for _, m := range store.VitalMetrics {
			row = append(row, roundPtr(p.P75[m]))
		}
		t.add(append(row, p.Samples)...)
	}
	return t, nil
}

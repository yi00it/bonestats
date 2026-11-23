// SPDX-License-Identifier: AGPL-3.0-only

// Package report builds the weekly and monthly email reports and sends
// them on schedule.
package report

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	htmltemplate "html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/yi00it/bonestats/store"
)

// Periods are the report schedules, in the order the UI offers them.
var Periods = []string{"weekly", "monthly"}

// LastPeriod returns the last complete week (Monday to Sunday) or calendar
// month before now, in loc. to is exclusive.
func LastPeriod(period string, now time.Time, loc *time.Location) (from, to time.Time) {
	now = now.In(loc)
	y, m, d := now.Date()
	if period == "monthly" {
		to = time.Date(y, m, 1, 0, 0, 0, 0, loc)
		return to.AddDate(0, -1, 0), to
	}
	to = time.Date(y, m, d-(int(now.Weekday())+6)%7, 0, 0, 0, 0, loc)
	return to.AddDate(0, 0, -7), to
}

// sendHour is the local hour from which a period's report goes out, so it
// arrives in the morning rather than at midnight.
const sendHour = 7

// DuePeriod returns the last period whose report may go out at now.
func DuePeriod(period string, now time.Time, loc *time.Location) (from, to time.Time) {
	return LastPeriod(period, now.Add(-sendHour*time.Hour), loc)
}

func previous(period string, from time.Time) time.Time {
	if period == "monthly" {
		return from.AddDate(0, -1, 0)
	}
	return from.AddDate(0, 0, -7)
}

const dayLayout = "2006-01-02"

// Day formats a period's first day the way Store.ReportSent records it.
func Day(t time.Time) string { return t.Format(dayLayout) }

// Data is what one report shows.
type Data struct {
	Domain         string
	Period         string // weekly or monthly
	Label          string // e.g. "Nov 16 – 22, 2026"
	Metrics        []Metric
	Pages          []Line
	Sources        []Line
	Countries      []Line
	Goals          []Line
	Empty          bool // no visits in the period
	DashboardURL   string
	UnsubscribeURL string
	Email          string
	PreviousLabel  string // "the week before", "September"
}

// A Metric is one headline number and its change from the previous period.
type Metric struct {
	Name   string
	Value  string
	Change string // "+12%", "−3 pp" or "" when there's nothing to compare
	Good   bool   // whether the change is an improvement
	Flat   bool
}

// A Line is one row of a top list.
type Line struct {
	Label string
	Value string
	Bar   int // width in percent of the top row
}

// Build collects a report's numbers for the period [from, to).
func Build(ctx context.Context, st *store.Store, r store.Report, from, to time.Time, loc *time.Location) (Data, error) {
	goals, err := st.Goals(ctx, r.SiteID)
	if err != nil {
		return Data{}, err
	}
	q := store.Query{SiteID: r.SiteID, From: from, To: to, Location: loc, Goals: goals}
	sum, err := st.Summary(ctx, q)
	if err != nil {
		return Data{}, err
	}
	pq := q
	pq.From, pq.To = previous(r.Period, from), from
	prev, err := st.Summary(ctx, pq)
	if err != nil {
		return Data{}, err
	}

	last := to.AddDate(0, 0, -1)
	d := Data{
		Domain:         r.Domain,
		Period:         r.Period,
		Label:          label(r.Period, from, last),
		Empty:          sum.Visits == 0,
		Email:          r.Email,
		DashboardURL:   r.BaseURL + "/?" + url.Values{"site": {r.Domain}, "range": {"custom"}, "from": {from.Format(dayLayout)}, "to": {last.Format(dayLayout)}}.Encode(),
		UnsubscribeURL: r.BaseURL + "/reports/unsubscribe/" + r.Token,
		PreviousLabel:  "the week before",
	}
	if r.Period == "monthly" {
		d.PreviousLabel = pq.From.Format("January")
	}
	cmp := prev.Visits > 0
	d.Metrics = []Metric{
		countMetric("Visitors", sum.Visitors, prev.Visitors, cmp),
		countMetric("Visits", sum.Visits, prev.Visits, cmp),
		countMetric("Pageviews", sum.Pageviews, prev.Pageviews, cmp),
		{Name: "Bounce rate", Value: fmt.Sprintf("%.0f%%", sum.BounceRate)},
		{Name: "Visit duration", Value: duration(sum.AvgDuration)},
	}
	if cmp && sum.Visits > 0 {
		pp := math.Round(sum.BounceRate - prev.BounceRate)
		d.Metrics[3].Change, d.Metrics[3].Good, d.Metrics[3].Flat = signed(pp)+" pp", pp < 0, pp == 0
		d.Metrics[4].Change, d.Metrics[4].Good, d.Metrics[4].Flat = change(sum.AvgDuration.Seconds(), prev.AvgDuration.Seconds())
	}
	if d.Empty {
		return d, nil
	}

	lines := func(dim string, name func(string) string) ([]Line, error) {
		rows, err := st.Breakdown(ctx, q, dim, 5)
		if err != nil {
			return nil, err
		}
		var out []Line
		for _, row := range rows {
			n := row.Visitors
			if dim == "page" {
				n = row.Count
			}
			label := row.Label
			if name != nil {
				label = name(label)
			}
			out = append(out, Line{Label: label, Value: number(n), Bar: bar(n, rows, dim)})
		}
		return out, nil
	}
	if d.Pages, err = lines("page", nil); err != nil {
		return Data{}, err
	}
	if d.Sources, err = lines("source", nil); err != nil {
		return Data{}, err
	}
	if d.Countries, err = lines("country", countryName); err != nil {
		return Data{}, err
	}
	stats, err := st.GoalStats(ctx, q)
	if err != nil {
		return Data{}, err
	}
	var top int64
	for _, g := range stats {
		top = max(top, g.Visitors)
	}
	for _, g := range stats {
		if g.Visitors > 0 && len(d.Goals) < 5 {
			d.Goals = append(d.Goals, Line{Label: g.Goal.Name(), Value: fmt.Sprintf("%s · %.1f%%", number(g.Visitors), g.CR), Bar: int(100 * g.Visitors / top)})
		}
	}
	return d, nil
}

func bar(n int64, rows []store.Row, dim string) int {
	top := rows[0].Visitors
	if dim == "page" {
		top = rows[0].Count
	}
	if top == 0 {
		return 0
	}
	return int(100 * n / top)
}

func label(period string, from, last time.Time) string {
	if period == "monthly" {
		return from.Format("January 2006")
	}
	if from.Month() == last.Month() {
		return from.Format("Jan 2") + " – " + last.Format("2, 2006")
	}
	if from.Year() == last.Year() {
		return from.Format("Jan 2") + " – " + last.Format("Jan 2, 2006")
	}
	return from.Format("Jan 2, 2006") + " – " + last.Format("Jan 2, 2006")
}

func countMetric(name string, n, prev int64, cmp bool) Metric {
	m := Metric{Name: name, Value: number(n)}
	if cmp {
		m.Change, m.Good, m.Flat = change(float64(n), float64(prev))
	}
	return m
}

// change formats the relative change from prev to v.
func change(v, prev float64) (string, bool, bool) {
	if prev == 0 {
		return "", false, false
	}
	pct := math.Round(100 * (v - prev) / prev)
	return signed(pct) + "%", pct > 0, pct == 0
}

func signed(f float64) string {
	switch {
	case f > 0:
		return "+" + strconv.FormatFloat(f, 'f', 0, 64)
	case f < 0:
		return "−" + strconv.FormatFloat(-f, 'f', 0, 64)
	}
	return "±0"
}

// number groups thousands: 12,345.
func number(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func duration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

//go:embed email.html
var htmlSource string

//go:embed email.txt
var textSource string

var (
	htmlTmpl = htmltemplate.Must(htmltemplate.New("").Funcs(htmltemplate.FuncMap{"list": func(a ...any) []any { return a }}).Parse(htmlSource))
	textTmpl = template.Must(template.New("").Parse(textSource))
)

// Subject is the email's subject line.
func (d Data) Subject() string {
	return fmt.Sprintf("%s: %s report for %s", d.Domain, strings.ToLower(d.PeriodName()), d.Label)
}

// PeriodName is "Weekly" or "Monthly".
func (d Data) PeriodName() string {
	if d.Period == "monthly" {
		return "Monthly"
	}
	return "Weekly"
}

// Render returns the email's plain text and HTML bodies.
func (d Data) Render() (text, html string, err error) {
	var t, h bytes.Buffer
	if err := textTmpl.Execute(&t, d); err != nil {
		return "", "", err
	}
	if err := htmlTmpl.Execute(&h, d); err != nil {
		return "", "", err
	}
	return t.String(), h.String(), nil
}

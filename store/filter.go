// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"regexp"
	"strings"
	"time"
)

// Filter narrows the dashboard, e.g. {Dim: "country", Op: "is", Value: "DE"}.
type Filter struct {
	Dim, Op, Value string
}

const (
	OpIs       = "is"
	OpIsNot    = "is_not"
	OpContains = "contains"
)

// Dimension is something the dashboard can be broken down and filtered by.
type Dimension struct {
	Key, Label string
	Col        string // sessions column; empty for page, goal and props
}

// Dimensions in the order the "add filter" form lists them.
var Dimensions = []Dimension{
	{"page", "Page", ""},
	{"entry_page", "Entry page", "entry_path"},
	{"exit_page", "Exit page", "exit_path"},
	{"goal", "Goal", ""},
	{"channel", "Channel", "channel"},
	{"source", "Source", "referrer"},
	{"referrer", "Referrer URL", "referrer_url"},
	{"utm_source", "UTM source", "utm_source"},
	{"utm_medium", "UTM medium", "utm_medium"},
	{"utm_campaign", "UTM campaign", "utm_campaign"},
	{"utm_term", "UTM term", "utm_term"},
	{"utm_content", "UTM content", "utm_content"},
	{"country", "Country", "country"},
	{"region", "Region", "region"},
	{"city", "City", "city"},
	{"browser", "Browser", "browser"},
	{"browser_version", "Browser version", "browser_version"},
	{"os", "OS", "os"},
	{"os_version", "OS version", "os_version"},
	{"device", "Device", "device"},
	{"screen", "Screen size", "screen"},
}

var propKeyRe = regexp.MustCompile(`^[A-Za-z0-9_ .\-]{1,64}$`)

// ValidPropKey reports whether k can be used as a property name.
func ValidPropKey(k string) bool { return propKeyRe.MatchString(k) }

// DimensionByKey resolves a dimension key; "prop:<name>" is a custom property.
func DimensionByKey(key string) (Dimension, bool) {
	if name, ok := strings.CutPrefix(key, "prop:"); ok && ValidPropKey(name) {
		return Dimension{Key: key, Label: "Property " + name}, true
	}
	for _, d := range Dimensions {
		if d.Key == key {
			return d, true
		}
	}
	return Dimension{}, false
}

// ParseFilter parses the URL form "dim|op|value".
func ParseFilter(s string) (Filter, bool) {
	parts := strings.SplitN(s, "|", 3)
	if len(parts) != 3 || parts[2] == "" {
		return Filter{}, false
	}
	f := Filter{Dim: parts[0], Op: parts[1], Value: parts[2]}
	if _, ok := DimensionByKey(f.Dim); !ok {
		return Filter{}, false
	}
	if f.Op != OpIs && f.Op != OpIsNot && f.Op != OpContains {
		return Filter{}, false
	}
	return f, true
}

func (f Filter) String() string { return f.Dim + "|" + f.Op + "|" + f.Value }

// Query selects one site, a time window and optional filters. Location is the
// timezone whose calendar days, weeks and months the time series follows.
type Query struct {
	SiteID   int64
	From     time.Time
	To       time.Time
	Location *time.Location
	Filters  []Filter
	Goals    []Goal // used to resolve goal filters
}

// WithoutGoalFilters returns q minus its goal filters (the baseline for
// conversion rates).
func (q Query) WithoutGoalFilters() Query {
	var fs []Filter
	for _, f := range q.Filters {
		if f.Dim != "goal" {
			fs = append(fs, f)
		}
	}
	q.Filters = fs
	return q
}

// GoalFilter returns the goal selected with "goal is X", if any.
func (q Query) GoalFilter() (Goal, bool) {
	for _, f := range q.Filters {
		if f.Dim == "goal" && f.Op == OpIs {
			return q.resolveGoal(f.Value), true
		}
	}
	return Goal{}, false
}

// resolveGoal finds a configured goal by name; unknown names are treated as
// raw custom event names so any event can be filtered on.
func (q Query) resolveGoal(name string) Goal {
	for _, g := range q.Goals {
		if g.Name() == name {
			return g
		}
	}
	return Goal{EventName: name}
}

// GoalCond is the SQL condition matching events of goal g on table alias a.
func GoalCond(g Goal, a string) (string, []any) {
	if g.PagePath != "" {
		return a + ".name = 'pageview' AND " + a + ".path GLOB ?", []any{globPattern(g.PagePath)}
	}
	return a + ".name = ?", []any{g.EventName}
}

// globPattern turns a goal path where only * is special into an SQLite GLOB.
func globPattern(p string) string {
	return strings.NewReplacer("[", "[[]", "?", "[?]").Replace(p)
}

// GoalMatcher returns a Go predicate equivalent to GoalCond.
func GoalMatcher(g Goal) func(name, path string) bool {
	if g.PagePath == "" {
		return func(name, _ string) bool { return name == g.EventName }
	}
	parts := strings.Split(g.PagePath, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re := regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")
	return func(name, path string) bool { return name == "pageview" && re.MatchString(path) }
}

func opCond(expr, op string) string {
	switch op {
	case OpIsNot:
		return expr + " <> ?"
	case OpContains:
		return "instr(lower(" + expr + "), lower(?)) > 0"
	default:
		return expr + " = ?"
	}
}

// exists wraps a condition on events alias e2 into a per-session EXISTS,
// negated for "is not" so that e.g. "page is not /x" means visits that never
// saw /x.
func exists(op, cond string) string {
	not := ""
	if op == OpIsNot {
		not = "NOT "
		cond = strings.Replace(cond, " <> ?", " = ?", 1)
	}
	return not + "EXISTS (SELECT 1 FROM events e2 WHERE e2.session_id = s.id AND " + cond + ")"
}

// conds builds the WHERE conditions for the filters. With eventScope the
// query is over events e joined to sessions s, and page filters apply to the
// event rows directly; otherwise everything is expressed per session.
func (q Query) conds(eventScope bool) (string, []any) {
	var sb strings.Builder
	var args []any
	for _, f := range q.Filters {
		d, _ := DimensionByKey(f.Dim)
		sb.WriteString(" AND ")
		switch {
		case d.Col != "":
			sb.WriteString(opCond("s."+d.Col, f.Op))
			args = append(args, f.Value)
		case f.Dim == "page" && eventScope && f.Op != OpIsNot:
			// only the matching pageviews; "is not" stays per visit (below)
			// so it means "visits that never saw the page"
			sb.WriteString(opCond("e.path", f.Op))
			args = append(args, f.Value)
		case f.Dim == "page":
			sb.WriteString(exists(f.Op, "e2.name = 'pageview' AND "+opCond("e2.path", f.Op)))
			args = append(args, f.Value)
		case f.Dim == "goal":
			if f.Op == OpContains {
				sb.WriteString(exists(f.Op, opCond("e2.name", f.Op)))
				args = append(args, f.Value)
				break
			}
			c, a := GoalCond(q.resolveGoal(f.Value), "e2")
			sb.WriteString(exists(f.Op, c))
			args = append(args, a...)
		default: // prop:<name>
			name := strings.TrimPrefix(f.Dim, "prop:")
			sb.WriteString(exists(f.Op, opCond("json_extract(e2.props, ?)", f.Op)))
			args = append(args, `$."`+name+`"`, f.Value)
		}
	}
	return sb.String(), args
}

// sessionScope is the FROM/WHERE for visit-level metrics.
func (q Query) sessionScope() (string, []any) {
	c, a := q.conds(false)
	return `FROM sessions s WHERE s.site_id = ? AND s.started_at >= ? AND s.started_at < ? AND s.pageviews > 0` + c,
		append([]any{q.SiteID, q.From.Unix(), q.To.Unix()}, a...)
}

// eventScope is the FROM/WHERE for event-level metrics.
func (q Query) eventScope() (string, []any) {
	c, a := q.conds(true)
	return `FROM events e JOIN sessions s ON s.id = e.session_id WHERE e.site_id = ? AND e.ts >= ? AND e.ts < ?` + c,
		append([]any{q.SiteID, q.From.Unix(), q.To.Unix()}, a...)
}

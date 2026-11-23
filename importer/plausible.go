// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/collect"
	"github.com/yi00it/bonestats/store"
)

// Plausible exports daily totals per dimension: imported_<table>_<first
// day>_<last day>.csv. Visitors are unique per day (and per row), so
// totals over several days count a returning visitor once per day, as
// Plausible does for imported data.
var plausibleFile = regexp.MustCompile(`^imported_([a-z_]+?)(?:_\d{8}_\d{8})?\.csv$`)

// plausibleTables are the files we read; custom properties are left out,
// as Plausible's own importer does.
var plausibleTables = map[string]bool{
	"visitors": true, "sources": true, "pages": true, "entry_pages": true, "exit_pages": true,
	"locations": true, "devices": true, "browsers": true, "operating_systems": true, "custom_events": true,
}

func importPlausible(ctx context.Context, w *store.ImportWriter, zr *zip.Reader, loc *time.Location, cutoff time.Time) (int64, error) {
	cutDay := cutoff.In(loc).Format("2006-01-02")
	var skipped int64
	found := false
	for _, f := range zr.File {
		m := plausibleFile.FindStringSubmatch(path.Base(f.Name))
		if m == nil || !plausibleTables[m[1]] {
			continue
		}
		if m[1] == "visitors" {
			found = true
		}
		n, err := importPlausibleFile(ctx, w, f, m[1], loc, cutDay)
		skipped += n
		if err != nil {
			return skipped, err
		}
	}
	if !found {
		return skipped, formatError("no imported_visitors CSV file in the ZIP: use the ZIP from Plausible's export (Site settings → Imports & exports)")
	}
	return skipped, nil
}

// row reads columns by their header name.
type row struct {
	cols map[string]int
	rec  []string
}

func (r row) str(name string) string {
	if i, ok := r.cols[name]; ok && i < len(r.rec) {
		return strings.TrimSpace(r.rec[i])
	}
	return ""
}

func (r row) num(name string) int64 {
	n, _ := strconv.ParseInt(r.str(name), 10, 64)
	return n
}

type aggKey struct{ day, dim, value string }

func importPlausibleFile(ctx context.Context, w *store.ImportWriter, f *zip.File, table string, loc *time.Location, cutDay string) (int64, error) {
	rc, err := openEntry(f)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	cr := csv.NewReader(rc)
	cr.FieldsPerRecord = -1
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return 0, formatError("%s: %v", f.Name, err)
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	if _, ok := cols["date"]; !ok {
		return 0, formatError("%s has no date column", f.Name)
	}

	// Rows with the same day, dimension and value are summed: Plausible
	// splits some of them further (by hostname, link URL…).
	agg := map[aggKey]*store.ImportedRow{}
	add := func(day time.Time, dim, value, hint string, fill func(*store.ImportedRow)) {
		if dim != "" && value == "" {
			return
		}
		k := aggKey{day.Format("2006-01-02"), dim, value}
		r := agg[k]
		if r == nil {
			r = &store.ImportedRow{Day: day, Dim: dim, Value: value, Hint: hint}
			agg[k] = r
		}
		fill(r)
	}
	var skipped int64
	for {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return skipped, formatError("%s: %v", f.Name, err)
		}
		r := row{cols, rec}
		ds := r.str("date")
		day, err := time.ParseInLocation("2006-01-02", ds, loc)
		if err != nil {
			return skipped, formatError("%s: invalid date %q", f.Name, ds)
		}
		if ds >= cutDay {
			skipped++
			continue
		}
		plausibleRow(table, r, day, add)
	}

	keys := make([]aggKey, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.day != b.day {
			return a.day < b.day
		}
		if a.dim != b.dim {
			return a.dim < b.dim
		}
		return a.value < b.value
	})
	for _, k := range keys {
		if err := w.AddRow(*agg[k]); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

type adder func(day time.Time, dim, value, hint string, fill func(*store.ImportedRow))

// visits counts one dimension's rows by visits, as the dashboard does.
func visits(r row) func(*store.ImportedRow) {
	return func(x *store.ImportedRow) {
		x.Visitors += r.num("visitors")
		x.Count += r.num("visits")
	}
}

func plausibleRow(table string, r row, day time.Time, add adder) {
	switch table {
	case "visitors":
		add(day, "", "", "", func(x *store.ImportedRow) {
			x.Visitors += r.num("visitors")
			x.Visits += r.num("visits")
			x.Pageviews += r.num("pageviews")
			x.Bounces += r.num("bounces")
			x.Duration += r.num("visit_duration")
		})

	case "sources":
		source, ref := r.str("source"), r.str("referrer")
		refHost, refURL := "", ""
		if ref != "" {
			if !strings.Contains(ref, "://") {
				ref = "https://" + ref
			}
			refHost, refURL = collect.Referrer(ref, "")
		}
		if source == "" || source == store.DirectSource {
			source = store.DirectSource
		}
		chHost := refHost
		if chHost == "" && source != store.DirectSource {
			chHost = source
		}
		f := visits(r)
		add(day, "source", source, "", f)
		add(day, "referrer", refURL, "", f)
		add(day, "channel", collect.VisitChannel(chHost, r.str("utm_source"), r.str("utm_medium"), ""), "", f)
		for _, u := range []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"} {
			add(day, u, r.str(u), "", f)
		}

	case "pages":
		add(day, "page", collect.CleanPath(r.str("page")), "", func(x *store.ImportedRow) {
			x.Visitors += r.num("visitors")
			x.Count += r.num("pageviews")
			x.TimeTotal += r.num("total_time_on_page") * 1000
			x.TimeN += r.num("total_time_on_page_visits")
			x.ScrollTotal += r.num("total_scroll_depth")
			x.ScrollN += r.num("total_scroll_depth_visits")
		})

	case "entry_pages":
		add(day, "entry_page", collect.CleanPath(r.str("entry_page")), "", func(x *store.ImportedRow) {
			x.Visitors += r.num("visitors")
			x.Count += r.num("entrances")
		})

	case "exit_pages":
		add(day, "exit_page", collect.CleanPath(r.str("exit_page")), "", func(x *store.ImportedRow) {
			x.Visitors += r.num("visitors")
			x.Count += r.num("exits")
		})

	case "locations":
		// Regions come as ISO 3166-2 codes and cities as GeoNames ids,
		// while bonestats shows their names, so only countries are kept.
		if c := strings.ToUpper(r.str("country")); len(c) == 2 && c != "ZZ" {
			add(day, "country", c, "", visits(r))
		}

	case "devices":
		add(day, "device", plausibleDevice(r.str("device")), "", visits(r))

	case "browsers":
		b := plausibleBrowser(r.str("browser"))
		add(day, "browser", b, "", visits(r))
		if v := major(r.str("browser_version")); b != "" && v != "" {
			add(day, "browser_version", b+" "+v, "", visits(r))
		}

	case "operating_systems":
		os := plausibleOS(r.str("operating_system"))
		add(day, "os", os, "", visits(r))
		add(day, "os_version", osVersion(os, r.str("operating_system_version")), "", visits(r))

	case "custom_events":
		if name := r.str("name"); name != "" && name != "pageview" && name != "engagement" && len(name) <= 64 {
			add(day, "event", name, "", func(x *store.ImportedRow) {
				x.Visitors += r.num("visitors")
				x.Count += r.num("events")
			})
		}
	}
}

func notSet(s string) bool { return s == "" || s == "(not set)" || s == "Unknown" }

func plausibleDevice(d string) string {
	switch d {
	case "Desktop", "Laptop":
		return "Desktop"
	case "Mobile", "Tablet":
		return d
	}
	return ""
}

func plausibleBrowser(b string) string {
	if notSet(b) {
		return ""
	}
	if n, ok := map[string]string{
		"Microsoft Edge": "Edge", "Samsung Browser": "Samsung Internet", "Yandex Browser": "Yandex",
		"DuckDuckGo Privacy Browser": "DuckDuckGo", "Opera GX": "Opera", "Mobile App": "Other",
	}[b]; ok {
		return n
	}
	return b
}

func plausibleOS(o string) string {
	if notSet(o) {
		return ""
	}
	if n, ok := map[string]string{"Mac": "macOS", "GNU/Linux": "Linux", "Chrome OS": "ChromeOS", "Ubuntu": "Linux"}[o]; ok {
		return n
	}
	return o
}

// major returns the major version: "141.0.7390" → "141".
func major(v string) string {
	if notSet(v) {
		return ""
	}
	m, _, _ := strings.Cut(v, ".")
	if _, err := strconv.Atoi(m); err != nil {
		return ""
	}
	return m
}

// osVersion names an OS version the way the collector does, e.g.
// "Windows 10/11", "iOS 18" or "macOS 10.15+".
func osVersion(os, v string) string {
	if os == "" || notSet(v) {
		return ""
	}
	switch os {
	case "Windows":
		switch v {
		case "10", "11":
			return "Windows 10/11"
		case "8.1", "8", "7":
			return "Windows " + v
		}
		return ""
	case "macOS":
		parts := strings.SplitN(v, ".", 3)
		if len(parts) >= 2 && parts[0] == "10" && parts[1] == "15" {
			return "macOS 10.15+"
		}
		if len(parts) >= 2 {
			return "macOS " + parts[0] + "." + parts[1]
		}
		return "macOS " + parts[0]
	}
	if m := major(v); m != "" {
		return os + " " + m
	}
	return ""
}

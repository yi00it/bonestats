// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/collect"
	"github.com/yi00it/bonestats/store"
)

// Umami has no export with single visits, so its events come straight from
// its database as CSV (docs/IMPORT.md has the query), ordered by session and
// time:
//
//	created_at, session_id, visit_id, event_type, event_name, url_path,
//	url_query, referrer_domain, referrer_path, browser, os, device, screen,
//	country, city
//
// visit_id is optional (older versions don't have it); without it a visit
// ends after 30 minutes without an event, as in bonestats.
var umamiRequired = []string{"created_at", "session_id", "event_type", "url_path"}

// Umami's event types.
const (
	umamiPageview = "1"
	umamiEvent    = "2"
)

var timeLayouts = []string{
	"2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05.999999Z07:00",
	time.RFC3339Nano, "2006-01-02 15:04:05.999999", "2006-01-02T15:04:05.999999",
}

func parseTime(s string) (time.Time, bool) {
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type umamiRow struct {
	t        time.Time
	name     string // "pageview" or a custom event name
	path     string
	query    url.Values
	refHost  string
	refPath  string
	row      row
	visitKey string
}

func importUmami(ctx context.Context, w *store.ImportWriter, r io.Reader, domain string, loc *time.Location, cutoff time.Time) (int64, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return 0, formatError("can't read the CSV header: %v", err)
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))] = i
	}
	for _, c := range umamiRequired {
		if _, ok := cols[c]; !ok {
			return 0, formatError("the CSV has no %s column: export it with the query in docs/IMPORT.md", c)
		}
	}
	_, hasVisitID := cols["visit_id"]

	var skipped int64
	var visit []umamiRow
	flush := func() error {
		if len(visit) == 0 {
			return nil
		}
		v := umamiVisit(visit, domain, loc)
		visit = visit[:0]
		return w.AddVisit(v, loc)
	}
	line := 1
	for {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		rec, err := cr.Read()
		line++
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return skipped, formatError("line %d: %v", line, err)
		}
		rw := row{cols, rec}
		t, ok := parseTime(rw.str("created_at"))
		if !ok {
			return skipped, formatError("line %d: invalid created_at %q", line, rw.str("created_at"))
		}
		var name string
		switch rw.str("event_type") {
		case umamiPageview:
			name = "pageview"
		case umamiEvent:
			name = strings.TrimSpace(rw.str("event_name"))
			if name == "" || len(name) > 64 || name == "pageview" {
				continue
			}
		default:
			continue // link clicks, pixels, performance reports
		}
		if !t.Before(cutoff) {
			skipped++
			continue
		}
		key := rw.str("session_id")
		if hasVisitID {
			key += "/" + rw.str("visit_id")
		}
		// A new visit: another session or visit, or 30 minutes of quiet.
		if n := len(visit); n > 0 && (visit[n-1].visitKey != key || t.Sub(visit[n-1].t) > store.SessionTimeout || t.Before(visit[n-1].t)) {
			if err := flush(); err != nil {
				return skipped, err
			}
		}
		q, _ := url.ParseQuery(rw.str("url_query"))
		visit = append(visit, umamiRow{t: t, name: name, path: collect.CleanPath(rw.str("url_path")), query: q,
			refHost: rw.str("referrer_domain"), refPath: rw.str("referrer_path"), row: rw, visitKey: key})
	}
	return skipped, flush()
}

// umamiVisit turns one visit's events into a bonestats visit. Its source,
// device and location come from its first event.
func umamiVisit(events []umamiRow, domain string, loc *time.Location) store.ImportedVisit {
	first := events[0]
	r := first.row

	// Visitors are counted per day in bonestats, so the visitor id is the
	// Umami session per calendar day.
	h := sha256.Sum256([]byte("umami/" + r.str("session_id") + "/" + first.t.In(loc).Format("2006-01-02")))
	visitor := int64(binary.BigEndian.Uint64(h[:8]) >> 1)

	refHost, refURL := "", ""
	if first.refHost != "" {
		refHost, refURL = collect.Referrer("https://"+first.refHost+first.refPath, domain)
	}
	q := first.query
	clickID := ""
	for id := range store.ClickIDs {
		if q.Get(id) != "" {
			clickID = id
		}
	}
	limit := func(s string) string {
		if len(s) > 100 {
			return strings.ToValidUTF8(s[:100], "")
		}
		return s
	}
	browser := umamiBrowser(r.str("browser"))
	os, osVer := umamiOS(r.str("os"))
	width, _, _ := strings.Cut(r.str("screen"), "x")
	wpx, _ := strconv.Atoi(width)
	country := strings.ToUpper(r.str("country"))
	if len(country) != 2 {
		country = ""
	}
	v := store.Visit{
		Referrer:    store.Source(refHost, q.Get("utm_source")),
		ReferrerURL: refURL,
		Channel:     collect.VisitChannel(refHost, q.Get("utm_source"), q.Get("utm_medium"), clickID),
		UTMSource:   limit(q.Get("utm_source")), UTMMedium: limit(q.Get("utm_medium")), UTMCampaign: limit(q.Get("utm_campaign")),
		UTMTerm: limit(q.Get("utm_term")), UTMContent: limit(q.Get("utm_content")),
		Country: country,
		// Umami's regions are codes, while bonestats shows region names.
		City:    limit(r.str("city")),
		Browser: browser, OS: os, OSVersion: osVer,
		Device: umamiDevice(r.str("device")),
		Screen: collect.ScreenBucket(wpx),
	}

	out := store.ImportedVisit{Visitor: visitor, Visit: v}
	for i, e := range events {
		ie := store.ImportedEvent{Time: e.t, Name: e.name, Path: e.path}
		if e.name == "pageview" {
			// A page was looked at until the next one opened.
			for _, next := range events[i+1:] {
				if next.name == "pageview" {
					ms := min(next.t.Sub(e.t), store.SessionTimeout).Milliseconds()
					ie.Engaged = &ms
					break
				}
			}
		}
		out.Events = append(out.Events, ie)
	}
	return out
}

func umamiBrowser(b string) string {
	switch strings.ToLower(b) {
	case "":
		return ""
	case "chrome", "crios", "chromium-webview":
		return "Chrome"
	case "firefox", "fxios":
		return "Firefox"
	case "safari", "ios", "ios-webview":
		return "Safari"
	case "edge", "edge-chromium", "edge-ios":
		return "Edge"
	case "opera", "opera-mini":
		return "Opera"
	case "samsung":
		return "Samsung Internet"
	case "yandexbrowser":
		return "Yandex"
	case "facebook":
		return "Facebook"
	case "instagram":
		return "Instagram"
	case "vivaldi":
		return "Vivaldi"
	}
	return "Other"
}

// umamiOS maps Umami's OS names ("Windows 10", "Mac OS", "Android OS") to
// bonestats' OS and version.
func umamiOS(o string) (os, version string) {
	switch {
	case o == "":
		return "", ""
	case strings.HasPrefix(o, "Windows"):
		v := strings.TrimSpace(strings.TrimPrefix(o, "Windows"))
		return "Windows", osVersion("Windows", v)
	case o == "Mac OS":
		return "macOS", ""
	case o == "iOS":
		return "iOS", ""
	case o == "Android OS":
		return "Android", ""
	case o == "Chrome OS":
		return "ChromeOS", ""
	case o == "Linux":
		return "Linux", ""
	}
	return "Other", ""
}

func umamiDevice(d string) string {
	switch strings.ToLower(d) {
	case "desktop", "laptop":
		return "Desktop"
	case "mobile":
		return "Mobile"
	case "tablet":
		return "Tablet"
	}
	return ""
}

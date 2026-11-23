// SPDX-License-Identifier: AGPL-3.0-only

package importer

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

func testStore(t *testing.T) (*store.Store, store.Site) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.AddSite(ctx, "example.com"); err != nil {
		t.Fatal(err)
	}
	site, err := st.SiteByDomain(ctx, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	return st, site
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "umami.csv")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// plausibleZip is a two-day export: Jan 1 and Jan 2, 2026.
func plausibleZip(t *testing.T) string {
	return writeZip(t, map[string]string{
		"imported_visitors_20260101_20260102.csv": "date,visitors,pageviews,bounces,visits,visit_duration\n" +
			"2026-01-01,10,30,4,12,600\n2026-01-02,5,10,5,5,0\n",
		"imported_pages_20260101_20260102.csv": "date,hostname,page,visits,visitors,pageviews,total_scroll_depth,total_scroll_depth_visits,total_time_on_page,total_time_on_page_visits\n" +
			"2026-01-01,example.com,/,12,10,20,500,10,300,10\n" +
			"2026-01-01,www.example.com,/,1,1,2,0,0,0,0\n" +
			"2026-01-02,example.com,/blog/,5,5,10,250,5,50,5\n",
		"imported_sources_20260101_20260102.csv": "date,source,referrer,utm_source,utm_medium,utm_campaign,utm_content,utm_term,pageviews,visitors,visits,visit_duration,bounces\n" +
			"2026-01-01,Google,,,,,,,20,7,8,400,2\n" +
			"2026-01-01,,,,,,,,10,3,4,200,2\n" +
			"2026-01-02,Hacker News,news.ycombinator.com/item,,,launch,,,10,5,5,0,5\n",
		"imported_locations_20260101_20260102.csv": "date,country,region,city,visitors,visits,visit_duration,bounces,pageviews\n" +
			"2026-01-01,DE,DE-BE,2950159,6,7,300,2,15\n2026-01-01,ZZ,,0,1,1,0,1,1\n",
		"imported_browsers_20260101_20260102.csv": "date,browser,browser_version,visitors,visits,visit_duration,bounces,pageviews\n" +
			"2026-01-01,Microsoft Edge,141.0,2,2,0,1,3\n",
		"imported_operating_systems_20260101_20260102.csv": "date,operating_system,operating_system_version,visitors,visits,visit_duration,bounces,pageviews\n" +
			"2026-01-01,Windows,11,2,2,0,1,3\n2026-01-01,Mac,10.15,1,1,0,1,1\n",
		"imported_devices_20260101_20260102.csv": "date,device,visitors,visits,visit_duration,bounces,pageviews\n" +
			"2026-01-01,Laptop,3,3,0,1,3\n2026-01-01,Desktop,2,2,0,1,3\n",
		"imported_custom_events_20260101_20260102.csv": "date,name,link_url,path,visitors,events\n" +
			"2026-01-01,Outbound Link: Click,https://a.example,,2,3\n2026-01-01,Outbound Link: Click,https://b.example,,1,1\n" +
			"2026-01-01,signup,,,2,2\n",
		"imported_custom_props_20260101_20260102.csv": "date,property,value,visitors,events\n2026-01-01,plan,pro,1,1\n",
	})
}

func query(site store.Site, filters ...store.Filter) store.Query {
	return store.Query{SiteID: site.ID, From: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
		To: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Location: time.UTC, Filters: filters}
}

func rows(t *testing.T, st *store.Store, q store.Query, dim string) map[string]store.Row {
	t.Helper()
	rs, err := st.Breakdown(context.Background(), q, dim, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Row{}
	for _, r := range rs {
		out[r.Label] = r
	}
	return out
}

func TestPlausible(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	res, err := Run(ctx, st, site, Plausible, plausibleZip(t), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if im := res.Import; im.FirstDay != "2026-01-01" || im.LastDay != "2026-01-02" || im.Visits != 17 || im.Pageviews != 40 {
		t.Errorf("import %+v", im)
	}

	q := query(site)
	sum, err := st.Summary(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Visitors != 15 || sum.Visits != 17 || sum.Pageviews != 40 || int(sum.BounceRate) != 52 || sum.AvgDuration != 35*time.Second {
		t.Errorf("summary %+v", sum)
	}
	series, err := st.Timeseries(ctx, store.Query{SiteID: site.ID, From: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		To: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), Location: time.UTC}, store.Day)
	if err != nil {
		t.Fatal(err)
	}
	if v := series.Values["visitors"]; len(v) != 2 || v[0] != 10 || v[1] != 5 {
		t.Errorf("series %v", v)
	}

	pages := rows(t, st, q, "page")
	if p := pages["/"]; p.Count != 22 || p.Visitors != 11 || p.TimeOnPage == nil || *p.TimeOnPage != 30*time.Second || p.Scroll == nil || *p.Scroll != 50 {
		t.Errorf("page / %+v", p)
	}
	if p := pages["/blog"]; p.Count != 10 {
		t.Errorf("trailing slash kept: %v", pages)
	}
	src := rows(t, st, q, "source")
	if src["Google"].Count != 8 || src[store.DirectSource].Count != 4 || src["Hacker News"].Count != 5 {
		t.Errorf("sources %v", src)
	}
	ch := rows(t, st, q, "channel")
	if ch["Organic Search"].Count != 8 || ch["Direct"].Count != 4 || ch["Organic Social"].Count != 5 {
		t.Errorf("channels %v", ch)
	}
	if r := rows(t, st, q, "referrer"); r["news.ycombinator.com/item"].Count != 5 {
		t.Errorf("referrers %v", r)
	}
	if r := rows(t, st, q, "utm_campaign"); r["launch"].Count != 5 {
		t.Errorf("campaigns %v", r)
	}
	if r := rows(t, st, q, "country"); len(r) != 1 || r["DE"].Count != 7 {
		t.Errorf("countries %v", r)
	}
	if r := rows(t, st, q, "region"); len(r) != 0 {
		t.Errorf("regions imported: %v", r)
	}
	if r := rows(t, st, q, "browser_version"); r["Edge 141"].Count != 2 {
		t.Errorf("browser versions %v", r)
	}
	if r := rows(t, st, q, "os_version"); r["Windows 10/11"].Count != 2 || r["macOS 10.15+"].Count != 1 {
		t.Errorf("os versions %v", r)
	}
	if r := rows(t, st, q, "device"); r["Desktop"].Count != 5 {
		t.Errorf("devices %v", r)
	}
	ev, err := st.CustomEvents(ctx, q, 10)
	if err != nil || len(ev) != 2 || ev[0].Label != "Outbound Link: Click" || ev[0].Count != 4 {
		t.Errorf("events %v, %v", ev, err)
	}
	goals := []store.Goal{{EventName: "signup"}, {PagePath: "/blog*"}}
	q.Goals = goals
	gs, err := st.GoalStats(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Goal.EventName == "signup" && (g.Visitors != 2 || g.Count != 2) {
			t.Errorf("signup goal %+v", g)
		}
		if g.Goal.PagePath != "" && (g.Visitors != 5 || g.Count != 10) {
			t.Errorf("page goal %+v", g)
		}
	}

	// Filtered views leave imported totals out.
	fq := query(site, store.Filter{Dim: "country", Op: store.OpIs, Value: "DE"})
	if sum, _ := st.Summary(ctx, fq); sum.Visitors != 0 || sum.Visits != 0 {
		t.Errorf("filtered summary includes imported data: %+v", sum)
	}
	if r := rows(t, st, fq, "page"); len(r) != 0 {
		t.Errorf("filtered pages include imported data: %v", r)
	}
	if ok, _ := st.HasImportedTotals(ctx, site.ID, q.From, q.To); !ok {
		t.Error("HasImportedTotals")
	}
	if first, ok := st.FirstVisit(ctx, site.ID); !ok || first.UTC().Format("2006-01-02") != "2026-01-01" {
		t.Errorf("first visit %v %v", first, ok)
	}

	// The same days again are refused.
	if _, err := Run(ctx, st, site, Plausible, plausibleZip(t), time.UTC); !errors.Is(err, store.ErrImportOverlap) {
		t.Errorf("overlapping import: %v", err)
	}
	ims, _ := st.Imports(ctx, site.ID)
	if len(ims) != 1 {
		t.Fatalf("imports %v", ims)
	}
	if err := st.DeleteImport(ctx, site.ID, ims[0].ID); err != nil {
		t.Fatal(err)
	}
	if sum, _ := st.Summary(ctx, q); sum.Visits != 0 {
		t.Errorf("deleted import still counted: %+v", sum)
	}
}

// Days that bonestats recorded itself are skipped.
func TestPlausibleStopsAtNativeData(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	if err := st.RecordEvent(ctx, store.Event{SiteID: site.ID, Time: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC), Visitor: 1, Name: "pageview", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, st, site, Plausible, plausibleZip(t), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Import.LastDay != "2026-01-01" || res.Skipped == 0 {
		t.Errorf("result %+v", res)
	}
}

func TestPlausibleBadFiles(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	for name, p := range map[string]string{
		"not a zip":   writeFile(t, "hello"),
		"no visitors": writeZip(t, map[string]string{"imported_pages_20260101_20260102.csv": "date,page\n"}),
		"bad date":    writeZip(t, map[string]string{"imported_visitors_20260101_20260102.csv": "date,visitors\n01/01/2026,3\n"}),
		"no date":     writeZip(t, map[string]string{"imported_visitors_20260101_20260102.csv": "day,visitors\n2026-01-01,3\n"}),
	} {
		if _, err := Run(ctx, st, site, Plausible, p, time.UTC); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if ims, _ := st.Imports(ctx, site.ID); len(ims) != 0 {
		t.Errorf("failed imports left behind: %v", ims)
	}
}

const umamiHeader = "created_at,session_id,visit_id,event_type,event_name,url_path,url_query,referrer_domain,referrer_path,browser,os,device,screen,country,city\n"

func TestUmami(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	csv := umamiHeader +
		// visit 1: from Google, two pages and a signup
		"2026-01-01 10:00:00.000+00,s1,v1,1,,/,utm_source=news&utm_medium=email,www.google.com,/search,chrome,Windows 10,desktop,1920x1080,DE,Berlin\n" +
		"2026-01-01 10:00:30.000+00,s1,v1,1,,/pricing/,,,,chrome,Windows 10,desktop,1920x1080,DE,Berlin\n" +
		"2026-01-01 10:01:00.000+00,s1,v1,2,signup,/pricing,,,,chrome,Windows 10,desktop,1920x1080,DE,Berlin\n" +
		// same session, a new visit an hour later
		"2026-01-01 11:30:00.000+00,s1,v2,1,,/,,,,chrome,Windows 10,desktop,1920x1080,DE,Berlin\n" +
		// visit 3: an ad click, direct, an internal referrer
		"2026-01-02 08:00:00.000+00,s2,v3,1,,/,gclid=abc,example.com,/old,ios,iOS,mobile,390x844,fr,\n" +
		// skipped: a link event and an unknown type
		"2026-01-02 08:00:05.000+00,s2,v3,3,,/,,,,ios,iOS,mobile,390x844,fr,\n"
	res, err := Run(ctx, st, site, Umami, writeFile(t, csv), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if im := res.Import; im.Visits != 3 || im.Pageviews != 4 || im.FirstDay != "2026-01-01" || im.LastDay != "2026-01-02" {
		t.Errorf("import %+v", im)
	}
	q := query(site)
	sum, _ := st.Summary(ctx, q)
	if sum.Visitors != 2 || sum.Visits != 3 || sum.Pageviews != 4 || sum.AvgDuration != 10*time.Second {
		t.Errorf("summary %+v", sum)
	}
	if r := rows(t, st, q, "channel"); r["Email"].Count != 1 || r["Paid Search"].Count != 1 || r["Direct"].Count != 1 {
		t.Errorf("channels %v", r)
	}
	if r := rows(t, st, q, "source"); r["Google"].Count != 1 {
		t.Errorf("sources %v", r)
	}
	if r := rows(t, st, q, "page"); r["/pricing"].Count != 1 || r["/pricing"].TimeOnPage != nil || *r["/"].TimeOnPage != 30*time.Second {
		t.Errorf("pages %v", r)
	}
	for dim, want := range map[string]string{"browser": "Chrome", "os": "Windows", "os_version": "Windows 10/11", "device": "Desktop",
		"screen": "≥ 1920px", "country": "DE", "city": "Berlin"} {
		if r := rows(t, st, q, dim); r[want].Count != 2 {
			t.Errorf("%s: %v", dim, r)
		}
	}
	if r := rows(t, st, q, "country"); r["FR"].Count != 1 {
		t.Errorf("lowercase country: %v", r)
	}
	ev, _ := st.CustomEvents(ctx, q, 10)
	if len(ev) != 1 || ev[0].Label != "signup" {
		t.Errorf("events %v", ev)
	}
	// Imported visits are real visits: filters work on them.
	if sum, _ := st.Summary(ctx, query(site, store.Filter{Dim: "country", Op: store.OpIs, Value: "DE"})); sum.Visits != 2 {
		t.Errorf("filtered: %+v", sum)
	}
	// A funnel over imported visits.
	f, err := st.FunnelStats(ctx, store.Query{SiteID: site.ID, From: q.From, To: q.To, Location: time.UTC,
		Goals: []store.Goal{{ID: 1, PagePath: "/pricing"}, {ID: 2, EventName: "signup"}}}, store.Funnel{Steps: []int64{1, 2}})
	if err != nil || len(f) != 2 || f[1].Visitors != 1 {
		t.Errorf("funnel %v %v", f, err)
	}
	ims, _ := st.Imports(ctx, site.ID)
	if err := st.DeleteImport(ctx, site.ID, ims[0].ID); err != nil {
		t.Fatal(err)
	}
	if sum, _ := st.Summary(ctx, q); sum.Visits != 0 || sum.Pageviews != 0 {
		t.Errorf("deleted: %+v", sum)
	}
}

func TestUmamiWithoutVisitID(t *testing.T) {
	st, site := testStore(t)
	csv := "created_at,session_id,event_type,event_name,url_path\n" +
		"2026-01-01T10:00:00Z,s1,1,,/\n2026-01-01T10:10:00Z,s1,1,,/a\n2026-01-01T11:00:00Z,s1,1,,/b\n"
	res, err := Run(context.Background(), st, site, Umami, writeFile(t, csv), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Import.Visits != 2 {
		t.Errorf("a 50-minute gap should start a new visit: %+v", res.Import)
	}
}

func TestUmamiBadFiles(t *testing.T) {
	st, site := testStore(t)
	for name, body := range map[string]string{
		"no columns": "a,b\n1,2\n",
		"bad time":   umamiHeader + "yesterday,s1,v1,1,,/,,,,,,,,,\n",
		"only later": "", // replaced below
	} {
		if name == "only later" {
			if err := st.RecordEvent(context.Background(), store.Event{SiteID: site.ID, Time: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), Visitor: 1, Name: "pageview", Path: "/"}); err != nil {
				t.Fatal(err)
			}
			body = umamiHeader + "2026-01-01 10:00:00+00,s1,v1,1,,/,,,,,,,,,\n"
		}
		_, err := Run(context.Background(), st, site, Umami, writeFile(t, body), time.UTC)
		if !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		} else if !strings.Contains(err.Error(), "invalid file") {
			t.Errorf("%s: message %q", name, err)
		}
	}
	if ims, _ := st.Imports(context.Background(), site.ID); len(ims) != 0 {
		t.Errorf("failed imports left behind: %v", ims)
	}
}

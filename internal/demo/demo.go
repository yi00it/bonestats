// SPDX-License-Identifier: AGPL-3.0-only

// Package demo fills the database with realistic, made-up traffic so the
// dashboard can be explored before any real site sends data.
package demo

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"bonestats/internal/store"
)

type Options struct {
	Domain   string
	Days     int
	Visitors int // average visitors per day
	Location *time.Location
	Replace  bool // delete an existing site with the same domain first
}

type Result struct {
	Visits, Pageviews, Events int
}

type weighted[T any] struct {
	v T
	w float64
}

func pick[T any](r *rand.Rand, items []weighted[T]) T {
	total := 0.0
	for _, it := range items {
		total += it.w
	}
	x := r.Float64() * total
	for _, it := range items {
		if x -= it.w; x < 0 {
			return it.v
		}
	}
	return items[len(items)-1].v
}

type place struct{ region, city string }

type campaign struct{ source, medium, name, term, content string }

var (
	landingPages = []weighted[string]{
		{"/", 40}, {"/blog/privacy-first-analytics", 12}, {"/blog/core-web-vitals-guide", 9},
		{"/blog/gdpr-checklist", 7}, {"/pricing", 8}, {"/features", 7}, {"/docs/getting-started", 6},
		{"/blog", 4}, {"/about", 3}, {"/docs", 4},
	}
	nextPages = []weighted[string]{
		{"/pricing", 20}, {"/features", 16}, {"/docs", 10}, {"/docs/getting-started", 10},
		{"/blog", 9}, {"/signup", 8}, {"/about", 6}, {"/contact", 4}, {"/", 8},
		{"/blog/privacy-first-analytics", 3}, {"/blog/core-web-vitals-guide", 2},
	}
	// referrer host -> paths it links from (empty = sends origin only)
	referrers = []weighted[string]{
		{"", 40}, {"google.com", 26}, {"duckduckgo.com", 5}, {"news.ycombinator.com", 5},
		{"reddit.com", 5}, {"github.com", 4}, {"linkedin.com", 3}, {"bing.com", 3},
		{"x.com", 3}, {"mastodon.social", 2}, {"ecosia.org", 2}, {"dev.to", 2}, {"youtube.com", 1},
	}
	referrerPaths = map[string][]string{
		"news.ycombinator.com": {"/item"},
		"reddit.com":           {"/r/selfhosted", "/r/privacy", "/r/webdev"},
		"github.com":           {"/awesome-selfhosted/awesome-selfhosted", "/bonestats/bonestats"},
		"dev.to":               {"/maria/privacy-first-analytics-3kd1"},
		"mastodon.social":      {"/@bonestats"},
	}
	campaigns = []weighted[campaign]{
		{campaign{"newsletter", "email", "october-update", "", "header-link"}, 3},
		{campaign{"newsletter", "email", "october-update", "", "footer-link"}, 2},
		{campaign{"twitter", "social", "launch", "", ""}, 3},
		{campaign{"producthunt", "referral", "ph-launch", "", ""}, 2},
		{campaign{"linkedin", "social", "launch", "", "carousel"}, 2},
		{campaign{"google", "cpc", "brand-search", "privacy analytics", "ad-a"}, 2},
		{campaign{"google", "cpc", "brand-search", "google analytics alternative", "ad-b"}, 2},
	}
	countries = []weighted[string]{
		{"DE", 18}, {"US", 14}, {"NL", 8}, {"FR", 8}, {"GB", 7}, {"TR", 6}, {"ES", 5},
		{"IT", 5}, {"PL", 4}, {"SE", 4}, {"AT", 3}, {"CH", 3}, {"BE", 3}, {"CA", 3},
		{"IN", 3}, {"DK", 2}, {"FI", 2}, {"BR", 2}, {"JP", 2},
	}
	places = map[string][]weighted[place]{
		"DE": {{place{"Bavaria", "Munich"}, 4}, {place{"Berlin", "Berlin"}, 5}, {place{"Hamburg", "Hamburg"}, 3}, {place{"North Rhine-Westphalia", "Cologne"}, 3}, {place{"Hesse", "Frankfurt am Main"}, 2}},
		"US": {{place{"California", "San Francisco"}, 4}, {place{"New York", "New York"}, 4}, {place{"Texas", "Austin"}, 2}, {place{"Washington", "Seattle"}, 2}, {place{"Illinois", "Chicago"}, 2}},
		"NL": {{place{"North Holland", "Amsterdam"}, 5}, {place{"South Holland", "Rotterdam"}, 2}, {place{"Utrecht", "Utrecht"}, 2}},
		"FR": {{place{"Île-de-France", "Paris"}, 6}, {place{"Auvergne-Rhône-Alpes", "Lyon"}, 2}, {place{"Occitanie", "Toulouse"}, 1}},
		"GB": {{place{"England", "London"}, 6}, {place{"England", "Manchester"}, 2}, {place{"Scotland", "Edinburgh"}, 1}},
		"TR": {{place{"Istanbul", "Istanbul"}, 6}, {place{"Ankara", "Ankara"}, 2}, {place{"Izmir", "Izmir"}, 2}},
		"ES": {{place{"Madrid", "Madrid"}, 3}, {place{"Catalonia", "Barcelona"}, 3}},
		"IT": {{place{"Lombardy", "Milan"}, 3}, {place{"Lazio", "Rome"}, 3}},
		"PL": {{place{"Mazovia", "Warsaw"}, 3}, {place{"Lesser Poland", "Krakow"}, 2}},
		"SE": {{place{"Stockholm", "Stockholm"}, 4}, {place{"Västra Götaland", "Gothenburg"}, 1}},
		"AT": {{place{"Vienna", "Vienna"}, 4}, {place{"Styria", "Graz"}, 1}},
		"CH": {{place{"Zurich", "Zurich"}, 3}, {place{"Geneva", "Geneva"}, 1}},
		"BE": {{place{"Brussels Capital", "Brussels"}, 2}, {place{"Flanders", "Antwerp"}, 2}},
		"CA": {{place{"Ontario", "Toronto"}, 3}, {place{"British Columbia", "Vancouver"}, 2}},
		"IN": {{place{"Karnataka", "Bengaluru"}, 3}, {place{"Maharashtra", "Mumbai"}, 2}},
		"DK": {{place{"Capital Region", "Copenhagen"}, 1}},
		"FI": {{place{"Uusimaa", "Helsinki"}, 1}},
		"BR": {{place{"São Paulo", "São Paulo"}, 1}},
		"JP": {{place{"Tokyo", "Tokyo"}, 1}},
	}
	desktopOS = []weighted[string]{{"Windows", 50}, {"macOS", 35}, {"Linux", 12}, {"ChromeOS", 3}}
	versions  = map[string][]weighted[string]{
		"Chrome": {{"141", 50}, {"140", 30}, {"139", 12}, {"138", 8}}, "Safari": {{"18", 70}, {"17", 25}, {"16", 5}},
		"Firefox": {{"144", 45}, {"143", 35}, {"140", 20}}, "Edge": {{"141", 60}, {"140", 40}},
		"Samsung Internet": {{"28", 60}, {"27", 40}}, "Opera": {{"122", 100}}, "Vivaldi": {{"7", 100}},
	}
	osVersions = map[string][]weighted[string]{
		"Windows": {{"Windows 10/11", 95}, {"Windows 7", 5}}, "macOS": {{"macOS 10.15+", 100}},
		"iOS": {{"iOS 18", 70}, {"iOS 17", 25}, {"iOS 16", 5}}, "Android": {{"Android 15", 40}, {"Android 14", 35}, {"Android 13", 25}},
	}
	// hourly traffic shape (local time), peaks late morning and evening
	hourWeights = []float64{1, .6, .4, .3, .3, .5, 1, 2, 3.5, 4.5, 5, 5, 4.5, 4.5, 4.5, 4.2, 4, 4, 4.2, 4.8, 5, 4.2, 3, 1.8}
	outbound    = []weighted[string]{{"https://github.com/bonestats/bonestats", 5}, {"https://x.com/bonestats", 2}, {"https://web.dev/articles/vitals", 3}, {"https://gdpr.eu/checklist", 2}}
	brokenPaths = []weighted[string]{{"/blog/old-launch-post", 4}, {"/docs/v1/install", 3}, {"/pricing-2024", 2}, {"/wp-login.php", 1}}
)

func browserFor(r *rand.Rand, os string) string {
	switch os {
	case "iOS":
		return pick(r, []weighted[string]{{"Safari", 80}, {"Chrome", 15}, {"Firefox", 5}})
	case "Android":
		return pick(r, []weighted[string]{{"Chrome", 75}, {"Samsung Internet", 18}, {"Firefox", 4}, {"Opera", 3}})
	case "macOS":
		return pick(r, []weighted[string]{{"Chrome", 45}, {"Safari", 40}, {"Firefox", 10}, {"Edge", 5}})
	case "ChromeOS":
		return "Chrome"
	default:
		return pick(r, []weighted[string]{{"Chrome", 58}, {"Edge", 20}, {"Firefox", 15}, {"Opera", 4}, {"Vivaldi", 3}})
	}
}

// lognormal returns a value whose median is m with spread s.
func lognormal(r *rand.Rand, m, s float64) float64 { return m * math.Exp(r.NormFloat64()*s) }

func Seed(ctx context.Context, st *store.Store, o Options) (Result, error) {
	var res Result
	domain := store.NormalizeDomain(o.Domain)
	if o.Replace {
		if old, err := st.SiteByDomain(ctx, domain); err == nil {
			if err := st.DeleteSite(ctx, old.ID); err != nil {
				return res, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return res, err
		}
	}
	if err := st.AddSite(ctx, domain); err != nil {
		return res, err
	}
	site, err := st.SiteByDomain(ctx, domain)
	if err != nil {
		return res, err
	}
	if err := seedGoals(ctx, st, site.ID); err != nil {
		return res, err
	}

	r := rand.New(rand.NewPCG(42, uint64(len(domain))))
	now := time.Now().In(o.Location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, o.Location)
	spikeDay := o.Days / 4 // a "front page of Hacker News" day

	hourTotal := 0.0
	for _, w := range hourWeights {
		hourTotal += w
	}

	for d := o.Days - 1; d >= 0; d-- {
		day := today.AddDate(0, 0, -d)
		progress := 1 - float64(d)/float64(o.Days) // traffic grows over time
		n := float64(o.Visitors) * (0.7 + 0.6*progress) * (0.85 + 0.3*r.Float64())
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			n *= 0.65
		}
		spike := d == spikeDay || d == spikeDay-1
		if d == spikeDay {
			n *= 4
		} else if d == spikeDay-1 {
			n *= 1.8
		}
		for i := 0; i < int(n); i++ {
			// pick a start time following the daily traffic curve
			x, hour := r.Float64()*hourTotal, 0
			for hour < 23 && x > hourWeights[hour] {
				x -= hourWeights[hour]
				hour++
			}
			start := day.Add(time.Duration(hour)*time.Hour + time.Duration(r.IntN(3600))*time.Second)
			if !start.Before(now) {
				continue
			}
			ref := pick(r, referrers)
			if spike && r.Float64() < 0.6 {
				ref = "news.ycombinator.com"
			}
			if err := visit(ctx, st, r, site.ID, start, now, ref, &res); err != nil {
				return res, err
			}
		}
	}
	// a few people on the site right now
	for i := 0; i < 2+r.IntN(4); i++ {
		start := now.Add(-time.Duration(30+r.IntN(200)) * time.Second)
		if err := visit(ctx, st, r, site.ID, start, now, pick(r, referrers), &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// seedGoals sets up the goals and funnels the demo traffic converts on.
func seedGoals(ctx context.Context, st *store.Store, siteID int64) error {
	for _, g := range []store.Goal{
		{EventName: "signup"}, {EventName: "start_trial"}, {EventName: "purchase", Currency: "EUR"},
		{PagePath: "/pricing"}, {PagePath: "/signup"}, {PagePath: "/blog/*"},
		{EventName: "newsletter_subscribe"}, {EventName: "File Download"}, {EventName: "Outbound Link: Click"}, {EventName: "404"},
	} {
		g.SiteID = siteID
		if err := st.AddGoal(ctx, g); err != nil {
			return err
		}
	}
	goals, err := st.Goals(ctx, siteID)
	if err != nil {
		return err
	}
	id := map[string]int64{}
	for _, g := range goals {
		id[g.Name()] = g.ID
	}
	for _, f := range []store.Funnel{
		{Name: "Signup flow", Steps: []int64{id["Visit /pricing"], id["Visit /signup"], id["signup"], id["purchase"]}},
		{Name: "Blog to signup", Steps: []int64{id["Visit /blog/*"], id["Visit /pricing"], id["signup"]}},
	} {
		f.SiteID = siteID
		if err := st.AddFunnel(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

func visit(ctx context.Context, st *store.Store, r *rand.Rand, siteID int64, t, now time.Time, ref string, res *Result) error {
	device := pick(r, []weighted[string]{{"Desktop", 58}, {"Mobile", 37}, {"Tablet", 5}})
	var os, screen string
	switch device {
	case "Desktop":
		os = pick(r, desktopOS)
		screen = pick(r, []weighted[string]{{"1440–1919px", 35}, {"≥ 1920px", 30}, {"1280–1439px", 25}, {"992–1279px", 10}})
	case "Tablet":
		os = pick(r, []weighted[string]{{"iOS", 65}, {"Android", 35}})
		screen = pick(r, []weighted[string]{{"768–991px", 70}, {"992–1279px", 30}})
	default:
		os = pick(r, []weighted[string]{{"iOS", 52}, {"Android", 48}})
		screen = "< 576px"
	}
	browser := browserFor(r, os)
	country := pick(r, countries)
	v := store.Visit{Browser: browser, OS: os, Device: device, Screen: screen, Country: country}
	if vs := versions[browser]; vs != nil {
		v.BrowserVersion = browser + " " + pick(r, vs)
	}
	if vs := osVersions[os]; vs != nil {
		v.OSVersion = pick(r, vs)
	}
	if ps := places[country]; ps != nil {
		p := pick(r, ps)
		v.Region, v.City = p.region, p.city
	}
	if r.Float64() < 0.12 {
		c := pick(r, campaigns)
		v.UTMSource, v.UTMMedium, v.UTMCampaign, v.UTMTerm, v.UTMContent = c.source, c.medium, c.name, c.term, c.content
		ref = ""
	}
	if ref != "" {
		v.Referrer, v.ReferrerURL = ref, ref
		if paths := referrerPaths[ref]; paths != nil {
			v.ReferrerURL += paths[r.IntN(len(paths))]
		}
	}
	v.Channel = store.Channel(v.Referrer, v.UTMSource, v.UTMMedium)

	visitor := r.Int64()
	pages := 1
	if r.Float64() > 0.45 { // ~45% bounce
		pages = 2 + int(r.ExpFloat64()*1.5)
	}
	path := pick(r, landingPages)
	if r.Float64() < 0.015 {
		path = pick(r, brokenPaths)
	}
	plan := ""
	record := func(e store.Event) error {
		e.SiteID, e.Visitor, e.Visit = siteID, visitor, v
		if e.Path == "" {
			e.Path = path
		}
		if err := st.RecordEvent(ctx, e); err != nil {
			return err
		}
		if e.Name == "pageview" {
			res.Pageviews++
		} else {
			res.Events++
		}
		return nil
	}

	for p := 0; p < pages && t.Before(now); p++ {
		if err := record(store.Event{Time: t, Name: "pageview"}); err != nil {
			return err
		}
		stay := time.Duration(5+r.ExpFloat64()*50) * time.Second
		mid := t.Add(stay / 2)

		switch {
		case strings.HasPrefix(path, "/blog/old") || strings.HasPrefix(path, "/docs/v1") || path == "/pricing-2024" || path == "/wp-login.php":
			if err := record(store.Event{Time: t, Name: "404", Props: map[string]string{"path": path}}); err != nil {
				return err
			}
			pages = p + 1 // they leave
		case path == "/signup" && r.Float64() < 0.4:
			plan = pick(r, []weighted[string]{{"free", 55}, {"pro", 35}, {"business", 10}})
			if err := record(store.Event{Time: mid, Name: "signup", Props: map[string]string{"plan": plan}}); err != nil {
				return err
			}
			if plan != "free" && r.Float64() < 0.5 {
				amount := map[string]float64{"pro": 29, "business": 99}[plan]
				if r.Float64() < 0.3 {
					amount *= 10 // yearly
				}
				if err := record(store.Event{Time: mid.Add(20 * time.Second), Name: "purchase",
					Props: map[string]string{"plan": plan}, Revenue: &amount}); err != nil {
					return err
				}
			}
		case path == "/pricing" && r.Float64() < 0.1:
			trial := pick(r, []weighted[string]{{"pro", 75}, {"business", 25}})
			if err := record(store.Event{Time: mid, Name: "start_trial", Props: map[string]string{"plan": trial}}); err != nil {
				return err
			}
		case path == "/docs/getting-started" && r.Float64() < 0.15:
			if err := record(store.Event{Time: mid, Name: "File Download",
				Props: map[string]string{"url": "https://demo.example.com/files/getting-started.pdf"}}); err != nil {
				return err
			}
		case strings.HasPrefix(path, "/blog/") && r.Float64() < 0.06:
			if err := record(store.Event{Time: mid, Name: "newsletter_subscribe"}); err != nil {
				return err
			}
		case r.Float64() < 0.05:
			if err := record(store.Event{Time: mid, Name: "Outbound Link: Click",
				Props: map[string]string{"url": pick(r, outbound)}}); err != nil {
				return err
			}
		}

		t = t.Add(stay)
		if t.After(now) {
			t = now
		}
		scroll := int64(math.Min(100, math.Max(10, lognormal(r, scrollMedian(path), 0.35))))
		engaged := int64(float64(stay.Milliseconds()) * (0.6 + 0.4*r.Float64()))
		if _, err := st.RecordLeave(ctx, store.Leave{SiteID: siteID, Visitor: visitor, Time: t, Path: path, Scroll: &scroll, Engaged: &engaged}); err != nil {
			return err
		}
		if r.Float64() < 0.75 { // not every browser reports vitals
			if err := st.RecordVitals(ctx, vitals(r, siteID, t, path, device)); err != nil {
				return err
			}
		}
		if path == "/pricing" && r.Float64() < 0.35 {
			path = "/signup"
		} else {
			path = pick(r, nextPages)
		}
		t = t.Add(time.Duration(1+r.IntN(4)) * time.Second)
	}
	res.Visits++
	return nil
}

func scrollMedian(path string) float64 {
	switch {
	case strings.HasPrefix(path, "/blog/"):
		return 70
	case path == "/pricing", path == "/signup":
		return 80
	case path == "/":
		return 55
	default:
		return 60
	}
}

func vitals(r *rand.Rand, siteID int64, t time.Time, path, device string) store.Vitals {
	slow := 1.0
	if device == "Mobile" {
		slow = 1.5
	}
	if strings.HasPrefix(path, "/blog/") {
		slow *= 1.35 // heavy images
	}
	ptr := func(f float64) *float64 { return &f }
	ttfb := lognormal(r, 220, 0.5)
	fcp := ttfb + lognormal(r, 700*slow, 0.4)
	lcp := fcp + lognormal(r, 600*slow, 0.6)
	v := store.Vitals{
		SiteID: siteID, Time: t, Path: path, Device: device,
		TTFB: ptr(math.Round(ttfb)), FCP: ptr(math.Round(fcp)), LCP: ptr(math.Round(lcp)),
	}
	cls := 0.0
	if r.Float64() < 0.35 {
		cls = math.Round(r.ExpFloat64()*0.06*slow*1000) / 1000
	}
	v.CLS = ptr(cls)
	if r.Float64() < 0.6 { // only visits with interactions have INP
		v.INP = ptr(math.Round(lognormal(r, 90*slow, 0.6)))
	}
	return v
}

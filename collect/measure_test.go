// SPDX-License-Identifier: AGPL-3.0-only

package collect

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

// Readable slugs and file names keep their path; tokens still don't.
func TestScrubPathKeepsSlugs(t *testing.T) {
	cases := map[string]string{
		"/blog/Top-10-JavaScript-Frameworks":           "/blog/Top-10-JavaScript-Frameworks",
		"/haber/iPhone-17-Pro-Max-incelemesi":          "/haber/iPhone-17-Pro-Max-incelemesi",
		"/haber/iOS-18-ile-gelen-yenilikler":           "/haber/iOS-18-ile-gelen-yenilikler",
		"/tv/Samsung-4K-TV-incelemesi-2026":            "/tv/Samsung-4K-TV-incelemesi-2026",
		"/files/Annual_Report_2025_Final.pdf":          "/files/Annual_Report_2025_Final.pdf",
		"/invite/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM": "/invite/[id]",
		"/key/aB3xK-9zLm2-pXy7w-Qr5tV":                 "/key/[id]",
		"/t/dGhpc0lzQV9zZWNyZXRfdG9rZW4x":              "/t/[id]",
	}
	for in, want := range cases {
		if got := scrubPath(in); got != want {
			t.Errorf("scrubPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanPath("/yaz%C4%B1l%C4%B1m/"); got != "/yazılım" {
		t.Errorf("cleanPath = %q, want /yazılım", got)
	}
	if got := cleanPath("/users/jane%40example.com"); got != "/users/[email]" {
		t.Errorf("cleanPath = %q", got)
	}
}

func TestDevicesAndVersions(t *testing.T) {
	const (
		chromeAndroid = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36"
		samsung       = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/27.0 Chrome/125.0.0.0 Mobile Safari/537.36"
		androidTablet = "Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/27.0 Chrome/125.0.0.0 Safari/537.36"
		safari26      = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Mobile/15E148 Safari/604.1"
		instagram     = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/22F76 Instagram 385.0.0 (iPhone16,1; iOS 18_5; tr_TR)"
		mac           = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15"
		windows       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
		cubot         = "Mozilla/5.0 (Linux; Android 13; CUBOT KINGKONG 9 Build/TP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/120.0 Mobile Safari/537.36 [FB_IAB/FB4A]"
	)
	versions := []struct{ ua, os, want string }{
		{chromeAndroid, "Android", ""}, {samsung, "Android", "Android 14"}, {safari26, "iOS", "iOS 26"}, {instagram, "iOS", "iOS 18"},
	}
	for _, c := range versions {
		if got := osVersion(c.ua, c.os); got != c.want {
			t.Errorf("osVersion(%.40q) = %q, want %q", c.ua, got, c.want)
		}
	}
	devices := []struct {
		ua           string
		width, touch int
		want         string
	}{
		{chromeAndroid, 844, 5, "Mobile"}, // held sideways
		{androidTablet, 800, 5, "Tablet"},
		{windows, 700, 0, "Desktop"}, // narrow window
		{windows, 0, 0, "Desktop"},
		{mac, 1180, 0, "Desktop"},
		{mac, 1180, 5, "Tablet"}, // iPad asking for desktop sites
		{instagram, 390, 5, "Mobile"},
		{"SomethingNew/1.0", 400, 0, "Mobile"},
		{"SomethingNew/1.0", 0, 0, ""},
	}
	for _, c := range devices {
		if got := deviceClass(c.ua, c.width, c.touch); got != c.want {
			t.Errorf("deviceClass(%.40q, %d, %d) = %q, want %q", c.ua, c.width, c.touch, got, c.want)
		}
	}
	if got := parseBrowser(instagram); got != "Instagram" {
		t.Errorf("browser = %q, want Instagram", got)
	}
	if got := parseBrowser(cubot); got != "Facebook" {
		t.Errorf("browser = %q, want Facebook", got)
	}
	if isBot(cubot) {
		t.Error("a Cubot phone is not a bot")
	}
	if !isBot("Mozilla/5.0 (compatible; Google-InspectionTool/1.0;)") {
		t.Error("Google-InspectionTool is a bot")
	}
}

func TestVisitorHashUsesIPv6Network(t *testing.T) {
	salt, ua := []byte("salt"), "ua"
	hash := func(ip string) int64 { return visitorHash(salt, "example.com", netip.MustParseAddr(ip), ua) }
	if hash("2001:db8:1:2::1") != hash("2001:db8:1:2:aaaa:bbbb:cccc:dddd") {
		t.Error("two addresses in one /64 are two visitors")
	}
	if hash("2001:db8:1:2::1") == hash("2001:db8:1:3::1") {
		t.Error("two /64 networks are one visitor")
	}
	if hash("203.0.113.1") == hash("203.0.113.2") {
		t.Error("two IPv4 addresses are one visitor")
	}
}

type testSite struct {
	t    *testing.T
	st   *store.Store
	site store.Site
	c    *Collector
}

func newTestSite(t *testing.T) *testSite {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	geo, _ := OpenGeo("")
	return &testSite{t, st, site, &Collector{Store: st, Geo: geo, Location: time.UTC}}
}

// read sends a pageview and a leave beacon that makes the visit count.
func (s *testSite) read(ip, ua, pageview string) {
	for _, body := range []string{pageview, `{"d":"example.com","n":"leave","p":"/","et":5000}`} {
		r := httptest.NewRequest("POST", "/api/event", strings.NewReader(body))
		r.Header.Set("User-Agent", ua)
		r.RemoteAddr = ip + ":5000"
		s.c.ServeHTTP(httptest.NewRecorder(), r)
	}
}

func (s *testSite) labels(dim string) map[string]bool {
	s.t.Helper()
	now := time.Now()
	rows, err := s.st.Breakdown(context.Background(), store.Query{SiteID: s.site.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour)}, dim, 20)
	if err != nil {
		s.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Label] = true
	}
	return out
}

// An ad click ID marks a paid visit, sources get their names, and an iPad
// that calls itself a Mac is a tablet running iOS.
func TestVisitAttributes(t *testing.T) {
	s := newTestSite(t)
	const firefox = "Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/131.0"
	const ipad = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Safari/605.1.15"
	s.read("203.0.113.1", firefox, `{"d":"example.com","n":"pageview","p":"/","w":1200,"r":"https://www.google.com.tr/","c":"gclid"}`)
	s.read("203.0.113.2", firefox, `{"d":"example.com","n":"pageview","p":"/","w":1200,"r":"https://www.google.com/"}`)
	s.read("203.0.113.3", firefox, `{"d":"example.com","n":"pageview","p":"/","w":1200,"r":"android-app://com.google.android.gm/"}`)
	s.read("203.0.113.4", ipad, `{"d":"example.com","n":"pageview","p":"/","w":1180,"t":5,"source":"partner"}`)
	s.read("203.0.113.5", firefox, `{"d":"example.com","n":"pageview","p":"/","w":1200}`)

	if got := s.labels("channel"); len(got) != 5 || !got["Paid Search"] || !got["Organic Search"] || !got["Email"] || !got["Referral"] || !got["Direct"] {
		t.Errorf("channels = %v", got)
	}
	if got := s.labels("source"); len(got) != 4 || !got["Google"] || !got["Gmail"] || !got["partner"] || !got[store.DirectSource] {
		t.Errorf("sources = %v", got)
	}
	if got := s.labels("device"); len(got) != 2 || !got["Tablet"] || !got["Desktop"] {
		t.Errorf("devices = %v", got)
	}
	if got := s.labels("os"); !got["iOS"] || got["macOS"] {
		t.Errorf("os = %v", got)
	}
}

// A visit in progress at midnight continues under yesterday's hash; anyone
// arriving after midnight gets today's.
func TestVisitContinuesAcrossMidnight(t *testing.T) {
	ctx := context.Background()
	s := newTestSite(t)
	ip, other, ua := netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2"), "ua"
	before := time.Date(2026, 1, 1, 23, 50, 0, 0, time.UTC)
	after := before.Add(15 * time.Minute)
	visitor := func(at time.Time, ip netip.Addr) int64 {
		t.Helper()
		v, err := s.c.visitor(ctx, s.st, s.site.ID, at, "example.com", ip, ua)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v, otherBefore := visitor(before, ip), visitor(before, other)
	if err := s.st.RecordEvent(ctx, store.Event{SiteID: s.site.ID, Time: before, Visitor: v, Name: "pageview", Path: "/"}); err != nil {
		t.Fatal(err)
	}
	if err := s.c.RotateSalt(ctx, after); err != nil { // the periodic job runs first
		t.Fatal(err)
	}
	if got := visitor(after, ip); got != v {
		t.Error("the visit in progress got a new visitor hash at midnight")
	}
	if visitor(after, other) == otherBefore {
		t.Error("a new visitor after midnight got yesterday's hash")
	}
	// Once the visit has timed out, and after the grace period, it is today's hash.
	late := after.Add(store.SessionTimeout)
	if got := visitor(late, ip); got == v {
		t.Error("yesterday's hash is still used after the grace period")
	}
	if s.c.RotateSalt(ctx, late); s.c.prevSalt != nil {
		t.Error("yesterday's salt is still in memory after the grace period")
	}
}

package collect

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestReferrer(t *testing.T) {
	cases := map[string][2]string{
		"https://www.google.com/":                    {"google.com", "google.com"},
		"https://www.reddit.com/r/selfhosted/?utm=x": {"reddit.com", "reddit.com/r/selfhosted"},
		"https://example.com/page":                   {"", ""},
		"https://blog.example.com/":                  {"", ""},
		"not a url":                                  {"", ""},
		"":                                           {"", ""},
	}
	for in, want := range cases {
		if h, u := referrer(in, "example.com"); h != want[0] || u != want[1] {
			t.Errorf("referrer(%q) = %q, %q, want %q", in, h, u, want)
		}
	}
}

func TestVersions(t *testing.T) {
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.7390.54 Safari/537.36"
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1"
	if got := browserVersion(chrome, "Chrome"); got != "Chrome 141" {
		t.Errorf("chrome version = %q", got)
	}
	if got := osVersion(chrome, "Windows"); got != "Windows 10/11" {
		t.Errorf("windows version = %q", got)
	}
	if got := browserVersion(iphone, "Safari") + " / " + osVersion(iphone, "iOS"); got != "Safari 18 / iOS 18" {
		t.Errorf("iphone = %q", got)
	}
}

func TestCleanProps(t *testing.T) {
	got := cleanProps(map[string]any{"plan": "pro", "seats": 3.0, "ok": true, "bad key!": "x", "obj": map[string]any{}})
	if len(got) != 3 || got["plan"] != "pro" || got["seats"] != "3" || got["ok"] != "true" {
		t.Errorf("cleanProps = %v", got)
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{"/a/b/?x=1#y": "/a/b", "": "/", "/": "/", "page": "/page"}
	for in, want := range cases {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	if !originAllowed("https://www.example.com", "example.com") || !originAllowed("https://shop.example.com", "example.com") {
		t.Error("own origin rejected")
	}
	if originAllowed("https://notexample.com", "example.com") || originAllowed("https://example.com.evil.io", "example.com") {
		t.Error("foreign origin accepted")
	}
}

func TestUserAgents(t *testing.T) {
	ff := "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.5; rv:131.0) Gecko/20100101 Firefox/131.0"
	edge := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0"
	if parseBrowser(ff) != "Firefox" || parseOS(ff) != "macOS" {
		t.Errorf("firefox: %s %s", parseBrowser(ff), parseOS(ff))
	}
	if parseBrowser(edge) != "Edge" || parseOS(edge) != "Windows" {
		t.Errorf("edge: %s %s", parseBrowser(edge), parseOS(edge))
	}
	if !isBot("Mozilla/5.0 (compatible; Googlebot/2.1)") || isBot(ff) {
		t.Error("bot detection")
	}
}

func TestGeo(t *testing.T) {
	path := "../../data/dbip-country-lite.mmdb"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no GeoIP database downloaded")
	}
	g, err := OpenGeo(path)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if loc := g.Lookup(netip.MustParseAddr("8.8.8.8")); loc.Country != "US" {
		t.Errorf("8.8.8.8 = %+v, want US", loc)
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote string, headers map[string][]string) *http.Request {
		r := httptest.NewRequest("POST", "/api/event", nil)
		r.RemoteAddr = remote
		for k, vs := range headers {
			for _, v := range vs {
				r.Header.Add(k, v)
			}
		}
		return r
	}
	cases := []struct {
		name   string
		r      *http.Request
		header string
		want   string
	}{
		{"direct", req("203.0.113.9:5000", nil), "", "203.0.113.9"},
		{"untrusted header ignored", req("203.0.113.9:5000", map[string][]string{"X-Forwarded-For": {"1.2.3.4"}}), "", "203.0.113.9"},
		{"proxy", req("127.0.0.1:5000", map[string][]string{"X-Forwarded-For": {"198.51.100.7"}}), "X-Forwarded-For", "198.51.100.7"},
		{"forged entry skipped", req("127.0.0.1:5000", map[string][]string{"X-Forwarded-For": {"6.6.6.6, 198.51.100.7"}}), "X-Forwarded-For", "198.51.100.7"},
		{"forged header line skipped", req("127.0.0.1:5000", map[string][]string{"X-Forwarded-For": {"6.6.6.6", "198.51.100.7"}}), "X-Forwarded-For", "198.51.100.7"},
		{"x-real-ip", req("127.0.0.1:5000", map[string][]string{"X-Real-Ip": {"198.51.100.8"}}), "X-Real-IP", "198.51.100.8"},
		// A proxy that only sets X-Real-IP passes the client's own
		// X-Forwarded-For through; it must not be believed.
		{"other header ignored", req("127.0.0.1:5000", map[string][]string{"X-Real-Ip": {"198.51.100.8"}, "X-Forwarded-For": {"6.6.6.6"}}), "X-Real-IP", "198.51.100.8"},
		{"no fallback to x-real-ip", req("127.0.0.1:5000", map[string][]string{"X-Real-Ip": {"6.6.6.6"}}), "X-Forwarded-For", "127.0.0.1"},
		{"ipv4-mapped", req("[::ffff:203.0.113.9]:5000", nil), "", "203.0.113.9"},
	}
	for _, c := range cases {
		if got := ClientIP(c.r, c.header).String(); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestScrubPath(t *testing.T) {
	cases := map[string]string{
		"/": "/",
		"/blog/2024-01-15-my-first-post-about-go":      "/blog/2024-01-15-my-first-post-about-go",
		"/users/jane@example.com/settings":             "/users/[email]/settings",
		"/users/jane%40example.com":                    "/users/[email]",
		"/orders/3f2b8c1e-9d4a-4f6b-a1c2-7e5d9b0a6c4f": "/orders/[id]",
		"/reset/9f86d081884c7d659a2feaa0c55ad015":      "/reset/[id]",
		"/invite/eyJhbGciOiJIUzI1NiJ9xQ3kZ7pLmN2vR8sT": "/invite/[id]",
		"/products/12345":                              "/products/12345",
		"/category/SummerSale":                         "/category/SummerSale",
	}
	for in, want := range cases {
		if got := scrubPath(in); got != want {
			t.Errorf("scrubPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanPath("/reset/9f86d081884c7d659a2feaa0c55ad015?x=1"); got != "/reset/[id]" {
		t.Errorf("cleanPath = %q", got)
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(60) // 1 per second, burst 60
	ip := netip.MustParseAddr("203.0.113.9")
	now := time.Now()
	for i := 0; i < 60; i++ {
		if !l.allow(ip, now) {
			t.Fatalf("beacon %d of the burst refused", i)
		}
	}
	if l.allow(ip, now) {
		t.Error("beacon beyond the burst accepted")
	}
	if !l.allow(netip.MustParseAddr("203.0.113.10"), now) {
		t.Error("another client was limited too")
	}
	if !l.allow(ip, now.Add(1100*time.Millisecond)) {
		t.Error("bucket did not refill")
	}
	// IPv6 clients share a bucket per /64.
	a, b := netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::ffff")
	for i := 0; i < 60; i++ {
		l.allow(a, now)
	}
	if l.allow(b, now) {
		t.Error("same /64 got a fresh bucket")
	}
	if !newRateLimiter(0).allow(ip, now) {
		t.Error("0 should mean unlimited")
	}
}

package collect

import (
	"net/netip"
	"os"
	"testing"
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

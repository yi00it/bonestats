// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestSettingsPages(t *testing.T) {
	s, get := dashboardClient(t)
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))

	if body := get("/sites").Body.String(); !strings.Contains(body, "No sites yet") {
		t.Error("empty sites page")
	}

	// Adding a site opens its settings, where the snippet is.
	w := post(h, "/sites", "domain="+url.QueryEscape("https://www.Example.com/"), c)
	sites, _ := s.Store.Sites(t.Context())
	if len(sites) != 1 {
		t.Fatalf("%d sites", len(sites))
	}
	base := "/sites/" + strconv.FormatInt(sites[0].ID, 10)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != base+"/settings?added=1" {
		t.Fatalf("add site: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := post(h, "/sites", "domain=not+a+domain", c); !strings.Contains(w.Header().Get("Location"), "/sites?error=domain") {
		t.Errorf("bad domain: %q", w.Header().Get("Location"))
	}

	body := get("/sites").Body.String()
	if !strings.Contains(body, `href="`+base+`/settings"`) || strings.Contains(body, "bs.js") || strings.Contains(body, "Stats API") {
		t.Error("sites page should only list the sites")
	}

	body = get(base + "/settings?added=1").Body.String()
	for _, want := range []string{`data-domain="example.com"`, "is added", `action="` + base + `/delete"`,
		`href="` + base + `/settings" aria-current="page"`, `href="` + base + `/goals"`} {
		if !strings.Contains(body, want) {
			t.Errorf("site settings lack %q", want)
		}
	}
	if w := get("/sites/999/settings"); w.Code != http.StatusNotFound {
		t.Errorf("unknown site: %d", w.Code)
	}
	if body := get(base + "/goals").Body.String(); !strings.Contains(body, `href="`+base+`/goals" aria-current="page"`) {
		t.Error("goals tab not marked as current")
	}
	if body := get("/?site=example.com").Body.String(); !strings.Contains(body, `href="`+base+`/settings"`) {
		t.Error("dashboard doesn't link the site settings")
	}

	body = get("/settings").Body.String()
	if !strings.Contains(body, "Stats API") || !strings.Contains(body, "Privacy") {
		t.Error("settings page lacks the API keys or privacy notes")
	}
}

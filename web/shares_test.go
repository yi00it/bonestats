// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

func anonGet(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSharedLinks(t *testing.T) {
	s, get := dashboardClient(t)
	ctx := context.Background()
	addSite(t, s, "other.example")
	site := addSite(t, s, "shared.example")
	record(t, s, store.Event{SiteID: site.ID, Time: time.Now().Add(-time.Minute), Visitor: 1, Name: "pageview", Path: "/hello",
		Visit: store.Visit{Country: "DE", Channel: "Direct"}})
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	base := "/sites/" + strconv.FormatInt(site.ID, 10)

	if body := get("/sites").Body.String(); !strings.Contains(body, `href="`+base+`/share"`) {
		t.Error("sites page doesn't link the share page")
	}
	if w := post(h, base+"/shares", "name=", c); !strings.Contains(w.Header().Get("Location"), "error=share-name") {
		t.Errorf("empty name: %q", w.Header().Get("Location"))
	}
	if w := post(h, base+"/shares", "name=Team", c); w.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", w.Code)
	}
	shares, _ := s.Store.Shares(ctx, site.ID)
	if len(shares) != 1 || len(shares[0].Token) < 20 {
		t.Fatalf("shares %v", shares)
	}
	link := "/share/" + shares[0].Token
	if body := get(base + "/share").Body.String(); !strings.Contains(body, link) || !strings.Contains(body, "Team") {
		t.Error("share page doesn't list the link")
	}

	// Anyone with the link sees that site, read-only.
	w := anonGet(h, link)
	if w.Code != http.StatusOK {
		t.Fatalf("shared view: %d", w.Code)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("headers %v", w.Header())
	}
	body := w.Body.String()
	for _, want := range []string{"shared.example", "/hello", `action="` + link + `"`, `href="` + link + `?`} {
		if !strings.Contains(body, want) {
			t.Errorf("shared view lacks %q", want)
		}
	}
	for _, leak := range []string{"other.example", `href="/sites`, "/logout", "/export", "Set up goals"} {
		if strings.Contains(body, leak) {
			t.Errorf("shared view shows %q", leak)
		}
	}
	// It can't be pointed at another site, and filters work.
	if body := anonGet(h, link+"?site=other.example").Body.String(); !strings.Contains(body, "/hello") || strings.Contains(body, "other.example") {
		t.Error("site parameter switched the shared site")
	}
	if w := anonGet(h, link+"?f="+url.QueryEscape("country|is|FR")); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "/hello</a>") {
		t.Error("filter not applied in the shared view")
	}
	if w := anonGet(h, link+"?fdim=country&fop=is&fval=DE"); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), link+"?") {
		t.Errorf("filter form redirects to %q", w.Header().Get("Location"))
	}

	// Wrong tokens, the admin pages and the export stay closed.
	for _, p := range []string{"/share/nope", "/share/" + strings.ToLower(shares[0].Token)} {
		if w := anonGet(h, p); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/", "/sites", "/export?site=shared.example"} {
		if w := anonGet(h, p); w.Code != http.StatusSeeOther {
			t.Errorf("%s without login: %d", p, w.Code)
		}
	}

	// Deleting the link, or the site, turns it off.
	if w := post(h, base+"/shares/"+strconv.FormatInt(shares[0].ID, 10)+"/delete", "", c); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if w := anonGet(h, link); w.Code != http.StatusNotFound {
		t.Errorf("deleted link: %d", w.Code)
	}
	sh, _ := s.Store.AddShare(ctx, site.ID, "again")
	s.Store.DeleteSite(ctx, site.ID)
	if w := anonGet(h, "/share/"+sh.Token); w.Code != http.StatusNotFound {
		t.Errorf("link of a deleted site: %d", w.Code)
	}
}

// A host with its own login serves shared links only if it resolves them.
func TestSharedLinksWithHostLogin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	sh, _ := st.AddShare(ctx, site.ID, "x")

	s := &Server{Location: time.UTC, Collector: http.NotFoundHandler(),
		Authenticate: func(r *http.Request) (*store.Store, bool) { return st, true }}
	h := s.Handler()
	if w := anonGet(h, "/share/"+sh.Token); w.Code == http.StatusOK {
		t.Error("shared link served without ResolveShare")
	}
	if strings.Contains(anonGet(h, "/sites").Body.String(), "/share\"") {
		t.Error("share page linked without ResolveShare")
	}

	s = &Server{Location: time.UTC, Collector: http.NotFoundHandler(),
		Authenticate: func(r *http.Request) (*store.Store, bool) { return st, true },
		ResolveShare: func(r *http.Request, token string) (*store.Store, bool) { return st, token == sh.Token }}
	h = s.Handler()
	if w := anonGet(h, "/share/"+sh.Token); w.Code != http.StatusOK {
		t.Errorf("host shared link: %d", w.Code)
	}
	if w := anonGet(h, "/share/other"); w.Code != http.StatusNotFound {
		t.Errorf("unknown host link: %d", w.Code)
	}
}

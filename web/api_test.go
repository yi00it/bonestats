// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

// apiFixture is a server with one site, a little traffic and an API key.
func apiFixture(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	s, h := testServer(t)
	ctx := context.Background()
	site := addSite(t, s, "example.com")
	if err := s.Store.AddGoal(ctx, store.Goal{SiteID: site.ID, EventName: "signup", Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	de := store.Visit{Country: "DE", Region: "DE-BE", Browser: "Firefox", Channel: "Direct"}
	fr := store.Visit{Country: "FR", Browser: "Chrome", Channel: "Organic Search", Referrer: "Google"}
	rev := 29.9
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-3 * time.Minute), Visitor: 1, Name: "pageview", Path: "/", Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-2 * time.Minute), Visitor: 1, Name: "pageview", Path: "/pricing", Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-time.Minute), Visitor: 1, Name: "signup", Path: "/pricing",
		Props: map[string]string{"plan": "pro"}, Revenue: &rev, Visit: de})
	record(t, s, store.Event{SiteID: site.ID, Time: now.Add(-time.Minute), Visitor: 2, Name: "pageview", Path: "/=cmd", Visit: fr})
	key, err := s.Store.AddAPIKey(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	return s, h, key
}

func apiGet(h http.Handler, path, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", w.Body, err)
	}
	return out
}

func TestAPIKeys(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.AddAPIKey(ctx, "  "); err != store.ErrAPIKeyName {
		t.Errorf("empty name: %v", err)
	}
	key, err := st.AddAPIKey(ctx, "Grafana")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, store.APIKeyPrefix) || len(key) < 20 {
		t.Errorf("key %q", key)
	}
	for k, want := range map[string]bool{key: true, key + "x": false, "": false, "bs_nope": false, strings.TrimPrefix(key, "bs_"): false} {
		if ok, err := st.UseAPIKey(ctx, k); err != nil || ok != want {
			t.Errorf("UseAPIKey(%q) = %v, %v; want %v", k, ok, err, want)
		}
	}
	keys, err := st.APIKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys %v, %v", keys, err)
	}
	if k := keys[0]; k.Name != "Grafana" || !strings.HasPrefix(key, k.Prefix) || len(k.Prefix) >= len(key) || k.LastUsed == nil {
		t.Errorf("listed key %+v", k)
	}
	if err := st.DeleteAPIKey(ctx, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.UseAPIKey(ctx, key); ok {
		t.Error("revoked key still works")
	}
}

func TestAPIAuth(t *testing.T) {
	_, h, key := apiFixture(t)
	for _, k := range []string{"", "bs_wrong", "Bearer " + key} {
		w := apiGet(h, "/api/v1/sites", k)
		if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("key %q: status %d", k, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/sites", nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: "anything"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("cookie instead of key: status %d", w.Code)
	}
	w = apiGet(h, "/api/v1/sites", key)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	sites := decode(t, w)["sites"].([]any)
	if len(sites) != 1 || sites[0].(map[string]any)["domain"] != "example.com" {
		t.Errorf("sites %v", sites)
	}
	if w := apiGet(h, "/api/v1/nope", key); w.Code != http.StatusNotFound || decode(t, w)["error"] == nil {
		t.Errorf("unknown endpoint: status %d", w.Code)
	}
	if w := apiGet(h, "/api/v1/sites", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("status %d", w.Code)
	}
}

func TestAPISummary(t *testing.T) {
	_, h, key := apiFixture(t)
	w := apiGet(h, "/api/v1/summary?site=example.com&range=7d&compare=previous", key)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	out := decode(t, w)
	res := out["results"].(map[string]any)
	if res["visitors"] != 2.0 || res["visits"] != 2.0 || res["pageviews"] != 3.0 || res["bounce_rate"] != 50.0 {
		t.Errorf("results %v", res)
	}
	q := out["query"].(map[string]any)
	today := time.Now().UTC().Format(dateLayout)
	if q["site"] != "example.com" || q["to"] != today {
		t.Errorf("query %v", q)
	}
	if prev, ok := out["previous"].(map[string]any); !ok || prev["results"].(map[string]any)["visitors"] != 0.0 {
		t.Errorf("previous %v", out["previous"])
	}
	if _, ok := decode(t, apiGet(h, "/api/v1/summary?site=example.com", key))["previous"]; ok {
		t.Error("compares without compare")
	}

	w = apiGet(h, "/api/v1/summary?site=example.com&f="+url.QueryEscape("country|is|FR"), key)
	if res := decode(t, w)["results"].(map[string]any); res["visitors"] != 1.0 {
		t.Errorf("filtered: %v", res)
	}
}

func TestAPIBadRequests(t *testing.T) {
	_, h, key := apiFixture(t)
	for path, status := range map[string]int{
		"/api/v1/summary":                                                             http.StatusBadRequest,
		"/api/v1/summary?site=other.com":                                              http.StatusNotFound,
		"/api/v1/summary?site=example.com&range=forever":                              http.StatusBadRequest,
		"/api/v1/summary?site=example.com&range=custom&from=2026-01-05":               http.StatusBadRequest,
		"/api/v1/summary?site=example.com&f=country|equals|DE":                        http.StatusBadRequest,
		"/api/v1/summary?site=example.com&compare=sometimes":                          http.StatusBadRequest,
		"/api/v1/breakdown?site=example.com":                                          http.StatusBadRequest,
		"/api/v1/breakdown?site=example.com&dimension=goal":                           http.StatusBadRequest,
		"/api/v1/breakdown?site=example.com&dimension=page&limit=0":                   http.StatusBadRequest,
		"/api/v1/breakdown?site=example.com&dimension=page&limit=5000":                http.StatusBadRequest,
		"/api/v1/timeseries?site=example.com&interval=week":                           http.StatusBadRequest,
		"/api/v1/timeseries?site=example.com&range=91d&interval=hour":                 http.StatusBadRequest,
		"/api/v1/summary?site=example.com&range=custom&from=2026-01-05&to=2026-01-07": http.StatusOK,
		"/api/v1/timeseries?site=example.com&range=28d&interval=hour":                 http.StatusOK,
	} {
		w := apiGet(h, path, key)
		if w.Code != status {
			t.Errorf("%s: status %d, want %d: %s", path, w.Code, status, w.Body)
		}
		if out := decode(t, w); status != http.StatusOK && out["error"] == "" {
			t.Errorf("%s: no error message", path)
		}
	}
}

func TestAPIBreakdown(t *testing.T) {
	_, h, key := apiFixture(t)
	get := func(params string) []any {
		t.Helper()
		w := apiGet(h, "/api/v1/breakdown?site=example.com&"+params, key)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", params, w.Code, w.Body)
		}
		return decode(t, w)["results"].([]any)
	}
	pages := get("dimension=page")
	if len(pages) != 3 {
		t.Fatalf("pages %v", pages)
	}
	p := pages[0].(map[string]any)
	if _, ok := p["pageviews"]; !ok || p["time_on_page"] == nil {
		t.Errorf("page row %v", p)
	}
	if got := get("dimension=page&limit=1"); len(got) != 1 {
		t.Errorf("limit: %d rows", len(got))
	}
	src := get("dimension=source")
	if len(src) != 2 {
		t.Errorf("sources %v", src)
	}
	regions := get("dimension=region")
	if len(regions) != 1 || regions[0].(map[string]any)["country"] != "DE" {
		t.Errorf("regions %v", regions)
	}
	events := get("dimension=event")
	if len(events) != 1 || events[0].(map[string]any)["value"] != "signup" || events[0].(map[string]any)["events"] != 1.0 {
		t.Errorf("events %v", events)
	}
	props := get("dimension=prop:plan&f=" + url.QueryEscape("goal|is|signup"))
	if len(props) != 1 || props[0].(map[string]any)["value"] != "pro" || props[0].(map[string]any)["revenue"] != 29.9 {
		t.Errorf("props %v", props)
	}
	cr := get("dimension=country&f=" + url.QueryEscape("goal|is|signup"))
	if len(cr) != 1 || cr[0].(map[string]any)["conversion_rate"] != 100.0 {
		t.Errorf("conversion rates %v", cr)
	}
}

func TestAPIOtherEndpoints(t *testing.T) {
	s, h, key := apiFixture(t)
	ctx := context.Background()
	site, _ := s.Store.SiteByDomain(ctx, "example.com")
	if err := s.Store.AddGoal(ctx, store.Goal{SiteID: site.ID, PagePath: "/pricing"}); err != nil {
		t.Fatal(err)
	}
	goals, _ := s.Store.Goals(ctx, site.ID)
	if err := s.Store.AddFunnel(ctx, store.Funnel{SiteID: site.ID, Name: "Buy", Steps: []int64{goals[1].ID, goals[0].ID}}); err != nil {
		t.Fatal(err)
	}

	out := decode(t, apiGet(h, "/api/v1/timeseries?site=example.com&range=7d", key))
	rows := out["results"].([]any)
	if out["interval"] != "day" || len(rows) != 7 || rows[6].(map[string]any)["visitors"] != 2.0 {
		t.Errorf("timeseries %v", out)
	}
	if d := rows[0].(map[string]any)["date"].(string); !regexp.MustCompile(`^\d{4}-\d\d-\d\d$`).MatchString(d) {
		t.Errorf("date %q", d)
	}

	g := decode(t, apiGet(h, "/api/v1/goals?site=example.com", key))["results"].([]any)
	if len(g) != 2 {
		t.Fatalf("goals %v", g)
	}
	for _, x := range g {
		m := x.(map[string]any)
		if m["goal"] == "signup" && (m["revenue"] != 29.9 || m["currency"] != "EUR" || m["conversion_rate"] != 50.0) {
			t.Errorf("signup goal %v", m)
		}
		if m["goal"] == "Visit /pricing" && (m["type"] != "page" || m["revenue"] != nil) {
			t.Errorf("page goal %v", m)
		}
	}

	f := decode(t, apiGet(h, "/api/v1/funnels?site=example.com", key))["results"].([]any)
	steps := f[0].(map[string]any)["steps"].([]any)
	if len(f) != 1 || len(steps) != 2 || steps[1].(map[string]any)["visitors"] != 1.0 {
		t.Errorf("funnels %v", f)
	}

	v := decode(t, apiGet(h, "/api/v1/vitals?site=example.com", key))
	if lcp := v["results"].(map[string]any)["lcp"].(map[string]any); lcp["p75"] != nil || lcp["samples"] != 0.0 {
		t.Errorf("vitals without samples: %v", lcp)
	}
}

// A host with its own login serves the API only if it resolves keys.
func TestAPIWithHostLogin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddSite(context.Background(), "example.com")
	s := &Server{Location: time.UTC, Collector: http.NotFoundHandler(),
		Authenticate: func(r *http.Request) (*store.Store, bool) { return st, true }}
	h := s.Handler()
	if w := apiGet(h, "/api/v1/sites", "bs_anything"); w.Code != http.StatusNotFound {
		t.Errorf("API served without AuthenticateAPI: %d", w.Code)
	}
	if body := apiGet(h, "/sites", "").Body.String(); strings.Contains(body, "Stats API") {
		t.Error("API keys shown without AuthenticateAPI")
	}

	s = &Server{Location: time.UTC, Collector: http.NotFoundHandler(),
		Authenticate: func(r *http.Request) (*store.Store, bool) { return st, true },
		AuthenticateAPI: func(r *http.Request, key string) (*store.Store, bool) {
			return st, key == "bs_host"
		}}
	h = s.Handler()
	if w := apiGet(h, "/api/v1/sites", "bs_host"); w.Code != http.StatusOK {
		t.Errorf("host key: %d", w.Code)
	}
	if w := apiGet(h, "/api/v1/sites", "bs_other"); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong host key: %d", w.Code)
	}
}

func TestAPIKeyPages(t *testing.T) {
	s, get := dashboardClient(t)
	addSite(t, s, "example.com")
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))

	w := post(h, "/api-keys", "name=Grafana", c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	key := regexp.MustCompile(`bs_[A-Z2-7]{26}`).FindString(w.Body.String())
	if key == "" {
		t.Fatal("new key not shown")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("page with the key may be cached")
	}
	if w := apiGet(h, "/api/v1/sites", key); w.Code != http.StatusOK {
		t.Errorf("new key: status %d", w.Code)
	}
	body := get("/sites").Body.String()
	if strings.Contains(body, key) || !strings.Contains(body, key[:9]+"…") || !strings.Contains(body, "Grafana") {
		t.Error("sites page should list the key by its prefix only")
	}
	if w := post(h, "/api-keys", "name=", c); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "error=api-key-name") {
		t.Errorf("empty name: %d %q", w.Code, w.Header().Get("Location"))
	}
	keys, _ := s.Store.APIKeys(context.Background())
	if w := post(h, "/api-keys/"+strconv.FormatInt(keys[0].ID, 10)+"/delete", "", c); w.Code != http.StatusSeeOther {
		t.Errorf("revoke: %d", w.Code)
	}
	if w := apiGet(h, "/api/v1/sites", key); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key: status %d", w.Code)
	}
	// Forms need a login.
	if w := post(h, "/api-keys", "name=x"); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("anonymous: %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestExport(t *testing.T) {
	s, _, _ := apiFixture(t)
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("/?site=example.com"); !strings.Contains(w.Body.String(), `href="/export?range=30d&amp;site=example.com"`) {
		t.Error("dashboard has no export link")
	}

	w := get("/export?site=example.com&range=7d")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="bonestats-example.com-`) {
		t.Errorf("Content-Disposition %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][][]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		files[f.Name] = recs
	}
	for _, name := range []string{"summary.csv", "timeseries.csv", "page.csv", "source.csv", "country.csv", "event.csv",
		"goals.csv", "properties.csv", "funnels.csv", "vitals.csv"} {
		if _, ok := files[name]; !ok {
			t.Errorf("export lacks %s", name)
		}
	}
	if ts := files["timeseries.csv"]; len(ts) != 8 || ts[0][0] != "date" {
		t.Errorf("timeseries.csv: %v", ts)
	}
	pages := files["page.csv"]
	if strings.Join(pages[0], ",") != "value,visitors,pageviews,time_on_page,scroll_depth" {
		t.Errorf("page.csv header %v", pages[0])
	}
	found := false
	for _, row := range pages {
		found = found || row[0] == "/=cmd"
	}
	if !found {
		t.Errorf("page.csv rows %v", pages)
	}
	if p := files["properties.csv"]; len(p) != 2 || p[1][0] != "plan" || p[1][1] != "pro" {
		t.Errorf("properties.csv %v", p)
	}

	// Filters carry over.
	w = get("/export?site=example.com&f=" + url.QueryEscape("country|is|FR"))
	zr, _ = zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	for _, f := range zr.File {
		if f.Name == "summary.csv" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			if !strings.Contains(string(b), "visitors,1\n") {
				t.Errorf("filtered summary: %s", b)
			}
		}
	}
	if w := apiGet(h, "/export?site=example.com", ""); w.Code != http.StatusSeeOther {
		t.Errorf("export without login: %d", w.Code)
	}
}

func TestCSVCell(t *testing.T) {
	for v, want := range map[any]string{
		"/blog": "/blog", "=HYPERLINK(1)": "'=HYPERLINK(1)", "+1": "'+1", "-1": "'-1", "@x": "'@x", "": "",
		int64(42): "42", 1.5: "1.5", nil: "",
	} {
		if got := csvCell(v); got != want {
			t.Errorf("csvCell(%v) = %q, want %q", v, got, want)
		}
	}
}

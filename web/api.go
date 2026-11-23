// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yi00it/bonestats/store"
)

// The stats API is read-only. It takes the dashboard's URL parameters (site,
// range, from, to, f) and an API key in "Authorization: Bearer <key>". See
// docs/API.md.

const (
	defaultAPILimit = 100
	maxAPILimit     = 1000
	// Hourly series are capped so one request can't ask for years of hours.
	maxHourlyRange = 31 * 24 * time.Hour
)

// apiEnabled reports whether the API is served: always with the built-in
// login, and with a host's own login only if it resolves API keys too.
func (s *Server) apiEnabled() bool { return s.Authenticate == nil || s.AuthenticateAPI != nil }

func (s *Server) apiRoutes(mux *http.ServeMux) {
	if !s.apiEnabled() {
		return
	}
	mux.Handle("GET /api/v1/sites", s.apiAuth(s.apiSites))
	mux.Handle("GET /api/v1/summary", s.apiAuth(s.apiSummary))
	mux.Handle("GET /api/v1/timeseries", s.apiAuth(s.apiTimeseries))
	mux.Handle("GET /api/v1/breakdown", s.apiAuth(s.apiBreakdown))
	mux.Handle("GET /api/v1/goals", s.apiAuth(s.apiGoals))
	mux.Handle("GET /api/v1/funnels", s.apiAuth(s.apiFunnels))
	mux.Handle("GET /api/v1/vitals", s.apiAuth(s.apiVitals))
	mux.Handle("/api/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiError(w, http.StatusNotFound, "unknown endpoint, see docs/API.md")
	}))
}

type apiHandler func(w http.ResponseWriter, r *http.Request, st *store.Store)

func (s *Server) apiAuth(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="bonestats"`)
			apiError(w, http.StatusUnauthorized, "send an API key as Authorization: Bearer <key>")
			return
		}
		var st *store.Store
		if s.AuthenticateAPI != nil {
			st, ok = s.AuthenticateAPI(r, strings.TrimSpace(key))
		} else {
			var err error
			ok, err = s.Store.UseAPIKey(r.Context(), strings.TrimSpace(key))
			if err != nil {
				log.Printf("api auth: %v", err)
				apiError(w, http.StatusInternalServerError, "internal error")
				return
			}
			st = s.Store
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="bonestats", error="invalid_token"`)
			apiError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		h(w, r, st)
	})
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func apiJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: %v", err)
	}
}

// apiView resolves the parameters strictly: the API rejects what the
// dashboard quietly replaces.
func (s *Server) apiView(w http.ResponseWriter, r *http.Request, st *store.Store) (*view, bool) {
	qs := r.URL.Query()
	if qs.Get("site") == "" {
		apiError(w, http.StatusBadRequest, "the site parameter is required, e.g. site=example.com")
		return nil, false
	}
	v, err := s.resolveView(r.Context(), st, qs)
	if errors.Is(err, errNoSites) || (err == nil && !v.knownSite) {
		apiError(w, http.StatusNotFound, "no site "+strconv.Quote(qs.Get("site")))
		return nil, false
	}
	if err != nil {
		log.Printf("api: %v", err)
		apiError(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if !v.knownWindow {
		apiError(w, http.StatusBadRequest, "invalid range: use one of today, yesterday, 7d, 28d, 30d, 91d, month, lastmonth, year, 12mo, all, or custom with from and to (YYYY-MM-DD)")
		return nil, false
	}
	if v.badFilter != "" {
		apiError(w, http.StatusBadRequest, "invalid filter "+strconv.Quote(v.badFilter)+": use f=dimension|is|value (or is_not, contains)")
		return nil, false
	}
	return v, true
}

// queryInfo echoes what a response covers.
func (v *view) queryInfo() map[string]any {
	filters := []map[string]string{}
	for _, f := range v.q.Filters {
		filters = append(filters, map[string]string{"dimension": f.Dim, "operator": f.Op, "value": f.Value})
	}
	return map[string]any{
		"site": v.site.Domain, "from": v.from.Format(dateLayout), "to": v.lastDay().Format(dateLayout),
		"filters": filters,
	}
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("api: %v", err)
	apiError(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) apiSites(w http.ResponseWriter, r *http.Request, st *store.Store) {
	sites, err := st.Sites(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	out := []map[string]any{}
	for _, site := range sites {
		out = append(out, map[string]any{"domain": site.Domain, "created_at": site.CreatedAt.UTC().Format(time.RFC3339)})
	}
	apiJSON(w, map[string]any{"sites": out})
}

func (s *Server) apiSummary(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	compare := r.URL.Query().Get("compare")
	var mode string
	switch compare {
	case "", "off":
		mode = "off"
	case "previous":
		mode = ""
	case "year":
		mode = "year"
	default:
		apiError(w, http.StatusBadRequest, "compare must be previous, year or off")
		return
	}
	ctx := r.Context()
	sum, err := st.Summary(ctx, v.q)
	if err != nil {
		internalError(w, err)
		return
	}
	out := map[string]any{"query": v.queryInfo(), "results": summaryObject(sum)}
	if pFrom, pTo, _, comparing := previous(mode, v.from, v.to); comparing {
		pq := v.q
		pq.From, pq.To = pFrom, pTo
		// Up to the same point in time, as the dashboard compares.
		if v.to.After(v.now) {
			pq.To = pFrom.Add(v.now.Sub(v.from))
		}
		prev, err := st.Summary(ctx, pq)
		if err != nil {
			internalError(w, err)
			return
		}
		out["previous"] = map[string]any{"from": pFrom.Format(dateLayout), "to": pTo.AddDate(0, 0, -1).Format(dateLayout),
			"results": summaryObject(prev)}
	}
	apiJSON(w, out)
}

func (s *Server) apiTimeseries(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	iv := interval(v.from, v.to)
	switch p := r.URL.Query().Get("interval"); p {
	case "":
	case "hour", "day", "month":
		iv = store.Interval(p)
	default:
		apiError(w, http.StatusBadRequest, "interval must be hour, day or month")
		return
	}
	if iv == store.Hour && v.to.Sub(v.from) > maxHourlyRange {
		apiError(w, http.StatusBadRequest, "hourly intervals cover at most 31 days")
		return
	}
	t, err := timeseriesTable(r.Context(), st, v.q, iv)
	if err != nil {
		internalError(w, err)
		return
	}
	apiJSON(w, map[string]any{"query": v.queryInfo(), "interval": iv, "results": t.objects()})
}

func apiLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	p := r.URL.Query().Get("limit")
	if p == "" {
		return defaultAPILimit, true
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > maxAPILimit {
		apiError(w, http.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(maxAPILimit))
		return 0, false
	}
	return n, true
}

func (s *Server) apiBreakdown(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	dim := r.URL.Query().Get("dimension")
	if !validBreakdownDim(dim) {
		apiError(w, http.StatusBadRequest, "dimension must be one of "+strings.Join(breakdownDims(), ", ")+", or prop:<name>")
		return
	}
	limit, ok := apiLimit(w, r)
	if !ok {
		return
	}
	t, err := breakdownTable(r.Context(), st, v.q, dim, limit)
	if err != nil {
		internalError(w, err)
		return
	}
	apiJSON(w, map[string]any{"query": v.queryInfo(), "dimension": dim, "results": t.objects()})
}

func (s *Server) apiGoals(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	t, err := goalsTable(r.Context(), st, v.q)
	if err != nil {
		internalError(w, err)
		return
	}
	apiJSON(w, map[string]any{"query": v.queryInfo(), "results": t.objects()})
}

func (s *Server) apiFunnels(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, f := range v.funnels {
		steps, err := st.FunnelStats(r.Context(), v.q, f)
		if err != nil {
			internalError(w, err)
			return
		}
		js := []map[string]any{}
		for _, s := range steps {
			js = append(js, map[string]any{"goal": s.Name, "visitors": s.Visitors, "percent": round(s.Percent), "drop_off": round(s.DropOff)})
		}
		out = append(out, map[string]any{"name": f.Name, "steps": js})
	}
	apiJSON(w, map[string]any{"query": v.queryInfo(), "results": out})
}

func (s *Server) apiVitals(w http.ResponseWriter, r *http.Request, st *store.Store) {
	v, ok := s.apiView(w, r, st)
	if !ok {
		return
	}
	limit, ok := apiLimit(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	sum, err := st.VitalsSummary(ctx, v.q)
	if err != nil {
		internalError(w, err)
		return
	}
	pages, err := vitalsTable(ctx, st, v.q, limit)
	if err != nil {
		internalError(w, err)
		return
	}
	results := map[string]any{}
	for _, m := range store.VitalMetrics {
		var p75 any
		if sum[m].Samples > 0 {
			p75 = round(sum[m].P75)
		}
		results[m] = map[string]any{"p75": p75, "samples": sum[m].Samples}
	}
	apiJSON(w, map[string]any{"query": v.queryInfo(), "results": results, "pages": pages.objects()})
}

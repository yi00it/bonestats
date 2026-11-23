// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/yi00it/bonestats/store"
)

// view is what the dashboard's URL parameters select: a site, a date range
// and filters. The dashboard, the CSV export and the API share it, so a
// dashboard URL's parameters work for all three.
type view struct {
	sites       []store.Site
	site        store.Site
	qs          url.Values // the parameters, with site and range filled in
	from, to    time.Time  // [from, to), aligned to midnight in s.Location
	label       string     // e.g. "Last 30 days"
	now         time.Time
	goals       []store.Goal
	funnels     []store.Funnel
	q           store.Query
	badFilter   string // the first f parameter that didn't parse, if any
	knownSite   bool   // the site parameter named an existing site
	knownWindow bool   // range (and from/to for custom) were valid
}

var errNoSites = errors.New("no sites")

// resolveView reads the dashboard parameters. A missing or unknown site
// selects the first one, and an invalid range selects today; the API checks
// knownSite and knownWindow to reject those instead.
func (s *Server) resolveView(ctx context.Context, st *store.Store, params url.Values) (*view, error) {
	qs := url.Values{}
	for k, vs := range params {
		qs[k] = append([]string{}, vs...)
	}
	sites, err := st.Sites(ctx)
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, errNoSites
	}
	v := &view{sites: sites, site: sites[0], qs: qs}
	for _, other := range sites {
		if other.Domain == qs.Get("site") {
			v.site, v.knownSite = other, true
		}
	}
	qs.Set("site", v.site.Domain)
	if qs.Get("range") == "" {
		qs.Set("range", "30d")
	}

	if v.goals, err = st.Goals(ctx, v.site.ID); err != nil {
		return nil, err
	}
	if v.funnels, err = st.Funnels(ctx, v.site.ID); err != nil {
		return nil, err
	}

	v.now = time.Now().In(s.Location)
	first, ok := st.FirstVisit(ctx, v.site.ID)
	if !ok {
		first = v.now
	}
	v.from, v.to, v.label = window(qs.Get("range"), qs.Get("from"), qs.Get("to"), v.now, first)
	v.knownWindow = v.label != "Today" || qs.Get("range") == "today"

	var filters []store.Filter
	for _, raw := range qs["f"] {
		f, ok := store.ParseFilter(raw)
		if !ok {
			if v.badFilter == "" {
				v.badFilter = raw
			}
			continue
		}
		filters = append(filters, f)
	}
	v.q = store.Query{SiteID: v.site.ID, From: v.from, To: v.to, Location: s.Location, Filters: filters, Goals: v.goals}
	return v, nil
}

// lastDay is the window's last day, for display ([from, to) ends at midnight).
func (v *view) lastDay() time.Time { return v.to.AddDate(0, 0, -1) }

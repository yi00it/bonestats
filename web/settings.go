// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"net/http"
	"strconv"

	"github.com/yi00it/bonestats/store"
)

// The admin pages: /sites lists the sites, /sites/{id}/settings and its
// sibling tabs (goals, share, reports, import) set up one site, and
// /settings holds what applies to every site.

func siteSettingsURL(site store.Site) string {
	return "/sites/" + strconv.FormatInt(site.ID, 10) + "/settings"
}

// ---- sites ----

func (s *Server) sitesPage(w http.ResponseWriter, r *http.Request) {
	sites, err := s.store(r).Sites(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, "sites", map[string]any{
		"Title": "Sites",
		"Sites": sites,
		"Error": errorMessages[r.URL.Query().Get("error")],
	})
}

// addSite adds a site and opens its settings, where the snippet is.
func (s *Server) addSite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := s.store(r)
	domain := r.PostFormValue("domain")
	if err := st.AddSite(ctx, domain); err != nil {
		http.Redirect(w, r, "/sites?error="+errorCode(err), http.StatusSeeOther)
		return
	}
	sites, err := st.Sites(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	domain = store.NormalizeDomain(domain)
	for _, site := range sites {
		if site.Domain == domain {
			http.Redirect(w, r, siteSettingsURL(site)+"?added=1", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

func (s *Server) deleteSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store(r).DeleteSite(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

// siteSettingsPage is a site's General tab: how to install the script, and
// deleting the site.
func (s *Server) siteSettingsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	s.render(w, "site", map[string]any{
		"Title":   "Settings · " + site.Domain,
		"Site":    site,
		"Added":   r.URL.Query().Get("added") != "",
		"BaseURL": baseURL(r),
	})
}

// ---- settings for every site ----

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "")
}

// renderSettings shows the settings page. newKey is an API key that was
// just created: the only time it can be shown.
func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, newKey string) {
	ctx := r.Context()
	st := s.store(r)
	var (
		keys  []store.APIKey
		sites []store.Site
		err   error
	)
	if s.apiEnabled() {
		if keys, err = st.APIKeys(ctx); err != nil {
			serverError(w, err)
			return
		}
		if sites, err = st.Sites(ctx); err != nil {
			serverError(w, err)
			return
		}
	}
	if newKey != "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	example := "example.com"
	if len(sites) > 0 {
		example = sites[0].Domain
	}
	s.render(w, "settings", map[string]any{
		"Title":                "Settings",
		"BaseURL":              baseURL(r),
		"Error":                errorMessages[r.URL.Query().Get("error")],
		"Retention":            s.Retention,
		"IgnorePrivacySignals": s.IgnorePrivacySignals,
		"APIEnabled":           s.apiEnabled(),
		"APIKeys":              keys,
		"NewKey":               newKey,
		"ExampleDomain":        example,
	})
}

func (s *Server) addAPIKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.store(r).AddAPIKey(r.Context(), r.PostFormValue("name"))
	if err != nil {
		http.Redirect(w, r, "/settings?error="+errorCode(err)+"#api", http.StatusSeeOther)
		return
	}
	s.renderSettings(w, r, key)
}

func (s *Server) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store(r).DeleteAPIKey(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings#api", http.StatusSeeOther)
}

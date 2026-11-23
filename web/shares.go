// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"log"
	"net/http"
	"strconv"

	"github.com/yi00it/bonestats/store"
)

// sharesEnabled reports whether shared links are served: always with the
// built-in login, and with a host's own login only if it resolves them.
func (s *Server) sharesEnabled() bool { return s.Authenticate == nil || s.ResolveShare != nil }

// findShare looks up the link named in the path, answering 404 if there is
// none.
func (s *Server) findShare(w http.ResponseWriter, r *http.Request) (*store.Store, store.Share, store.Site, bool) {
	token := r.PathValue("token")
	st := s.Store
	if s.ResolveShare != nil {
		var ok bool
		if st, ok = s.ResolveShare(r, token); !ok {
			http.NotFound(w, r)
			return nil, store.Share{}, store.Site{}, false
		}
	}
	sh, site, ok, err := st.ShareByToken(r.Context(), token)
	if err != nil {
		serverError(w, err)
		return nil, store.Share{}, store.Site{}, false
	}
	if !ok {
		http.NotFound(w, r)
		return nil, store.Share{}, store.Site{}, false
	}
	return st, sh, site, true
}

// sharedDashboard serves a shared link: one site's dashboard, read-only,
// without a login, after its password if it has one.
func (s *Server) sharedDashboard(w http.ResponseWriter, r *http.Request) {
	// The link is the password: never send it on to other sites.
	w.Header().Set("Referrer-Policy", "no-referrer")
	st, sh, site, ok := s.findShare(w, r)
	if !ok {
		return
	}
	unlocked, err := shareUnlocked(r, st, sh)
	if err != nil {
		serverError(w, err)
		return
	}
	if !unlocked {
		s.renderSharePassword(w, sh, site, http.StatusUnauthorized, "")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.renderDashboard(w, r, st, &shared{site: site, token: sh.Token})
}

func sharesURL(site store.Site) string { return "/sites/" + strconv.FormatInt(site.ID, 10) + "/share" }

func (s *Server) sharesPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	shares, err := s.store(r).Shares(r.Context(), site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	scheme := "http"
	if https(r) {
		scheme = "https"
	}
	s.render(w, "shares", map[string]any{
		"Title":   "Share · " + site.Domain,
		"Site":    site,
		"Shares":  shares,
		"BaseURL": scheme + "://" + r.Host,
		"Error":   errorMessages[r.URL.Query().Get("error")],
	})
}

func (s *Server) addShare(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	hash := ""
	if pw := r.PostFormValue("password"); pw != "" {
		if len(pw) < minSharePassword || len(pw) > 200 {
			http.Redirect(w, r, sharesURL(site)+"?error=share-password", http.StatusSeeOther)
			return
		}
		var err error
		if hash, err = hashSharePassword(pw); err != nil {
			serverError(w, err)
			return
		}
	}
	sh, err := s.store(r).AddShare(r.Context(), site.ID, r.PostFormValue("name"))
	if err != nil {
		http.Redirect(w, r, sharesURL(site)+"?error="+errorCode(err), http.StatusSeeOther)
		return
	}
	if hash != "" {
		if err := s.store(r).SetSharePassword(r.Context(), site.ID, sh.ID, hash); err != nil {
			serverError(w, err)
			return
		}
	}
	http.Redirect(w, r, sharesURL(site), http.StatusSeeOther)
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err := s.store(r).DeleteShare(r.Context(), site.ID, id); err != nil {
		log.Printf("delete share: %v", err)
		serverError(w, err)
		return
	}
	http.Redirect(w, r, sharesURL(site), http.StatusSeeOther)
}

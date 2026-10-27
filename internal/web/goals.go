package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"bonestats/internal/store"
)

// siteFromPath loads the site named by the {id} path segment.
func (s *Server) siteFromPath(w http.ResponseWriter, r *http.Request) (store.Site, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		sites, err := s.Store.Sites(r.Context())
		if err != nil {
			serverError(w, err)
			return store.Site{}, false
		}
		for _, st := range sites {
			if st.ID == id {
				return st, true
			}
		}
	}
	http.NotFound(w, r)
	return store.Site{}, false
}

func goalsURL(site store.Site, errMsg string) string {
	u := fmt.Sprintf("/sites/%d/goals", site.ID)
	if errMsg != "" {
		u += "?error=" + url.QueryEscape(errMsg)
	}
	return u
}

// suggestedGoals are events the tracker add-ons and docs produce.
var suggestedGoals = []string{"Outbound Link: Click", "File Download", "404"}

func (s *Server) goalsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	goals, err := s.Store.Goals(r.Context(), site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	funnels, err := s.Store.Funnels(r.Context(), site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	names := map[int64]string{}
	existing := map[string]bool{}
	for _, g := range goals {
		names[g.ID] = g.Name()
		existing[g.Name()] = true
	}
	type funnelView struct {
		store.Funnel
		StepNames []string
	}
	var fv []funnelView
	for _, f := range funnels {
		v := funnelView{Funnel: f}
		for _, id := range f.Steps {
			v.StepNames = append(v.StepNames, names[id])
		}
		fv = append(fv, v)
	}
	var suggestions []string
	for _, g := range suggestedGoals {
		if !existing[g] {
			suggestions = append(suggestions, g)
		}
	}
	s.render(w, "goals", map[string]any{
		"Title":       "Goals · " + site.Domain,
		"Site":        site,
		"Goals":       goals,
		"Funnels":     fv,
		"Suggestions": suggestions,
		"StepSlots":   []int{1, 2, 3, 4, 5, 6, 7, 8},
		"Error":       r.URL.Query().Get("error"),
	})
}

func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	g := store.Goal{SiteID: site.ID}
	if r.PostFormValue("kind") == "page" {
		g.PagePath = r.PostFormValue("path")
	} else {
		g.EventName = r.PostFormValue("event")
		g.Currency = r.PostFormValue("currency")
	}
	msg := ""
	if err := s.Store.AddGoal(r.Context(), g); err != nil {
		msg = err.Error()
		if strings.Contains(msg, "UNIQUE") {
			msg = "That goal already exists."
		}
	}
	http.Redirect(w, r, goalsURL(site, msg), http.StatusSeeOther)
}

func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err := s.Store.DeleteGoal(r.Context(), site.ID, id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, goalsURL(site, ""), http.StatusSeeOther)
}

func (s *Server) addFunnel(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	f := store.Funnel{SiteID: site.ID, Name: r.PostFormValue("name")}
	for _, v := range r.PostForm["step"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Steps = append(f.Steps, id)
		}
	}
	msg := ""
	if err := s.Store.AddFunnel(r.Context(), f); err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, goalsURL(site, msg), http.StatusSeeOther)
}

func (s *Server) deleteFunnel(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err := s.Store.DeleteFunnel(r.Context(), site.ID, id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, goalsURL(site, ""), http.StatusSeeOther)
}

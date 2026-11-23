// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/yi00it/bonestats/report"
	"github.com/yi00it/bonestats/store"
)

// reportsEnabled reports whether email reports are on the Sites page. Hosts
// with their own login (Authenticate) have no email reports yet.
func (s *Server) reportsEnabled() bool { return s.Authenticate == nil }

// zoneName names a timezone for the UI.
func zoneName(loc *time.Location) string {
	if loc.String() == "Local" {
		return "the server's timezone (" + time.Now().In(loc).Format("MST") + ")"
	}
	return loc.String()
}

// timeNow is time.Now, replaced in tests.
var timeNow = time.Now

func baseURL(r *http.Request) string {
	scheme := "http"
	if https(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func reportsURL(site store.Site) string {
	return "/sites/" + strconv.FormatInt(site.ID, 10) + "/reports"
}

func (s *Server) reportsPage(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	s.renderReports(w, r, site, http.StatusOK, errorMessages[r.URL.Query().Get("error")], r.URL.Query().Get("sent"))
}

func (s *Server) renderReports(w http.ResponseWriter, r *http.Request, site store.Site, status int, errMsg, sent string) {
	reports, err := s.store(r).Reports(r.Context(), site.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	s.renderStatus(w, status, "reports", map[string]any{
		"Title":     "Reports · " + site.Domain,
		"Site":      site,
		"Reports":   reports,
		"Mail":      s.Mailer != nil,
		"Error":     errMsg,
		"Sent":      sent,
		"Location":  zoneName(s.Location),
		"SendHour":  "7:00",
		"FromLabel": s.MailFrom,
	})
}

func (s *Server) addReport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	if s.Mailer == nil {
		http.NotFound(w, r)
		return
	}
	period := r.PostFormValue("period")
	// The first email covers the next period to end, not the one that
	// ended before the address was added.
	from, _ := report.DuePeriod(period, timeNow(), s.Location)
	if _, err := s.store(r).AddReport(r.Context(), site.ID, r.PostFormValue("email"), period, baseURL(r), report.Day(from)); err != nil {
		http.Redirect(w, r, reportsURL(site)+"?error="+errorCode(err), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, reportsURL(site), http.StatusSeeOther)
}

func (s *Server) deleteReport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err := s.store(r).DeleteReport(r.Context(), site.ID, id); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, reportsURL(site), http.StatusSeeOther)
}

// sendReport sends the last complete period's report to one recipient
// right away, to try the setup. It doesn't count as that period's report.
func (s *Server) sendReport(w http.ResponseWriter, r *http.Request) {
	site, ok := s.siteFromPath(w, r)
	if !ok {
		return
	}
	if s.Mailer == nil {
		http.NotFound(w, r)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	rep, ok, err := s.store(r).ReportByID(r.Context(), site.ID, id)
	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	rep.BaseURL = baseURL(r)
	from, to := report.LastPeriod(rep.Period, timeNow(), s.Location)
	if err := report.Send(r.Context(), s.store(r), s.Mailer, rep, from, to, s.Location); err != nil {
		log.Printf("send report to %s: %v", rep.Email, err)
		s.renderReports(w, r, site, http.StatusBadGateway, "Sending to "+rep.Email+" failed: "+err.Error(), "")
		return
	}
	http.Redirect(w, r, reportsURL(site)+"?sent="+url.QueryEscape(rep.Email), http.StatusSeeOther)
}

// unsubscribePage asks to confirm, so that link checkers which open every
// link in an email don't unsubscribe anyone.
func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	rep, ok, err := s.Store.ReportByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		serverError(w, err)
		return
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	s.renderStatus(w, status, "unsubscribe", map[string]any{"Title": "Unsubscribe", "Found": ok, "Report": rep})
}

// unsubscribe removes the recipient. Mail apps POST here too, for their
// one-click unsubscribe button (RFC 8058).
func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	rep, ok, err := s.Store.ReportByToken(r.Context(), r.PathValue("token"))
	if err == nil && ok {
		err = s.Store.Unsubscribe(r.Context(), rep.Token)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	s.render(w, "unsubscribe", map[string]any{"Title": "Unsubscribed", "Found": ok, "Done": true, "Report": rep})
}

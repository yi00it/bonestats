// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/report"
	"github.com/yi00it/bonestats/store"
)

type fakeMailer struct {
	sent []report.Message
	err  error
}

func (f *fakeMailer) Send(ctx context.Context, m report.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func TestReportsPage(t *testing.T) {
	s, get := dashboardClient(t)
	site := addSite(t, s, "example.com")
	base := "/sites/" + strconv.FormatInt(site.ID, 10)

	// Without SMTP the page explains how to turn reports on.
	if body := get("/sites").Body.String(); !strings.Contains(body, `href="`+base+`/reports"`) {
		t.Error("sites page doesn't link the reports page")
	}
	if body := get(base + "/reports").Body.String(); !strings.Contains(body, "BONESTATS_SMTP_URL") || strings.Contains(body, `name="email"`) {
		t.Error("reports page without SMTP")
	}

	m := &fakeMailer{}
	s.Mailer, s.MailFrom = m, "bonestats <stats@example.com>"
	h := s.Handler()
	c := sessionCookie(t, post(h, "/login", "password="+url.QueryEscape("correct horse")))
	if w := post(h, base+"/reports", "email=nope&period=weekly", c); !strings.Contains(w.Header().Get("Location"), "error=report-email") {
		t.Errorf("bad email: %q", w.Header().Get("Location"))
	}
	if w := post(h, base+"/reports", "email=me%40example.com&period=daily", c); !strings.Contains(w.Header().Get("Location"), "error=report-period") {
		t.Errorf("bad period: %q", w.Header().Get("Location"))
	}
	if w := post(h, base+"/reports", "email=me%40example.com&period=weekly", c); w.Code != http.StatusSeeOther || strings.Contains(w.Header().Get("Location"), "error") {
		t.Fatalf("add: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := post(h, base+"/reports", "email=me%40example.com&period=weekly", c); !strings.Contains(w.Header().Get("Location"), "error=exists") {
		t.Errorf("duplicate: %q", w.Header().Get("Location"))
	}
	rs, _ := s.Store.Reports(context.Background(), site.ID)
	if len(rs) != 1 || rs[0].BaseURL != "http://example.com" {
		t.Fatalf("reports %+v", rs)
	}
	// The first email covers the next week to end, not the last one.
	if from, _ := report.DuePeriod("weekly", time.Now(), s.Location); rs[0].LastPeriod != report.Day(from) {
		t.Errorf("last period %q", rs[0].LastPeriod)
	}
	body := get(base + "/reports").Body.String()
	if !strings.Contains(body, "me@example.com") || !strings.Contains(body, "Send now") {
		t.Error("reports page doesn't list the address")
	}

	// Send now: the last week's report, without marking that week as sent.
	id := strconv.FormatInt(rs[0].ID, 10)
	if w := post(h, base+"/reports/"+id+"/send", "", c); w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "sent=me%40example.com") {
		t.Fatalf("send: %d %q", w.Code, w.Header().Get("Location"))
	}
	if len(m.sent) != 1 || m.sent[0].To != "me@example.com" || !strings.Contains(m.sent[0].Subject, "example.com: weekly report") {
		t.Errorf("sent %+v", m.sent)
	}
	if after, _ := s.Store.Reports(context.Background(), site.ID); after[0].LastPeriod != rs[0].LastPeriod {
		t.Error("send now marked the period as sent")
	}
	m.err = errors.New("535 authentication failed")
	if w := post(h, base+"/reports/"+id+"/send", "", c); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "535 authentication failed") {
		t.Errorf("failed send: %d", w.Code)
	}

	// Unsubscribing: GET only asks, POST (also a mail app's one-click) does it.
	link := "/reports/unsubscribe/" + rs[0].Token
	if w := anonGet(h, link); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Stop sending weekly reports for example.com to me@example.com?") {
		t.Errorf("unsubscribe page: %d", w.Code)
	}
	if rs, _ := s.Store.Reports(context.Background(), site.ID); len(rs) != 1 {
		t.Fatal("GET unsubscribed")
	}
	r := httptest.NewRequest("POST", link, strings.NewReader("List-Unsubscribe=One-Click"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Unsubscribed") {
		t.Errorf("one-click unsubscribe: %d", w.Code)
	}
	if rs, _ := s.Store.Reports(context.Background(), site.ID); len(rs) != 0 {
		t.Error("still subscribed")
	}
	if w := anonGet(h, link); w.Code != http.StatusNotFound {
		t.Errorf("old link: %d", w.Code)
	}

	// Remove, and only the admin can manage recipients.
	post(h, base+"/reports", "email=x%40example.com&period=monthly", c)
	rs, _ = s.Store.Reports(context.Background(), site.ID)
	if w := post(h, base+"/reports/"+strconv.FormatInt(rs[0].ID, 10)+"/delete", ""); w.Header().Get("Location") != "/login" {
		t.Errorf("anonymous remove: %q", w.Header().Get("Location"))
	}
	if w := post(h, base+"/reports/"+strconv.FormatInt(rs[0].ID, 10)+"/delete", "", c); w.Code != http.StatusSeeOther {
		t.Errorf("remove: %d", w.Code)
	}
	if rs, _ := s.Store.Reports(context.Background(), site.ID); len(rs) != 0 {
		t.Error("not removed")
	}
}

func TestReportsWithHostLogin(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{Location: time.UTC, Collector: http.NotFoundHandler(), Mailer: &fakeMailer{},
		Authenticate: func(r *http.Request) (*store.Store, bool) { return st, true }}
	h := s.Handler()
	st.AddSite(context.Background(), "example.com")
	if strings.Contains(anonGet(h, "/sites").Body.String(), "/reports") {
		t.Error("reports linked for a host login")
	}
	if w := anonGet(h, "/sites/1/reports"); w.Code == http.StatusOK {
		t.Error("reports page served for a host login")
	}
}

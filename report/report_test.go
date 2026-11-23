// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yi00it/bonestats/store"
)

func TestPeriods(t *testing.T) {
	ist, _ := time.LoadLocation("Europe/Istanbul")
	at := func(s string) time.Time { tm, _ := time.ParseInLocation("2006-01-02 15:04", s, ist); return tm }
	for _, c := range []struct {
		period, now, from, to string
	}{
		{"weekly", "2026-11-23 06:59", "2026-11-09", "2026-11-16"}, // Monday before 7:00: the week before last
		{"weekly", "2026-11-23 07:00", "2026-11-16", "2026-11-23"}, // from 7:00: last week
		{"weekly", "2026-11-29 23:59", "2026-11-16", "2026-11-23"}, // Sunday
		{"monthly", "2026-12-01 06:00", "2026-10-01", "2026-11-01"},
		{"monthly", "2026-12-01 07:00", "2026-11-01", "2026-12-01"},
		{"monthly", "2027-01-15 12:00", "2026-12-01", "2027-01-01"},
	} {
		from, to := DuePeriod(c.period, at(c.now), ist)
		if Day(from) != c.from || Day(to) != c.to || from.Location() != ist || from.Hour() != 0 {
			t.Errorf("%s at %s: %s – %s, want %s – %s", c.period, c.now, from, to, c.from, c.to)
		}
	}
	// Weeks across a DST change still start at midnight.
	ber, _ := time.LoadLocation("Europe/Berlin")
	from, to := LastPeriod("weekly", time.Date(2026, 11, 2, 12, 0, 0, 0, ber), ber)
	if from.Format("2006-01-02 15:04") != "2026-10-26 00:00" || to.Format("2006-01-02 15:04") != "2026-11-02 00:00" {
		t.Errorf("DST week %s – %s", from, to)
	}
}

func TestLabelsAndNumbers(t *testing.T) {
	d := func(s string) time.Time { tm, _ := time.Parse("2006-01-02", s); return tm }
	for _, c := range [][4]string{
		{"weekly", "2026-11-16", "2026-11-22", "Nov 16 – 22, 2026"},
		{"weekly", "2026-11-30", "2026-12-06", "Nov 30 – Dec 6, 2026"},
		{"weekly", "2026-12-28", "2027-01-03", "Dec 28, 2026 – Jan 3, 2027"},
		{"monthly", "2026-11-01", "2026-11-30", "November 2026"},
	} {
		if got := label(c[0], d(c[1]), d(c[2])); got != c[3] {
			t.Errorf("label %v = %q", c, got)
		}
	}
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if number(n) != want {
			t.Errorf("number(%d) = %q", n, number(n))
		}
	}
	if s, good, _ := change(120, 100); s != "+20%" || !good {
		t.Errorf("change up: %s %v", s, good)
	}
	if s, good, _ := change(50, 100); s != "−50%" || good {
		t.Errorf("change down: %s %v", s, good)
	}
	if s, _, _ := change(5, 0); s != "" {
		t.Errorf("change from 0: %q", s)
	}
}

type fakeMailer struct {
	sent []Message
	err  error
}

func (f *fakeMailer) Send(ctx context.Context, m Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func testStore(t *testing.T) (*store.Store, store.Site) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	st.AddSite(ctx, "example.com")
	site, _ := st.SiteByDomain(ctx, "example.com")
	return st, site
}

func view(t *testing.T, st *store.Store, site store.Site, visitor int64, at time.Time, path, country string) {
	t.Helper()
	ctx := context.Background()
	e := store.Event{SiteID: site.ID, Time: at, Visitor: visitor, Name: "pageview", Path: path, Visit: store.Visit{Country: country, Channel: "Direct"}}
	if err := st.RecordEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	ms := store.MinEngaged.Milliseconds()
	if _, err := st.RecordLeave(ctx, store.Leave{SiteID: site.ID, Visitor: visitor, Time: at, Path: path, Engaged: &ms}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildAndRender(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	week := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		view(t, st, site, int64(i+1), week.Add(time.Duration(i+10)*time.Hour), "/<script>", "DE")
	}
	view(t, st, site, 9, week.AddDate(0, 0, -3), "/old", "FR") // the week before
	r, err := st.AddReport(ctx, site.ID, "me@example.com", "weekly", "https://stats.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	r.Domain = "example.com"
	d, err := Build(ctx, st, r, week, week.AddDate(0, 0, 7), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if d.Empty || d.Metrics[0].Value != "3" || d.Metrics[0].Change != "+200%" || !d.Metrics[0].Good {
		t.Errorf("metrics %+v", d.Metrics)
	}
	if len(d.Pages) != 1 || d.Pages[0].Label != "/<script>" || d.Pages[0].Bar != 100 || len(d.Countries) != 1 || d.Countries[0].Label != "Germany" {
		t.Errorf("lists %+v %+v", d.Pages, d.Countries)
	}
	if d.Subject() != "example.com: weekly report for Nov 16 – 22, 2026" {
		t.Errorf("subject %q", d.Subject())
	}
	text, html, err := d.Render()
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://stats.example.com/?from=2026-11-16&amp;range=custom&amp;site=example.com&amp;to=2026-11-22"
	for _, want := range []string{"Germany", "/&lt;script&gt;", wantURL, "/reports/unsubscribe/" + r.Token, "&#43;200%"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
	if strings.Contains(html, "/<script>") {
		t.Error("HTML doesn't escape paths")
	}
	for _, want := range []string{"Visitors", "3  (+200% vs the week before)", "/<script>", "Unsubscribe: https://stats.example.com/reports/unsubscribe/"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	// A week without visits says so.
	d, _ = Build(ctx, st, r, week.AddDate(0, 0, 14), week.AddDate(0, 0, 21), time.UTC)
	if _, html, _ := d.Render(); !d.Empty || !strings.Contains(html, "No visits in this period") {
		t.Error("empty week")
	}
}

func TestSendDue(t *testing.T) {
	st, site := testStore(t)
	ctx := context.Background()
	monday := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	// Added last Wednesday, so the week before that counts as done.
	from, _ := DuePeriod("weekly", monday.AddDate(0, 0, -5), time.UTC)
	w, _ := st.AddReport(ctx, site.ID, "w@example.com", "weekly", "https://s.example", Day(from))
	mfrom, _ := DuePeriod("monthly", monday, time.UTC)
	st.AddReport(ctx, site.ID, "m@example.com", "monthly", "https://s.example", Day(mfrom))

	m := &fakeMailer{err: errors.New("connection refused")}
	failed := map[int64]time.Time{}
	SendDue(ctx, st, m, time.UTC, monday, failed)
	rs, _ := st.Reports(ctx, site.ID)
	if rs[0].LastError != "connection refused" || rs[0].LastPeriod == "2026-11-16" {
		t.Fatalf("after failure: %+v", rs[0])
	}
	// Retried after an hour, not before.
	m.err = nil
	SendDue(ctx, st, m, time.UTC, monday.Add(30*time.Minute), failed)
	if len(m.sent) != 0 {
		t.Fatal("retried too soon")
	}
	SendDue(ctx, st, m, time.UTC, monday.Add(time.Hour), failed)
	if len(m.sent) != 1 || m.sent[0].To != "w@example.com" || !strings.Contains(m.sent[0].Subject, "Nov 16 – 22") || m.sent[0].UnsubscribeURL != "https://s.example/reports/unsubscribe/"+w.Token {
		t.Fatalf("sent %+v", m.sent)
	}
	rs, _ = st.Reports(ctx, site.ID)
	if rs[0].LastError != "" || rs[0].LastPeriod != "2026-11-16" || rs[0].LastSent.IsZero() {
		t.Errorf("after sending: %+v", rs[0])
	}
	// Once per period; the monthly one waits for December.
	SendDue(ctx, st, m, time.UTC, monday.Add(5*time.Hour), failed)
	if len(m.sent) != 1 {
		t.Errorf("sent twice: %d", len(m.sent))
	}
	SendDue(ctx, st, m, time.UTC, time.Date(2026, 12, 1, 7, 0, 0, 0, time.UTC), failed)
	if len(m.sent) != 3 || !strings.Contains(m.sent[1].Subject, "weekly report for Nov 23 – 29") || m.sent[2].To != "m@example.com" || !strings.Contains(m.sent[2].Subject, "monthly report for November 2026") {
		t.Errorf("December 1 %+v", m.sent)
	}
	// Unsubscribing stops it.
	st.Unsubscribe(ctx, w.Token)
	SendDue(ctx, st, m, time.UTC, time.Date(2026, 12, 7, 9, 0, 0, 0, time.UTC), failed)
	if len(m.sent) != 3 {
		t.Errorf("sent after unsubscribing: %d", len(m.sent))
	}
}

func TestCleanEmail(t *testing.T) {
	for _, ok := range []string{"a@example.com", " a.b+c@sub.example.org "} {
		if _, err := store.CleanEmail(ok); err != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "a", "a@", "A <a@example.com>", "a@example.com\r\nBcc: x@y.z", "a@example.com, b@example.com"} {
		if _, err := store.CleanEmail(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeSMTP accepts one message without TLS or login and returns it.
func fakeSMTP(t *testing.T) (string, chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { io.WriteString(c, s+"\r\n") }
		say("220 fake")
		var envelope []string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch cmd := strings.ToUpper(strings.Fields(line + " x")[0]); cmd {
			case "EHLO", "HELO":
				say("250 fake")
			case "MAIL", "RCPT":
				envelope = append(envelope, line)
				say("250 ok")
			case "DATA":
				say("354 go on")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				got <- strings.Join(envelope, "\n") + "\n\n" + b.String()
				say("250 queued")
			case "QUIT":
				say("221 bye")
				return
			default:
				say("502 no")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSMTP(t *testing.T) {
	if _, err := NewSMTP("http://x", "a@b.c"); err == nil {
		t.Error("http URL accepted")
	}
	if _, err := NewSMTP("smtp://x", "nope"); err == nil {
		t.Error("bad From accepted")
	}
	if m, _ := NewSMTP("smtps://u:p%40ss@mail.example.com", "a@b.c"); m.host != "mail.example.com:465" || m.password != "p@ss" || !m.implicit {
		t.Errorf("smtps %+v", m)
	}

	addr, got := fakeSMTP(t)
	m, err := NewSMTP("smtp://"+addr, "bonestats <stats@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	err = m.Send(context.Background(), Message{To: "me@example.com", Subject: "example.com: weekly report for Nov 16 – 22, 2026",
		Text: "Visitors 3\n", HTML: "<p>Visitors 3</p>", UnsubscribeURL: "https://s.example/reports/unsubscribe/T"})
	if err != nil {
		t.Fatal(err)
	}
	raw := <-got
	env, data, _ := strings.Cut(raw, "\n\n")
	if !strings.Contains(env, "FROM:<stats@example.com>") || !strings.Contains(env, "TO:<me@example.com>") {
		t.Errorf("envelope %q", env)
	}
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subj != "example.com: weekly report for Nov 16 – 22, 2026" || msg.Header.Get("List-Unsubscribe") != "<https://s.example/reports/unsubscribe/T>" ||
		msg.Header.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" || msg.Header.Get("From") != `"bonestats" <stats@example.com>` {
		t.Errorf("headers %v (subject %q)", msg.Header, subj)
	}
	_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p) // decodes quoted-printable
		types = append(types, p.Header.Get("Content-Type")+":"+string(b))
	}
	if len(types) != 2 || types[0] != "text/plain; charset=utf-8:Visitors 3\r\n" || types[1] != "text/html; charset=utf-8:<p>Visitors 3</p>" {
		t.Errorf("parts %q", types)
	}

	// Header injection through the recipient is refused.
	if err := m.Send(context.Background(), Message{To: "a@b.c\r\nBcc: x@y.z"}); err == nil {
		t.Error("CRLF in To accepted")
	}
}

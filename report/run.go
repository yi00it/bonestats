// SPDX-License-Identifier: AGPL-3.0-only

package report

import (
	"context"
	"log"
	"time"

	"github.com/yi00it/bonestats/store"
)

// checkEvery is how often Run looks for reports that are due.
const checkEvery = 15 * time.Minute

// retryAfter is how long Run waits before trying a failed report again.
const retryAfter = time.Hour

// Send builds the report r for the period starting at from and emails it.
func Send(ctx context.Context, st *store.Store, m Mailer, r store.Report, from, to time.Time, loc *time.Location) error {
	d, err := Build(ctx, st, r, from, to, loc)
	if err != nil {
		return err
	}
	text, html, err := d.Render()
	if err != nil {
		return err
	}
	return m.Send(ctx, Message{To: r.Email, Subject: d.Subject(), Text: text, HTML: html, UnsubscribeURL: d.UnsubscribeURL})
}

// Run sends every report once its period is over, until ctx ends. Each
// period goes out once; one that failed is tried again an hour later, and
// one missed while bonestats wasn't running is sent when it starts, unless
// a newer period has ended since.
func Run(ctx context.Context, st *store.Store, m Mailer, loc *time.Location) {
	failed := map[int64]time.Time{}
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		SendDue(ctx, st, m, loc, time.Now(), failed)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SendDue sends the reports due at now. failed remembers when reports
// failed, to wait before trying them again.
func SendDue(ctx context.Context, st *store.Store, m Mailer, loc *time.Location, now time.Time, failed map[int64]time.Time) {
	for _, period := range Periods {
		from, to := DuePeriod(period, now, loc)
		due, err := st.DueReports(ctx, period, Day(from))
		if err != nil {
			log.Printf("reports: %v", err)
			return
		}
		for _, r := range due {
			if at, ok := failed[r.ID]; ok && now.Sub(at) < retryAfter {
				continue
			}
			err := Send(ctx, st, m, r, from, to, loc)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				failed[r.ID] = now
				log.Printf("reports: %s report of %s to %s: %v", period, r.Domain, r.Email, err)
			} else {
				delete(failed, r.ID)
			}
			if err := st.ReportSent(ctx, r.ID, Day(from), err); err != nil {
				log.Printf("reports: %v", err)
			}
		}
	}
}

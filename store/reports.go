// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrReportEmail  = errors.New("enter one email address, like name@example.com")
	ErrReportPeriod = errors.New("a report is weekly or monthly")
)

// A Report sends one site's statistics to one email address every week or
// month.
type Report struct {
	ID         int64
	SiteID     int64
	Domain     string // of the site, filled in by DueReports and ReportByToken
	Email      string
	Period     string // "weekly" or "monthly"
	Token      string // for the unsubscribe link
	BaseURL    string // the dashboard's address, for links
	CreatedAt  time.Time
	LastPeriod string // first day of the last period sent, YYYY-MM-DD
	LastSent   time.Time
	LastError  string
}

// CleanEmail returns a bare, valid email address, or ErrReportEmail.
func CleanEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	a, err := mail.ParseAddress(email)
	if err != nil || a.Name != "" || a.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n<>") {
		return "", ErrReportEmail
	}
	return email, nil
}

// AddReport adds a recipient. lastPeriod is the first day of the last
// period that counts as sent already, so the first email covers the next
// period to end, not one that ended before the address was added.
func (s *Store) AddReport(ctx context.Context, siteID int64, email, period, baseURL, lastPeriod string) (Report, error) {
	email, err := CleanEmail(email)
	if err != nil {
		return Report{}, err
	}
	if period != "weekly" && period != "monthly" {
		return Report{}, ErrReportPeriod
	}
	r := Report{SiteID: siteID, Email: email, Period: period, Token: rand.Text(), BaseURL: baseURL, CreatedAt: time.Now(), LastPeriod: lastPeriod}
	res, err := s.db.ExecContext(ctx, `INSERT INTO reports(site_id, email, period, token, base_url, created_at, last_period) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		siteID, email, period, r.Token, baseURL, r.CreatedAt.Unix(), lastPeriod)
	if err != nil {
		return Report{}, err
	}
	r.ID, _ = res.LastInsertId()
	return r, nil
}

const reportCols = `r.id, r.site_id, s.domain, r.email, r.period, r.token, r.base_url, r.created_at, r.last_period, r.last_sent, r.last_error`

func scanReport(sc interface{ Scan(...any) error }) (Report, error) {
	var r Report
	var created int64
	var sent sql.NullInt64
	err := sc.Scan(&r.ID, &r.SiteID, &r.Domain, &r.Email, &r.Period, &r.Token, &r.BaseURL, &created, &r.LastPeriod, &sent, &r.LastError)
	r.CreatedAt = time.Unix(created, 0)
	if sent.Valid {
		r.LastSent = time.Unix(sent.Int64, 0)
	}
	return r, err
}

func (s *Store) queryReports(ctx context.Context, where string, args ...any) ([]Report, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT `+reportCols+` FROM reports r JOIN sites s ON s.id = r.site_id WHERE `+where+` ORDER BY r.created_at, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reports lists a site's recipients.
func (s *Store) Reports(ctx context.Context, siteID int64) ([]Report, error) {
	return s.queryReports(ctx, `r.site_id = ?`, siteID)
}

// DueReports lists the recipients of a period whose last report is older
// than the period starting on day (YYYY-MM-DD).
func (s *Store) DueReports(ctx context.Context, period, day string) ([]Report, error) {
	return s.queryReports(ctx, `r.period = ? AND r.last_period < ?`, period, day)
}

// ReportByID returns one of a site's recipients.
func (s *Store) ReportByID(ctx context.Context, siteID, id int64) (Report, bool, error) {
	rs, err := s.queryReports(ctx, `r.site_id = ? AND r.id = ?`, siteID, id)
	if err != nil || len(rs) == 0 {
		return Report{}, false, err
	}
	return rs[0], true, nil
}

// ReportByToken returns the recipient an unsubscribe link belongs to.
func (s *Store) ReportByToken(ctx context.Context, token string) (Report, bool, error) {
	rs, err := s.queryReports(ctx, `r.token = ?`, token)
	if err != nil || len(rs) == 0 {
		return Report{}, false, err
	}
	return rs[0], true, nil
}

// ReportSent records that the period starting on day went out, or why it
// didn't (sendErr). A failed period is tried again later.
func (s *Store) ReportSent(ctx context.Context, id int64, day string, sendErr error) error {
	if sendErr != nil {
		msg := sendErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, err := s.db.ExecContext(ctx, `UPDATE reports SET last_error = ? WHERE id = ?`, msg, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET last_period = MAX(last_period, ?), last_sent = ?, last_error = '' WHERE id = ?`, day, time.Now().Unix(), id)
	return err
}

func (s *Store) DeleteReport(ctx context.Context, siteID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ? AND site_id = ?`, id, siteID)
	return err
}

// Unsubscribe deletes the recipient an unsubscribe link belongs to.
func (s *Store) Unsubscribe(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE token = ?`, token)
	return err
}

// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"
)

// An Import is data brought in from another analytics tool.
type Import struct {
	ID                int64
	SiteID            int64
	Source            string // "plausible" or "umami"
	FirstDay, LastDay string // YYYY-MM-DD, inclusive; empty if nothing was imported
	Visits, Pageviews int64
	Complete          bool
	CreatedAt         time.Time
}

// ErrImportOverlap is returned when imported days overlap an earlier import.
var ErrImportOverlap = errors.New("these days were already imported")

func (s *Store) Imports(ctx context.Context, siteID int64) ([]Import, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, site_id, source, first_day, last_day, visits, pageviews, complete, created_at
		FROM imports WHERE site_id = ? ORDER BY first_day, id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Import
	for rows.Next() {
		var im Import
		var created int64
		if err := rows.Scan(&im.ID, &im.SiteID, &im.Source, &im.FirstDay, &im.LastDay, &im.Visits, &im.Pageviews, &im.Complete, &created); err != nil {
			return nil, err
		}
		im.CreatedAt = time.Unix(created, 0)
		out = append(out, im)
	}
	return out, rows.Err()
}

// HasImportedTotals reports whether the site has imported daily totals in
// [from, to), which unfiltered views include and filtered views leave out.
func (s *Store) HasImportedTotals(ctx context.Context, siteID int64, from, to time.Time) (bool, error) {
	var n int
	err := s.rdb.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM imported WHERE site_id = ? AND dim = '' AND ts >= ? AND ts < ?)`,
		siteID, from.Unix(), to.Unix()).Scan(&n)
	return n > 0, err
}

// DeleteImport removes an import and everything it brought in.
func (s *Store) DeleteImport(ctx context.Context, siteID, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM imports WHERE id = ? AND site_id = ?`, id, siteID).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, q := range []string{
		`DELETE FROM events WHERE session_id IN (SELECT id FROM sessions WHERE import_id = ?)`,
		`DELETE FROM sessions WHERE import_id = ?`,
		`DELETE FROM imported WHERE import_id = ?`,
		`DELETE FROM imports WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NativeStart returns when the site's first visit recorded by bonestats
// itself started. Imports stop before it, so no day is counted twice.
func (s *Store) NativeStart(ctx context.Context, siteID int64) (time.Time, bool, error) {
	var ts sql.NullInt64
	err := s.rdb.QueryRowContext(ctx, `SELECT MIN(started_at) FROM sessions WHERE site_id = ? AND import_id IS NULL`, siteID).Scan(&ts)
	return time.Unix(ts.Int64, 0), ts.Valid, err
}

// ImportedVisit is one visit from another tool, with its events in order.
type ImportedVisit struct {
	Visitor int64
	Visit   Visit
	Events  []ImportedEvent
}

type ImportedEvent struct {
	Time    time.Time
	Name    string // "pageview" or a custom event name
	Path    string
	Props   map[string]string
	Engaged *int64 // ms the page was visible, pageviews only
}

// ImportedRow is one day's totals for one value of a dimension (Dim ""
// for the day's overall totals). Day is the calendar day in the dashboard's
// timezone.
type ImportedRow struct {
	Day                                    time.Time
	Dim, Value, Hint                       string
	Visitors, Visits, Pageviews, Bounces   int64
	Duration                               int64 // seconds, summed over visits
	Count                                  int64
	TimeTotal, TimeN, ScrollTotal, ScrollN int64
}

// An ImportWriter writes one import in batches, so incoming beacons aren't
// held up for long. Call Finish when done, or Abort to remove what was
// written.
type ImportWriter struct {
	s                 *Store
	ctx               context.Context
	id, siteID        int64
	tx                *sql.Tx
	pending           int
	first, last       string
	visits, pageviews int64
}

const importBatch = 2000

// BeginImport starts an import for the site.
func (s *Store) BeginImport(ctx context.Context, siteID int64, source string) (*ImportWriter, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO imports(site_id, source, created_at) VALUES(?, ?, ?)`, siteID, source, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &ImportWriter{s: s, ctx: ctx, id: id, siteID: siteID}, nil
}

func (w *ImportWriter) ID() int64 { return w.id }

func (w *ImportWriter) begin() error {
	if w.tx != nil {
		return nil
	}
	tx, err := w.s.db.BeginTx(w.ctx, nil)
	w.tx = tx
	return err
}

func (w *ImportWriter) step() error {
	w.pending++
	if w.pending < importBatch {
		return nil
	}
	return w.flush()
}

func (w *ImportWriter) flush() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx, w.pending = nil, 0
	return err
}

func (w *ImportWriter) seen(day string) {
	if w.first == "" || day < w.first {
		w.first = day
	}
	if day > w.last {
		w.last = day
	}
}

// AddVisit stores a visit as if bonestats had recorded it. Imported visits
// count without the visible-time check, which the other tool's bot
// filtering replaces.
func (w *ImportWriter) AddVisit(v ImportedVisit, loc *time.Location) error {
	if len(v.Events) == 0 {
		return nil
	}
	if err := w.begin(); err != nil {
		return err
	}
	var pageviews, engaged int64
	entry, exit := "", ""
	for _, e := range v.Events {
		if e.Name != "pageview" {
			continue
		}
		pageviews++
		if entry == "" {
			entry = e.Path
		}
		exit = e.Path
		if e.Engaged != nil {
			engaged += *e.Engaged
		}
	}
	start, end := v.Events[0].Time, v.Events[len(v.Events)-1].Time
	a := v.Visit
	res, err := w.tx.ExecContext(w.ctx, `
		INSERT INTO sessions(site_id, visitor, started_at, last_seen, pageviews, entry_path, exit_path,
			referrer, referrer_url, channel, utm_source, utm_medium, utm_campaign, utm_term, utm_content,
			country, region, city, browser, browser_version, os, os_version, device, screen, human, engaged, import_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		w.siteID, v.Visitor, start.Unix(), end.Unix(), pageviews, entry, exit,
		a.Referrer, a.ReferrerURL, a.Channel, a.UTMSource, a.UTMMedium, a.UTMCampaign, a.UTMTerm, a.UTMContent,
		a.Country, a.Region, a.City, a.Browser, a.BrowserVersion, a.OS, a.OSVersion, a.Device, a.Screen, engaged, w.id)
	if err != nil {
		return err
	}
	sid, _ := res.LastInsertId()
	for _, e := range v.Events {
		var props any
		if len(e.Props) > 0 {
			b, _ := json.Marshal(e.Props)
			props = string(b)
		}
		if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO events(site_id, ts, visitor, session_id, name, path, props, engaged)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, w.siteID, e.Time.Unix(), v.Visitor, sid, e.Name, e.Path, props, e.Engaged); err != nil {
			return err
		}
	}
	if pageviews > 0 {
		w.visits++
	}
	w.pageviews += pageviews
	w.seen(start.In(loc).Format("2006-01-02"))
	w.seen(end.In(loc).Format("2006-01-02"))
	return w.step()
}

// AddRow stores one row of daily totals.
func (w *ImportWriter) AddRow(r ImportedRow) error {
	if err := w.begin(); err != nil {
		return err
	}
	day := r.Day.Format("2006-01-02")
	noon := time.Date(r.Day.Year(), r.Day.Month(), r.Day.Day(), 12, 0, 0, 0, r.Day.Location())
	if _, err := w.tx.ExecContext(w.ctx, `INSERT INTO imported(import_id, site_id, ts, dim, value, hint, visitors, visits, pageviews,
		bounces, duration, count, time_total, time_n, scroll_total, scroll_n) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.id, w.siteID, noon.Unix(), r.Dim, r.Value, r.Hint, r.Visitors, r.Visits, r.Pageviews, r.Bounces, r.Duration, r.Count,
		r.TimeTotal, r.TimeN, r.ScrollTotal, r.ScrollN); err != nil {
		return err
	}
	if r.Dim == "" {
		w.visits += r.Visits
		w.pageviews += r.Pageviews
		w.seen(day)
	}
	return w.step()
}

// Finish commits the rest and marks the import complete. It fails if the
// imported days overlap an earlier import of the site, and then removes
// this one.
func (w *ImportWriter) Finish() (Import, error) {
	if err := w.flush(); err != nil {
		w.Abort()
		return Import{}, err
	}
	var overlap int
	err := w.s.db.QueryRowContext(w.ctx, `SELECT COUNT(*) FROM imports WHERE site_id = ? AND id <> ? AND complete = 1
		AND first_day <> '' AND first_day <= ? AND last_day >= ?`, w.siteID, w.id, w.last, w.first).Scan(&overlap)
	if err == nil && overlap > 0 && w.first != "" {
		err = ErrImportOverlap
	}
	if err == nil {
		_, err = w.s.db.ExecContext(w.ctx, `UPDATE imports SET first_day = ?, last_day = ?, visits = ?, pageviews = ?, complete = 1 WHERE id = ?`,
			w.first, w.last, w.visits, w.pageviews, w.id)
	}
	if err != nil {
		w.Abort()
		return Import{}, err
	}
	return Import{ID: w.id, SiteID: w.siteID, FirstDay: w.first, LastDay: w.last, Visits: w.visits, Pageviews: w.pageviews, Complete: true}, nil
}

// Abort removes everything this import wrote.
func (w *ImportWriter) Abort() {
	if w.tx != nil {
		w.tx.Rollback()
		w.tx = nil
	}
	// The request may be gone; clean up regardless.
	ctx := context.WithoutCancel(w.ctx)
	if err := w.s.DeleteImport(ctx, w.siteID, w.id); err != nil {
		log.Printf("abort import %d: %v", w.id, err)
	}
}

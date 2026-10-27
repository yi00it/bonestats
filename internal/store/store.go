// Package store owns the SQLite database: schema, writes from the collector,
// and housekeeping (salt rotation, retention).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SessionTimeout is the inactivity gap after which a new visit starts.
const SessionTimeout = 30 * time.Minute

const schemaVersion = 2

type Store struct {
	db *sql.DB
}

// Visit-level attributes live on sessions (they describe where a visit came
// from and on what device), so filtering by them covers every pageview of the
// visit. Events only carry what can differ within a visit.
const schema = `
CREATE TABLE IF NOT EXISTS sites (
	id         INTEGER PRIMARY KEY,
	domain     TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);

-- No IP and no persistent ID: visitor is a hash built with a salt that is
-- rotated and destroyed daily.
CREATE TABLE IF NOT EXISTS sessions (
	id              INTEGER PRIMARY KEY,
	site_id         INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	visitor         INTEGER NOT NULL,
	started_at      INTEGER NOT NULL,
	last_seen       INTEGER NOT NULL,
	pageviews       INTEGER NOT NULL DEFAULT 0,
	entry_path      TEXT NOT NULL DEFAULT '',
	exit_path       TEXT NOT NULL DEFAULT '',
	referrer        TEXT NOT NULL DEFAULT '', -- host only
	referrer_url    TEXT NOT NULL DEFAULT '', -- host + path, never the query
	channel         TEXT NOT NULL DEFAULT '',
	utm_source      TEXT NOT NULL DEFAULT '',
	utm_medium      TEXT NOT NULL DEFAULT '',
	utm_campaign    TEXT NOT NULL DEFAULT '',
	utm_term        TEXT NOT NULL DEFAULT '',
	utm_content     TEXT NOT NULL DEFAULT '',
	country         TEXT NOT NULL DEFAULT '',
	region          TEXT NOT NULL DEFAULT '',
	city            TEXT NOT NULL DEFAULT '',
	browser         TEXT NOT NULL DEFAULT '',
	browser_version TEXT NOT NULL DEFAULT '', -- major only, e.g. "Chrome 141"
	os              TEXT NOT NULL DEFAULT '',
	os_version      TEXT NOT NULL DEFAULT '',
	device          TEXT NOT NULL DEFAULT '',
	screen          TEXT NOT NULL DEFAULT ''  -- width bucket
);
CREATE INDEX IF NOT EXISTS sessions_visitor ON sessions(site_id, visitor, last_seen);
CREATE INDEX IF NOT EXISTS sessions_site_started ON sessions(site_id, started_at);

-- One row per pageview or custom event.
CREATE TABLE IF NOT EXISTS events (
	id         INTEGER PRIMARY KEY,
	site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	ts         INTEGER NOT NULL,
	visitor    INTEGER NOT NULL,
	session_id INTEGER NOT NULL,
	name       TEXT NOT NULL,  -- 'pageview' or a custom event name
	path       TEXT NOT NULL,
	props      TEXT,           -- JSON object of string values (custom events)
	revenue    REAL,           -- amount in the goal's currency
	scroll     INTEGER,        -- max scroll depth in percent (pageviews)
	engaged    INTEGER         -- visible time on the page in ms (pageviews)
);
CREATE INDEX IF NOT EXISTS events_site_ts ON events(site_id, ts);
CREATE INDEX IF NOT EXISTS events_session ON events(session_id);

-- Core Web Vitals, one row per page load. NULL = metric not reported.
CREATE TABLE IF NOT EXISTS vitals (
	id      INTEGER PRIMARY KEY,
	site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	ts      INTEGER NOT NULL,
	path    TEXT NOT NULL,
	device  TEXT NOT NULL DEFAULT '',
	lcp     REAL,
	cls     REAL,
	inp     REAL,
	fcp     REAL,
	ttfb    REAL
);
CREATE INDEX IF NOT EXISTS vitals_site_ts ON vitals(site_id, ts);

-- A goal is either a custom event name or a pageview path pattern (* = any).
CREATE TABLE IF NOT EXISTS goals (
	id         INTEGER PRIMARY KEY,
	site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	event_name TEXT NOT NULL DEFAULT '',
	page_path  TEXT NOT NULL DEFAULT '',
	currency   TEXT NOT NULL DEFAULT '', -- set = revenue goal
	created_at INTEGER NOT NULL,
	UNIQUE(site_id, event_name, page_path)
);

CREATE TABLE IF NOT EXISTS funnels (
	id         INTEGER PRIMARY KEY,
	site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	steps      TEXT NOT NULL, -- JSON array of goal ids, in order
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS salts (
	day  TEXT PRIMARY KEY,
	salt BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
);
`

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY churn
	// and is plenty for this workload.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		var existing int
		s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'events'`).Scan(&existing)
		if existing > 0 {
			version = 1 // database from before versioning
		}
	}
	if version == 1 {
		if err := s.migrateV1(ctx); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// migrateV1 moves visit attributes from events onto sessions.
func (s *Store) migrateV1(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{`CREATE INDEX IF NOT EXISTS events_session ON events(session_id)`}
	for _, c := range []string{"entry_path", "exit_path", "referrer", "referrer_url", "channel", "utm_source", "utm_medium",
		"utm_campaign", "utm_term", "utm_content", "country", "region", "city", "browser", "browser_version",
		"os", "os_version", "device", "screen"} {
		stmts = append(stmts, fmt.Sprintf(`ALTER TABLE sessions ADD COLUMN %s TEXT NOT NULL DEFAULT ''`, c))
	}
	stmts = append(stmts,
		`UPDATE sessions SET (referrer, referrer_url, utm_source, utm_medium, utm_campaign, browser, os, device, country) =
			(SELECT e.referrer, e.referrer, e.utm_source, e.utm_medium, e.utm_campaign, e.browser, e.os, e.device, e.country
			 FROM events e WHERE e.session_id = sessions.id ORDER BY e.ts, e.id LIMIT 1)
		 WHERE EXISTS (SELECT 1 FROM events e WHERE e.session_id = sessions.id)`,
		`UPDATE sessions SET
			entry_path = COALESCE((SELECT path FROM events e WHERE e.session_id = sessions.id AND e.name = 'pageview' ORDER BY ts, id LIMIT 1), ''),
			exit_path = COALESCE((SELECT path FROM events e WHERE e.session_id = sessions.id AND e.name = 'pageview' ORDER BY ts DESC, id DESC LIMIT 1), '')`,
	)
	for _, c := range []string{"referrer", "utm_source", "utm_medium", "utm_campaign", "browser", "os", "device", "country"} {
		stmts = append(stmts, fmt.Sprintf(`ALTER TABLE events DROP COLUMN %s`, c))
	}
	for _, c := range []string{"props TEXT", "revenue REAL", "scroll INTEGER", "engaged INTEGER"} {
		stmts = append(stmts, `ALTER TABLE events ADD COLUMN `+c)
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", strings.Fields(q)[0:3], err)
		}
	}

	// Channels are derived in Go.
	rows, err := tx.QueryContext(ctx, `SELECT id, referrer, utm_source, utm_medium FROM sessions`)
	if err != nil {
		return err
	}
	channels := map[int64]string{}
	for rows.Next() {
		var id int64
		var ref, src, med string
		if err := rows.Scan(&id, &ref, &src, &med); err != nil {
			rows.Close()
			return err
		}
		channels[id] = Channel(ref, src, med)
	}
	rows.Close()
	for id, ch := range channels {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET channel = ? WHERE id = ?`, ch, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- sites ----

type Site struct {
	ID        int64
	Domain    string
	CreatedAt time.Time
}

// NormalizeDomain lowercases and strips scheme, path, port and "www.".
func NormalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndexByte(d, ':'); i >= 0 {
		d = d[:i]
	}
	return strings.TrimPrefix(d, "www.")
}

func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, domain, created_at FROM sites ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var st Site
		var created int64
		if err := rows.Scan(&st.ID, &st.Domain, &created); err != nil {
			return nil, err
		}
		st.CreatedAt = time.Unix(created, 0)
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) SiteByDomain(ctx context.Context, domain string) (Site, error) {
	var st Site
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, domain, created_at FROM sites WHERE domain = ?`, domain).
		Scan(&st.ID, &st.Domain, &created)
	st.CreatedAt = time.Unix(created, 0)
	return st, err
}

func (s *Store) AddSite(ctx context.Context, domain string) error {
	domain = NormalizeDomain(domain)
	if domain == "" || strings.ContainsAny(domain, " \t") {
		return errors.New("invalid domain")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sites(domain, created_at) VALUES(?, ?)`, domain, time.Now().Unix())
	return err
}

func (s *Store) DeleteSite(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
	return err
}

// FirstVisit returns when the site's oldest stored visit started.
func (s *Store) FirstVisit(ctx context.Context, siteID int64) (time.Time, bool) {
	var ts sql.NullInt64
	s.db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM sessions WHERE site_id = ?`, siteID).Scan(&ts)
	return time.Unix(ts.Int64, 0), ts.Valid
}

// ---- writes from the collector ----

// Visit describes where a visit came from; it is stored when the visit starts.
type Visit struct {
	Referrer, ReferrerURL, Channel                         string
	UTMSource, UTMMedium, UTMCampaign, UTMTerm, UTMContent string
	Country, Region, City                                  string
	Browser, BrowserVersion, OS, OSVersion, Device, Screen string
}

type Event struct {
	SiteID  int64
	Time    time.Time
	Visitor int64
	Name    string
	Path    string
	Props   map[string]string
	Revenue *float64
	Visit   Visit
}

// RecordEvent stores a pageview or custom event and attaches it to the
// visitor's current session, opening a new session if needed.
func (s *Store) RecordEvent(ctx context.Context, e Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := e.Time.Unix()
	isPageview := e.Name == "pageview"
	pv := 0
	entry := ""
	if isPageview {
		pv, entry = 1, e.Path
	}

	sessionID, err := currentSession(ctx, tx, e.SiteID, e.Visitor, now)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		v := e.Visit
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sessions(site_id, visitor, started_at, last_seen, pageviews, entry_path, exit_path,
				referrer, referrer_url, channel, utm_source, utm_medium, utm_campaign, utm_term, utm_content,
				country, region, city, browser, browser_version, os, os_version, device, screen)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.SiteID, e.Visitor, now, now, pv, entry, entry,
			v.Referrer, v.ReferrerURL, v.Channel, v.UTMSource, v.UTMMedium, v.UTMCampaign, v.UTMTerm, v.UTMContent,
			v.Country, v.Region, v.City, v.Browser, v.BrowserVersion, v.OS, v.OSVersion, v.Device, v.Screen)
		if err != nil {
			return err
		}
		sessionID, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions SET last_seen = MAX(last_seen, ?), pageviews = pageviews + ?,
				entry_path = CASE WHEN entry_path = '' THEN ? ELSE entry_path END,
				exit_path = CASE WHEN ? THEN ? ELSE exit_path END
			WHERE id = ?`,
			now, pv, entry, isPageview, e.Path, sessionID); err != nil {
			return err
		}
	}

	var props any
	if len(e.Props) > 0 {
		b, _ := json.Marshal(e.Props)
		props = string(b)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events(site_id, ts, visitor, session_id, name, path, props, revenue)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		e.SiteID, now, e.Visitor, sessionID, e.Name, e.Path, props, e.Revenue); err != nil {
		return err
	}
	return tx.Commit()
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func currentSession(ctx context.Context, q querier, siteID, visitor, now int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `
		SELECT id FROM sessions
		WHERE site_id = ? AND visitor = ? AND last_seen >= ?
		ORDER BY last_seen DESC LIMIT 1`,
		siteID, visitor, now-int64(SessionTimeout.Seconds())).Scan(&id)
	return id, err
}

// Leave records that the visitor left (or hid) a page: it extends the visit
// so its duration includes time on the last page, and stores scroll depth
// and engaged time on that page's pageview.
type Leave struct {
	SiteID, Visitor int64
	Time            time.Time
	Path            string
	Scroll          *int64 // percent
	Engaged         *int64 // ms
}

func (s *Store) RecordLeave(ctx context.Context, l Leave) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := l.Time.Unix()
	sessionID, err := currentSession(ctx, tx, l.SiteID, l.Visitor, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen = MAX(last_seen, ?) WHERE id = ?`, now, sessionID); err != nil {
		return err
	}
	const target = `(SELECT id FROM events WHERE session_id = ? AND name = 'pageview' AND path = ? ORDER BY ts DESC, id DESC LIMIT 1)`
	if l.Scroll != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE events SET scroll = MAX(COALESCE(scroll, 0), ?) WHERE id = `+target,
			*l.Scroll, sessionID, l.Path); err != nil {
			return err
		}
	}
	if l.Engaged != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE events SET engaged = COALESCE(engaged, 0) + ? WHERE id = `+target,
			*l.Engaged, sessionID, l.Path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Vitals struct {
	SiteID                   int64
	Time                     time.Time
	Path                     string
	Device                   string
	LCP, CLS, INP, FCP, TTFB *float64
}

func (s *Store) RecordVitals(ctx context.Context, v Vitals) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO vitals(site_id, ts, path, device, lcp, cls, inp, fcp, ttfb)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.SiteID, v.Time.Unix(), v.Path, v.Device, v.LCP, v.CLS, v.INP, v.FCP, v.TTFB)
	return err
}

// ---- goals & funnels ----

type Goal struct {
	ID        int64
	SiteID    int64
	EventName string // custom event goal ...
	PagePath  string // ... or pageview goal, "*" matches anything
	Currency  string // non-empty for revenue goals
}

// Name is how the goal is shown and referred to in filters.
func (g Goal) Name() string {
	if g.PagePath != "" {
		return "Visit " + g.PagePath
	}
	return g.EventName
}

func (s *Store) Goals(ctx context.Context, siteID int64) ([]Goal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, site_id, event_name, page_path, currency FROM goals WHERE site_id = ? ORDER BY id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.SiteID, &g.EventName, &g.PagePath, &g.Currency); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) AddGoal(ctx context.Context, g Goal) error {
	g.EventName = strings.TrimSpace(g.EventName)
	g.PagePath = strings.TrimSpace(g.PagePath)
	g.Currency = strings.ToUpper(strings.TrimSpace(g.Currency))
	switch {
	case (g.EventName == "") == (g.PagePath == ""):
		return errors.New("a goal needs either an event name or a page path")
	case len(g.EventName) > 64:
		return errors.New("event name is too long")
	case g.PagePath != "" && !strings.HasPrefix(g.PagePath, "/"):
		return errors.New("page paths start with /")
	case g.Currency != "" && (len(g.Currency) != 3 || g.PagePath != ""):
		return errors.New("revenue needs a 3-letter currency code and an event goal")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO goals(site_id, event_name, page_path, currency, created_at) VALUES(?, ?, ?, ?, ?)`,
		g.SiteID, g.EventName, g.PagePath, g.Currency, time.Now().Unix())
	return err
}

// DeleteGoal removes a goal and every funnel that uses it.
func (s *Store) DeleteGoal(ctx context.Context, siteID, id int64) error {
	funnels, err := s.Funnels(ctx, siteID)
	if err != nil {
		return err
	}
	for _, f := range funnels {
		for _, step := range f.Steps {
			if step == id {
				if err := s.DeleteFunnel(ctx, siteID, f.ID); err != nil {
					return err
				}
				break
			}
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM goals WHERE id = ? AND site_id = ?`, id, siteID)
	return err
}

type Funnel struct {
	ID     int64
	SiteID int64
	Name   string
	Steps  []int64 // goal ids
}

func (s *Store) Funnels(ctx context.Context, siteID int64) ([]Funnel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, name, steps FROM funnels WHERE site_id = ? ORDER BY id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Funnel
	for rows.Next() {
		var f Funnel
		var steps string
		if err := rows.Scan(&f.ID, &f.SiteID, &f.Name, &steps); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(steps), &f.Steps)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) AddFunnel(ctx context.Context, f Funnel) error {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len(f.Name) > 100 {
		return errors.New("a funnel needs a name")
	}
	if len(f.Steps) < 2 || len(f.Steps) > 8 {
		return errors.New("a funnel needs 2 to 8 steps")
	}
	steps, _ := json.Marshal(f.Steps)
	_, err := s.db.ExecContext(ctx, `INSERT INTO funnels(site_id, name, steps, created_at) VALUES(?, ?, ?, ?)`,
		f.SiteID, f.Name, string(steps), time.Now().Unix())
	return err
}

func (s *Store) DeleteFunnel(ctx context.Context, siteID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM funnels WHERE id = ? AND site_id = ?`, id, siteID)
	return err
}

// ---- salts & settings ----

// DailySalt returns the salt for the given day, creating it if needed and
// deleting every other day's salt, so yesterday's hashes can never be
// recomputed or linked to today's.
func (s *Store) DailySalt(ctx context.Context, day string) ([]byte, error) {
	var salt []byte
	err := s.db.QueryRowContext(ctx, `SELECT salt FROM salts WHERE day = ?`, day).Scan(&salt)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	salt = make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM salts WHERE day <> ?`, day); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO salts(day, salt) VALUES(?, ?)`, day, salt); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT salt FROM salts WHERE day = ?`, day).Scan(&salt); err != nil {
		return nil, err
	}
	return salt, tx.Commit()
}

// Secret returns a persistent random secret stored under key, generating it
// on first use. Used to sign the admin login cookie.
func (s *Store) Secret(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	v = make([]byte, 32)
	if _, err := rand.Read(v); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES(?, ?)`, key, v); err != nil {
		return nil, err
	}
	return s.Secret(ctx, key)
}

// ---- retention ----

// Prune deletes all data older than the cutoff and returns the number of
// deleted events.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	cut := before.Unix()
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE ts < ?`, cut)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen < ?`, cut); err != nil {
		return n, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM vitals WHERE ts < ?`, cut); err != nil {
		return n, err
	}
	return n, nil
}

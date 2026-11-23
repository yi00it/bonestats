// SPDX-License-Identifier: AGPL-3.0-only

// Package store owns the SQLite database: schema, writes from the collector,
// and housekeeping (salt rotation, retention).
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SessionTimeout is the inactivity gap after which a new visit starts.
const SessionTimeout = 30 * time.Minute

// MinEngaged is how long a page must be visible before its visit counts.
// Bots that run the tracking script usually load a page and are gone before
// the browser reports any time on it; their visits are kept but never shown.
const MinEngaged = time.Second

const schemaVersion = 6

// Store uses two pools: db, the single writer, and rdb, read-only
// connections for dashboard queries. With WAL, readers never block the
// writer, so slow dashboard queries can't hold up incoming beacons.
type Store struct {
	db  *sql.DB
	rdb *sql.DB
}

// readers is the size of the read-only pool.
const readers = 4

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
	screen          TEXT NOT NULL DEFAULT '', -- width bucket
	human           INTEGER NOT NULL DEFAULT 0, -- 1 once a page was visible for MinEngaged
	engaged         INTEGER NOT NULL DEFAULT 0, -- visible time on all pages of the visit in ms
	import_id       INTEGER                     -- set for visits imported from another tool
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
-- event_id is the load's pageview, so a later report from the same load
-- updates its row and the row can be filtered by the visit's attributes.
CREATE TABLE IF NOT EXISTS vitals (
	id       INTEGER PRIMARY KEY,
	site_id  INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	ts       INTEGER NOT NULL,
	path     TEXT NOT NULL,
	device   TEXT NOT NULL DEFAULT '',
	lcp      REAL,
	cls      REAL,
	inp      REAL,
	fcp      REAL,
	ttfb     REAL,
	event_id INTEGER
);
CREATE INDEX IF NOT EXISTS vitals_site_ts ON vitals(site_id, ts);
CREATE UNIQUE INDEX IF NOT EXISTS vitals_event ON vitals(event_id);

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

-- Admin logins: sha256 of the cookie token, so a leaked database can't be
-- used to log in.
CREATE TABLE IF NOT EXISTS logins (
	token_hash BLOB PRIMARY KEY,
	expires    INTEGER NOT NULL
);

-- Data imported from other analytics tools. complete is 0 while an import
-- runs (or if it failed), so it can be deleted.
CREATE TABLE IF NOT EXISTS imports (
	id         INTEGER PRIMARY KEY,
	site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	source     TEXT NOT NULL,             -- 'plausible' or 'umami'
	first_day  TEXT NOT NULL DEFAULT '',  -- YYYY-MM-DD, inclusive
	last_day   TEXT NOT NULL DEFAULT '',
	visits     INTEGER NOT NULL DEFAULT 0,
	pageviews  INTEGER NOT NULL DEFAULT 0,
	complete   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);

-- Daily totals imported from tools that only export aggregates (Plausible).
-- dim '' holds the day's totals; other rows break the day down by a
-- dashboard dimension. ts is noon of the day, so the day stays inside the
-- same local calendar day in any timezone. These rows are added to
-- unfiltered views only: they can't be filtered.
CREATE TABLE IF NOT EXISTS imported (
	import_id    INTEGER NOT NULL REFERENCES imports(id) ON DELETE CASCADE,
	site_id      INTEGER NOT NULL,
	ts           INTEGER NOT NULL,
	dim          TEXT NOT NULL,
	value        TEXT NOT NULL,
	hint         TEXT NOT NULL DEFAULT '',
	visitors     INTEGER NOT NULL DEFAULT 0,
	visits       INTEGER NOT NULL DEFAULT 0,
	pageviews    INTEGER NOT NULL DEFAULT 0,
	bounces      INTEGER NOT NULL DEFAULT 0,
	duration     INTEGER NOT NULL DEFAULT 0, -- seconds, summed over visits
	count        INTEGER NOT NULL DEFAULT 0, -- what the dimension counts (see Breakdown)
	time_total   INTEGER NOT NULL DEFAULT 0, -- ms visible, pages only
	time_n       INTEGER NOT NULL DEFAULT 0,
	scroll_total INTEGER NOT NULL DEFAULT 0, -- percent, pages only
	scroll_n     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS imported_site_dim_ts ON imported(site_id, dim, ts);
CREATE INDEX IF NOT EXISTS imported_import ON imported(import_id);

-- Keys for the stats API: sha256 of the key, so a leaked database can't be
-- used to read the stats. prefix is the start of the key, to tell keys apart.
CREATE TABLE IF NOT EXISTS api_keys (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	prefix     TEXT NOT NULL,
	key_hash   BLOB NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	last_used  INTEGER
);
`

func Open(path string) (*Store, error) {
	if err := restrictFiles(path); err != nil {
		return nil, err
	}
	pragmas := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	// secure_delete overwrites deleted rows (old salts, expired logins) with
	// zeros instead of leaving them in free pages of the file.
	db, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=secure_delete(1)")
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
	// Opened after the migration has switched the file to WAL.
	rdb, err := sql.Open("sqlite", "file:"+path+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(readers)
	rdb.SetMaxIdleConns(readers)
	s.rdb = rdb
	return s, nil
}

// restrictFiles makes the database readable by its owner only. It holds the
// day's salt, from which today's visitor hashes could be brute-forced back
// to IP addresses, and the login and password hashes. SQLite gives the -wal
// and -shm files the database file's permissions.
func restrictFiles(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	return errors.Join(s.rdb.Close(), s.db.Close())
}

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
	if version >= 1 && version < 4 {
		if err := s.migrateV3(ctx); err != nil {
			return err
		}
	}
	if version >= 1 && version < 5 {
		if err := s.migrateV4(ctx); err != nil {
			return err
		}
	}
	if version >= 1 && version < 6 {
		if err := s.migrateV5(ctx); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Indexes on columns that a migration adds to older databases.
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS sessions_import ON sessions(import_id) WHERE import_id IS NOT NULL`); err != nil {
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

// migrateV5 adds sessions.import_id. A database that an older version opened
// after this one has user_version 5 again but already has the column.
func (s *Store) migrateV5(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'import_id'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN import_id INTEGER`)
	return err
}

// migrateV3 adds sessions.human and sets it for the visits that already
// count.
func (s *Store) migrateV3(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN human INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET human = 1
		WHERE EXISTS (SELECT 1 FROM events e WHERE e.session_id = sessions.id AND e.engaged >= ?)`,
		MinEngaged.Milliseconds()); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateV4 adds the visit's visible time and the vitals' pageview, and
// brings stored visits in line with how new ones are recorded: paths are
// percent-decoded, sources get their names, a visit without a referrer takes
// its UTM source as source, and channels are derived again.
func (s *Store) migrateV4(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`ALTER TABLE sessions ADD COLUMN engaged INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE vitals ADD COLUMN event_id INTEGER`,
		`UPDATE sessions SET engaged = COALESCE((SELECT SUM(e.engaged) FROM events e WHERE e.session_id = sessions.id), 0)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for _, c := range [][2]string{{"events", "path"}, {"sessions", "entry_path"}, {"sessions", "exit_path"},
		{"sessions", "referrer_url"}, {"vitals", "path"}, {"goals", "page_path"}} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT %s FROM %s WHERE instr(%[1]s, '%%') > 0`, c[1], c[0]))
		if err != nil {
			return err
		}
		decoded := map[string]string{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			if d := UnescapePath(p); d != p {
				decoded[p] = d
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for from, to := range decoded {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE OR IGNORE %s SET %s = ? WHERE %[2]s = ?`, c[0], c[1]), to, from); err != nil {
				return err
			}
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT referrer, utm_source, utm_medium FROM sessions`)
	if err != nil {
		return err
	}
	type key struct{ ref, src, med string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ref, &k.src, &k.med); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET channel = ?, referrer = ?
			WHERE referrer = ? AND utm_source = ? AND utm_medium = ?`,
			Channel(k.ref, k.src, k.med), Source(k.ref, k.src), k.ref, k.src, k.med); err != nil {
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
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, domain, created_at FROM sites ORDER BY domain`)
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
	err := s.rdb.QueryRowContext(ctx, `SELECT id, domain, created_at FROM sites WHERE domain = ?`, domain).
		Scan(&st.ID, &st.Domain, &created)
	st.CreatedAt = time.Unix(created, 0)
	return st, err
}

// Errors from the add/delete methods that are safe to show to the admin.
var (
	ErrInvalidDomain = errors.New("invalid domain")
	ErrGoalKind      = errors.New("a goal needs either an event name or a page path")
	ErrEventName     = errors.New("event name is too long")
	ErrGoalPath      = errors.New("page paths start with /")
	ErrCurrency      = errors.New("revenue needs a 3-letter currency code and an event goal")
	ErrFunnelName    = errors.New("a funnel needs a name")
	ErrFunnelSteps   = errors.New("a funnel needs 2 to 8 steps")
)

// hostnameRe accepts DNS hostnames (internationalized ones in their xn--
// form) and IPv4 addresses.
var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

func (s *Store) AddSite(ctx context.Context, domain string) error {
	domain = NormalizeDomain(domain)
	if len(domain) > 253 || !hostnameRe.MatchString(domain) {
		return ErrInvalidDomain
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sites(domain, created_at) VALUES(?, ?)`, domain, time.Now().Unix())
	return err
}

func (s *Store) DeleteSite(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
	return err
}

// FirstVisit returns when the site's oldest counted visit started.
func (s *Store) FirstVisit(ctx context.Context, siteID int64) (time.Time, bool) {
	var ts sql.NullInt64
	s.rdb.QueryRowContext(ctx, `SELECT MIN(t) FROM (
		SELECT MIN(started_at) AS t FROM sessions WHERE site_id = ? AND human = 1
		UNION ALL SELECT MIN(ts) - 12*3600 FROM imported WHERE site_id = ? AND dim = '')`, siteID, siteID).Scan(&ts)
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

// HasCurrentVisit reports whether the visitor has a visit that is still
// going on.
func (s *Store) HasCurrentVisit(ctx context.Context, siteID, visitor int64, now time.Time) (bool, error) {
	_, err := currentSession(ctx, s.rdb, siteID, visitor, now.Unix())
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Leave records that the visitor left (or hid) a page, or is still reading
// it: it stores scroll depth and visible time on that page's pageview and
// adds the time to the visit.
type Leave struct {
	SiteID, Visitor int64
	Time            time.Time
	Path            string
	Scroll          *int64  // percent
	Engaged         *int64  // ms
	Vitals          *Vitals // the page load's vitals so far, if reported
}

// RecordLeave attaches the report to the visitor's latest pageview of the
// path, in whichever of their visits it is: a tab can be read long after
// the visit's 30 minutes of inactivity. Only a visit that is still current
// is extended. A visit starts to count once one of its pages has been
// visible for MinEngaged. It reports whether the pageview's visit counts;
// without such a pageview nothing is stored.
func (s *Store) RecordLeave(ctx context.Context, l Leave) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := l.Time.Unix()
	var eventID, sessionID int64
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, s.id FROM sessions s JOIN events e ON e.session_id = s.id
		WHERE s.site_id = ? AND s.visitor = ? AND e.name = 'pageview' AND e.path = ?
		ORDER BY e.ts DESC, e.id DESC LIMIT 1`, l.SiteID, l.Visitor, l.Path).Scan(&eventID, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var engaged int64
	if l.Engaged != nil {
		engaged = *l.Engaged
	}
	if _, err := tx.ExecContext(ctx, `UPDATE events SET
			scroll = CASE WHEN ?1 IS NULL THEN scroll ELSE MAX(COALESCE(scroll, 0), ?1) END,
			engaged = CASE WHEN ?2 IS NULL THEN engaged ELSE COALESCE(engaged, 0) + ?2 END
		WHERE id = ?3`, l.Scroll, l.Engaged, eventID); err != nil {
		return false, err
	}
	var human bool
	if err := tx.QueryRowContext(ctx, `
		UPDATE sessions SET engaged = engaged + ?,
			last_seen = CASE WHEN last_seen >= ? THEN MAX(last_seen, ?) ELSE last_seen END,
			human = human OR EXISTS (SELECT 1 FROM events WHERE id = ? AND engaged >= ?)
		WHERE id = ? RETURNING human`,
		engaged, now-int64(SessionTimeout.Seconds()), now, eventID, MinEngaged.Milliseconds(), sessionID).Scan(&human); err != nil {
		return false, err
	}
	// Vitals only count for a page load that belongs to a counted visit. A
	// later report of the same load replaces the values it measured again.
	if v := l.Vitals; v != nil && human {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO vitals(site_id, ts, path, device, lcp, cls, inp, fcp, ttfb, event_id)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(event_id) DO UPDATE SET lcp = COALESCE(excluded.lcp, lcp), cls = COALESCE(excluded.cls, cls),
				inp = COALESCE(excluded.inp, inp), fcp = COALESCE(excluded.fcp, fcp), ttfb = COALESCE(excluded.ttfb, ttfb)`,
			l.SiteID, now, l.Path, v.Device, v.LCP, v.CLS, v.INP, v.FCP, v.TTFB, eventID); err != nil {
			return false, err
		}
	}
	return human, tx.Commit()
}

type Vitals struct {
	SiteID                   int64
	Time                     time.Time
	Path                     string
	Device                   string
	LCP, CLS, INP, FCP, TTFB *float64
}

// RecordVitals stores vitals that belong to no recorded pageview.
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
	rows, err := s.rdb.QueryContext(ctx, `
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
		return ErrGoalKind
	case len(g.EventName) > 64:
		return ErrEventName
	case g.PagePath != "" && (!strings.HasPrefix(g.PagePath, "/") || len(g.PagePath) > 512):
		return ErrGoalPath
	case g.Currency != "" && (len(g.Currency) != 3 || g.PagePath != ""):
		return ErrCurrency
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
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, site_id, name, steps FROM funnels WHERE site_id = ? ORDER BY id`, siteID)
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
		return ErrFunnelName
	}
	if len(f.Steps) < 2 || len(f.Steps) > 8 {
		return ErrFunnelSteps
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

// PurgeSalts deletes the salt of every day except keepDay, even when no
// beacon has arrived yet today to trigger the rotation, and checkpoints the
// write-ahead log so the deleted salts don't linger in it.
func (s *Store) PurgeSalts(ctx context.Context, keepDay string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM salts WHERE day <> ?`, keepDay)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		_, err = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return err
}

// Secret returns a persistent random secret stored under key, generating it
// on first use.
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

// ---- admin logins ----

func (s *Store) AddLogin(ctx context.Context, tokenHash []byte, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM logins WHERE expires < ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO logins(token_hash, expires) VALUES(?, ?)`, tokenHash, expires.Unix())
	return err
}

func (s *Store) LoginValid(ctx context.Context, tokenHash []byte) (bool, error) {
	var n int
	err := s.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM logins WHERE token_hash = ? AND expires >= ?`,
		tokenHash, time.Now().Unix()).Scan(&n)
	return n > 0, err
}

func (s *Store) DeleteLogin(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM logins WHERE token_hash = ?`, tokenHash)
	return err
}

// CheckPassword ends every login when the admin password has changed since
// the last start. fingerprint is a slow hash of the password.
func (s *Store) CheckPassword(ctx context.Context, fingerprint []byte) (changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'password_fingerprint'`).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if bytes.Equal(old, fingerprint) {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM logins`); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO settings(key, value) VALUES('password_fingerprint', ?)`, fingerprint); err != nil {
		return false, err
	}
	return old != nil, tx.Commit()
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
	if _, err := s.db.ExecContext(ctx, `DELETE FROM imported WHERE ts < ?`, cut); err != nil {
		return n, err
	}
	return n, nil
}

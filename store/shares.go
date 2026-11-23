// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrShareName is returned for an empty or overlong shared link name.
var ErrShareName = errors.New("a shared link needs a name of at most 100 characters")

// A Share is a secret link to one site's dashboard. The token is kept as
// is, so the link can be copied again later; anyone who has the database
// can read the statistics anyway.
type Share struct {
	ID        int64
	SiteID    int64
	Token     string
	Name      string
	CreatedAt time.Time
}

func (s *Store) AddShare(ctx context.Context, siteID int64, name string) (Share, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Share{}, ErrShareName
	}
	sh := Share{SiteID: siteID, Token: rand.Text(), Name: name, CreatedAt: time.Now()}
	res, err := s.db.ExecContext(ctx, `INSERT INTO shares(site_id, token, name, created_at) VALUES(?, ?, ?, ?)`,
		siteID, sh.Token, name, sh.CreatedAt.Unix())
	if err != nil {
		return Share{}, err
	}
	sh.ID, _ = res.LastInsertId()
	return sh, nil
}

func (s *Store) Shares(ctx context.Context, siteID int64) ([]Share, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, site_id, token, name, created_at FROM shares WHERE site_id = ? ORDER BY created_at, id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Share
	for rows.Next() {
		var sh Share
		var created int64
		if err := rows.Scan(&sh.ID, &sh.SiteID, &sh.Token, &sh.Name, &created); err != nil {
			return nil, err
		}
		sh.CreatedAt = time.Unix(created, 0)
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *Store) DeleteShare(ctx context.Context, siteID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE id = ? AND site_id = ?`, id, siteID)
	return err
}

// ShareSite returns the site a shared link shows.
func (s *Store) ShareSite(ctx context.Context, token string) (Site, bool, error) {
	var st Site
	var created int64
	err := s.rdb.QueryRowContext(ctx, `SELECT s.id, s.domain, s.created_at FROM shares h JOIN sites s ON s.id = h.site_id WHERE h.token = ?`, token).
		Scan(&st.ID, &st.Domain, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, false, nil
	}
	st.CreatedAt = time.Unix(created, 0)
	return st, err == nil, err
}

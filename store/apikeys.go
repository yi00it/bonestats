// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// APIKeyPrefix starts every API key, so a leaked key is easy to recognize
// (for example by secret scanners).
const APIKeyPrefix = "bs_"

// ErrAPIKeyName is returned for an empty or overlong key name.
var ErrAPIKeyName = errors.New("an API key needs a name of at most 100 characters")

type APIKey struct {
	ID        int64
	Name      string
	Prefix    string // the first characters of the key
	CreatedAt time.Time
	LastUsed  *time.Time
}

func apiKeyHash(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}

// AddAPIKey creates a key and returns it. Only its hash is stored, so this
// is the only time the key can be shown.
func (s *Store) AddAPIKey(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", ErrAPIKeyName
	}
	key := APIKeyPrefix + rand.Text()
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(name, prefix, key_hash, created_at) VALUES(?, ?, ?, ?)`,
		name, key[:len(APIKeyPrefix)+6], apiKeyHash(key), time.Now().Unix())
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *Store) APIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT id, name, prefix, created_at, last_used FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &created, &used); err != nil {
			return nil, err
		}
		k.CreatedAt = time.Unix(created, 0)
		if used.Valid {
			t := time.Unix(used.Int64, 0)
			k.LastUsed = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

// UseAPIKey reports whether key is valid, and notes when it was last used
// (to the minute, so busy scripts don't write on every request).
func (s *Store) UseAPIKey(ctx context.Context, key string) (bool, error) {
	if !strings.HasPrefix(key, APIKeyPrefix) {
		return false, nil
	}
	h := apiKeyHash(key)
	var id int64
	var used sql.NullInt64
	err := s.rdb.QueryRowContext(ctx, `SELECT id, last_used FROM api_keys WHERE key_hash = ?`, h).Scan(&id, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if now := time.Now().Unix(); !used.Valid || now-used.Int64 >= 60 {
		if _, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used = ? WHERE id = ?`, now, id); err != nil {
			return false, err
		}
	}
	return true, nil
}

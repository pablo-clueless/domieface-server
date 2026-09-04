package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"domieface/com/internal/store"
)

type uploadStore struct{ pool *pgxpool.Pool }

func (s *uploadStore) Reserve(ctx context.Context, upload store.Upload) error {
	const query = `
		INSERT INTO uploads (key, user_id, purpose, content_type, content_length)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := s.pool.Exec(ctx, query,
		upload.Key, upload.UserID, upload.Purpose, upload.ContentType, upload.ContentLength)
	return mapError(err)
}

// Claim attaches a key to a resource. The WHERE clause is the whole security
// check: the key must belong to this user, have been presigned for this
// purpose, and not already be attached. That last condition stops one upload
// being fanned out across many posts.
func (s *uploadStore) Claim(ctx context.Context, key, userID, purpose string, at time.Time) error {
	const query = `
		WITH target AS (
			SELECT key FROM uploads WHERE key = $1
		), claimed AS (
			UPDATE uploads SET attached_at = $4
			WHERE key = $1 AND user_id = $2 AND purpose = $3 AND attached_at IS NULL
			RETURNING key
		)
		SELECT (SELECT count(*) FROM target), (SELECT count(*) FROM claimed)`

	var found, claimed int
	if err := s.pool.QueryRow(ctx, query, key, userID, purpose, at).Scan(&found, &claimed); err != nil {
		return mapError(err)
	}
	switch {
	case found == 0:
		return store.ErrNotFound
	case claimed == 0:
		return store.ErrForbidden
	default:
		return nil
	}
}

// ReleaseAbandoned hands back keys that were presigned but never attached, and
// forgets the rows. The caller deletes the objects themselves — we drop our
// record either way, because an object we have stopped tracking is exactly what
// the storage lifecycle rule is there to sweep up.
func (s *uploadStore) ReleaseAbandoned(ctx context.Context, before time.Time, limit int) ([]string, error) {
	const query = `
		DELETE FROM uploads
		WHERE key IN (
			SELECT key FROM uploads
			WHERE attached_at IS NULL AND created_at < $1
			ORDER BY created_at
			LIMIT $2
		)
		RETURNING key`

	rows, err := s.pool.Query(ctx, query, before, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, mapError(err)
		}
		keys = append(keys, key)
	}
	return keys, mapError(rows.Err())
}

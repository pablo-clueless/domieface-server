package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"domieface/com/internal/store"
)

type tokenStore struct{ pool *pgxpool.Pool }

func (s *tokenStore) Issue(ctx context.Context, token store.RefreshToken) error {
	const query = `
		INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := s.pool.Exec(ctx, query,
		token.ID, token.UserID, token.FamilyID, token.TokenHash, token.ExpiresAt)
	return mapError(err)
}

func (s *tokenStore) ByHash(ctx context.Context, hash string) (*store.RefreshToken, error) {
	const query = `
		SELECT id, user_id, family_id, token_hash, expires_at, created_at, used_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1`

	var t store.RefreshToken
	err := s.pool.QueryRow(ctx, query, hash).Scan(
		&t.ID, &t.UserID, &t.FamilyID, &t.TokenHash,
		&t.ExpiresAt, &t.CreatedAt, &t.UsedAt, &t.RevokedAt)
	if err != nil {
		return nil, mapError(err)
	}
	return &t, nil
}

// MarkUsed consumes a token. The `used_at IS NULL` guard makes this the point
// where two concurrent refreshes are serialised: only one UPDATE affects a row,
// and the loser is told the token was already spent.
func (s *tokenStore) MarkUsed(ctx context.Context, id string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET used_at = $2 WHERE id = $1 AND used_at IS NULL`, id, at)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RevokeFamily kills every token descended from one login. Called when a
// consumed token is replayed, which means it leaked.
func (s *tokenStore) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, at)
	return mapError(err)
}

func (s *tokenStore) RevokeAllForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`,
		userID, at)
	return mapError(err)
}

// DeleteExpired clears rows that can no longer be exchanged. Expired tokens are
// kept a while past expiry so replay of a recently leaked token is still caught
// by the family check rather than silently missing.
func (s *tokenStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < $1`, before)
	if err != nil {
		return 0, mapError(err)
	}
	return tag.RowsAffected(), nil
}

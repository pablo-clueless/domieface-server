package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"domieface/com/internal/store"
)

type userStore struct{ pool *pgxpool.Pool }

// userColumns is the projection every user read shares, so the scan helper
// stays in step with the queries.
const userColumns = `id, email, username, display_name, bio, avatar_key, created_at, updated_at`

func scanUser(row pgx.Row) (*store.User, error) {
	var u store.User
	err := row.Scan(&u.ID, &u.Email, &u.Username, &u.DisplayName, &u.Bio, &u.AvatarKey, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, mapError(err)
	}
	return &u, nil
}

func (s *userStore) Create(ctx context.Context, in store.NewUser) (*store.User, error) {
	const query = `
		INSERT INTO users (id, email, username, display_name, password_hash)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING ` + userColumns

	return scanUser(s.pool.QueryRow(ctx, query,
		store.NewID(), in.Email, in.Username, in.DisplayName, in.PasswordHash))
}

func (s *userStore) ByID(ctx context.Context, id string) (*store.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (s *userStore) ByUsername(ctx context.Context, username string) (*store.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username))
}

func (s *userStore) CredentialsByEmail(ctx context.Context, email string) (*store.Credentials, error) {
	var c store.Credentials
	err := s.pool.QueryRow(ctx,
		`SELECT id, password_hash FROM users WHERE email = $1`, email,
	).Scan(&c.UserID, &c.PasswordHash)
	if err != nil {
		return nil, mapError(err)
	}
	return &c, nil
}

func (s *userStore) Update(ctx context.Context, id string, in store.UserUpdate) (*store.User, error) {
	// Build the SET list from whichever fields were actually supplied. An empty
	// PATCH body is legal and simply reads the row back unchanged.
	var (
		assignments []string
		args        []any
	)
	add := func(column string, value any) {
		args = append(args, value)
		assignments = append(assignments, column+" = $"+strconv.Itoa(len(args)))
	}

	if in.DisplayName != nil {
		add("display_name", *in.DisplayName)
	}
	if in.Bio != nil {
		add("bio", *in.Bio)
	}
	switch {
	case in.ClearAvatar:
		// The contract's `"avatarKey": null` means remove the avatar.
		assignments = append(assignments, "avatar_key = NULL")
	case in.AvatarKey != nil:
		add("avatar_key", *in.AvatarKey)
	}

	if len(assignments) == 0 {
		return s.ByID(ctx, id)
	}
	assignments = append(assignments, "updated_at = now()")

	args = append(args, id)
	query := `UPDATE users SET ` + strings.Join(assignments, ", ") +
		` WHERE id = $` + strconv.Itoa(len(args)) + ` RETURNING ` + userColumns

	return scanUser(s.pool.QueryRow(ctx, query, args...))
}

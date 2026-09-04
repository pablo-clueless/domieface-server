// Package postgres is the production implementation of the store interfaces.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"domieface/com/internal/store"
	"domieface/com/migrations"
)

// Store is a pgx-backed store.Store.
type Store struct {
	pool *pgxpool.Pool

	users   *userStore
	posts   *postStore
	tokens  *tokenStore
	uploads *uploadStore
}

// Connect opens a pool and verifies it can reach the database.
func Connect(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	s := &Store{pool: pool}
	s.users = &userStore{pool: pool}
	s.posts = &postStore{pool: pool}
	s.tokens = &tokenStore{pool: pool}
	s.uploads = &uploadStore{pool: pool}
	return s, nil
}

// Users implements store.Store.
func (s *Store) Users() store.UserStore { return s.users }

// Posts implements store.Store.
func (s *Store) Posts() store.PostStore { return s.posts }

// Tokens implements store.Store.
func (s *Store) Tokens() store.TokenStore { return s.tokens }

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return s.uploads }

// Ping implements store.Store.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close implements store.Store.
func (s *Store) Close() { s.pool.Close() }

// Migrate applies every embedded migration that has not run yet, in filename
// order. Each runs inside a transaction alongside its bookkeeping row, so a
// failure part-way through leaves nothing half-applied.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT        PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("reading migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("checking migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("applying migration %s: %w", name, err)
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name)
			return err
		}); err != nil {
			return err
		}
		slog.InfoContext(ctx, "migration applied", "version", name)
	}
	return nil
}

// mapError translates driver errors into the sentinels handlers understand, so
// no pgx type escapes this package.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		switch pgErr.ConstraintName {
		case "users_email_key":
			return &store.ConflictError{Field: "email", Reason: "already in use"}
		case "users_username_key":
			return &store.ConflictError{Field: "username", Reason: "already taken"}
		}
		return &store.ConflictError{Field: "", Reason: "already exists"}
	}
	return err
}

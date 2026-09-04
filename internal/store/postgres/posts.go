package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"domieface/com/internal/store"
)

type postStore struct{ pool *pgxpool.Pool }

// postColumns joins the author in so a feed page costs one query, not N+1.
const postColumns = `
	p.id, p.author_id, p.image_key, p.caption, p.created_at,
	u.id, u.email, u.username, u.display_name, u.bio, u.avatar_key, u.created_at, u.updated_at`

func scanPost(row pgx.Row) (*store.Post, error) {
	var (
		p      store.Post
		author store.User
	)
	err := row.Scan(
		&p.ID, &p.AuthorID, &p.ImageKey, &p.Caption, &p.CreatedAt,
		&author.ID, &author.Email, &author.Username, &author.DisplayName,
		&author.Bio, &author.AvatarKey, &author.CreatedAt, &author.UpdatedAt,
	)
	if err != nil {
		return nil, mapError(err)
	}
	p.Author = &author
	return &p, nil
}

func (s *postStore) Create(ctx context.Context, in store.NewPost) (*store.Post, error) {
	const query = `
		WITH inserted AS (
			INSERT INTO posts (id, author_id, image_key, caption)
			VALUES ($1, $2, $3, $4)
			RETURNING id, author_id, image_key, caption, created_at
		)
		SELECT ` + postColumns + `
		FROM inserted p JOIN users u ON u.id = p.author_id`

	return scanPost(s.pool.QueryRow(ctx, query, store.NewID(), in.AuthorID, in.ImageKey, in.Caption))
}

func (s *postStore) ByID(ctx context.Context, id string) (*store.Post, error) {
	const query = `SELECT ` + postColumns + `
		FROM posts p JOIN users u ON u.id = p.author_id
		WHERE p.id = $1`
	return scanPost(s.pool.QueryRow(ctx, query, id))
}

func (s *postStore) Delete(ctx context.Context, id, requesterID string) error {
	// Read the author in the same statement as the delete so a post that exists
	// but belongs to someone else is a 403, and one that never existed is a 404
	// — without a separate lookup that could race the delete.
	const query = `
		WITH target AS (
			SELECT id, author_id FROM posts WHERE id = $1
		), removed AS (
			DELETE FROM posts
			WHERE id = (SELECT id FROM target WHERE author_id = $2)
			RETURNING id
		)
		SELECT (SELECT count(*) FROM target), (SELECT count(*) FROM removed)`

	var found, removed int
	if err := s.pool.QueryRow(ctx, query, id, requesterID).Scan(&found, &removed); err != nil {
		return mapError(err)
	}
	switch {
	case found == 0:
		return store.ErrNotFound
	case removed == 0:
		return store.ErrForbidden
	default:
		return nil
	}
}

func (s *postStore) Feed(ctx context.Context, req store.PageRequest) (*store.Page[store.Post], error) {
	const query = `
		SELECT ` + postColumns + `
		FROM posts p JOIN users u ON u.id = p.author_id
		WHERE $1::timestamptz IS NULL OR (p.created_at, p.id) < ($1::timestamptz, $2::text)
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $3`
	return s.page(ctx, req, func(after *time.Time, afterID string, limit int) (pgx.Rows, error) {
		return s.pool.Query(ctx, query, after, afterID, limit)
	})
}

func (s *postStore) ByAuthor(ctx context.Context, authorID string, req store.PageRequest) (*store.Page[store.Post], error) {
	const query = `
		SELECT ` + postColumns + `
		FROM posts p JOIN users u ON u.id = p.author_id
		WHERE p.author_id = $4
		  AND ($1::timestamptz IS NULL OR (p.created_at, p.id) < ($1::timestamptz, $2::text))
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $3`
	return s.page(ctx, req, func(after *time.Time, afterID string, limit int) (pgx.Rows, error) {
		return s.pool.Query(ctx, query, after, afterID, limit, authorID)
	})
}

// page runs a keyset query and works out the next cursor. It asks for one row
// more than the caller wants: if that extra row comes back there is another
// page, and if it does not, nextCursor is empty and the client stops.
func (s *postStore) page(
	ctx context.Context,
	req store.PageRequest,
	run func(after *time.Time, afterID string, limit int) (pgx.Rows, error),
) (*store.Page[store.Post], error) {
	req = req.Normalise()

	var (
		after   *time.Time
		afterID string
	)
	if req.Cursor != "" {
		createdAt, id, err := store.DecodeCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		after, afterID = &createdAt, id
	}

	rows, err := run(after, afterID, req.Limit+1)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()

	items := make([]store.Post, 0, req.Limit)
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *post)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}

	page := &store.Page[store.Post]{Items: items}
	if len(items) > req.Limit {
		page.Items = items[:req.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = store.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

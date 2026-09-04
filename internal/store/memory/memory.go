// Package memory is an in-memory store.Store used as a test double.
//
// It exists so handler tests can exercise the full HTTP surface without a
// database. It is NOT a production store: everything is lost on restart and
// nothing is transactional. Postgres is the real implementation.
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"domieface/com/internal/store"
)

// Store is an in-memory store.Store.
type Store struct {
	mu sync.RWMutex

	users     map[string]*store.User
	passwords map[string]string // user ID -> password hash
	posts     map[string]*store.Post
	tokens    map[string]*store.RefreshToken
	uploads   map[string]*store.Upload
}

// New builds an empty store.
func New() *Store {
	return &Store{
		users:     make(map[string]*store.User),
		passwords: make(map[string]string),
		posts:     make(map[string]*store.Post),
		tokens:    make(map[string]*store.RefreshToken),
		uploads:   make(map[string]*store.Upload),
	}
}

// Users implements store.Store.
func (s *Store) Users() store.UserStore { return (*userStore)(s) }

// Posts implements store.Store.
func (s *Store) Posts() store.PostStore { return (*postStore)(s) }

// Tokens implements store.Store.
func (s *Store) Tokens() store.TokenStore { return (*tokenStore)(s) }

// Uploads implements store.Store.
func (s *Store) Uploads() store.UploadStore { return (*uploadStore)(s) }

// Ping implements store.Store.
func (s *Store) Ping(context.Context) error { return nil }

// Close implements store.Store.
func (s *Store) Close() {}

type userStore Store

func (s *userStore) Create(_ context.Context, in store.NewUser) (*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if u.Email == in.Email {
			return nil, &store.ConflictError{Field: "email", Reason: "already in use"}
		}
		if u.Username == in.Username {
			return nil, &store.ConflictError{Field: "username", Reason: "already taken"}
		}
	}

	now := time.Now().UTC()
	user := &store.User{
		ID:          store.NewID(),
		Email:       in.Email,
		Username:    in.Username,
		DisplayName: in.DisplayName,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.users[user.ID] = user
	s.passwords[user.ID] = in.PasswordHash

	return copyUser(user), nil
}

func (s *userStore) ByID(_ context.Context, id string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, ok := s.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return copyUser(user), nil
}

func (s *userStore) ByUsername(_ context.Context, username string) (*store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, u := range s.users {
		if u.Username == username {
			return copyUser(u), nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *userStore) CredentialsByEmail(_ context.Context, email string) (*store.Credentials, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, u := range s.users {
		if u.Email == strings.ToLower(email) {
			return &store.Credentials{UserID: u.ID, PasswordHash: s.passwords[u.ID]}, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *userStore) Update(_ context.Context, id string, in store.UserUpdate) (*store.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, ok := s.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}

	if in.DisplayName != nil {
		user.DisplayName = *in.DisplayName
	}
	if in.Bio != nil {
		user.Bio = *in.Bio
	}
	switch {
	case in.ClearAvatar:
		user.AvatarKey = nil
	case in.AvatarKey != nil:
		key := *in.AvatarKey
		user.AvatarKey = &key
	}
	user.UpdatedAt = time.Now().UTC()

	return copyUser(user), nil
}

type postStore Store

func (s *postStore) Create(_ context.Context, in store.NewPost) (*store.Post, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	author, ok := s.users[in.AuthorID]
	if !ok {
		return nil, store.ErrNotFound
	}

	post := &store.Post{
		ID:        store.NewID(),
		AuthorID:  in.AuthorID,
		ImageKey:  in.ImageKey,
		Caption:   in.Caption,
		CreatedAt: time.Now().UTC(),
	}
	s.posts[post.ID] = post

	return hydrate(post, author), nil
}

func (s *postStore) ByID(_ context.Context, id string) (*store.Post, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	post, ok := s.posts[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return hydrate(post, s.users[post.AuthorID]), nil
}

func (s *postStore) Delete(_ context.Context, id, requesterID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	post, ok := s.posts[id]
	if !ok {
		return store.ErrNotFound
	}
	if post.AuthorID != requesterID {
		return store.ErrForbidden
	}
	delete(s.posts, id)
	return nil
}

func (s *postStore) Feed(_ context.Context, req store.PageRequest) (*store.Page[store.Post], error) {
	return s.page(req, func(*store.Post) bool { return true })
}

func (s *postStore) ByAuthor(_ context.Context, authorID string, req store.PageRequest) (*store.Page[store.Post], error) {
	return s.page(req, func(p *store.Post) bool { return p.AuthorID == authorID })
}

// page mirrors the Postgres keyset query: order by (createdAt, id) descending,
// take everything strictly after the cursor, and fetch one extra row to decide
// whether there is a next page.
func (s *postStore) page(req store.PageRequest, keep func(*store.Post) bool) (*store.Page[store.Post], error) {
	req = req.Normalise()

	var (
		afterAt   time.Time
		afterID   string
		hasCursor bool
	)
	if req.Cursor != "" {
		createdAt, id, err := store.DecodeCursor(req.Cursor)
		if err != nil {
			return nil, err
		}
		afterAt, afterID, hasCursor = createdAt, id, true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := make([]store.Post, 0, len(s.posts))
	for _, p := range s.posts {
		if keep(p) {
			matched = append(matched, *hydrate(p, s.users[p.AuthorID]))
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})

	if hasCursor {
		filtered := matched[:0]
		for _, p := range matched {
			if p.CreatedAt.Before(afterAt) || (p.CreatedAt.Equal(afterAt) && p.ID < afterID) {
				filtered = append(filtered, p)
			}
		}
		matched = filtered
	}

	page := &store.Page[store.Post]{Items: matched}
	if len(matched) > req.Limit {
		page.Items = matched[:req.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = store.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func hydrate(p *store.Post, author *store.User) *store.Post {
	clone := *p
	clone.Author = copyUser(author)
	return &clone
}

type tokenStore Store

func (s *tokenStore) Issue(_ context.Context, token store.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clone := token
	s.tokens[token.ID] = &clone
	return nil
}

func (s *tokenStore) ByHash(_ context.Context, hash string) (*store.RefreshToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.tokens {
		if t.TokenHash == hash {
			clone := *t
			return &clone, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *tokenStore) MarkUsed(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, ok := s.tokens[id]
	if !ok || token.UsedAt != nil {
		// Matches the Postgres "used_at IS NULL" guard: consuming an already
		// consumed token affects no rows.
		return store.ErrNotFound
	}
	usedAt := at
	token.UsedAt = &usedAt
	return nil
}

func (s *tokenStore) RevokeFamily(_ context.Context, familyID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range s.tokens {
		if t.FamilyID == familyID && t.RevokedAt == nil {
			revokedAt := at
			t.RevokedAt = &revokedAt
		}
	}
	return nil
}

func (s *tokenStore) RevokeAllForUser(_ context.Context, userID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range s.tokens {
		if t.UserID == userID && t.RevokedAt == nil {
			revokedAt := at
			t.RevokedAt = &revokedAt
		}
	}
	return nil
}

func (s *tokenStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int64
	for id, t := range s.tokens {
		if t.ExpiresAt.Before(before) {
			delete(s.tokens, id)
			deleted++
		}
	}
	return deleted, nil
}

type uploadStore Store

func (s *uploadStore) Reserve(_ context.Context, upload store.Upload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	clone := upload
	if clone.CreatedAt.IsZero() {
		clone.CreatedAt = time.Now().UTC()
	}
	s.uploads[upload.Key] = &clone
	return nil
}

func (s *uploadStore) Claim(_ context.Context, key, userID, purpose string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	upload, ok := s.uploads[key]
	if !ok {
		return store.ErrNotFound
	}
	if upload.UserID != userID || upload.Purpose != purpose || upload.AttachedAt != nil {
		return store.ErrForbidden
	}
	attachedAt := at
	upload.AttachedAt = &attachedAt
	return nil
}

func (s *uploadStore) ReleaseAbandoned(_ context.Context, before time.Time, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var keys []string
	for key, u := range s.uploads {
		if len(keys) >= limit {
			break
		}
		if u.AttachedAt == nil && u.CreatedAt.Before(before) {
			keys = append(keys, key)
			delete(s.uploads, key)
		}
	}
	return keys, nil
}

func copyUser(u *store.User) *store.User {
	if u == nil {
		return nil
	}
	clone := *u
	if u.AvatarKey != nil {
		key := *u.AvatarKey
		clone.AvatarKey = &key
	}
	return &clone
}

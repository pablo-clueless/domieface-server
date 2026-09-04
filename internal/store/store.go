// Package store defines the domain models and the persistence interfaces the
// API is written against. The postgres subpackage is the production
// implementation; handlers never see SQL.
package store

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors handlers map onto contract error codes.
var (
	// ErrNotFound means the row does not exist. Handlers turn this into 404.
	ErrNotFound = errors.New("store: not found")
	// ErrForbidden means the row exists but does not belong to the caller.
	ErrForbidden = errors.New("store: forbidden")
)

// ConflictError reports a unique-constraint violation, naming the field so the
// handler can put it in the error envelope's details object.
type ConflictError struct {
	Field  string
	Reason string
}

func (e *ConflictError) Error() string { return "store: conflict on " + e.Field + ": " + e.Reason }

// User is the full private record. Only the owner ever sees Email.
type User struct {
	ID          string
	Email       string
	Username    string
	DisplayName string
	Bio         string
	AvatarKey   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewUser is the input to registration. Password is already hashed.
type NewUser struct {
	Email        string
	Username     string
	DisplayName  string
	PasswordHash string
}

// UserUpdate is a partial profile update. A nil pointer leaves the column
// alone; ClearAvatar is how the contract's `"avatarKey": null` is expressed.
// Username is absent deliberately — the contract makes it immutable.
type UserUpdate struct {
	DisplayName *string
	Bio         *string
	AvatarKey   *string
	ClearAvatar bool
}

// Credentials carries the stored password hash for a login attempt.
type Credentials struct {
	UserID       string
	PasswordHash string
}

// Post is one image with an optional caption. Author is always populated on
// read so the feed needs no second query.
type Post struct {
	ID        string
	AuthorID  string
	Author    *User
	ImageKey  string
	Caption   string
	CreatedAt time.Time
}

// NewPost is the input to post creation.
type NewPost struct {
	AuthorID string
	ImageKey string
	Caption  string
}

// RefreshToken is one issued refresh credential. Tokens rotate: using one
// marks it used and issues a successor in the same family, so replaying a
// consumed token is detectable and revokes the whole family.
type RefreshToken struct {
	ID        string
	UserID    string
	FamilyID  string
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
}

// Active reports whether the token may still be exchanged.
func (t *RefreshToken) Active(now time.Time) bool {
	return t.UsedAt == nil && t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// Upload records a presigned key so the janitor can reclaim it if the client
// never attaches it to a resource. The contract promises a 24 hour grace.
type Upload struct {
	Key           string
	UserID        string
	Purpose       string
	ContentType   string
	ContentLength int64
	CreatedAt     time.Time
	AttachedAt    *time.Time
}

// Page is one slice of a cursor-paginated list. NextCursor is empty on the
// last page, which the handler renders as JSON null.
type Page[T any] struct {
	Items      []T
	NextCursor string
}

// PageRequest is a keyset pagination request. Cursor is opaque to the client
// and decoded here.
type PageRequest struct {
	Cursor string
	Limit  int
}

// Store is the aggregate persistence interface the server depends on.
type Store interface {
	Users() UserStore
	Posts() PostStore
	Tokens() TokenStore
	Uploads() UploadStore
	Ping(ctx context.Context) error
	Close()
}

// UserStore persists accounts.
type UserStore interface {
	Create(ctx context.Context, in NewUser) (*User, error)
	ByID(ctx context.Context, id string) (*User, error)
	ByUsername(ctx context.Context, username string) (*User, error)
	// CredentialsByEmail returns the stored hash for a login attempt. It
	// returns ErrNotFound for an unknown email; the caller must still compare
	// against a dummy hash so timing does not disclose which emails exist.
	CredentialsByEmail(ctx context.Context, email string) (*Credentials, error)
	Update(ctx context.Context, id string, in UserUpdate) (*User, error)
}

// PostStore persists posts and reads them back in feed order.
type PostStore interface {
	Create(ctx context.Context, in NewPost) (*Post, error)
	ByID(ctx context.Context, id string) (*Post, error)
	// Delete removes a post, returning ErrForbidden when requesterID is not
	// the author and ErrNotFound when it does not exist.
	Delete(ctx context.Context, id, requesterID string) error
	Feed(ctx context.Context, req PageRequest) (*Page[Post], error)
	ByAuthor(ctx context.Context, authorID string, req PageRequest) (*Page[Post], error)
}

// TokenStore persists refresh tokens.
type TokenStore interface {
	Issue(ctx context.Context, token RefreshToken) error
	ByHash(ctx context.Context, hash string) (*RefreshToken, error)
	MarkUsed(ctx context.Context, id string, at time.Time) error
	RevokeFamily(ctx context.Context, familyID string, at time.Time) error
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// UploadStore tracks presigned keys through to attachment or collection.
type UploadStore interface {
	Reserve(ctx context.Context, upload Upload) error
	// Claim marks a key attached to a resource. It returns ErrNotFound for an
	// unknown key and ErrForbidden when the key belongs to another user, was
	// presigned for a different purpose, or has already been attached — which
	// is what stops one upload being reused across many posts.
	Claim(ctx context.Context, key, userID, purpose string, at time.Time) error
	// ReleaseAbandoned returns keys presigned before the cutoff that were never
	// attached, and forgets them. Deleting the objects themselves is the
	// caller's job.
	ReleaseAbandoned(ctx context.Context, before time.Time, limit int) ([]string, error)
}

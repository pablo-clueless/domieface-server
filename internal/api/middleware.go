package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"domieface/com/internal/auth"
	"domieface/com/internal/httpx"
	"domieface/com/internal/store"
)

type ctxKey int

const currentUserKey ctxKey = iota

// protected wraps a handler so it only runs for an authenticated caller, and
// makes the caller available through currentUser.
func (s *Server) protected(h httpx.Handler) http.Handler {
	return httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		user, err := s.authenticate(r)
		if err != nil {
			return err
		}
		return h(w, r.WithContext(context.WithValue(r.Context(), currentUserKey, user)))
	})
}

// currentUser returns the authenticated caller. It is only ever called from a
// handler registered through protected, so the value is always present.
func currentUser(ctx context.Context) *store.User {
	user, _ := ctx.Value(currentUserKey).(*store.User)
	return user
}

// authenticate verifies the bearer token and loads the user behind it.
//
// The two 401 codes are not interchangeable. TOKEN_EXPIRED tells the client to
// refresh and retry once; TOKEN_INVALID tells it to log the user out. Returning
// the wrong one either strands a user with a recoverable session or spins the
// client through a refresh that can never succeed.
func (s *Server) authenticate(r *http.Request) (*store.User, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, httpx.ErrTokenInvalid()
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return nil, httpx.ErrTokenInvalid()
	}

	userID, err := s.issuer.ParseAccess(strings.TrimSpace(token))
	if err != nil {
		if errors.Is(err, auth.ErrTokenExpired) {
			return nil, httpx.ErrTokenExpired()
		}
		return nil, httpx.ErrTokenInvalid()
	}

	user, err := s.store.Users().ByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A well-formed token for an account that no longer exists. The
			// token cannot be repaired by refreshing, so this is INVALID.
			return nil, httpx.ErrTokenInvalid()
		}
		return nil, httpx.ErrInternal(err)
	}
	return user, nil
}

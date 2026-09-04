package api

import (
	"errors"
	"net/http"
	"time"

	"domieface/com/internal/httpx"
	"domieface/com/internal/store"
	"domieface/com/internal/uploads"
	"domieface/com/internal/validate"
)

// updateMeRequest is a partial update. Optional distinguishes an absent field
// from an explicit null, which the contract relies on: `"avatarKey": null`
// removes the avatar, while omitting the key leaves it alone.
//
// There is no username field. The contract makes username immutable after
// registration, so it is not settable rather than silently ignored.
type updateMeRequest struct {
	DisplayName httpx.Optional[string] `json:"displayName"`
	Bio         httpx.Optional[string] `json:"bio"`
	AvatarKey   httpx.Optional[string] `json:"avatarKey"`
}

// handleGetMe implements GET /v1/users/me. The client calls this on launch when
// a refresh token exists; it is the "is my session still good" check.
func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, s.toUser(currentUser(r.Context())))
	return nil
}

// handleUpdateMe implements PATCH /v1/users/me.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) error {
	me := currentUser(r.Context())

	var req updateMeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	var (
		errs   validate.Errors
		update store.UserUpdate
	)

	if req.DisplayName.Present {
		if req.DisplayName.Null {
			errs.Add("displayName", "is required")
		} else {
			name := validate.DisplayName(&errs, "displayName", req.DisplayName.Value)
			update.DisplayName = &name
		}
	}

	if req.Bio.Present {
		// An explicit null clears the bio, which is optional.
		bio := ""
		if !req.Bio.Null {
			bio = validate.Bio(&errs, "bio", req.Bio.Value)
		}
		update.Bio = &bio
	}

	if req.AvatarKey.Present {
		if req.AvatarKey.Null {
			update.ClearAvatar = true
		} else if req.AvatarKey.Value == "" {
			errs.Add("avatarKey", "must not be empty; send null to remove the avatar")
		} else {
			key := req.AvatarKey.Value
			update.AvatarKey = &key
		}
	}

	if errs.Any() {
		return httpx.ErrValidation("Please check the highlighted fields.", errs)
	}

	// Claim the key before writing it, so a caller cannot attach an upload
	// somebody else presigned, one presigned for a post, or one already in use.
	if update.AvatarKey != nil {
		if err := s.claimUpload(r, *update.AvatarKey, uploads.PurposeAvatar); err != nil {
			return err
		}
	}

	user, err := s.store.Users().Update(r.Context(), me.ID, update)
	if err != nil {
		return conflictOrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toUser(user))
	return nil
}

// handleGetUser implements GET /v1/users/{username}.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) error {
	user, err := s.userByUsername(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s.toPublicUser(user))
	return nil
}

// handleUserPosts implements GET /v1/users/{username}/posts.
func (s *Server) handleUserPosts(w http.ResponseWriter, r *http.Request) error {
	user, err := s.userByUsername(r)
	if err != nil {
		return err
	}

	req, err := pageRequest(r)
	if err != nil {
		return err
	}

	page, err := s.store.Posts().ByAuthor(r.Context(), user.ID, req)
	if err != nil {
		return pageError(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toPostPage(page))
	return nil
}

// userByUsername resolves the {username} path segment, validating its shape
// first so an obviously impossible handle is a 404 rather than a wasted query.
func (s *Server) userByUsername(r *http.Request) (*store.User, error) {
	var errs validate.Errors
	username := validate.Username(&errs, "username", r.PathValue("username"))
	if errs.Any() {
		return nil, httpx.ErrNotFound("We could not find that account.")
	}

	user, err := s.store.Users().ByUsername(r.Context(), username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, httpx.ErrNotFound("We could not find that account.")
		}
		return nil, httpx.ErrInternal(err)
	}
	return user, nil
}

// claimUpload attaches a presigned key to the caller, translating the store's
// ownership verdict into contract error codes.
func (s *Server) claimUpload(r *http.Request, key string, purpose uploads.Purpose) error {
	err := s.store.Uploads().Claim(r.Context(), key, currentUser(r.Context()).ID, string(purpose), time.Now())
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return httpx.ErrValidation("That upload could not be found. It may have expired.",
			validate.Errors{keyFieldFor(purpose): "is not a valid upload key"})
	case errors.Is(err, store.ErrForbidden):
		// Belongs to someone else, was presigned for a different purpose, or is
		// already attached. All three are the caller's problem, and saying
		// which would leak whether a given key exists.
		return httpx.ErrForbidden("That upload cannot be used here.")
	default:
		return httpx.ErrInternal(err)
	}
}

func keyFieldFor(purpose uploads.Purpose) string {
	if purpose == uploads.PurposeAvatar {
		return "avatarKey"
	}
	return "imageKey"
}

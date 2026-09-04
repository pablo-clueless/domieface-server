package api

import (
	"errors"
	"net/http"
	"strconv"

	"domieface/com/internal/httpx"
	"domieface/com/internal/store"
	"domieface/com/internal/uploads"
	"domieface/com/internal/validate"
)

type createPostRequest struct {
	ImageKey string `json:"imageKey"`
	Caption  string `json:"caption"`
}

// handleFeed implements GET /v1/posts — the global chronological feed.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) error {
	req, err := pageRequest(r)
	if err != nil {
		return err
	}

	page, err := s.store.Posts().Feed(r.Context(), req)
	if err != nil {
		return pageError(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toPostPage(page))
	return nil
}

// handleCreatePost implements POST /v1/posts. Every post has exactly one image,
// so imageKey is required.
func (s *Server) handleCreatePost(w http.ResponseWriter, r *http.Request) error {
	me := currentUser(r.Context())

	var req createPostRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	var errs validate.Errors
	if req.ImageKey == "" {
		errs.Add("imageKey", "is required")
	}
	caption := validate.Caption(&errs, "caption", req.Caption)

	if errs.Any() {
		return httpx.ErrValidation("Please check the highlighted fields.", errs)
	}

	// Claim before insert: if the key is not the caller's, or was presigned as
	// an avatar, or has already been used, no post is created.
	if err := s.claimUpload(r, req.ImageKey, uploads.PurposePost); err != nil {
		return err
	}

	post, err := s.store.Posts().Create(r.Context(), store.NewPost{
		AuthorID: me.ID,
		ImageKey: req.ImageKey,
		Caption:  caption,
	})
	if err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusCreated, s.toPost(post))
	return nil
}

// handleGetPost implements GET /v1/posts/{id}.
func (s *Server) handleGetPost(w http.ResponseWriter, r *http.Request) error {
	post, err := s.store.Posts().ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return httpx.ErrNotFound("We could not find that post.")
		}
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toPost(post))
	return nil
}

// handleDeletePost implements DELETE /v1/posts/{id}. Deleting someone else's
// post is 403, not 404: the contract names that case explicitly.
func (s *Server) handleDeletePost(w http.ResponseWriter, r *http.Request) error {
	me := currentUser(r.Context())

	err := s.store.Posts().Delete(r.Context(), r.PathValue("id"), me.ID)
	switch {
	case err == nil:
		httpx.NoContent(w)
		return nil
	case errors.Is(err, store.ErrNotFound):
		return httpx.ErrNotFound("We could not find that post.")
	case errors.Is(err, store.ErrForbidden):
		return httpx.ErrForbidden("You can only delete your own posts.")
	default:
		return httpx.ErrInternal(err)
	}
}

// pageRequest reads the cursor and limit query parameters.
func pageRequest(r *http.Request) (store.PageRequest, error) {
	req := store.PageRequest{Cursor: r.URL.Query().Get("cursor")}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return req, httpx.ErrValidation("That limit is not valid.", validate.Errors{
				"limit": "must be a whole number between 1 and " + strconv.Itoa(store.MaxPageLimit),
			})
		}
		// Over-large limits are clamped rather than rejected, so a client
		// asking for more than we serve still gets a page.
		req.Limit = limit
	}
	return req.Normalise(), nil
}

// pageError turns a bad cursor into a validation failure. The contract tells
// clients to pass nextCursor back verbatim, so a cursor we did not issue is a
// client bug, not a server fault.
func pageError(err error) error {
	if errors.Is(err, store.ErrInvalidCursor) {
		return httpx.ErrValidation("That page cursor is not valid.", validate.Errors{
			"cursor": "must be a cursor returned by a previous request",
		})
	}
	return httpx.ErrInternal(err)
}

// Package api wires the HTTP surface described in HANDOFF.md onto the store.
package api

import (
	"domieface/com/internal/jsontime"
	"domieface/com/internal/store"
)

// userResponse is the full private shape returned for the authenticated user.
// It is the only response that carries an email address.
type userResponse struct {
	ID          string        `json:"id"`
	Email       string        `json:"email"`
	Username    string        `json:"username"`
	DisplayName string        `json:"displayName"`
	Bio         string        `json:"bio"`
	AvatarURL   *string       `json:"avatarUrl"`
	CreatedAt   jsontime.Time `json:"createdAt"`
}

// publicUserResponse is every other user, and every post author. Dropping
// `email` is enforced by the type rather than by remembering to clear a field.
type publicUserResponse struct {
	ID          string        `json:"id"`
	Username    string        `json:"username"`
	DisplayName string        `json:"displayName"`
	Bio         string        `json:"bio"`
	AvatarURL   *string       `json:"avatarUrl"`
	CreatedAt   jsontime.Time `json:"createdAt"`
}

// postResponse is one post. The client gets a URL, not a storage key: the key
// is an internal handle, and swapping the storage host later should not be a
// client change.
type postResponse struct {
	ID        string             `json:"id"`
	Author    publicUserResponse `json:"author"`
	ImageURL  string             `json:"imageUrl"`
	Caption   string             `json:"caption"`
	CreatedAt jsontime.Time      `json:"createdAt"`
}

// paginatedResponse is the only envelope in the API. NextCursor is a pointer so
// the last page serialises as `"nextCursor": null` rather than an empty string.
type paginatedResponse[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
}

// authResponse is returned by register, login and refresh.
//
// The contract names this shape but never spells out its fields. It carries the
// user so the client does not need a follow-up GET /users/me on every login,
// and the access token's expiry so the client can refresh before a request
// fails rather than after.
type authResponse struct {
	AccessToken          string        `json:"accessToken"`
	RefreshToken         string        `json:"refreshToken"`
	AccessTokenExpiresAt jsontime.Time `json:"accessTokenExpiresAt"`
	User                 userResponse  `json:"user"`
}

// presignResponse is the reply to POST /uploads/presign.
type presignResponse struct {
	UploadURL string        `json:"uploadUrl"`
	Key       string        `json:"key"`
	ExpiresAt jsontime.Time `json:"expiresAt"`
}

// avatarURL resolves a stored key to a public URL, preserving "no avatar" as
// JSON null rather than an empty string.
func (s *Server) avatarURL(key *string) *string {
	if key == nil || *key == "" {
		return nil
	}
	url := s.presigner.PublicURL(*key)
	return &url
}

func (s *Server) toUser(u *store.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Email:       u.Email,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Bio:         u.Bio,
		AvatarURL:   s.avatarURL(u.AvatarKey),
		CreatedAt:   jsontime.New(u.CreatedAt),
	}
}

func (s *Server) toPublicUser(u *store.User) publicUserResponse {
	return publicUserResponse{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Bio:         u.Bio,
		AvatarURL:   s.avatarURL(u.AvatarKey),
		CreatedAt:   jsontime.New(u.CreatedAt),
	}
}

func (s *Server) toPost(p *store.Post) postResponse {
	return postResponse{
		ID:        p.ID,
		Author:    s.toPublicUser(p.Author),
		ImageURL:  s.presigner.PublicURL(p.ImageKey),
		Caption:   p.Caption,
		CreatedAt: jsontime.New(p.CreatedAt),
	}
}

func (s *Server) toPostPage(page *store.Page[store.Post]) paginatedResponse[postResponse] {
	// Always a non-nil slice: an empty feed must serialise as [] and not null,
	// or the client's list rendering has to special-case it.
	items := make([]postResponse, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, s.toPost(&page.Items[i]))
	}

	var nextCursor *string
	if page.NextCursor != "" {
		nextCursor = &page.NextCursor
	}
	return paginatedResponse[postResponse]{Items: items, NextCursor: nextCursor}
}

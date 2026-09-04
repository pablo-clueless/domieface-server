package api

import (
	"errors"
	"net/http"

	"domieface/com/internal/auth"
	"domieface/com/internal/httpx"
	"domieface/com/internal/jsontime"
	"domieface/com/internal/store"
	"domieface/com/internal/validate"
)

type registerRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// handleRegister implements POST /v1/auth/register.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) error {
	var req registerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	var errs validate.Errors
	email := validate.Email(&errs, "email", req.Email)
	username := validate.Username(&errs, "username", req.Username)
	displayName := validate.DisplayName(&errs, "displayName", req.DisplayName)
	validate.Password(&errs, "password", req.Password)

	if errs.Any() {
		return httpx.ErrValidation("Please check the highlighted fields.", errs)
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	user, err := s.store.Users().Create(r.Context(), store.NewUser{
		Email:        email,
		Username:     username,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
	})
	if err != nil {
		return conflictOrInternal(err)
	}

	session, err := s.sessions.Start(r.Context(), user.ID)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusCreated, s.toAuthResponse(session, user))
	return nil
}

// handleLogin implements POST /v1/auth/login.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	// Login does not validate field shapes: a wrong password and a malformed
	// email should be indistinguishable, or the response tells an attacker
	// which addresses are registered.
	var discard validate.Errors
	email := validate.Email(&discard, "email", req.Email)

	credentials, err := s.store.Users().CredentialsByEmail(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Spend comparable time on the unknown-email branch so response
			// latency does not disclose whether the account exists.
			auth.BurnPasswordComparison(req.Password)
			return httpx.ErrInvalidCredentials()
		}
		return httpx.ErrInternal(err)
	}

	if !auth.VerifyPassword(credentials.PasswordHash, req.Password) {
		return httpx.ErrInvalidCredentials()
	}

	user, err := s.store.Users().ByID(r.Context(), credentials.UserID)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	session, err := s.sessions.Start(r.Context(), user.ID)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toAuthResponse(session, user))
	return nil
}

// handleRefresh implements POST /v1/auth/refresh. Public: the refresh token is
// the credential. Every call rotates, returning a new refresh token and
// invalidating the one presented.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) error {
	var req refreshTokenRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if req.RefreshToken == "" {
		return httpx.ErrValidation("A refresh token is required.", validate.Errors{
			"refreshToken": "is required",
		})
	}

	session, err := s.sessions.Rotate(r.Context(), req.RefreshToken)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTokenExpired):
			// The refresh token itself has expired; there is nothing left to
			// refresh with, so the client must send the user back to sign in.
			return httpx.ErrTokenInvalid()
		case errors.Is(err, auth.ErrTokenInvalid):
			return httpx.ErrTokenInvalid()
		default:
			return httpx.ErrInternal(err)
		}
	}

	user, err := s.store.Users().ByID(r.Context(), session.UserID)
	if err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.WriteJSON(w, http.StatusOK, s.toAuthResponse(session, user))
	return nil
}

// handleLogout implements POST /v1/auth/logout. It is deliberately forgiving:
// the contract tells the client to clear both tokens regardless of the
// response, so a token we cannot find is still a successful logout.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	var req refreshTokenRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}

	if err := s.sessions.Revoke(r.Context(), req.RefreshToken); err != nil {
		return httpx.ErrInternal(err)
	}

	httpx.NoContent(w)
	return nil
}

func (s *Server) toAuthResponse(session *auth.Session, user *store.User) authResponse {
	return authResponse{
		AccessToken:          session.AccessToken,
		RefreshToken:         session.RefreshToken,
		AccessTokenExpiresAt: jsontime.New(session.AccessExpiresAt),
		User:                 s.toUser(user),
	}
}

// conflictOrInternal maps a unique-constraint violation onto the contract's
// CONFLICT code, naming the field so the client can render it inline.
func conflictOrInternal(err error) error {
	var conflict *store.ConflictError
	if errors.As(err, &conflict) {
		switch conflict.Field {
		case "email":
			return httpx.ErrConflict("That email address is already registered.",
				map[string]string{"email": conflict.Reason})
		case "username":
			return httpx.ErrConflict("That username is already taken.",
				map[string]string{"username": conflict.Reason})
		default:
			return httpx.ErrConflict("That record already exists.", nil)
		}
	}
	return httpx.ErrInternal(err)
}

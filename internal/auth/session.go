package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"domieface/com/internal/store"
)

// Session is one issued credential pair.
type Session struct {
	UserID          string
	AccessToken     string
	RefreshToken    string
	AccessExpiresAt time.Time
}

// Service owns the refresh token lifecycle.
type Service struct {
	tokens     store.TokenStore
	issuer     *TokenIssuer
	refreshTTL time.Duration
	now        func() time.Time
}

// NewService builds the session service.
func NewService(tokens store.TokenStore, issuer *TokenIssuer, refreshTTL time.Duration) *Service {
	return &Service{tokens: tokens, issuer: issuer, refreshTTL: refreshTTL, now: time.Now}
}

// Start issues a fresh credential pair at the head of a new token family.
// Called on register and login.
func (s *Service) Start(ctx context.Context, userID string) (*Session, error) {
	return s.issue(ctx, userID, store.NewID())
}

// Rotate exchanges a refresh token for a new pair. Every exchange consumes the
// presented token and issues a successor in the same family, as the contract
// requires.
//
// Replaying a token that was already consumed means it leaked — either the
// client failed to persist its successor, or somebody copied it. We cannot tell
// which from here, so we revoke the whole family and make everyone sign in
// again. That is the conservative reading, and it is why the contract insists
// the client persists the new refresh token immediately.
func (s *Service) Rotate(ctx context.Context, presented string) (*Session, error) {
	if presented == "" {
		return nil, ErrTokenInvalid
	}

	token, err := s.tokens.ByHash(ctx, HashRefreshToken(presented))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("looking up refresh token: %w", err)
	}

	now := s.now()

	if token.UsedAt != nil {
		slog.WarnContext(ctx, "refresh token replayed, revoking family",
			"user_id", token.UserID, "family_id", token.FamilyID)
		if err := s.tokens.RevokeFamily(ctx, token.FamilyID, now); err != nil {
			return nil, fmt.Errorf("revoking replayed token family: %w", err)
		}
		return nil, ErrTokenInvalid
	}
	if token.RevokedAt != nil {
		return nil, ErrTokenInvalid
	}
	if !now.Before(token.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	// Consume it. The store's `used_at IS NULL` guard means that if two
	// requests race here only one wins; the loser is treated as a replay.
	if err := s.tokens.MarkUsed(ctx, token.ID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			if revokeErr := s.tokens.RevokeFamily(ctx, token.FamilyID, now); revokeErr != nil {
				return nil, fmt.Errorf("revoking raced token family: %w", revokeErr)
			}
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("consuming refresh token: %w", err)
	}

	return s.issue(ctx, token.UserID, token.FamilyID)
}

// Revoke ends the session a refresh token belongs to. It is idempotent: an
// unknown token is not an error, because the contract tells the client to clear
// its tokens regardless of what logout returns.
func (s *Service) Revoke(ctx context.Context, presented string) error {
	if presented == "" {
		return nil
	}

	token, err := s.tokens.ByHash(ctx, HashRefreshToken(presented))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("looking up refresh token: %w", err)
	}

	// Revoke the family rather than the single token: logging out should end
	// the session, not just invalidate the one credential presented.
	return s.tokens.RevokeFamily(ctx, token.FamilyID, s.now())
}

// RevokeAllForUser ends every session a user has open.
func (s *Service) RevokeAllForUser(ctx context.Context, userID string) error {
	return s.tokens.RevokeAllForUser(ctx, userID, s.now())
}

func (s *Service) issue(ctx context.Context, userID, familyID string) (*Session, error) {
	accessToken, accessExpiresAt, err := s.issuer.IssueAccess(userID)
	if err != nil {
		return nil, err
	}

	refreshPlaintext, refreshHash := NewRefreshToken()
	now := s.now()

	err = s.tokens.Issue(ctx, store.RefreshToken{
		ID:        store.NewID(),
		UserID:    userID,
		FamilyID:  familyID,
		TokenHash: refreshHash,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("storing refresh token: %w", err)
	}

	return &Session{
		UserID:          userID,
		AccessToken:     accessToken,
		RefreshToken:    refreshPlaintext,
		AccessExpiresAt: accessExpiresAt,
	}, nil
}

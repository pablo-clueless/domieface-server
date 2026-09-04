package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"domieface/com/internal/auth"
	"domieface/com/internal/store"
	"domieface/com/internal/store/memory"
)

var testSecret = []byte("a-test-secret-that-is-long-enough-for-hs256")

func TestAccessTokenRoundTrip(t *testing.T) {
	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)

	token, expiresAt, err := issuer.IssueAccess("user-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if time.Until(expiresAt) > 15*time.Minute {
		t.Errorf("expiry %v is beyond the 15 minute access token lifetime", expiresAt)
	}

	userID, err := issuer.ParseAccess(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if userID != "user-1" {
		t.Errorf("got subject %q, want %q", userID, "user-1")
	}
}

// The two 401 codes are not interchangeable: expired means refresh and retry,
// invalid means log the user out.
func TestExpiredAndInvalidAreDistinguished(t *testing.T) {
	expiredIssuer := auth.NewTokenIssuer(testSecret, -time.Minute)
	expired, _, err := expiredIssuer.IssueAccess("user-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)
	if _, err := issuer.ParseAccess(expired); !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("expired token: got %v, want ErrTokenExpired", err)
	}

	if _, err := issuer.ParseAccess("not-a-token"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("garbage: got %v, want ErrTokenInvalid", err)
	}
}

func TestTokenSignedWithAnotherSecretIsRejected(t *testing.T) {
	foreign := auth.NewTokenIssuer([]byte("a-completely-different-secret-value-here"), 15*time.Minute)
	token, _, err := foreign.IssueAccess("user-1")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)
	if _, err := issuer.ParseAccess(token); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("got %v, want ErrTokenInvalid", err)
	}
}

// An unsigned "alg: none" token must never be accepted. This is the classic JWT
// bypass, and it is only prevented because ParseAccess pins the algorithm.
func TestUnsignedTokenIsRejected(t *testing.T) {
	// {"alg":"none","typ":"JWT"}.{"sub":"user-1","iss":"domieface","exp":...}.
	const unsigned = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJzdWIiOiJ1c2VyLTEiLCJpc3MiOiJkb21pZWZhY2UiLCJleHAiOjQxMDI0NDQ4MDB9."

	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)
	if _, err := issuer.ParseAccess(unsigned); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("got %v, want ErrTokenInvalid", err)
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "correct-horse-battery" {
		t.Fatal("the password was stored in plaintext")
	}
	if !auth.VerifyPassword(hash, "correct-horse-battery") {
		t.Error("the correct password did not verify")
	}
	if auth.VerifyPassword(hash, "wrong-horse-battery") {
		t.Error("an incorrect password verified")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	first, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := auth.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == second {
		t.Error("two hashes of the same password are identical, so no salt is applied")
	}
}

func newSessionService(t *testing.T) (*auth.Service, *memory.Store) {
	t.Helper()
	st := memory.New()
	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)
	return auth.NewService(st.Tokens(), issuer, 30*24*time.Hour), st
}

func TestRefreshRotatesTheToken(t *testing.T) {
	sessions, _ := newSessionService(t)
	ctx := context.Background()

	first, err := sessions.Start(ctx, "user-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	second, err := sessions.Rotate(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Error("refresh returned the same token; it must rotate")
	}
	if second.UserID != "user-1" {
		t.Errorf("got user %q, want user-1", second.UserID)
	}
}

// Replaying a consumed refresh token means it leaked. The whole family is
// revoked, so the successor the legitimate client is holding stops working too
// and everyone has to sign in again.
func TestReplayingAConsumedTokenRevokesTheFamily(t *testing.T) {
	sessions, _ := newSessionService(t)
	ctx := context.Background()

	first, err := sessions.Start(ctx, "user-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := sessions.Rotate(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}

	if _, err := sessions.Rotate(ctx, first.RefreshToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Fatalf("replay: got %v, want ErrTokenInvalid", err)
	}

	// The successor must now be dead as well.
	if _, err := sessions.Rotate(ctx, second.RefreshToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("successor after replay: got %v, want ErrTokenInvalid", err)
	}
}

func TestRotateRejectsUnknownToken(t *testing.T) {
	sessions, _ := newSessionService(t)

	if _, err := sessions.Rotate(context.Background(), "never-issued"); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("got %v, want ErrTokenInvalid", err)
	}
}

func TestRotateRejectsExpiredRefreshToken(t *testing.T) {
	st := memory.New()
	issuer := auth.NewTokenIssuer(testSecret, 15*time.Minute)
	// A negative lifetime issues a refresh token that is already past expiry.
	sessions := auth.NewService(st.Tokens(), issuer, -time.Hour)
	ctx := context.Background()

	session, err := sessions.Start(ctx, "user-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := sessions.Rotate(ctx, session.RefreshToken); !errors.Is(err, auth.ErrTokenExpired) {
		t.Errorf("got %v, want ErrTokenExpired", err)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	sessions, _ := newSessionService(t)
	ctx := context.Background()

	session, err := sessions.Start(ctx, "user-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := sessions.Revoke(ctx, session.RefreshToken); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := sessions.Rotate(ctx, session.RefreshToken); !errors.Is(err, auth.ErrTokenInvalid) {
		t.Errorf("got %v, want ErrTokenInvalid after logout", err)
	}
}

// The contract tells the client to clear its tokens regardless of what logout
// returns, so logging out twice must not be an error.
func TestLogoutIsIdempotent(t *testing.T) {
	sessions, _ := newSessionService(t)
	ctx := context.Background()

	if err := sessions.Revoke(ctx, "never-issued"); err != nil {
		t.Errorf("revoking an unknown token: got %v, want nil", err)
	}
	if err := sessions.Revoke(ctx, ""); err != nil {
		t.Errorf("revoking an empty token: got %v, want nil", err)
	}
}

func TestRefreshTokenIsStoredHashedNotInPlaintext(t *testing.T) {
	sessions, st := newSessionService(t)
	ctx := context.Background()

	session, err := sessions.Start(ctx, "user-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Looking the token up by its plaintext must find nothing; only the digest
	// is a valid key.
	if _, err := st.Tokens().ByHash(ctx, session.RefreshToken); !errors.Is(err, store.ErrNotFound) {
		t.Error("the refresh token plaintext was usable as a lookup key, so it is stored unhashed")
	}
	if _, err := st.Tokens().ByHash(ctx, auth.HashRefreshToken(session.RefreshToken)); err != nil {
		t.Errorf("looking the token up by its digest failed: %v", err)
	}
}

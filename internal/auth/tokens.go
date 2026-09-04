package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Errors the API layer maps onto TOKEN_EXPIRED and TOKEN_INVALID. The
// distinction matters to the client: expired means refresh and retry once,
// invalid means log the user out.
var (
	// ErrTokenExpired means the credential was ours but has passed its lifetime.
	ErrTokenExpired = errors.New("auth: token expired")
	// ErrTokenInvalid means the credential is malformed, forged or revoked.
	ErrTokenInvalid = errors.New("auth: token invalid")
)

// tokenIssuer is the `iss` claim, checked on parse so a token minted by some
// other service sharing the secret is still rejected.
const tokenIssuer = "domieface"

// Claims are the access token's payload. Subject carries the user ID.
type Claims struct {
	jwt.RegisteredClaims
}

// TokenIssuer mints and verifies short-lived access tokens.
type TokenIssuer struct {
	secret    []byte
	accessTTL time.Duration
	now       func() time.Time // swapped in tests
}

// NewTokenIssuer builds an issuer signing with HMAC-SHA256.
func NewTokenIssuer(secret []byte, accessTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: secret, accessTTL: accessTTL, now: time.Now}
}

// AccessTTL is the lifetime stamped on issued access tokens.
func (ti *TokenIssuer) AccessTTL() time.Duration { return ti.accessTTL }

// IssueAccess mints an access token for a user.
func (ti *TokenIssuer) IssueAccess(userID string) (string, time.Time, error) {
	now := ti.now()
	expiresAt := now.Add(ti.accessTTL)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    tokenIssuer,
			Subject:   userID,
			ID:        hex.EncodeToString(randomBytes(8)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})

	signed, err := token.SignedString(ti.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing access token: %w", err)
	}
	return signed, expiresAt, nil
}

// ParseAccess verifies a token and returns the user ID it authenticates.
func (ti *TokenIssuer) ParseAccess(raw string) (string, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		// Pin the algorithm. Without this a token signed with "none", or an
		// RS256 token using our secret as the public key, would be accepted.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return ti.secret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(tokenIssuer),
		jwt.WithExpirationRequired(),
	)

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return "", ErrTokenExpired
		}
		return "", ErrTokenInvalid
	}
	if claims.Subject == "" {
		return "", ErrTokenInvalid
	}
	return claims.Subject, nil
}

// NewRefreshToken returns the opaque credential handed to the client together
// with the SHA-256 digest stored in the database. The plaintext is never
// persisted, so a database leak does not hand over live sessions.
func NewRefreshToken() (plaintext, hash string) {
	plaintext = base64.RawURLEncoding.EncodeToString(randomBytes(32))
	return plaintext, HashRefreshToken(plaintext)
}

// HashRefreshToken digests a refresh token for lookup. SHA-256 rather than
// bcrypt: the input is 256 bits of entropy we generated, so it is not
// guessable, and lookup has to be an indexed equality search.
func HashRefreshToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	return buf
}

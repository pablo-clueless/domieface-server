// Package auth handles password hashing, access token minting and the rotating
// refresh token lifecycle.
package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is deliberately above bcrypt.DefaultCost (10). The contract sets
// no composition rules on passwords — length is what matters — so the work
// factor is what protects a leaked hash.
const bcryptCost = 12

// dummyHash is a valid bcrypt hash of a value nobody knows. Login compares
// against it when the email is unknown, so a request for a non-existent account
// costs the same as one for a real account and cannot be timed to enumerate
// which emails are registered.
const dummyHash = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// HashPassword returns a bcrypt hash suitable for storage.
func HashPassword(plaintext string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword reports whether plaintext matches hash.
func VerifyPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// BurnPasswordComparison performs the same work as VerifyPassword against a
// throwaway hash. Call it on the unknown-email branch of login so both branches
// take comparable time.
func BurnPasswordComparison(plaintext string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(plaintext))
}

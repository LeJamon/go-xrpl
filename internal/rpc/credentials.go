package rpc

import (
	"crypto/sha256"
	"crypto/subtle"
)

// constantTimeCredentialsMatch compares both credential fields after hashing
// them to fixed-size digests. Comparing the digests avoids the length-based
// early exit in subtle.ConstantTimeCompare while keeping each field check
// independent of the other.
func constantTimeCredentialsMatch(user, password, expectedUser, expectedPassword string) bool {
	userDigest := sha256.Sum256([]byte(user))
	expectedUserDigest := sha256.Sum256([]byte(expectedUser))
	passwordDigest := sha256.Sum256([]byte(password))
	expectedPasswordDigest := sha256.Sum256([]byte(expectedPassword))

	userMatch := subtle.ConstantTimeCompare(userDigest[:], expectedUserDigest[:])
	passwordMatch := subtle.ConstantTimeCompare(passwordDigest[:], expectedPasswordDigest[:])
	return (userMatch & passwordMatch) == 1
}

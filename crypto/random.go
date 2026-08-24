package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrInvalidRandomLength is returned when a negative byte count is requested.
	ErrInvalidRandomLength = errors.New("random byte length must not be negative")
	// ErrRandomGeneration is returned when random number generation fails.
	ErrRandomGeneration = errors.New("failed to generate random bytes")
)

// RandomBytes generates n cryptographically secure random bytes.
// It uses crypto/rand which reads from the system's CSPRNG.
func RandomBytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, ErrInvalidRandomLength
	}
	if n == 0 {
		return nil, nil
	}

	b := make([]byte, n)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRandomGeneration, err)
	}
	return b, nil
}

// RandomSeed generates a random 16-byte seed suitable for key derivation.
// This matches the standard XRPL seed size.
func RandomSeed() ([]byte, error) {
	return RandomBytes(FamilySeedSize)
}

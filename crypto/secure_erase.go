package crypto

import (
	"runtime"
)

// SecureErase overwrites the contents of a byte slice with zeros.
//
// The implementation is a plain Go loop followed by runtime.KeepAlive(b). The
// KeepAlive prevents the compiler from dropping the backing array before the
// stores complete, but it does not stop the optimiser from eliding stores it
// can prove are dead. Treat this as best-effort scrubbing: it is comparable
// in strength to the standard library's clear(b) (Go 1.21+) and weaker than
// platform primitives like memset_s / RtlSecureZeroMemory. Remnants may
// remain in caches, registers, or swap space.
//
// See: http://www.daemonology.net/blog/2014-09-04-how-to-zero-a-buffer.html
func SecureErase(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}

// SecretKey wraps a secret key byte slice and provides secure erasure on close.
// It should be used for all cryptographic secret keys to ensure they are
// properly cleared from memory when no longer needed.
type SecretKey struct {
	data   []byte
	closed bool
}

// NewSecretKey creates a new SecretKey wrapping the given byte slice.
// The SecretKey takes ownership of the slice and will clear it on Close().
func NewSecretKey(data []byte) *SecretKey {
	return &SecretKey{
		data:   data,
		closed: false,
	}
}

// NewSecretKeyWithCopy creates a new SecretKey by copying the given data.
// This is useful when the original data should not be modified.
func NewSecretKeyWithCopy(data []byte) *SecretKey {
	copied := make([]byte, len(data))
	copy(copied, data)
	return &SecretKey{
		data:   copied,
		closed: false,
	}
}

// Data returns the underlying secret key bytes.
// Returns nil if the key has been closed.
func (sk *SecretKey) Data() []byte {
	if sk == nil || sk.closed {
		return nil
	}
	return sk.data
}

// Len returns the length of the secret key.
// Returns 0 if the key has been closed.
func (sk *SecretKey) Len() int {
	if sk == nil || sk.closed {
		return 0
	}
	return len(sk.data)
}

// Close securely erases the secret key data and marks the key as closed.
// After calling Close, Data() will return nil.
// It is safe to call Close multiple times.
func (sk *SecretKey) Close() {
	if sk == nil || sk.closed {
		return
	}
	SecureErase(sk.data)
	sk.data = nil
	sk.closed = true
}

// IsClosed returns true if the secret key has been closed.
func (sk *SecretKey) IsClosed() bool {
	return sk == nil || sk.closed
}

// Copy returns a copy of the secret key data.
// Returns nil if the key has been closed.
func (sk *SecretKey) Copy() []byte {
	if sk == nil || sk.closed {
		return nil
	}
	result := make([]byte, len(sk.data))
	copy(result, sk.data)
	return result
}

// SecretKeySecp256k1Size is the size of a secp256k1 secret key in bytes.
const SecretKeySecp256k1Size = 32

// SecretKeyEd25519Size is the size of an Ed25519 secret key seed in bytes.
const SecretKeyEd25519Size = 32

// SecretKeySecp256k1WithPrefixSize is the size of a secp256k1 secret key with prefix.
const SecretKeySecp256k1WithPrefixSize = 33

// SecretKeyEd25519WithPrefixSize is the size of an Ed25519 secret key with prefix.
const SecretKeyEd25519WithPrefixSize = 33

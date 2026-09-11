//go:build cgo

// Package shim binds the secp256k1 operations used by the XRPL secp256k1
// implementation to libsecp256k1. The native context is randomized once and
// retained for the process lifetime; immutable operations are safe to call
// concurrently.
package shim

// #cgo pkg-config: libsecp256k1
// #include "shim.h"
import "C"

import (
	cryptorand "crypto/rand"
	"runtime"
	"unsafe"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
)

func init() {
	var seed [32]byte
	if _, err := cryptorand.Read(seed[:]); err != nil {
		panic("secp256k1: unable to obtain context randomization seed")
	}
	defer rootcrypto.SecureErase(seed[:])
	if C.goxrpl_secp256k1_init(
		(*C.uchar)(unsafe.Pointer(&seed[0]))) != 1 {
		panic("secp256k1: unable to initialize randomized libsecp256k1 context")
	}
}

// SecretKeyValid reports whether secret is exactly 32 bytes and represents a
// scalar in [1, curve order - 1].
func SecretKeyValid(secret []byte) bool {
	if len(secret) != 32 {
		return false
	}
	rc := C.goxrpl_secp256k1_secret_key_valid(
		(*C.uchar)(unsafe.Pointer(&secret[0])), C.size_t(len(secret)))
	runtime.KeepAlive(secret)
	return rc == 1
}

// SecretKeyReduce reduces a 32-byte big-endian candidate modulo the
// secp256k1 group order and rejects a zero result. The returned scalar owns
// its backing array and the input is not mutated.
func SecretKeyReduce(candidate []byte) ([]byte, bool) {
	if len(candidate) != 32 {
		return nil, false
	}
	if candidate[0]&0x80 == 0 {
		if !SecretKeyValid(candidate) {
			return nil, false
		}
		return append([]byte(nil), candidate...), true
	}

	base := make([]byte, 32)
	base[0] = 0x80
	defer rootcrypto.SecureErase(base)
	tweak := append([]byte(nil), candidate...)
	tweak[0] &= 0x7f
	defer rootcrypto.SecureErase(tweak)
	return SecretKeyTweakAdd(base, tweak)
}

// PublicKeyCreate derives a compressed public key from a 32-byte secret. The
// returned slice owns its backing array and is independent of secret.
func PublicKeyCreate(secret []byte) ([]byte, bool) {
	if len(secret) != 32 {
		return nil, false
	}
	output := make([]byte, 33)
	rc := C.goxrpl_secp256k1_public_key_create(
		(*C.uchar)(unsafe.Pointer(&secret[0])), C.size_t(len(secret)),
		(*C.uchar)(unsafe.Pointer(&output[0])), C.size_t(len(output)))
	runtime.KeepAlive(secret)
	if rc != 1 {
		return nil, false
	}
	return output, true
}

// ParsePublicKey parses a libsecp256k1 public-key encoding and returns an
// independently owned serialization in the requested format. Both compressed
// (33-byte) and uncompressed or hybrid (65-byte) inputs are accepted.
func ParsePublicKey(pub []byte, compressed bool) ([]byte, bool) {
	if len(pub) != 33 && len(pub) != 65 {
		return nil, false
	}
	outputLen := 65
	if compressed {
		outputLen = 33
	}
	output := make([]byte, outputLen)
	serializedLen := C.size_t(len(output))
	rc := C.goxrpl_secp256k1_public_key_parse(
		(*C.uchar)(unsafe.Pointer(&pub[0])), C.size_t(len(pub)),
		(*C.uchar)(unsafe.Pointer(&output[0])), &serializedLen,
		boolToCInt(compressed))
	runtime.KeepAlive(pub)
	if rc != 1 || int(serializedLen) != outputLen {
		return nil, false
	}
	return output, true
}

// SignDigest signs a 32-byte digest with a 32-byte secret using deterministic
// RFC6979 ECDSA and returns an owned, low-S DER signature.
func SignDigest(hash32, secret []byte) ([]byte, bool) {
	if len(hash32) != 32 || len(secret) != 32 {
		return nil, false
	}
	output := make([]byte, 72)
	serializedLen := C.size_t(len(output))
	rc := C.goxrpl_secp256k1_sign_digest(
		(*C.uchar)(unsafe.Pointer(&hash32[0])),
		(*C.uchar)(unsafe.Pointer(&secret[0])), C.size_t(len(secret)),
		(*C.uchar)(unsafe.Pointer(&output[0])), &serializedLen)
	runtime.KeepAlive(hash32)
	runtime.KeepAlive(secret)
	if rc != 1 || serializedLen == 0 || serializedLen > C.size_t(len(output)) {
		return nil, false
	}
	return output[:int(serializedLen)], true
}

// SecretKeyTweakAdd returns secret + tweak modulo the secp256k1 group order.
// The input slices are not mutated. An all-zero tweak is accepted.
func SecretKeyTweakAdd(secret, tweak []byte) ([]byte, bool) {
	if len(secret) != 32 || len(tweak) != 32 {
		return nil, false
	}
	output := make([]byte, 32)
	rc := C.goxrpl_secp256k1_secret_key_tweak_add(
		(*C.uchar)(unsafe.Pointer(&secret[0])), C.size_t(len(secret)),
		(*C.uchar)(unsafe.Pointer(&tweak[0])), C.size_t(len(tweak)),
		(*C.uchar)(unsafe.Pointer(&output[0])), C.size_t(len(output)))
	runtime.KeepAlive(secret)
	runtime.KeepAlive(tweak)
	if rc != 1 {
		rootcrypto.SecureErase(output)
		return nil, false
	}
	return output, true
}

// PublicKeyTweakAdd returns pub + tweak*G as a compressed public key. The
// input slices are not mutated; compressed, uncompressed, and hybrid public
// keys are accepted as input. An all-zero tweak is accepted.
func PublicKeyTweakAdd(pub, tweak []byte) ([]byte, bool) {
	if (len(pub) != 33 && len(pub) != 65) || len(tweak) != 32 {
		return nil, false
	}
	output := make([]byte, 33)
	rc := C.goxrpl_secp256k1_public_key_tweak_add(
		(*C.uchar)(unsafe.Pointer(&pub[0])), C.size_t(len(pub)),
		(*C.uchar)(unsafe.Pointer(&tweak[0])), C.size_t(len(tweak)),
		(*C.uchar)(unsafe.Pointer(&output[0])), C.size_t(len(output)))
	runtime.KeepAlive(pub)
	runtime.KeepAlive(tweak)
	if rc != 1 {
		return nil, false
	}
	return output, true
}

// VerifyDigest accepts a DER-encoded ECDSA signature with either low-S or
// high-S. The shim normalizes to low-S before calling
// secp256k1_ecdsa_verify; canonicality gating is the caller's responsibility.
// pub must be an XRPL-canonical 33-byte compressed secp256k1 key.
func VerifyDigest(hash32 []byte, pub []byte, sigDER []byte) bool {
	if len(hash32) != 32 || rootcrypto.PublicKeyType(pub) != rootcrypto.KeyTypeSecp256k1 || len(sigDER) == 0 {
		return false
	}
	rc := C.goxrpl_secp256k1_verify_digest(
		(*C.uchar)(unsafe.Pointer(&pub[0])), C.size_t(len(pub)),
		(*C.uchar)(unsafe.Pointer(&sigDER[0])), C.size_t(len(sigDER)),
		(*C.uchar)(unsafe.Pointer(&hash32[0])))
	runtime.KeepAlive(hash32)
	runtime.KeepAlive(pub)
	runtime.KeepAlive(sigDER)
	return rc == 1
}

func boolToCInt(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

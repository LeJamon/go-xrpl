//go:build cgo

// Package shim is the cgo verify binding for libsecp256k1. Signing and
// key derivation stay in pure Go. The verify context is process-lifetime
// and libsecp256k1's verify ops are thread-safe, so the call path needs
// no OS-thread locking.
package shim

// #cgo pkg-config: libsecp256k1
// #include "shim.h"
import "C"

import "unsafe"

func init() {
	C.goxrpl_secp256k1_init()
}

// VerifyDigest accepts a DER-encoded ECDSA signature with either low-S
// or high-S — the shim normalizes to low-S before calling
// secp256k1_ecdsa_verify, which itself rejects high-S. Canonicality
// gating (if any) is the caller's responsibility.
func VerifyDigest(hash32 []byte, pub []byte, sigDER []byte) bool {
	if len(hash32) != 32 || len(pub) == 0 || len(sigDER) == 0 {
		return false
	}
	rc := C.goxrpl_secp256k1_verify_digest(
		(*C.uchar)(unsafe.Pointer(&pub[0])), C.size_t(len(pub)),
		(*C.uchar)(unsafe.Pointer(&sigDER[0])), C.size_t(len(sigDER)),
		(*C.uchar)(unsafe.Pointer(&hash32[0])),
	)
	return rc == 1
}

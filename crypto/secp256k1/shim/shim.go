//go:build cgo

// Package shim is the cgo binding for libsecp256k1's verify path. Only
// signature verification is wired through C; signing and key derivation
// stay in pure Go. The verify context is process-lifetime and
// libsecp256k1's verify operations are thread-safe, so no OS-thread
// locking is needed on the call path.
package shim

// #cgo pkg-config: libsecp256k1
// #include "shim.h"
import "C"

import (
	"sync"
	"unsafe"
)

var initOnce sync.Once

func ensureInit() {
	initOnce.Do(func() {
		C.goxrpl_secp256k1_init()
	})
}

// VerifyDigest verifies a DER-encoded ECDSA signature against a 32-byte
// message hash and a SEC1 public key (33-byte compressed or 65-byte
// uncompressed). Returns true iff the signature is valid AND low-S;
// libsecp256k1 rejects high-S signatures internally.
func VerifyDigest(hash32 []byte, pub []byte, sigDER []byte) bool {
	if len(hash32) != 32 || len(pub) == 0 || len(sigDER) == 0 {
		return false
	}
	ensureInit()
	rc := C.goxrpl_secp256k1_verify_digest(
		(*C.uchar)(unsafe.Pointer(&pub[0])), C.size_t(len(pub)),
		(*C.uchar)(unsafe.Pointer(&sigDER[0])), C.size_t(len(sigDER)),
		(*C.uchar)(unsafe.Pointer(&hash32[0])),
	)
	return rc == 1
}

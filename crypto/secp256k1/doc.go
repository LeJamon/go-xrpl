// Package secp256k1 implements the secp256k1 ECDSA signing algorithm for the XRP
// Ledger.
//
// It provides the secp256k1 variant of the
// [github.com/LeJamon/go-xrpl/crypto].Algorithm interface: deriving a keypair from a
// family seed (including the iterated scalar derivation XRPL specifies), signing,
// and verifying. As rippled requires, signatures must be fully canonical — verify
// rejects non-canonical signatures with [ErrSignatureNotCanonical].
//
// Signature verification is on the consensus hot path. With cgo enabled it links
// libsecp256k1 (verify_cgo.go); under CGO_ENABLED=0 it falls back to a slower
// pure-Go implementation (verify_purego.go).
package secp256k1

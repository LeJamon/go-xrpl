// Package secp256k1 implements the secp256k1 ECDSA signing algorithm for the XRP
// Ledger.
//
// It provides the secp256k1 variant of the
// [github.com/LeJamon/go-xrpl/crypto].Algorithm interface: deriving a keypair from a
// family seed (including the iterated scalar derivation XRPL specifies), signing,
// and verifying. As rippled requires, signatures must be fully canonical: the
// strict Validate path rejects non-canonical signatures by returning false.
//
// All secp256k1 operations use the process-lifetime libsecp256k1 context through
// the cgo shim. The context is randomized before concurrent use.
package secp256k1

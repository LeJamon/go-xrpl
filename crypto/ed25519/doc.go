// Package ed25519 implements the Ed25519 signing algorithm for the XRP Ledger.
//
// It provides the Ed25519 variant of the [github.com/LeJamon/go-xrpl/crypto].Algorithm
// interface: deriving a keypair from a family seed, signing, and verifying. XRPL
// distinguishes key types by a one-byte prefix — Ed25519 public keys are prefixed
// with 0xED — and uses a dedicated three-byte family-seed prefix for Ed25519 seeds.
//
// Ed25519 may not be used for validator keypairs; that case returns
// [ErrValidatorNotSupported].
package ed25519

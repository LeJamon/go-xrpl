// Package crypto provides cryptographic operations for the XRPL protocol.
//
// It supports two key algorithms: secp256k1 (the original XRPL algorithm)
// and Ed25519. The package handles key generation from seeds, transaction
// signing, signature verification, DER encoding of signatures, and
// multi-signing with sorted signer lists.
//
// The common subpackage provides the SHA-512Half hash function used
// extensively throughout the XRPL protocol for key derivation, node
// identification, and integrity verification.
package crypto

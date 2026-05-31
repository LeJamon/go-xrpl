// Package interfaces declares the cryptographic operations that
// [github.com/LeJamon/goXRPLd/codec/addresscodec] depends on.
//
// CryptoImplementation (keypair derivation, signing, and verification) is defined
// here, separate from both the address codec and the crypto packages, so the codec
// can call into cryptography without forming an import cycle with the crypto
// packages that in turn depend on the codec.
package interfaces

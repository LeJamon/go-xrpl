// Package addresscodec implements XRPL base58 address encoding and decoding.
//
// It handles the conversion between raw byte representations and human-readable
// XRPL identifiers, including classic addresses (rXXX...), X-addresses,
// account seeds, node public keys, and validator keys. Each format uses a
// distinct type prefix byte and a four-byte checksum for error detection.
//
// The base58 alphabet used is the ripple alphabet, which differs from the
// Bitcoin base58 alphabet.
package addresscodec

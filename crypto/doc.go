// Package crypto provides shared cryptographic types and helpers for the XRP
// Ledger.
//
// The concrete signing implementations live in the ed25519 and secp256k1
// subpackages. This package defines their common Algorithm interface and key
// types, validates signature canonicality, encodes and decodes DER signatures,
// generates family-seed entropy, and provides best-effort secret erasure.
package crypto

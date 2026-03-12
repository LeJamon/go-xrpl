// Package binarycodec implements binary serialization and deserialization of
// XRPL objects using the canonical field ordering defined by the XRPL protocol.
//
// It converts between JSON representations and the compact binary format used
// for on-ledger storage, transaction signing, and network transmission. Fields
// are serialized in a deterministic order based on their type code and field
// code, ensuring identical binary output across all implementations.
//
// The codec supports all XRPL serialized types including amounts, account IDs,
// hashes, path sets, and nested objects (STObject, STArray).
package binarycodec

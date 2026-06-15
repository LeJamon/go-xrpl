// Package entry holds protocol-level definitions for XRPL ledger entries
// (Serializable Ledger Entries, "SLE").
//
// At present the package exposes:
//
//   - Type: the ledger-entry type-id enum mirroring
//     rippled/include/xrpl/protocol/detail/ledger_entries.macro.
//   - LedgerSpecificFlags: the per-entry-type flag constants mirroring
//     rippled/include/xrpl/protocol/LedgerFormats.h (Lsf* prefix).
//   - MPToken-related protocol limits (metadata length, transfer fee, and
//     maximum amount) used by MPT transaction handlers.
//
// Typed SLE structs (with Encode / Hash methods) are not yet defined here.
// Decode-only typed views currently live under internal/tx/ledgerfields/
// and SLE serialization goes through binarycodec against generic
// map[string]any payloads.
package entry

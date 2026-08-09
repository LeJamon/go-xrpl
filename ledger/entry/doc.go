// Package entry holds protocol-level definitions for XRPL ledger entries
// (Serializable Ledger Entries, "SLE").
//
// The package exposes:
//
//   - Type: the ledger-entry type-id enum mirroring
//     rippled/include/xrpl/protocol/detail/ledger_entries.macro.
//   - Lsf* and Lsif* per-entry-type flag constants mirroring
//     rippled/include/xrpl/protocol/LedgerFormats.h (Lsf* prefix).
//   - Generated typed SLE models with Decode, Encode, ToMap, metadata emission,
//     and Hash methods.
//
// MPToken protocol limits live in protocol; deprecated aliases remain here for
// source compatibility.
package entry

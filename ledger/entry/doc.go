// Package entry defines the Serializable Ledger Entry (SLE) types for all
// XRPL ledger objects.
//
// Each ledger object type (AccountRoot, Offer, RippleState, DirectoryNode,
// NFTokenPage, AMM, etc.) is represented as a Go struct with typed fields
// that map to the XRPL protocol's serialized field definitions. These structs
// support conversion to and from JSON maps for binary codec serialization.
//
// The package covers 40+ ledger entry types as defined in rippled's
// ledger_entries.macro.
package entry

// Package keylet provides functions to compute the 256-bit SHA-512Half keys
// that uniquely identify ledger entries in the XRPL state tree.
//
// The name "keylet" (key + let, "little key") comes from the rippled C++
// implementation. Each function in this package corresponds to a specific
// ledger entry type and deterministically derives its key from the entry's
// identifying fields (e.g., account ID, sequence number, currency/issuer pair).
//
// These keys are used to look up, insert, and delete entries in the SHAMap
// that represents ledger state.
package keylet

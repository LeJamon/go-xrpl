// Package nodestore provides blockchain state storage for XRPL node data.
//
// It stores and retrieves SHAMap tree nodes (inner nodes and leaf data) that
// make up ledger state and transaction trees. The nodestore is built on top
// of the kvstore interface, with support for batched writes, LRU caching,
// negative caching, and rotating database backends for online deletion.
//
// Node data is keyed by its SHA-512Half hash and encoded with type and
// compression metadata.
package nodestore

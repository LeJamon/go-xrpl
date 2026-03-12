// Package shamap implements the SHAMap, a Merkle-like radix tree used by the
// XRPL for ledger state and transaction storage.
//
// Each leaf node is keyed by a 256-bit identifier and the tree produces a
// single root hash that cryptographically commits to all entries. Inner nodes
// branch on successive 4-bit nibbles of the key, giving the tree a maximum
// depth of 64. The SHAMap supports copy-on-write snapshots, incremental
// hashing, disk-backed persistence through the nodestore, and efficient
// difference computation between tree versions.
package shamap

// Package sha512half provides SHA-512Half, the fundamental hashing primitive of
// the XRP Ledger.
//
// SHA-512Half is the first 32 bytes of a SHA-512 digest. The XRPL protocol uses it
// (typically over a domain-separating prefix plus the serialized payload) for
// transaction IDs, ledger and SHAMap node hashes, signing hashes, and elsewhere
// on the consensus hot path. [Sum] accepts multiple slices and hashes their
// bytes in order without adding separators.
package sha512half

// Package kvstore defines a generic key-value storage interface for
// persistent data backends.
//
// It provides the KeyValueStore interface along with reader, writer, batcher,
// and iterator contracts. Two implementations are included: an in-memory
// backend (memorydb) suitable for testing, and a Pebble-based backend for
// production use with disk-backed persistence.
//
// The design is inspired by go-ethereum's ethdb package.
package kvstore

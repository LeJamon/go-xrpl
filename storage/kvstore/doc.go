// Package kvstore defines a generic key-value storage interface for
// persistent data backends.
//
// It provides KeyValueStore along with explicit batch and iterator lifecycle
// contracts. Inputs are borrowed for a method call, while batches copy queued
// writes and Get returns caller-owned data. Two implementations are included:
// an in-memory backend (memorydb) suitable for testing, and a Pebble-based
// backend for production use with disk-backed persistence.
//
// The design is inspired by go-ethereum's ethdb package.
package kvstore

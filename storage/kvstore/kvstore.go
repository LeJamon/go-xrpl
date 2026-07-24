package kvstore

// KeyValueReader wraps the Has and Get methods of a key-value store.
type KeyValueReader interface {
	Has(key []byte) (bool, error)
	// Get returns a caller-owned value. Mutating the returned slice must not
	// alter the stored value or any slice returned by a later Get.
	Get(key []byte) ([]byte, error)
}

// KeyValueWriter wraps the Put and Delete methods of a key-value store.
type KeyValueWriter interface {
	Put(key []byte, value []byte) error
	Delete(key []byte) error
}

// Batcher wraps the NewBatch method of a key-value store.
type Batcher interface {
	NewBatch() Batch
}

// Batch is a write-only key-value store that accumulates changes to be flushed.
type Batch interface {
	KeyValueWriter
	// ValueSize returns an estimate of the in-memory data size of all accumulated writes.
	ValueSize() int
	// Write flushes accumulated writes to the underlying store.
	Write() error
	// Reset clears accumulated writes.
	Reset()
}

// Iteratee wraps the NewIterator method of a key-value store.
type Iteratee interface {
	NewIterator(prefix []byte, start []byte) Iterator
}

// Iterator iterates over a key-value store's key/value pairs.
// The iterator must be released after use.
type Iterator interface {
	Next() bool
	Key() []byte
	Value() []byte
	Error() error
	Release()
}

// KeyValueStore contains all the methods required to allow handling different
// key-value data stores in a high level manner.
type KeyValueStore interface {
	KeyValueReader
	KeyValueWriter
	Batcher
	Iteratee
	Stat() (string, error)
	Compact(start []byte, limit []byte) error
	// Sync makes all previously written data durable. Backends whose writes
	// are already durable (or that have no durable medium, like an in-memory
	// store) return nil.
	Sync() error
	Close() error
}

// RotatingStore keeps a writable generation and one read-only archive
// generation. Rotate installs a fresh writable generation, moves the previous
// writable generation to the archive slot, and retires the former archive.
type RotatingStore interface {
	KeyValueStore
	// CanRotateWithoutRefresh reports whether the archive is empty, so retiring
	// it cannot discard a record.
	CanRotateWithoutRefresh() (bool, error)
	// Promote returns a caller-owned value and ensures an archive hit is copied
	// into the current writable generation before it returns.
	Promote(key []byte) ([]byte, error)
	// committed is true once the durable generation manifest names the new
	// writable/archive pair. An error with committed=true reports work after the
	// manifest rename; the rotation must not be retried.
	Rotate(lastRotated, minimumOnline uint32) (committed bool, err error)
	RotationState() (lastRotated, minimumOnline uint32)
}

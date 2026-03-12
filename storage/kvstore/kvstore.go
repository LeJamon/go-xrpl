package kvstore

// KeyValueReader wraps the Has and Get methods of a key-value store.
type KeyValueReader interface {
	Has(key []byte) (bool, error)
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
	Close() error
}

package kvstore

// Batch is a write-only key-value store that accumulates changes to be flushed.
// A batch owns all key and value data passed to Put and Delete after those
// methods return. Close must be called to release its resources.
type Batch interface {
	Put(key []byte, value []byte) error
	Delete(key []byte) error
	// ValueSize returns an estimate of the in-memory data size of all accumulated writes.
	ValueSize() int
	// Write flushes accumulated writes to the underlying store.
	Write() error
	// Reset clears accumulated writes.
	Reset()
	Close() error
}

// Iterator iterates over a key-value store's key/value pairs.
// Key and Value are valid until the next call to Next or Close. Close must be
// called to release the iterator and any store resources it pins.
type Iterator interface {
	Next() bool
	Key() []byte
	Value() []byte
	Error() error
	Close() error
}

// KeyValueStore contains all the methods required to allow handling different
// key-value data stores. Input slices are borrowed only for the duration of a
// method call and may be mutated by the caller after the method returns.
type KeyValueStore interface {
	// Get returns a caller-owned value. Mutating the returned slice must not
	// alter the stored value or any slice returned by a later Get.
	Get(key []byte) ([]byte, error)
	Put(key []byte, value []byte) error
	NewBatch() (Batch, error)
	NewIterator(prefix []byte, start []byte) (Iterator, error)
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

// RotationIdentity is a path-free snapshot of a rotating store's durable
// manifest identity and generation boundary.
type RotationIdentity struct {
	OwnerID       [16]byte
	WritableID    [32]byte
	ArchiveID     [32]byte
	LastRotated   uint32
	MinimumOnline uint32
}

// RotationIdentityStore exposes the durable generation manifest without
// exposing backend paths.
type RotationIdentityStore interface {
	RotationIdentity() (RotationIdentity, error)
}

package kvstore

import "context"

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

// BatchReadingStore optionally provides bounded, context-aware reads for an
// explicit set of keys. Results are the sorted prefix of the sorted input and
// retain duplicate requests. For a non-empty request, maxNodes and maxBytes
// must be positive. Implementations must return caller-owned key and value
// slices and must not mutate keys.
type BatchReadingStore interface {
	GetBatch(ctx context.Context, keys [][]byte, maxNodes, maxBytes int) ([]ReadResult, error)
}

// ReadResult is one result from a BatchReadingStore read. Found distinguishes
// a missing key from a key whose stored value is empty.
type ReadResult struct {
	Key   []byte
	Value []byte
	Found bool
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

// BatchPromotingStore extends a rotating store with bounded locality-aware promotion.
type BatchPromotingStore interface {
	RotatingStore
	PromoteBatch(keys [][]byte, maxBytes int) ([]Promotion, PromotionStats, error)
}

// Promotion is one hash-ordered result from a bounded promotion operation.
type Promotion struct {
	Key   []byte
	Value []byte
	Found bool
}

// PromotionStats describes the logical reads and writes performed by one batch.
type PromotionStats struct {
	Requested      int
	Consumed       int
	WritableHits   int
	WritableMisses int
	ArchiveHits    int
	ArchiveMisses  int
	Promoted       int
	PromotedBytes  int
	BufferedBytes  int
	Batches        int
	// ArchiveLookups counts archive-generation lookups, including retries and fallback rereads.
	ArchiveLookups int
	// ArchiveLookupsAvoided is the number of archive lookups skipped because
	// the writable generation resolved the key first, including retries and fallback rereads.
	ArchiveLookupsAvoided int
	// PrefetchBytes is the payload size fetched while prefetching promotion
	// records, including retry payloads and fallback rereads.
	PrefetchBytes int
	// VersionMismatches includes repeated invalidated observations of a key.
	VersionMismatches int
	Retries           int
	// Fallbacks counts single-stripe rereads after the retry budget is exhausted.
	Fallbacks int
}

// CacheMetrics is a point-in-time snapshot of shared block-cache activity.
type CacheMetrics struct {
	Hits   int64
	Misses int64
}

// CacheMetricsStore exposes optional backend cache instrumentation.
type CacheMetricsStore interface {
	CacheMetrics() CacheMetrics
}

// IOMetrics snapshots persistence counters and size gauges for active generations.
// Counters can reset when generations rotate or the store reopens.
// CompactionBytesRead measures bytes read by Pebble compactions; foreground
// point-read bytes are not exposed by this optional interface.
type IOMetrics struct {
	LogicalBytesWritten    uint64
	WALBytesWritten        uint64
	FlushBytesWritten      uint64
	CompactionBytesRead    uint64
	CompactionBytesWritten uint64
	SSTableBytes           uint64
	MemTableBytes          uint64
}

// IOMetricsStore exposes optional backend persistence instrumentation.
type IOMetricsStore interface {
	IOMetrics() IOMetrics
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

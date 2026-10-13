// Package flatstate provides a bounded, durable execution-state base.
//
// A Store owns one Pebble database whose object records are addressed by the
// canonical 256-bit ledger key.  Views are point-in-time leases.  Advancing a
// store is a fenced, single-writer operation: a durable marker is written and
// synced before any object mutation, and the marker is cleared only in the
// final synced batch with the new descriptor and publication outbox record.
package flatstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	objectPrefix = "object/"
	metaCurrent  = "meta/current"
	metaAdvance  = "meta/advancing"
	outboxPrefix = "outbox/"

	descriptorVersion uint32 = 1
	markerVersion     uint32 = 1
	outboxVersion     uint32 = 1
	changeAlgorithm   uint32 = 1

	defaultCacheBytes     int64 = 64 << 20
	defaultMemTableSize   int64 = 16 << 20
	minimumMemTableSize   int64 = 64 << 10
	memTableCacheDivisor        = 4
	defaultBatchBytes           = 4 << 20
	defaultBatchRecords         = 4096
	defaultOutboxCount          = 1024
	defaultOutboxBytes    int64 = 64 << 20
	defaultOutboxAge            = 24 * time.Hour
	defaultMaxActiveViews       = 256
	maxIntentBytes              = 4 << 10
	maxMarkerBytes              = 16 << 10
	maxDescriptorBytes          = 256
	maxOutboxRecordBytes        = 32 << 10
)

var (
	// ErrUnavailable means that the flat base cannot safely serve a new view.
	// It is returned while an advancing marker is present or after an
	// uncertain durability result.  Callers should use their authenticated
	// root-backed view until Resume succeeds.
	ErrUnavailable = errors.New("flatstate: flat base unavailable")
	// ErrClosed is returned after a store or view has been closed.
	ErrClosed = errors.New("flatstate: closed")
	// ErrWriterBusy means another process-local writer owns the transition.
	ErrWriterBusy = errors.New("flatstate: writer busy")
	// ErrWriterFenced means a worker tried to write after its reservation ended.
	ErrWriterFenced = errors.New("flatstate: writer fenced")
	// ErrParentMismatch means that the expected source descriptor is not the
	// descriptor currently installed in the flat base.
	ErrParentMismatch = errors.New("flatstate: parent descriptor mismatch")
	// ErrInvalidChangeOrder means that a change iterator was not strictly
	// increasing by its 256-bit key.
	ErrInvalidChangeOrder = errors.New("flatstate: changes must be sorted and unique")
	// ErrAuthorization means that the caller did not provide an explicit,
	// matching durable root-closure authorization.
	ErrAuthorization = errors.New("flatstate: root closure authorization required")
	// ErrLimit means a configured bounded queue or record limit was exceeded.
	ErrLimit = errors.New("flatstate: configured limit exceeded")
	// ErrCorrupt means a record in the flat database failed validation.
	ErrCorrupt = errors.New("flatstate: corrupt record")
	// ErrActiveViews means Close was requested while leases remain open.  The
	// operation is retryable after all views have been closed.
	ErrActiveViews = errors.New("flatstate: active views remain")
)

// Hash names the 256-bit ledger or SHAMap identity used by the surrounding
// ledger packages.  It is an alias so existing [32]byte hashes pass through
// the flat-state API without conversion.
type Hash = [32]byte

// MemoryBudget is the shared byte-admission contract used for transition
// batches.  Store operations never wait on the budget; a failed admission is
// returned as ErrLimit so the caller can release or retry its reservation.
type MemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
	Limit() uint64
	Changed() <-chan struct{}
}

// Descriptor identifies an exact durable ledger version and its flat-state
// schema identity.  The full descriptor, rather than sequence or state root
// alone, is required when selecting a source for advancement.
type Descriptor struct {
	LedgerHash Hash
	ParentHash Hash
	Sequence   uint32
	StateRoot  Hash
	TxRoot     Hash
	Schema     uint32
	Epoch      uint64
}

// Equal reports whether two descriptors identify the same exact version and
// flat-state identity.
func (d Descriptor) Equal(other Descriptor) bool {
	return d == other
}

// IsZero reports whether d is the absence of a descriptor.
func (d Descriptor) IsZero() bool {
	return d == (Descriptor{})
}

func (d Descriptor) samePublicationVersion(other Descriptor) bool {
	d.Epoch = 0
	other.Epoch = 0
	return d == other
}

// RootClosureAuthorization is an explicit caller-supplied contract that the
// source and target roots remain durably available for the requested
// transition.  Flatstate does not infer this evidence from either root.  The
// token is opaque to this package and must be non-empty.
type RootClosureAuthorization struct {
	SourceRoot Hash
	TargetRoot Hash
	Token      []byte
}

func (a RootClosureAuthorization) validate(source, target Descriptor) error {
	if len(a.Token) == 0 || a.SourceRoot != source.StateRoot || a.TargetRoot != target.StateRoot {
		return ErrAuthorization
	}
	return nil
}

// Change is one final object mutation.  Delete masks the previous value; a
// non-delete change owns Value for the duration of the iterator call and may
// be reused after that call returns.
type Change struct {
	Key    Hash
	Value  []byte
	Delete bool
}

// ChangeIterator streams sorted, unique changes without requiring the store
// to buffer the complete delta in memory.
type ChangeIterator interface {
	Next(context.Context) (Change, bool, error)
	Close() error
}

// SliceChangeIterator adapts a caller-owned slice for tests and small imports.
// Advance still processes it in bounded batches.
type SliceChangeIterator struct {
	changes []Change
	index   int
}

// NewSliceChangeIterator returns an iterator over changes.  The slice and
// values are borrowed until Advance or Resume returns.
func NewSliceChangeIterator(changes []Change) *SliceChangeIterator {
	return &SliceChangeIterator{changes: changes}
}

func (i *SliceChangeIterator) Next(ctx context.Context) (Change, bool, error) {
	if err := contextErr(ctx); err != nil {
		return Change{}, false, err
	}
	if i.index >= len(i.changes) {
		return Change{}, false, nil
	}
	change := i.changes[i.index]
	i.index++
	return change, true, nil
}

func (i *SliceChangeIterator) Close() error { return nil }

// AdvanceRequest describes a source-to-target transition.  Expected may be
// nil only for an empty initial import.  Changes must be sorted by Key and
// contain no duplicate key.
type AdvanceRequest struct {
	Expected        *Descriptor
	Target          Descriptor
	Changes         ChangeIterator
	Authorization   RootClosureAuthorization
	PublicationKind string
}

// RebaseRequest describes a source-to-target transition that is authorized to
// replace the current lineage. Expected must identify the exact current
// descriptor; unlike Advance, the target need not name the current ledger as
// its parent. Changes remains a sorted, unique source-to-target diff.
type RebaseRequest struct {
	Expected        *Descriptor
	Target          Descriptor
	Changes         ChangeIterator
	Authorization   RootClosureAuthorization
	PublicationKind string
}

// ResumeRequest supplies the deterministic source-to-target replay required
// after reopening a store with an advancing marker.
type ResumeRequest struct {
	Authorization   RootClosureAuthorization
	Changes         ChangeIterator
	PublicationKind string
}

// OutboxRecord is a bounded publication obligation.  Payloads and transaction
// bytes are intentionally absent; consumers stream those from durable roots.
type OutboxRecord struct {
	CommitID   CommitID
	Descriptor Descriptor
	Intent     string
	CreatedAt  time.Time
}

// CommitID is the stable semantic identifier of a ledger publication intent.
type CommitID Hash

// SemanticCommitID derives an ID from the exact ledger hash and publication
// kind.  Flat epoch, writer epoch, and retry attempt are excluded so retries
// remain idempotent after rebuilding the materialization.
func SemanticCommitID(ledger Hash, publicationKind string) CommitID {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(publicationKind)))
	h := sha256.New()
	_, _ = h.Write([]byte("go-xrpl/flatstate/commit/v1\x00"))
	_, _ = h.Write(ledger[:])
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(publicationKind))
	var result CommitID
	copy(result[:], h.Sum(nil))
	return result
}

// Options bounds Pebble's execution-state resources and transition batches.
// Zero values select conservative defaults.
type Options struct {
	CacheBytes int64
	// MemTableSize is the requested maximum Pebble memtable size. Open may
	// lower the effective target to reserve part of CacheBytes for blocks.
	MemTableSize int64
	// MemoryBudget optionally accounts for temporary bytes retained by a
	// transition batch. The caller owns the budget's policy and lifecycle.
	MemoryBudget MemoryBudget

	// MaxActiveViews bounds point-in-time snapshots retained by this store.
	MaxActiveViews int

	MaxBatchBytes   int
	MaxBatchRecords int

	OutboxMaxCount int
	OutboxMaxBytes int64
	OutboxMaxAge   time.Duration

	// Clock is used for outbox age admission and record timestamps.  It is
	// primarily useful for deterministic service tests.
	Clock func() time.Time
	// Sync is an optional durability hook.  The default appends a synced WAL
	// record.  A hook is called only after the pending Pebble writes have been
	// committed with NoSync and may return an uncertainty/error; callers must
	// then recover before the base is made available.
	Sync func(*pebble.DB) error
}

func resolveMemTableSize(cacheBytes, requested int64) int64 {
	cacheTarget := cacheBytes / memTableCacheDivisor
	if cacheTarget < minimumMemTableSize {
		cacheTarget = minimumMemTableSize
	}
	if requested < cacheTarget {
		return requested
	}
	return cacheTarget
}

func (o Options) withDefaults() (Options, error) {
	if o.CacheBytes < 0 || o.MemTableSize < 0 || o.MaxActiveViews < 0 || o.MaxBatchBytes < 0 || o.MaxBatchRecords < 0 ||
		o.OutboxMaxCount < 0 || o.OutboxMaxBytes < 0 || o.OutboxMaxAge < 0 {
		return Options{}, fmt.Errorf("%w: negative option", ErrLimit)
	}
	if o.CacheBytes == 0 {
		o.CacheBytes = defaultCacheBytes
	}
	if o.MemTableSize == 0 {
		o.MemTableSize = defaultMemTableSize
	}
	if o.MaxBatchBytes == 0 {
		o.MaxBatchBytes = defaultBatchBytes
	}
	if o.MaxBatchRecords == 0 {
		o.MaxBatchRecords = defaultBatchRecords
	}
	if o.OutboxMaxCount == 0 {
		o.OutboxMaxCount = defaultOutboxCount
	}
	if o.OutboxMaxBytes == 0 {
		o.OutboxMaxBytes = defaultOutboxBytes
	}
	if o.OutboxMaxAge == 0 {
		o.OutboxMaxAge = defaultOutboxAge
	}
	if o.MaxActiveViews == 0 {
		o.MaxActiveViews = defaultMaxActiveViews
	}
	if o.MaxBatchBytes < 1 || o.MaxBatchRecords < 1 || o.OutboxMaxCount < 1 || o.OutboxMaxBytes < 1 || o.MemTableSize < 1 || o.MaxActiveViews < 1 {
		return Options{}, fmt.Errorf("%w: limits must be positive", ErrLimit)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o, nil
}

// Store is one process-exclusive durable flat state base.
type Store struct {
	mu sync.Mutex
	cv *sync.Cond

	cacheMu     sync.Mutex
	cachedViews []*cachedView

	db    *pebble.DB
	cache *pebble.Cache
	path  string
	opts  Options

	effectiveMemTableSize int64

	closed        bool
	closing       bool
	ready         bool
	uncertain     bool
	writerActive  bool
	writerEpoch   uint64
	marker        *advanceMarker
	descriptor    Descriptor
	hasDescriptor bool
	activeViews   map[*View]struct{}
	activeReads   int
	outboxCount   int
	outboxBytes   int64
	outboxOldest  time.Time
	failures      uint64

	writeMu sync.Mutex
}

const cachedViewLimit = 2

type cachedView struct {
	view       *View
	descriptor Descriptor
	borrows    int
	retired    bool
}

// ViewLease keeps a bounded execution view alive for one operation.  The
// underlying view is owned by Store and must be released after the operation.
type ViewLease struct {
	store *Store
	entry *cachedView
	once  sync.Once
}

// View returns the point-in-time view held by the lease.
func (l *ViewLease) View() *View {
	if l == nil || l.entry == nil {
		return nil
	}
	return l.entry.view
}

// Close releases the lease.  It is safe to call Close more than once.
func (l *ViewLease) Close() {
	if l == nil || l.store == nil || l.entry == nil {
		return
	}
	l.once.Do(func() { l.store.releaseCachedView(l.entry) })
}

// Open opens or creates a flat state database.  A marker found during open
// suppresses new views until Resume is called with an explicit root
// authorization and deterministic change iterator.
func Open(path string, options Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("flatstate: empty path")
	}
	resolved, err := options.withDefaults()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("flatstate: create %s: %w", path, err)
	}
	cache := pebble.NewCache(resolved.CacheBytes)
	effectiveMemTableSize := resolveMemTableSize(resolved.CacheBytes, resolved.MemTableSize)
	pebbleOptions := &pebble.Options{
		Cache:                       cache,
		MemTableSize:                uint64(effectiveMemTableSize),
		MemTableStopWritesThreshold: 4,
		MaxOpenFiles:                128,
	}
	db, err := pebble.Open(path, pebbleOptions)
	if err != nil {
		cache.Unref()
		return nil, fmt.Errorf("flatstate: open %s: %w", path, err)
	}
	s := &Store{
		db:                    db,
		cache:                 cache,
		path:                  path,
		opts:                  resolved,
		effectiveMemTableSize: effectiveMemTableSize,
		ready:                 true,
		activeViews:           make(map[*View]struct{}),
	}
	s.cv = sync.NewCond(&s.mu)
	if err := s.loadMetadata(); err != nil {
		_ = db.Close()
		cache.Unref()
		return nil, err
	}
	s.refreshOutboxStats()
	return s, nil
}

// Stats returns the cached operational counters for the flat base. It does not
// inspect Pebble or perform any other storage I/O, so it is safe for metrics
// collection callbacks.
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	age := time.Duration(0)
	if !s.outboxOldest.IsZero() {
		age = s.opts.Clock().Sub(s.outboxOldest)
		if age < 0 {
			age = 0
		}
	}
	return Stats{
		MemTableTargetBytes: s.effectiveMemTableSize,
		OutboxCount:         s.outboxCount,
		OutboxBytes:         s.outboxBytes,
		OutboxAge:           age,
		Advancing:           s.writerActive || s.marker != nil,
		Failures:            s.failures,
	}
}

func (s *Store) loadMetadata() error {
	current, closer, err := s.db.Get([]byte(metaCurrent))
	if err == nil {
		defer closer.Close()
		descriptor, decodeErr := decodeDescriptor(current)
		if decodeErr != nil {
			return fmt.Errorf("%w: current descriptor: %v", ErrCorrupt, decodeErr)
		}
		s.descriptor, s.hasDescriptor = descriptor, true
	} else if !errors.Is(err, pebble.ErrNotFound) {
		return fmt.Errorf("flatstate: read current descriptor: %w", err)
	}
	markerBytes, closer, err := s.db.Get([]byte(metaAdvance))
	if err == nil {
		defer closer.Close()
		if len(markerBytes) > maxMarkerBytes {
			return fmt.Errorf("%w: advancing marker too large", ErrCorrupt)
		}
		marker, decodeErr := decodeAdvanceMarker(markerBytes)
		if decodeErr != nil {
			return fmt.Errorf("%w: advancing marker: %v", ErrCorrupt, decodeErr)
		}
		s.marker = &marker
		s.ready = false
	} else if errors.Is(err, pebble.ErrNotFound) {
		s.ready = true
	} else {
		return fmt.Errorf("flatstate: read advancing marker: %w", err)
	}
	return nil
}

// Current returns the ready descriptor.  The bool is false before the first
// import.  A marker or uncertain sync makes the base unavailable.
func (s *Store) Current() (Descriptor, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return Descriptor{}, false, ErrClosed
	}
	if !s.ready || s.uncertain {
		return Descriptor{}, false, ErrUnavailable
	}
	return s.descriptor, s.hasDescriptor, nil
}

// Marker returns a copy of the durable advancing marker, if one is present.
// It is useful to a recovery coordinator that is preparing a root-backed
// replay.  Marker visibility does not make the flat base readable.
func (s *Store) Marker() (AdvanceMarker, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return AdvanceMarker{}, false, ErrClosed
	}
	if s.marker == nil {
		return AdvanceMarker{}, false, nil
	}
	return s.marker.public(), true, nil
}

// OpenView captures a descriptor and Pebble snapshot under the short
// readiness guard.  Metadata is verified after releasing the guard so no
// storage read or callback runs under the publication lock.
func (s *Store) OpenView(ctx context.Context) (*View, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if !s.ready || s.uncertain || s.marker != nil || s.writerActive {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	if len(s.activeViews) >= s.opts.MaxActiveViews {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: active view limit", ErrLimit)
	}
	expected := s.descriptor
	hasExpected := s.hasDescriptor
	snapshot := s.db.NewSnapshot()
	view := &View{store: s, snapshot: snapshot, descriptor: expected, hasDescriptor: hasExpected}
	view.cond = sync.NewCond(&view.mu)
	view.active = 1 // metadata validation is part of the captured lease
	s.activeViews[view] = struct{}{}
	s.mu.Unlock()
	closeInvalid := func(err error) (*View, error) {
		view.end()
		_ = view.Close()
		return nil, err
	}

	actualBytes, closer, err := snapshot.Get([]byte(metaCurrent))
	if err == nil {
		actual, decodeErr := decodeDescriptor(actualBytes)
		_ = closer.Close()
		if decodeErr != nil || !hasExpected || !actual.Equal(expected) {
			if decodeErr != nil {
				return closeInvalid(fmt.Errorf("%w: snapshot descriptor: %v", ErrCorrupt, decodeErr))
			}
			return closeInvalid(ErrUnavailable)
		}
	} else if errors.Is(err, pebble.ErrNotFound) {
		if hasExpected {
			return closeInvalid(ErrUnavailable)
		}
	} else {
		return closeInvalid(err)
	}
	if _, markerCloser, markerErr := snapshot.Get([]byte(metaAdvance)); markerErr == nil {
		_ = markerCloser.Close()
		return closeInvalid(ErrUnavailable)
	} else if !errors.Is(markerErr, pebble.ErrNotFound) {
		return closeInvalid(markerErr)
	}
	view.end()
	return view, nil
}

// BorrowView returns a lease on a bounded cached view for descriptor.  Cached
// views are exact-descriptor views and are retired at the start of every
// publication transition.  When the two-entry cache is occupied, callers can
// fall back to their authenticated root-backed view.
func (s *Store) BorrowView(ctx context.Context, descriptor Descriptor) (*ViewLease, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	s.mu.Lock()
	if err := s.cachedViewStateLocked(descriptor); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	for _, entry := range s.cachedViews {
		if entry.retired || !entry.descriptor.Equal(descriptor) {
			continue
		}
		s.mu.Unlock()
		if err := entry.view.begin(); err != nil {
			s.mu.Lock()
			s.removeCachedViewLocked(entry)
			s.mu.Unlock()
			return nil, err
		}
		s.mu.Lock()
		if err := s.cachedViewStateLocked(descriptor); err != nil || entry.retired {
			if err == nil {
				err = ErrUnavailable
			}
			s.mu.Unlock()
			entry.view.end()
			return nil, err
		}
		entry.borrows++
		s.mu.Unlock()
		return &ViewLease{store: s, entry: entry}, nil
	}
	if len(s.cachedViews) >= cachedViewLimit {
		s.mu.Unlock()
		return nil, ErrLimit
	}
	s.mu.Unlock()

	view, err := s.OpenView(ctx)
	if err != nil {
		return nil, err
	}
	if err := view.begin(); err != nil {
		_ = view.Close()
		return nil, err
	}
	s.mu.Lock()
	if err := s.cachedViewStateLocked(descriptor); err != nil || !view.hasDescriptor || !view.descriptor.Equal(descriptor) {
		if err == nil {
			err = ErrUnavailable
		}
		s.mu.Unlock()
		view.end()
		_ = view.Close()
		return nil, err
	}
	entry := &cachedView{view: view, descriptor: descriptor, borrows: 1}
	s.cachedViews = append(s.cachedViews, entry)
	s.mu.Unlock()
	return &ViewLease{store: s, entry: entry}, nil
}

func (s *Store) cachedViewStateLocked(descriptor Descriptor) error {
	if s.closed || s.closing {
		return ErrClosed
	}
	if !s.ready || s.uncertain || s.marker != nil || s.writerActive || !s.hasDescriptor || !s.descriptor.Equal(descriptor) {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) removeCachedViewLocked(target *cachedView) {
	for index, entry := range s.cachedViews {
		if entry != target {
			continue
		}
		copy(s.cachedViews[index:], s.cachedViews[index+1:])
		s.cachedViews[len(s.cachedViews)-1] = nil
		s.cachedViews = s.cachedViews[:len(s.cachedViews)-1]
		return
	}
}

func (s *Store) releaseCachedView(entry *cachedView) {
	s.cacheMu.Lock()
	s.mu.Lock()
	closeView := false
	if entry.borrows > 0 {
		entry.borrows--
		closeView = entry.borrows == 0 && entry.retired
		if closeView {
			s.removeCachedViewLocked(entry)
		}
	}
	s.mu.Unlock()
	s.cacheMu.Unlock()

	entry.view.end()
	if closeView {
		_ = entry.view.Close()
	}
}

// Advance applies a sorted delta in bounded batches and publishes Target in
// one final synced batch.  A marker remains durable after any failure after
// marker sync, making Resume the only path to readiness.
func (s *Store) Advance(ctx context.Context, request AdvanceRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if request.Changes == nil {
		return errors.New("flatstate: nil change iterator")
	}
	if request.PublicationKind == "" {
		request.PublicationKind = "flat-state"
	}
	err := s.advance(ctx, request.Expected, request.Target, request.Authorization, request.PublicationKind, request.Changes, false, false)
	if err != nil {
		s.recordFailure()
	}
	return err
}

// Rebase applies a bounded, fully authorized source-to-target diff when the
// selected ledger is on a different lineage or skips a flat-state version.
// Ordinary Advance remains strict about direct descriptor successors.
func (s *Store) Rebase(ctx context.Context, request RebaseRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if request.Expected == nil {
		return ErrParentMismatch
	}
	if request.Changes == nil {
		return errors.New("flatstate: nil change iterator")
	}
	if request.PublicationKind == "" {
		request.PublicationKind = "flat-state"
	}
	err := s.advance(ctx, request.Expected, request.Target, request.Authorization, request.PublicationKind, request.Changes, false, true)
	if err != nil {
		s.recordFailure()
	}
	return err
}

// Resume replays the marker's source-to-target delta from the beginning.  The
// caller must supply fresh explicit root authorization; roots in the marker
// are identities, not evidence.
func (s *Store) Resume(ctx context.Context, request ResumeRequest) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if request.Changes == nil {
		return errors.New("flatstate: nil recovery change iterator")
	}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.marker == nil {
		s.mu.Unlock()
		if s.uncertain {
			return ErrUnavailable
		}
		return nil
	}
	marker := *s.marker
	s.mu.Unlock()
	intent := marker.Intent
	if request.PublicationKind != "" {
		if request.PublicationKind != intent {
			return fmt.Errorf("%w: recovery publication intent mismatch", ErrParentMismatch)
		}
	}
	err := s.advance(ctx, &marker.Source, marker.Target, request.Authorization, intent, request.Changes, true, false)
	if err != nil {
		s.recordFailure()
	}
	return err
}

func (s *Store) advance(ctx context.Context, expected *Descriptor, target Descriptor, authorization RootClosureAuthorization, intent string, changes ChangeIterator, resuming, rebase bool) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if len(intent) > maxIntentBytes {
		return fmt.Errorf("%w: publication intent is too large", ErrLimit)
	}
	if err := authorization.validate(zeroDescriptor(expected), target); err != nil {
		return err
	}

	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.writerActive {
		s.mu.Unlock()
		return ErrWriterBusy
	}
	if resuming {
		if s.marker == nil || !s.marker.Source.Equal(zeroDescriptor(expected)) || !s.marker.Target.Equal(target) ||
			(s.marker.Source.IsZero() && s.hasDescriptor) || (!s.marker.Source.IsZero() && (!s.hasDescriptor || !s.descriptor.Equal(s.marker.Source))) {
			s.mu.Unlock()
			return ErrParentMismatch
		}
	} else {
		if !s.ready || s.uncertain || s.marker != nil {
			s.mu.Unlock()
			return ErrUnavailable
		}
		if expected == nil {
			if s.hasDescriptor {
				s.mu.Unlock()
				return ErrParentMismatch
			}
		} else if !s.hasDescriptor || !s.descriptor.Equal(*expected) {
			s.mu.Unlock()
			return ErrParentMismatch
		}
		if !rebase && s.hasDescriptor && target.ParentHash != s.descriptor.LedgerHash {
			s.mu.Unlock()
			return ErrParentMismatch
		}
	}
	if !resuming {
		// Capacity inspection is storage I/O.  Keep it outside the readiness
		// guard while retaining a close lease, then revalidate the state before
		// reserving the writer.
		s.activeReads++
		s.mu.Unlock()
		capacityErr := s.checkOutboxCapacity(target, intent)
		s.mu.Lock()
		s.activeReads--
		s.cv.Broadcast()
		if capacityErr != nil {
			s.mu.Unlock()
			return capacityErr
		}
		if s.closed || s.closing || !s.ready || s.uncertain || s.marker != nil || s.writerActive {
			s.mu.Unlock()
			return ErrUnavailable
		}
	}
	s.writerActive = true
	s.ready = false
	s.writerEpoch++
	epoch := s.writerEpoch
	s.mu.Unlock()
	s.retireCachedViews()

	markerWritten := resuming
	beforeFlush := func() error {
		if markerWritten {
			return nil
		}
		marker := newAdvanceMarker(expected, target, intent, epoch)
		if err := s.writeMarker(ctx, epoch, marker); err != nil {
			return err
		}
		s.mu.Lock()
		s.marker = &marker
		s.mu.Unlock()
		markerWritten = true
		return nil
	}

	if err := s.applyChanges(ctx, epoch, changes, beforeFlush, func(batch *pebble.Batch) error {
		return s.finalize(ctx, epoch, target, intent, batch)
	}); err != nil {
		if errors.Is(err, errSyncUncertain) {
			s.markUncertain(epoch)
		} else {
			s.finishWriter(epoch, false, false, Descriptor{})
		}
		return err
	}
	s.finishWriter(epoch, true, true, target)
	return nil
}

func (s *Store) retireCachedViews() {
	s.cacheMu.Lock()
	s.mu.Lock()
	views := make([]*View, 0, len(s.cachedViews))
	kept := s.cachedViews[:0]
	for _, entry := range s.cachedViews {
		entry.retired = true
		if entry.borrows == 0 {
			views = append(views, entry.view)
			continue
		}
		kept = append(kept, entry)
	}
	s.cachedViews = kept
	s.mu.Unlock()
	s.cacheMu.Unlock()
	for _, view := range views {
		_ = view.Close()
	}
}

func zeroDescriptor(descriptor *Descriptor) Descriptor {
	if descriptor == nil {
		return Descriptor{}
	}
	return *descriptor
}

func (s *Store) writeMarker(ctx context.Context, epoch uint64, marker advanceMarker) error {
	if err := s.checkWriter(ctx, epoch); err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	encoded := encodeAdvanceMarker(marker)
	if err := batch.Set([]byte(metaAdvance), encoded, nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("%w: marker commit: %v", errSyncUncertain, err)
	}
	if err := s.sync(); err != nil {
		return fmt.Errorf("%w: marker sync: %v", errSyncUncertain, err)
	}
	return nil
}

func (s *Store) applyChanges(ctx context.Context, epoch uint64, changes ChangeIterator, beforeFlush func() error, finalize func(*pebble.Batch) error) error {
	defer changes.Close()
	batch := s.db.NewBatch()
	defer batch.Close()
	batchBytes := uint64(0)
	reservedBytes := uint64(0)
	releaseReserved := func() {
		if s.opts.MemoryBudget != nil && reservedBytes != 0 {
			s.opts.MemoryBudget.Release(reservedBytes)
			reservedBytes = 0
		}
	}
	defer releaseReserved()
	batchRecords := 0
	var previous Hash
	havePrevious := false
	flush := func() error {
		if batchRecords == 0 {
			return nil
		}
		if err := s.checkWriter(ctx, epoch); err != nil {
			return err
		}
		if err := beforeFlush(); err != nil {
			return err
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			return err
		}
		// The durable marker fences these chunks until final publication syncs
		// all preceding WAL records. Interrupted transitions replay from zero.
		releaseReserved()
		batch.Reset()
		batchBytes = 0
		batchRecords = 0
		return nil
	}
	reserve := func(bytes uint64) error {
		if s.opts.MemoryBudget == nil {
			return nil
		}
		if s.opts.MemoryBudget.TryReserve(bytes) {
			reservedBytes += bytes
			return nil
		}
		// A full local batch may be released before retrying admission. This
		// keeps the transition bounded by the shared account without waiting
		// while holding the writer lease.
		if batchRecords > 0 {
			if err := flush(); err != nil {
				return err
			}
			if s.opts.MemoryBudget.TryReserve(bytes) {
				reservedBytes += bytes
				return nil
			}
		}
		return fmt.Errorf("%w: transition record requires %d bytes", ErrLimit, bytes)
	}
	for {
		if err := contextErr(ctx); err != nil {
			return err
		}
		change, ok, err := changes.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return finalize(batch)
		}
		if havePrevious && bytes.Compare(previous[:], change.Key[:]) >= 0 {
			return ErrInvalidChangeOrder
		}
		previous, havePrevious = change.Key, true
		key := objectKey(change.Key)
		recordBytes := uint64(len(key)) + uint64(len(change.Value))
		if change.Delete {
			recordBytes = uint64(len(key))
		}
		if batchRecords > 0 && (batchRecords >= s.opts.MaxBatchRecords || batchBytes+recordBytes > uint64(s.opts.MaxBatchBytes)) {
			if err := flush(); err != nil {
				return err
			}
		}
		if err := reserve(recordBytes); err != nil {
			return err
		}
		if err := s.checkWriter(ctx, epoch); err != nil {
			return err
		}
		if change.Delete {
			if err := batch.Delete(key, nil); err != nil {
				return err
			}
		} else {
			if err := batch.Set(key, change.Value, nil); err != nil {
				return err
			}
		}
		batchBytes += recordBytes
		batchRecords++
		// A valid value larger than the regular batch ceiling gets a one-record
		// streaming batch.  It is admitted explicitly rather than silently
		// changing protocol semantics or buffering a complete transition.
		if batchRecords == 1 && batchBytes > uint64(s.opts.MaxBatchBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

func (s *Store) finalize(ctx context.Context, epoch uint64, target Descriptor, intent string, batch *pebble.Batch) error {
	if err := s.checkWriter(ctx, epoch); err != nil {
		return err
	}
	commitID := SemanticCommitID(target.LedgerHash, intent)
	record := OutboxRecord{CommitID: commitID, Descriptor: target, Intent: intent, CreatedAt: s.opts.Clock().UTC()}
	encodedOutbox, err := encodeOutbox(record)
	if err != nil {
		return err
	}
	if existing, found, getErr := s.getOutbox(commitID); getErr != nil {
		return getErr
	} else if found {
		if !existing.Descriptor.samePublicationVersion(target) || existing.Intent != intent {
			return fmt.Errorf("%w: conflicting outbox commit %x", ErrCorrupt, commitID)
		}
		encodedOutbox = encodeOutboxUnchecked(existing)
	}
	if err := batch.Set([]byte(metaCurrent), encodeDescriptor(target), nil); err != nil {
		return err
	}
	if err := batch.Set(outboxKey(commitID), encodedOutbox, nil); err != nil {
		return err
	}
	if err := batch.Delete([]byte(metaAdvance), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("%w: publication commit: %v", errSyncUncertain, err)
	}
	if err := s.sync(); err != nil {
		return fmt.Errorf("%w: final sync: %v", errSyncUncertain, err)
	}
	s.refreshOutboxStats()
	return nil
}

func (s *Store) checkWriter(ctx context.Context, epoch uint64) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return ErrClosed
	}
	if !s.writerActive || s.writerEpoch != epoch {
		return ErrWriterFenced
	}
	return nil
}

func (s *Store) markUncertain(epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writerEpoch == epoch {
		s.uncertain = true
		s.ready = false
		s.writerActive = false
		s.cv.Broadcast()
	}
}

func (s *Store) finishWriter(epoch uint64, ready, clearMarker bool, target Descriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writerEpoch != epoch {
		return
	}
	s.writerActive = false
	if ready && clearMarker {
		s.ready = true
		s.uncertain = false
		s.marker = nil
		s.descriptor = target
		s.hasDescriptor = true
	} else {
		// Without a marker or uncertain commit, no batch reached storage.
		s.ready = s.marker == nil && !s.uncertain
	}
	s.cv.Broadcast()
}

func (s *Store) sync() error {
	if s.opts.Sync != nil {
		return s.opts.Sync(s.db)
	}
	return s.db.LogData(nil, pebble.Sync)
}

func (s *Store) checkOutboxCapacity(target Descriptor, intent string) error {
	commitID := SemanticCommitID(target.LedgerHash, intent)
	existing, found, err := s.getOutbox(commitID)
	if err != nil {
		return err
	}
	if found {
		if !existing.Descriptor.samePublicationVersion(target) || existing.Intent != intent {
			return fmt.Errorf("%w: conflicting outbox commit %x", ErrCorrupt, commitID)
		}
		return nil
	}
	count, bytesUsed, oldest, err := s.outboxStats()
	if err != nil {
		return err
	}
	encoded, err := encodeOutbox(OutboxRecord{CommitID: commitID, Descriptor: target, Intent: intent, CreatedAt: s.opts.Clock().UTC()})
	if err != nil {
		return err
	}
	if count >= s.opts.OutboxMaxCount || bytesUsed > s.opts.OutboxMaxBytes-int64(len(encoded)) {
		return fmt.Errorf("%w: outbox count or bytes", ErrLimit)
	}
	if !oldest.IsZero() && s.opts.OutboxMaxAge > 0 && s.opts.Clock().Sub(oldest) > s.opts.OutboxMaxAge {
		return fmt.Errorf("%w: outbox oldest age", ErrLimit)
	}
	return nil
}

// PendingOutbox lists at most limit publication records in commit-ID order.
// A non-positive limit uses the configured count ceiling.
func (s *Store) PendingOutbox(ctx context.Context, limit int) ([]OutboxRecord, error) {
	if limit <= 0 {
		limit = s.opts.OutboxMaxCount
	}
	if limit > s.opts.OutboxMaxCount {
		limit = s.opts.OutboxMaxCount
	}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	snapshot := s.db.NewSnapshot()
	s.activeReads++
	s.mu.Unlock()
	iterator, err := snapshot.NewIter(&pebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: prefixUpperBound([]byte(outboxPrefix))})
	if err != nil {
		_ = snapshot.Close()
		s.mu.Lock()
		s.activeReads--
		s.cv.Broadcast()
		s.mu.Unlock()
		return nil, err
	}
	defer func() {
		_ = iterator.Close()
		_ = snapshot.Close()
		s.mu.Lock()
		s.activeReads--
		s.cv.Broadcast()
		s.mu.Unlock()
	}()
	result := make([]OutboxRecord, 0, minInt(limit, 16))
	for iterator.First(); iterator.Valid(); iterator.Next() {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if len(result) >= limit {
			break
		}
		value, err := iterator.ValueAndErr()
		if err != nil {
			return nil, err
		}
		record, err := decodeOutbox(value)
		if err != nil {
			return nil, fmt.Errorf("%w: outbox: %v", ErrCorrupt, err)
		}
		result = append(result, record)
	}
	return result, iterator.Error()
}

// AckOutbox durably removes one publication obligation.  A missing ID is an
// idempotent success.
func (s *Store) AckOutbox(ctx context.Context, commitID CommitID) (err error) {
	defer func() {
		if err != nil {
			s.recordFailure()
		}
	}()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.writerActive {
		s.mu.Unlock()
		return ErrWriterBusy
	}
	s.activeReads++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeReads--
		s.cv.Broadcast()
		s.mu.Unlock()
	}()
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Delete(outboxKey(commitID), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.NoSync); err != nil {
		return err
	}
	if err := s.sync(); err != nil {
		s.mu.Lock()
		s.uncertain = true
		s.ready = false
		s.mu.Unlock()
		return fmt.Errorf("%w: outbox ack sync: %v", errSyncUncertain, err)
	}
	s.refreshOutboxStats()
	return nil
}

func (s *Store) recordFailure() {
	s.mu.Lock()
	s.failures++
	s.mu.Unlock()
}

func (s *Store) refreshOutboxStats() {
	count, bytesUsed, oldest, err := s.outboxStats()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.outboxCount = count
	s.outboxBytes = bytesUsed
	s.outboxOldest = oldest
	s.mu.Unlock()
}

// Close closes all open views after their in-flight operations finish, then
// closes Pebble.  New work is rejected once closing starts.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.closing {
		for !s.closed {
			s.cv.Wait()
		}
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	s.mu.Unlock()

	s.cacheMu.Lock()
	s.mu.Lock()
	for _, entry := range s.cachedViews {
		entry.retired = true
	}
	views := make([]*View, 0, len(s.activeViews))
	for view := range s.activeViews {
		views = append(views, view)
	}
	s.mu.Unlock()
	s.cacheMu.Unlock()
	for _, view := range views {
		_ = view.Close()
	}
	s.mu.Lock()
	for s.writerActive || s.activeReads != 0 || len(s.activeViews) != 0 {
		s.cv.Wait()
	}
	s.closed = true
	db, cache := s.db, s.cache
	s.cachedViews = nil
	s.cv.Broadcast()
	s.mu.Unlock()
	closeErr := db.Close()
	cache.Unref()
	return closeErr
}

// View is a stable point-in-time read lease.  Close is idempotent.
type View struct {
	store         *Store
	snapshot      *pebble.Snapshot
	descriptor    Descriptor
	hasDescriptor bool

	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
	active int
}

func (v *View) begin() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrClosed
	}
	v.active++
	return nil
}

func (v *View) end() {
	v.mu.Lock()
	v.active--
	if v.active == 0 {
		v.cond.Broadcast()
	}
	v.mu.Unlock()
}

// Descriptor returns the exact captured descriptor and whether the view is
// backed by an imported ledger.  An empty initial base has no descriptor.
func (v *View) Descriptor() (Descriptor, bool, error) {
	if err := v.begin(); err != nil {
		return Descriptor{}, false, err
	}
	defer v.end()
	return v.descriptor, v.hasDescriptor, nil
}

// Get returns a caller-owned canonical value.  A missing object returns
// found=false with nil error.
func (v *View) Get(key Hash) ([]byte, bool, error) {
	if err := v.begin(); err != nil {
		return nil, false, err
	}
	defer v.end()
	value, closer, err := v.snapshot.Get(objectKey(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), true, nil
}

// Succ returns the smallest visible key strictly greater than after.
func (v *View) Succ(after Hash) (Hash, []byte, bool, error) {
	if err := v.begin(); err != nil {
		return Hash{}, nil, false, err
	}
	defer v.end()
	iterator, err := v.snapshot.NewIter(&pebble.IterOptions{LowerBound: []byte(objectPrefix), UpperBound: prefixUpperBound([]byte(objectPrefix))})
	if err != nil {
		return Hash{}, nil, false, err
	}
	defer iterator.Close()
	seek := objectKey(after)
	iterator.SeekGE(seek)
	if iterator.Valid() && bytes.Equal(iterator.Key(), seek) {
		iterator.Next()
	}
	if !iterator.Valid() {
		return Hash{}, nil, false, iterator.Error()
	}
	key, ok := decodeObjectKey(iterator.Key())
	if !ok {
		return Hash{}, nil, false, fmt.Errorf("%w: malformed object key", ErrCorrupt)
	}
	value, err := iterator.ValueAndErr()
	if err != nil {
		return Hash{}, nil, false, err
	}
	return key, append([]byte(nil), value...), true, nil
}

// ForEach iterates all objects in ascending ledger-key order.  Returning
// false stops successfully.  Values passed to the callback are caller-owned.
func (v *View) ForEach(ctx context.Context, fn func(Hash, []byte) bool) error {
	return v.iterate(ctx, nil, fn)
}

// ForEachFrom iterates keys strictly greater than after.
func (v *View) ForEachFrom(ctx context.Context, after Hash, fn func(Hash, []byte) bool) error {
	return v.iterate(ctx, &after, fn)
}

func (v *View) iterate(ctx context.Context, after *Hash, fn func(Hash, []byte) bool) error {
	if fn == nil {
		return errors.New("flatstate: nil iteration callback")
	}
	if err := v.begin(); err != nil {
		return err
	}
	defer v.end()
	iterator, err := v.snapshot.NewIter(&pebble.IterOptions{LowerBound: []byte(objectPrefix), UpperBound: prefixUpperBound([]byte(objectPrefix))})
	if err != nil {
		return err
	}
	defer iterator.Close()
	if after == nil {
		iterator.First()
	} else {
		iterator.SeekGE(objectKey(*after))
		if iterator.Valid() && bytes.Equal(iterator.Key(), objectKey(*after)) {
			iterator.Next()
		}
	}
	for iterator.Valid() {
		if err := contextErr(ctx); err != nil {
			return err
		}
		key, ok := decodeObjectKey(iterator.Key())
		if !ok {
			return fmt.Errorf("%w: malformed object key", ErrCorrupt)
		}
		value, err := iterator.ValueAndErr()
		if err != nil {
			return err
		}
		if !fn(key, append([]byte(nil), value...)) {
			return nil
		}
		iterator.Next()
	}
	return iterator.Error()
}

// Close releases the view's snapshot after in-flight operations finish.
func (v *View) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	for v.active != 0 {
		v.cond.Wait()
	}
	err := v.snapshot.Close()
	v.mu.Unlock()
	v.store.mu.Lock()
	delete(v.store.activeViews, v)
	v.store.cv.Broadcast()
	v.store.mu.Unlock()
	return err
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (s *Store) getOutbox(commitID CommitID) (OutboxRecord, bool, error) {
	value, closer, err := s.db.Get(outboxKey(commitID))
	if errors.Is(err, pebble.ErrNotFound) {
		return OutboxRecord{}, false, nil
	}
	if err != nil {
		return OutboxRecord{}, false, err
	}
	defer closer.Close()
	record, err := decodeOutbox(value)
	if err != nil {
		return OutboxRecord{}, false, fmt.Errorf("%w: outbox: %v", ErrCorrupt, err)
	}
	return record, true, nil
}

func (s *Store) outboxStats() (count int, bytesUsed int64, oldest time.Time, err error) {
	iterator, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: prefixUpperBound([]byte(outboxPrefix))})
	if err != nil {
		return 0, 0, time.Time{}, err
	}
	defer iterator.Close()
	for iterator.First(); iterator.Valid(); iterator.Next() {
		if count >= s.opts.OutboxMaxCount || bytesUsed > s.opts.OutboxMaxBytes {
			return count, bytesUsed, oldest, nil
		}
		value, valueErr := iterator.ValueAndErr()
		if valueErr != nil {
			return 0, 0, time.Time{}, valueErr
		}
		record, decodeErr := decodeOutbox(value)
		if decodeErr != nil {
			return 0, 0, time.Time{}, fmt.Errorf("%w: outbox: %v", ErrCorrupt, decodeErr)
		}
		count++
		bytesUsed += int64(len(iterator.Key()) + len(value))
		if oldest.IsZero() || record.CreatedAt.Before(oldest) {
			oldest = record.CreatedAt
		}
	}
	if iterErr := iterator.Error(); iterErr != nil {
		return 0, 0, time.Time{}, iterErr
	}
	return count, bytesUsed, oldest, nil
}

func objectKey(key Hash) []byte {
	result := make([]byte, len(objectPrefix)+len(key))
	copy(result, objectPrefix)
	copy(result[len(objectPrefix):], key[:])
	return result
}

func decodeObjectKey(key []byte) (Hash, bool) {
	if len(key) != len(objectPrefix)+len(Hash{}) || !bytes.HasPrefix(key, []byte(objectPrefix)) {
		return Hash{}, false
	}
	var result Hash
	copy(result[:], key[len(objectPrefix):])
	return result, true
}

func outboxKey(id CommitID) []byte {
	result := make([]byte, len(outboxPrefix)+len(id))
	copy(result, outboxPrefix)
	copy(result[len(outboxPrefix):], id[:])
	return result
}

func prefixUpperBound(prefix []byte) []byte {
	upper := append([]byte(nil), prefix...)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper[:i+1]
		}
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var errSyncUncertain = errors.New("flatstate: uncertain durability")

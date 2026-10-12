package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/flatstate"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/rootlease"
	"github.com/LeJamon/go-xrpl/internal/ledger/skiplist"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
)

type ExecutionConfig struct {
	Family        shamap.Family
	Overlay       overlay.Config
	Workspace     shamap.DirtyMemoryBudget
	RootMemory    *membudget.Account
	MaxRootLeases int
	Flat          *flatstate.Store
}

// ExecutionStore is shared by rooted ledger views. Mutable forks own their
// overlays; sealed views retain no decoded SHAMap nodes.
type ExecutionStore struct {
	config ExecutionConfig
	roots  *rootlease.Registry
	stats  executionStatsCounters
}

type executionStatsCounters struct {
	builds        atomic.Uint64
	buildErrors   atomic.Uint64
	buildNodes    atomic.Uint64
	buildLeaves   atomic.Uint64
	buildBytes    atomic.Uint64
	buildNanos    atomic.Uint64
	maxFrontier   atomic.Uint64
	flatHits      atomic.Uint64
	rootFallbacks atomic.Uint64
}

// ExecutionStats reports bounded execution work and read-path counters. The
// counters are process-local diagnostics and intentionally carry no ledger
// hashes or object keys.
type ExecutionStats struct {
	Builds           uint64
	BuildErrors      uint64
	BuildLeaves      uint64
	BuildBytes       uint64
	RootBorrowers    uint64
	RootBuildTime    time.Duration
	RootNodes        uint64
	Frontier         uint64
	FlatHits         uint64
	FlatFallbacks    uint64
	DistinctRootKeys uint64
}

func (s *ExecutionStore) Stats() ExecutionStats {
	rootStats := s.roots.Stats()
	return ExecutionStats{
		Builds:           s.stats.builds.Load(),
		BuildErrors:      s.stats.buildErrors.Load(),
		BuildLeaves:      s.stats.buildLeaves.Load(),
		BuildBytes:       s.stats.buildBytes.Load(),
		RootBorrowers:    uint64(rootStats.Active),
		RootBuildTime:    time.Duration(s.stats.buildNanos.Load()),
		RootNodes:        s.stats.buildNodes.Load(),
		Frontier:         s.stats.maxFrontier.Load(),
		FlatHits:         s.stats.flatHits.Load(),
		FlatFallbacks:    s.stats.rootFallbacks.Load(),
		DistinctRootKeys: uint64(rootStats.Distinct),
	}
}

func updateExecutionMax(dst *atomic.Uint64, value uint64) {
	for {
		current := dst.Load()
		if value <= current || dst.CompareAndSwap(current, value) {
			return
		}
	}
}

func NewExecutionStore(config ExecutionConfig) (*ExecutionStore, error) {
	if config.Family == nil || config.Workspace == nil ||
		(config.Overlay.MemoryBudget == nil && config.Overlay.MaxMemoryBytes == 0) {
		return nil, errors.New("bounded execution requires a family and memory admission")
	}
	if config.MaxRootLeases == 0 {
		config.MaxRootLeases = 4096
	}
	roots, err := rootlease.New(config.RootMemory, config.MaxRootLeases)
	if err != nil {
		return nil, err
	}
	return &ExecutionStore{config: config, roots: roots}, nil
}

func (s *ExecutionStore) Roots() []rootlease.Root { return s.roots.Roots() }

func (s *ExecutionStore) AcquireRoot(root rootlease.Root) (func(), error) {
	return s.roots.Acquire(root)
}

// GuardPreservedRoots admits storage retirement when every active lease is in
// the caller's immutable preservation set. The set is read while admitting the
// guard and is not retained.
func (s *ExecutionStore) GuardPreservedRoots(preserved map[rootlease.Root]struct{}) (func(), bool) {
	return s.roots.Guard(preserved)
}

func (s *ExecutionStore) Close() error { return s.roots.Close() }

type executionView struct {
	state        *executionTree
	transactions *executionTree
}

type executionClosureState uint8

const (
	executionClosureUnknown executionClosureState = iota
	executionClosurePending
	executionClosureVerified
)

type executionTree struct {
	store         *ExecutionStore
	kind          shamap.Type
	base          header.LedgerHeader
	root          [32]byte
	sequence      uint32
	changes       *overlay.Overlay
	lease         *rootlease.Lease
	release       func()
	count         uint32
	countKnown    bool
	closure       executionClosureState
	parentClosure bool
	hashMu        sync.Mutex
	cached        [32]byte
	haveHash      bool
	resultLease   *rootlease.Lease
}

type durableExecutionFamily struct {
	shamap.Family
	durable interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}
}

func (f durableExecutionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.durable.FetchDurable(ctx, hash)
}

func (t *executionTree) reader() *shamap.RootReader {
	return shamap.NewRootReader(t.kind, t.root, t.store.config.Family,
		shamap.WithCommitmentBudget(t.store.config.Workspace))
}

func (t *executionTree) flatView(ctx context.Context) (*flatstate.View, error) {
	if t.kind != shamap.TypeState || t.store.config.Flat == nil {
		return nil, nil
	}
	v, err := t.store.config.Flat.OpenView(ctx)
	if err != nil {
		// An advancing or unavailable base is served by the authenticated root.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, nil
	}
	d, ready, err := v.Descriptor()
	if err != nil || !ready || d.LedgerHash != t.base.Hash || d.StateRoot != t.root ||
		d.ParentHash != t.base.ParentHash || d.Sequence != t.base.LedgerIndex || d.TxRoot != t.base.TxHash {
		_ = v.Close()
		return nil, err
	}
	return v, nil
}

type executionBase struct{ tree *executionTree }

type executionRecord struct {
	key   [32]byte
	value []byte
}

type executionCursor struct {
	next func() (executionRecord, error, bool)
	stop func()
}

func (b executionBase) OpenCursor(ctx context.Context, after *[32]byte) (overlay.Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	next, stop := iter.Pull2(func(yield func(executionRecord, error) bool) {
		err := b.Iterate(ctx, after, func(key [32]byte, data []byte) bool {
			return yield(executionRecord{key, data}, nil)
		})
		if err != nil {
			yield(executionRecord{}, err)
		}
	})
	return &executionCursor{next: next, stop: stop}, nil
}

func (c *executionCursor) Next(ctx context.Context) ([32]byte, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return [32]byte{}, nil, false, err
	}
	record, err, ok := c.next()
	return record.key, record.value, ok && err == nil, err
}

func (c *executionCursor) Close() error { c.stop(); return nil }

func (b executionBase) Get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	t := b.tree
	release, err := t.store.roots.Acquire(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		return nil, false, err
	}
	defer release()
	v, err := t.flatView(ctx)
	if err != nil {
		return nil, false, err
	}
	if v != nil {
		t.store.stats.flatHits.Add(1)
		defer v.Close()
		return v.Get(key)
	}
	t.store.stats.rootFallbacks.Add(1)
	item, kind, found, err := t.reader().GetLeaf(ctx, key)
	if err != nil || !found {
		return nil, found, err
	}
	if t.kind == shamap.TypeTransaction {
		return append([]byte{byte(kind)}, item.Data()...), true, nil
	}
	return item.Data(), true, nil
}

func (b executionBase) Iterate(ctx context.Context, after *[32]byte, fn func([32]byte, []byte) bool) error {
	t := b.tree
	release, err := t.store.roots.Acquire(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		return err
	}
	defer release()
	v, err := t.flatView(ctx)
	if err != nil {
		return err
	}
	if v != nil {
		t.store.stats.flatHits.Add(1)
		defer v.Close()
		if after == nil {
			return v.ForEach(ctx, fn)
		}
		return v.ForEachFrom(ctx, *after, fn)
	}
	t.store.stats.rootFallbacks.Add(1)
	return t.reader().IterateLeaves(ctx, after, func(item *shamap.Item, kind shamap.NodeType) bool {
		data := item.Data()
		if t.kind == shamap.TypeTransaction {
			data = append([]byte{byte(kind)}, data...)
		}
		return fn(item.Key(), data)
	})
}

func (t *executionTree) newMutable(sequence uint32) (*executionTree, error) {
	return t.newMutableContext(context.Background(), sequence)
}

func (t *executionTree) newMutableContext(ctx context.Context, sequence uint32) (*executionTree, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, countKnown := t.countSnapshot()
	if t.kind == shamap.TypeTransaction && !countKnown {
		if _, err := t.countLeaves(ctx); err != nil {
			return nil, err
		}
	}
	count, countKnown := t.countSnapshot()
	base := executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root, sequence: t.sequence, count: count, countKnown: countKnown}
	closure := t.closure
	if t.root == ([32]byte{}) && closure == executionClosureUnknown {
		closure = executionClosureVerified
	}
	out := &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root, sequence: sequence, count: count,
		countKnown: countKnown, closure: closure, parentClosure: closure == executionClosureVerified || closure == executionClosurePending}
	lease, err := t.store.roots.AcquireLease(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		return nil, err
	}
	out.lease, out.release = lease, lease.Release
	out.changes, err = overlay.New(executionBase{tree: &base}, t.store.config.Overlay)
	if err != nil {
		lease.Release()
		return nil, err
	}
	return out, nil
}

func (t *executionTree) fork() (*executionTree, error) {
	if t.changes == nil {
		return t.newMutable(t.sequence)
	}
	changes, err := t.changes.Fork()
	if err != nil {
		return nil, err
	}
	lease, err := t.store.roots.AcquireLease(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		_ = changes.Close()
		return nil, err
	}
	count, countKnown := t.countSnapshot()
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root,
		sequence: t.sequence, changes: changes, lease: lease, release: lease.Release, count: count, countKnown: countKnown,
		closure: t.closure, parentClosure: t.parentClosure}, nil
}

func (t *executionTree) close() {
	t.hashMu.Lock()
	resultLease := t.resultLease
	t.resultLease = nil
	t.hashMu.Unlock()
	if resultLease != nil {
		resultLease.Release()
	}
	if t.changes != nil {
		_ = t.changes.Close()
		t.changes = nil
	}
	if t.lease != nil {
		t.lease.Release()
		t.lease = nil
	} else if t.release != nil {
		t.release()
		t.release = nil
	}
	t.release = nil
}

func (t *executionTree) invalidateHash() {
	t.hashMu.Lock()
	resultLease := t.resultLease
	t.resultLease = nil
	t.haveHash = false
	t.cached = [32]byte{}
	t.hashMu.Unlock()
	if resultLease != nil {
		resultLease.Release()
	}
}

func (t *executionTree) releaseBuildLease() {
	t.hashMu.Lock()
	resultLease := t.resultLease
	t.resultLease = nil
	t.hashMu.Unlock()
	if resultLease != nil {
		resultLease.Release()
	}
}

func (t *executionTree) takeResultLease() *rootlease.Lease {
	t.hashMu.Lock()
	defer t.hashMu.Unlock()
	resultLease := t.resultLease
	t.resultLease = nil
	return resultLease
}

func (t *executionTree) countSnapshot() (uint32, bool) {
	t.hashMu.Lock()
	defer t.hashMu.Unlock()
	return t.count, t.countKnown
}

func (t *executionTree) countLeaves(ctx context.Context) (uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	t.hashMu.Lock()
	if t.countKnown {
		count := t.count
		t.hashMu.Unlock()
		return count, nil
	}
	root := t.root
	t.hashMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if root == ([32]byte{}) {
		t.hashMu.Lock()
		if !t.countKnown {
			t.count, t.countKnown = 0, true
		}
		count := t.count
		t.hashMu.Unlock()
		return count, nil
	}
	release, err := t.store.AcquireRoot(rootlease.Root{Hash: root, Kind: t.kind})
	if err != nil {
		return 0, err
	}
	defer release()
	count, err := t.reader().Count(ctx)
	if err != nil {
		return 0, err
	}
	if count > uint64(^uint32(0)) {
		return 0, errors.New("transaction leaf count exceeds uint32")
	}
	t.hashMu.Lock()
	if !t.countKnown {
		t.count, t.countKnown = uint32(count), true
	}
	result := t.count
	t.hashMu.Unlock()
	return result, nil
}

func (t *executionTree) get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	if t.changes != nil {
		return t.changes.Get(ctx, key)
	}
	return (executionBase{tree: t}).Get(ctx, key)
}

func (t *executionTree) GetContext(ctx context.Context, key [32]byte) (*shamap.Item, bool, error) {
	data, found, err := t.get(ctx, key)
	if err != nil || !found {
		return nil, found, err
	}
	if t.kind == shamap.TypeTransaction {
		if len(data) == 0 {
			return nil, false, errors.New("empty transaction overlay record")
		}
		data = data[1:]
	}
	return shamap.NewItem(key, data), true, nil
}

func (t *executionTree) Get(key [32]byte) (*shamap.Item, bool, error) {
	return t.GetContext(context.Background(), key)
}

func (t *executionTree) HasContext(ctx context.Context, key [32]byte) (bool, error) {
	_, found, err := t.get(ctx, key)
	return found, err
}

func (t *executionTree) Has(key [32]byte) (bool, error) {
	return t.HasContext(context.Background(), key)
}

func (t *executionTree) Put(key [32]byte, data []byte) error {
	return t.PutWithNodeType(key, data, shamap.NodeTypeAccountState)
}

func (t *executionTree) PutWithNodeType(key [32]byte, data []byte, kind shamap.NodeType) error {
	if t.changes == nil {
		return ErrLedgerImmutable
	}
	if len(data) < 12 {
		return errors.New("SHAMap item must contain at least 12 bytes")
	}
	if t.kind == shamap.TypeTransaction {
		if kind != shamap.NodeTypeTransactionNoMeta && kind != shamap.NodeTypeTransactionWithMeta {
			return errors.New("invalid transaction leaf type")
		}
		data = append([]byte{byte(kind)}, data...)
	} else if kind != shamap.NodeTypeAccountState {
		return errors.New("invalid state leaf type")
	}
	found, err := t.Has(key)
	if err != nil {
		return err
	}
	if err := t.changes.Set(key, data); err != nil {
		return err
	}
	t.hashMu.Lock()
	if !found {
		t.count++
	}
	t.hashMu.Unlock()
	t.invalidateHash()
	return nil
}

func (t *executionTree) Delete(key [32]byte) error {
	if t.changes == nil {
		return ErrLedgerImmutable
	}
	found, err := t.Has(key)
	if err != nil {
		return err
	}
	if !found {
		return ErrEntryNotFound
	}
	if err := t.changes.Delete(key); err != nil {
		return err
	}
	t.hashMu.Lock()
	if t.count > 0 {
		t.count--
	}
	t.hashMu.Unlock()
	t.invalidateHash()
	return nil
}

func (t *executionTree) PutItemsAtomically(items ...*shamap.Item) error {
	fork, err := t.fork()
	if err != nil {
		return err
	}
	defer fork.close()
	for _, item := range items {
		if item == nil {
			return errors.New("nil state item")
		}
		if err := fork.Put(item.Key(), item.Data()); err != nil {
			return err
		}
	}
	count, countKnown := fork.countSnapshot()
	t.close()
	t.changes, t.lease, t.release, t.closure, t.parentClosure = fork.changes, fork.lease, fork.release, fork.closure, fork.parentClosure
	t.hashMu.Lock()
	t.count, t.countKnown = count, countKnown
	t.hashMu.Unlock()
	fork.changes, fork.lease, fork.release = nil, nil, nil
	t.invalidateHash()
	return nil
}

const (
	executionCommitmentBatchEntries  = 64
	executionCommitmentBatchBytes    = 1 << 20
	executionCommitmentEntryOverhead = 128
)

// executionCommitmentSink copies each borrowed node only for the duration of a
// bounded StoreBatch call. A denied copy is reported immediately: waiting from
// inside BuildChanges would retain that builder's output charge and could
// deadlock the shared workspace waiting for its own callback to return.
type executionCommitmentSink struct {
	ctx     context.Context
	family  shamap.Family
	budget  shamap.DirtyMemoryBudget
	entries []shamap.FlushEntry
	charged uint64
}

func newExecutionCommitmentSink(ctx context.Context, family shamap.Family, budget shamap.DirtyMemoryBudget) *executionCommitmentSink {
	if ctx == nil {
		ctx = context.Background()
	}
	return &executionCommitmentSink{
		ctx:     ctx,
		family:  family,
		budget:  budget,
		entries: make([]shamap.FlushEntry, 0, executionCommitmentBatchEntries),
	}
}

func (s *executionCommitmentSink) add(entry shamap.FlushEntry) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	charge := uint64(len(entry.Data)) + executionCommitmentEntryOverhead
	if len(s.entries) >= executionCommitmentBatchEntries ||
		(len(s.entries) > 0 && s.charged+charge > executionCommitmentBatchBytes) {
		if err := s.flush(); err != nil {
			return err
		}
	}
	if s.budget != nil && !s.budget.TryReserve(charge) {
		return fmt.Errorf("bounded execution node batch needs %d bytes: %w", charge, shamap.ErrCommitmentBudget)
	}
	entry.Data = bytes.Clone(entry.Data)
	s.entries = append(s.entries, entry)
	s.charged += charge
	return nil
}

func (s *executionCommitmentSink) flush() error {
	if len(s.entries) == 0 {
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		s.abort()
		return err
	}
	if err := s.family.StoreBatch(s.ctx, s.entries); err != nil {
		s.abort()
		return fmt.Errorf("store bounded commitment batch: %w", err)
	}
	s.release()
	return nil
}

func (s *executionCommitmentSink) release() {
	if s.budget != nil && s.charged != 0 {
		s.budget.Release(s.charged)
	}
	clear(s.entries)
	s.entries = s.entries[:0]
	s.charged = 0
}

func (s *executionCommitmentSink) abort() { s.release() }

func (t *executionTree) hash(ctx context.Context) ([32]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t.changes == nil {
		return t.root, ctx.Err()
	}
	t.hashMu.Lock()
	defer t.hashMu.Unlock()
	if t.haveHash {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		if t.resultLease == nil {
			lease, err := t.store.roots.AcquireLease(rootlease.Root{Hash: t.cached, Kind: t.kind})
			if err != nil {
				return [32]byte{}, err
			}
			t.resultLease = lease
		}
		return t.cached, ctx.Err()
	}
	build, err := t.store.roots.BeginBuild()
	if err != nil {
		t.store.stats.buildErrors.Add(1)
		return [32]byte{}, err
	}
	defer build.End()
	changes, err := t.changes.Changes(ctx, nil)
	if err != nil {
		return [32]byte{}, err
	}
	defer changes.Close()
	next := func() (shamap.Change, error) {
		change, done, err := changes.Next(ctx)
		if err != nil {
			return shamap.Change{}, err
		}
		if done {
			return shamap.Change{}, io.EOF
		}
		out := shamap.Change{Key: change.Key, Data: change.Value, Delete: change.Deleted, Type: shamap.NodeTypeAccountState}
		if t.kind == shamap.TypeTransaction && !out.Delete {
			if len(out.Data) == 0 {
				return shamap.Change{}, errors.New("empty transaction overlay record")
			}
			out.Type, out.Data = shamap.NodeType(out.Data[0]), out.Data[1:]
		}
		return out, nil
	}
	sink := newExecutionCommitmentSink(ctx, t.store.config.Family, t.store.config.Workspace)
	t.store.stats.builds.Add(1)
	started := time.Now()
	defer func() { t.store.stats.buildNanos.Add(uint64(time.Since(started))) }()
	result, err := shamap.BuildChanges(ctx, t.kind, t.root, t.sequence, t.store.config.Family,
		next, sink.add, shamap.WithCommitmentBudget(t.store.config.Workspace))
	if err != nil {
		t.store.stats.buildErrors.Add(1)
		sink.abort()
		return [32]byte{}, err
	}
	if err := sink.flush(); err != nil {
		t.store.stats.buildErrors.Add(1)
		sink.abort()
		return [32]byte{}, err
	}
	if err := ctx.Err(); err != nil {
		t.store.stats.buildErrors.Add(1)
		return [32]byte{}, err
	}
	resultLease, err := t.store.roots.AcquireLease(rootlease.Root{Hash: result.Root, Kind: t.kind})
	if err != nil {
		t.store.stats.buildErrors.Add(1)
		return [32]byte{}, err
	}
	t.store.stats.buildNodes.Add(result.Nodes)
	t.store.stats.buildLeaves.Add(result.Leaves)
	t.store.stats.buildBytes.Add(result.Bytes)
	updateExecutionMax(&t.store.stats.maxFrontier, result.MaxFrontier)
	t.cached, t.haveHash, t.resultLease = result.Root, true, resultLease
	return result.Root, nil
}

func (t *executionTree) rooted(ctx context.Context, transferBuildLease bool) (*executionTree, error) {
	root, err := t.hash(ctx)
	if err != nil {
		return nil, err
	}
	closure := executionClosureUnknown
	if t.changes == nil {
		closure = t.closure
	} else if t.parentClosure {
		closure = executionClosurePending
		if root == t.root && t.closure == executionClosureVerified {
			closure = executionClosureVerified
		}
	}
	// A read snapshot is deliberately non-owning. The mutable source retains
	// the build lease for its cached root; explicit close/freeze paths transfer
	// that lease to the sealed descriptor instead.
	var resultLease *rootlease.Lease
	if t.changes != nil && transferBuildLease {
		resultLease = t.takeResultLease()
	}
	count, countKnown := t.countSnapshot()
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: root, sequence: t.sequence,
		count: count, countKnown: countKnown, closure: closure, resultLease: resultLease}, nil
}

func countExecutionRoot(ctx context.Context, tree *executionTree) (uint64, error) {
	if tree == nil || tree.root == ([32]byte{}) {
		return 0, nil
	}
	family := tree.store.config.Family
	if durable, ok := family.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		family = durableExecutionFamily{Family: family, durable: durable}
	}
	reader := shamap.NewRootReader(tree.kind, tree.root, family,
		shamap.WithCommitmentBudget(tree.store.config.Workspace))
	return reader.Count(ctx)
}

func verifyExecutionRoot(ctx context.Context, tree *executionTree) error {
	_, err := countExecutionRoot(ctx, tree)
	return err
}

// EstablishBoundedClosure authenticates a root-only view against its configured
// Family when needed and records the exact closure for later flat publication.
// For a pending incremental build, the caller must invoke this only after its
// NodeStore durability barrier has completed; promotion then reuses the
// verified parent closure and changed-node output without a full-state walk.
func (l *Ledger) EstablishBoundedClosure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.RLock()
	if l.execution == nil {
		l.mu.RUnlock()
		return ErrInvalidState
	}
	if l.writable || l.state == StateOpen {
		l.mu.RUnlock()
		return ErrLedgerImmutable
	}
	view := l.execution
	h := l.header
	state, tx := view.state, view.transactions
	stateClosure, txClosure := state.closure, tx.closure
	l.mu.RUnlock()
	if state.root != h.AccountHash || tx.root != h.TxHash || header.CalculateHash(h) != h.Hash {
		return errors.New("bounded execution roots do not match ledger header")
	}
	if stateClosure != executionClosureVerified {
		if stateClosure != executionClosurePending {
			if err := verifyExecutionRoot(ctx, state); err != nil {
				return fmt.Errorf("verify bounded state closure: %w", err)
			}
		}
	}
	if txClosure != executionClosureVerified {
		if txClosure != executionClosurePending {
			if err := verifyExecutionRoot(ctx, tx); err != nil {
				return fmt.Errorf("verify bounded transaction closure: %w", err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != view || l.header.AccountHash != h.AccountHash || l.header.TxHash != h.TxHash || l.header.Hash != h.Hash {
		return errors.New("bounded execution changed during closure verification")
	}
	view.state.closure = executionClosureVerified
	view.transactions.closure = executionClosureVerified
	return nil
}

// AuthenticateClosure verifies an immutable root view against its configured
// Family and records the result as pending. The caller must complete the
// corresponding durability barrier before promoting it with
// EstablishBoundedClosure.
func (l *Ledger) AuthenticateClosure(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.RLock()
	if l.execution == nil {
		l.mu.RUnlock()
		return ErrInvalidState
	}
	if l.writable || l.state == StateOpen {
		l.mu.RUnlock()
		return ErrLedgerImmutable
	}
	view, h := l.execution, l.header
	l.mu.RUnlock()
	return l.authenticateUnknownBoundedClosure(ctx, view, h)
}

// AuthenticateClosureWith records a service admission proof for this exact header.
// verify must authenticate both trees while retaining the backing generation.
// Durability is still established separately by EstablishBoundedClosure.
func (l *Ledger) AuthenticateClosureWith(ctx context.Context, verify func(context.Context, header.LedgerHeader) error) error {
	if verify == nil {
		return errors.New("nil closure verifier")
	}
	l.mu.RLock()
	if l.execution == nil || l.writable || l.state == StateOpen {
		l.mu.RUnlock()
		return ErrInvalidState
	}
	view, h := l.execution, l.header
	l.mu.RUnlock()
	if view.state.root != h.AccountHash || view.transactions.root != h.TxHash || header.CalculateHash(h) != h.Hash {
		return errors.New("bounded execution roots do not match ledger header")
	}
	if err := verify(ctx, h); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != view || l.header.Hash != h.Hash || l.header.AccountHash != h.AccountHash || l.header.TxHash != h.TxHash {
		return errors.New("bounded execution changed during closure verification")
	}
	view.state.closure = executionClosurePending
	view.transactions.closure = executionClosurePending
	return nil
}

// authenticateUnknownBoundedClosure verifies only roots that have not already
// been authenticated and records that proof as pending. Pending roots are
// promoted by EstablishBoundedClosure after the caller's storage durability
// barrier; this method must not make that claim on the strength of a read or a
// StoreBatch alone.
func (l *Ledger) authenticateUnknownBoundedClosure(ctx context.Context, view *executionView, h header.LedgerHeader) error {
	l.mu.RLock()
	if l.execution != view || l.header.Hash != h.Hash {
		l.mu.RUnlock()
		return errors.New("bounded execution changed during closure verification")
	}
	stateClosure := view.state.closure
	txClosure := view.transactions.closure
	storedTxCount, txCountKnown := view.transactions.countSnapshot()
	txCount := uint64(storedTxCount)
	l.mu.RUnlock()
	if view.state.root != h.AccountHash || view.transactions.root != h.TxHash || header.CalculateHash(h) != h.Hash {
		return errors.New("bounded execution roots do not match ledger header")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stateClosure == executionClosureUnknown {
		if err := verifyExecutionRoot(ctx, view.state); err != nil {
			return fmt.Errorf("verify bounded state closure: %w", err)
		}
	}
	if txClosure == executionClosureUnknown || !txCountKnown {
		count, err := countExecutionRoot(ctx, view.transactions)
		if err != nil {
			return fmt.Errorf("verify bounded transaction closure: %w", err)
		}
		txCount = count
		txCountKnown = true
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if txCountKnown && txCount > uint64(^uint32(0)) {
		return errors.New("transaction leaf count exceeds uint32")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != view || l.header.Hash != h.Hash || l.header.AccountHash != h.AccountHash || l.header.TxHash != h.TxHash {
		return errors.New("bounded execution changed during closure verification")
	}
	if view.state.closure == executionClosureUnknown {
		view.state.closure = executionClosurePending
	}
	if view.transactions.closure == executionClosureUnknown {
		view.transactions.closure = executionClosurePending
	}
	if txCountKnown {
		view.transactions.hashMu.Lock()
		if !view.transactions.countKnown {
			view.transactions.count, view.transactions.countKnown = uint32(txCount), true
		}
		view.transactions.hashMu.Unlock()
	}
	return nil
}

func (v *executionView) fork() (*executionView, error) {
	state, err := v.state.fork()
	if err != nil {
		return nil, err
	}
	txs, err := v.transactions.fork()
	if err != nil {
		state.close()
		return nil, err
	}
	return &executionView{state: state, transactions: txs}, nil
}

func (v *executionView) rooted(ctx context.Context, transferBuildLease bool) (*executionView, error) {
	state, err := v.state.rooted(ctx, transferBuildLease)
	if err != nil {
		return nil, err
	}
	txs, err := v.transactions.rooted(ctx, transferBuildLease)
	if err != nil {
		state.close()
		return nil, err
	}
	return &executionView{state: state, transactions: txs}, nil
}

func (v *executionView) close() { v.state.close(); v.transactions.close() }

func (v *executionView) releaseBuildLeases() {
	v.state.releaseBuildLease()
	v.transactions.releaseBuildLease()
}

func (t *executionTree) readSnapshot() (overlay.Reader, func(), error) {
	release, err := t.store.roots.Acquire(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		return nil, nil, err
	}
	if t.changes == nil {
		return executionBase{tree: t}, release, nil
	}
	view, err := t.changes.Snapshot()
	if err != nil {
		release()
		return nil, nil, err
	}
	return view, func() { _ = view.Close(); release() }, nil
}

func (t *executionTree) iterate(ctx context.Context, after *[32]byte, fn func([32]byte, []byte) bool) error {
	reader, release, err := t.readSnapshot()
	if err != nil {
		return err
	}
	defer release()
	return reader.Iterate(ctx, after, fn)
}

func (l *Ledger) executionSnapshot(ctx context.Context, mutable bool) (*Ledger, error) {
	var view *executionView
	var err error
	if mutable {
		view, err = l.execution.fork()
	} else {
		view, err = l.execution.rooted(ctx, false)
	}
	if err != nil {
		return nil, err
	}
	return &Ledger{execution: view, header: l.header, fees: l.fees, state: l.state,
		writable: mutable, dropsDestroyed: l.dropsDestroyed, rules: l.rules}, nil
}

func (v *executionView) successor(ctx context.Context, sequence uint32) (*executionView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := v.state.newMutableContext(ctx, sequence)
	if err != nil {
		return nil, err
	}
	empty := &executionTree{store: v.state.store, kind: shamap.TypeTransaction, sequence: sequence, countKnown: true}
	txs, err := empty.newMutableContext(ctx, sequence)
	if err != nil {
		state.close()
		return nil, err
	}
	return &executionView{state: state, transactions: txs}, nil
}

func (l *Ledger) closeExecution(closeTime time.Time, closeFlags uint8) error {
	staged, err := l.execution.fork()
	if err != nil {
		return err
	}
	defer staged.close()
	if err := skiplist.UpdateOnMap(staged.state, l.header.LedgerIndex, l.header.ParentHash); err != nil {
		return fmt.Errorf("failed to update skip list: %w", err)
	}
	rules, err := loadAmendmentsFromTree(context.Background(), staged.state)
	if err != nil {
		return err
	}
	rooted, err := staged.rooted(context.Background(), true)
	if err != nil {
		return err
	}
	hdr := l.header
	hdr.Drops -= uint64(l.dropsDestroyed)
	hdr.AccountHash, hdr.TxHash = rooted.state.root, rooted.transactions.root
	hdr.CloseTime, hdr.CloseFlags, hdr.Accepted = closeTime, closeFlags, true
	hdr.Hash = header.CalculateHash(hdr)
	rooted.state.base, rooted.transactions.base = hdr, hdr
	l.execution.close()
	l.execution, l.header, l.rules = rooted, hdr, rules
	l.state, l.writable = StateClosed, false
	return nil
}

func (t *executionTree) mapSnapshot(ctx context.Context) (*shamap.SHAMap, error) {
	root, err := t.hash(ctx)
	if err != nil {
		return nil, err
	}
	var tree *shamap.SHAMap
	if root == ([32]byte{}) {
		tree, err = shamap.NewBacked(t.kind, t.store.config.Family)
	} else {
		tree, err = shamap.NewFromRootHashContext(ctx, t.kind, root, t.store.config.Family)
	}
	if err != nil {
		return nil, err
	}
	tree.SetLedgerSeq(t.sequence)
	return tree, nil
}

// FreezeExecution preserves an obsolete published open view for concurrent
// readers while releasing its mutable buffers and spill files.
func (l *Ledger) FreezeExecution() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution == nil || !l.writable {
		return nil
	}
	if l.execution.state.changes == nil && l.execution.transactions.changes == nil {
		return nil
	}
	view, err := l.execution.rooted(context.Background(), true)
	if err != nil {
		return err
	}
	l.execution.close()
	l.execution = view
	return nil
}

// ReleaseExecutionBuildLeases releases pending construction ownership after a
// sealed ledger has been transferred to history, an outbox, or another
// retention owner. It leaves the authenticated descriptor and read paths
// unchanged.
func (l *Ledger) ReleaseExecutionBuildLeases() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil {
		l.execution.releaseBuildLeases()
	}
}

// Discard releases an abandoned mutable execution view. It is harmless after
func (l *Ledger) Discard() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil && l.writable {
		l.invalidateMutableStateLocked()
	} else if l.execution != nil {
		l.execution.releaseBuildLeases()
	}
}

func (l *Ledger) ExecutionJournalConfig() overlay.Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution.state.store.config.Overlay
}

// ExecutionStore returns the bounded execution store shared by this ledger's
// roots and mutable overlays. The store is owned by the service or fixture
// that created the ledger and must outlive every ledger using it.
func (l *Ledger) ExecutionStore() *ExecutionStore {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.execution == nil || l.execution.state == nil {
		return nil
	}
	return l.execution.state.store
}

func NewOpenFromParentHeader(ctx context.Context, parent *Ledger, hdr header.LedgerHeader) (*Ledger, error) {
	if parent == nil {
		return nil, errors.New("nil replay parent")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent.mu.RLock()
	defer parent.mu.RUnlock()
	if parent.writable || parent.state == StateOpen {
		return nil, ErrInvalidState
	}
	view, err := parent.execution.successor(ctx, hdr.LedgerIndex)
	if err != nil {
		return nil, err
	}
	return &Ledger{execution: view, header: hdr, fees: parent.fees, rules: parent.rules, writable: true, state: StateOpen}, nil
}

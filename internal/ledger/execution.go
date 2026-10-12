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

// BeginBuild admits a bounded commitment build without pinning a durable
// generation. The caller must register any destination root ownership before
// ending the returned token.
func (s *ExecutionStore) BeginBuild() (*rootlease.BuildLease, error) {
	return s.roots.BeginBuild()
}

// EndBuild ends a bounded commitment build admission. It is idempotent for a
// token and safe from deferred cleanup paths.
func (s *ExecutionStore) EndBuild(build *rootlease.BuildLease) {
	s.roots.EndBuild(build)
}

// AcquireExecutionLease registers a sealed root that must survive storage
// rotation until its owner transfers or releases it.
func (s *ExecutionStore) AcquireExecutionLease(root rootlease.Root) (*rootlease.Lease, error) {
	return s.roots.AcquireLease(root)
}

// ReleaseExecutionLease releases ownership after the sealed root has been
// transferred to history, flat outbox, or another retention owner.
func (s *ExecutionStore) ReleaseExecutionLease(lease *rootlease.Lease) {
	if lease != nil {
		lease.Release()
	}
}

func (s *ExecutionStore) GuardRoots(preserved func(rootlease.Root) bool) (func(), bool) {
	return s.roots.GuardFunc(preserved)
}

// GuardPreservedRoots admits storage retirement when every active lease is in
// the caller's immutable preservation set. The set is read while admitting the
// guard and is not retained.
func (s *ExecutionStore) GuardPreservedRoots(preserved map[rootlease.Root]struct{}) (func(), bool) {
	return s.roots.Guard(preserved)
}

func (s *ExecutionStore) Close() error { return s.roots.Close() }

type stateTree interface {
	Get([32]byte) (*shamap.Item, bool, error)
	GetContext(context.Context, [32]byte) (*shamap.Item, bool, error)
	Has([32]byte) (bool, error)
	HasContext(context.Context, [32]byte) (bool, error)
	Put([32]byte, []byte) error
	Delete([32]byte) error
	PutItemsAtomically(...*shamap.Item) error
}

type transactionTree interface {
	GetContext(context.Context, [32]byte) (*shamap.Item, bool, error)
	Has([32]byte) (bool, error)
	PutWithNodeType([32]byte, []byte, shamap.NodeType) error
}

func (l *Ledger) stateTree() stateTree {
	if l.execution != nil {
		return l.execution.state
	}
	return l.stateMap
}

func (l *Ledger) transactionTree() transactionTree {
	if l.execution != nil {
		return l.execution.transactions
	}
	return l.txMap
}

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
	closure       executionClosureState
	parentClosure bool
	hashMu        sync.Mutex
	cached        [32]byte
	haveHash      bool
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
	base := executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root, sequence: t.sequence, count: t.count}
	closure := t.closure
	if t.root == ([32]byte{}) && closure == executionClosureUnknown {
		closure = executionClosureVerified
	}
	out := &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root, sequence: sequence, count: t.count,
		closure: closure, parentClosure: closure == executionClosureVerified || closure == executionClosurePending}
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
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root,
		sequence: t.sequence, changes: changes, lease: lease, release: lease.Release, count: t.count,
		closure: t.closure, parentClosure: t.parentClosure}, nil
}

func (t *executionTree) close() {
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
	if !found {
		t.count++
	}
	t.haveHash = false
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
	if t.count > 0 {
		t.count--
	}
	t.haveHash = false
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
	t.close()
	t.changes, t.lease, t.release, t.count, t.closure, t.parentClosure = fork.changes, fork.lease, fork.release, fork.count, fork.closure, fork.parentClosure
	fork.changes, fork.lease, fork.release = nil, nil, nil
	t.haveHash = false
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
		return t.cached, ctx.Err()
	}
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
	t.store.stats.buildNodes.Add(result.Nodes)
	t.store.stats.buildLeaves.Add(result.Leaves)
	t.store.stats.buildBytes.Add(result.Bytes)
	updateExecutionMax(&t.store.stats.maxFrontier, result.MaxFrontier)
	t.cached, t.haveHash = result.Root, true
	return result.Root, nil
}

func (t *executionTree) rooted(ctx context.Context) (*executionTree, error) {
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
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: root, sequence: t.sequence,
		count: t.count, closure: closure}, nil
}

func verifyExecutionRoot(ctx context.Context, tree *executionTree) error {
	if tree == nil || tree.root == ([32]byte{}) {
		return nil
	}
	family := tree.store.config.Family
	if durable, ok := family.(interface {
		FetchDurable(context.Context, [32]byte) ([]byte, error)
	}); ok {
		family = durableExecutionFamily{Family: family, durable: durable}
	}
	reader := shamap.NewRootReader(tree.kind, tree.root, family,
		shamap.WithCommitmentBudget(tree.store.config.Workspace))
	_, err := reader.Count(ctx)
	return err
}

// HasVerifiedExecutionClosure reports whether both authenticated roots have a
// verified closure in this ledger's configured Family. A successful StoreBatch
// is not sufficient; pending roots become verified only after the caller has
// completed the corresponding storage durability barrier and invokes
// EstablishBoundedClosure.
func (l *Ledger) HasVerifiedExecutionClosure() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution != nil && !l.writable &&
		l.execution.state.closure == executionClosureVerified &&
		l.execution.transactions.closure == executionClosureVerified
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

func (v *executionView) rooted(ctx context.Context) (*executionView, error) {
	state, err := v.state.rooted(ctx)
	if err != nil {
		return nil, err
	}
	txs, err := v.transactions.rooted(ctx)
	if err != nil {
		state.close()
		return nil, err
	}
	return &executionView{state: state, transactions: txs}, nil
}

func (v *executionView) close() { v.state.close(); v.transactions.close() }

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
		view, err = l.execution.rooted(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &Ledger{execution: view, header: l.header, fees: l.fees, state: l.state,
		writable: mutable, dropsDestroyed: l.dropsDestroyed, rules: l.rules}, nil
}

func (v *executionView) successor(sequence uint32) (*executionView, error) {
	state, err := v.state.newMutable(sequence)
	if err != nil {
		return nil, err
	}
	empty := &executionTree{store: v.state.store, kind: shamap.TypeTransaction, sequence: sequence}
	txs, err := empty.newMutable(sequence)
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
	rooted, err := staged.rooted(context.Background())
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
	view, err := l.execution.rooted(context.Background())
	if err != nil {
		return err
	}
	l.execution.close()
	l.execution = view
	return nil
}

// EnableBoundedReads replaces an immutable ledger's resident maps with
// authenticated-root descriptors. The target Family is intentionally checked
// lazily: this is the historical/read path and its closure is uncertified until
// EstablishBoundedClosure is called.
func (l *Ledger) EnableBoundedReads(ctx context.Context, store *ExecutionStore) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.RLock()
	if l.execution != nil {
		sameStore := store != nil && l.execution.state.store == store
		l.mu.RUnlock()
		if !sameStore {
			return errors.New("bounded execution is already attached to a different store")
		}
		return nil
	}
	if l.writable || l.state == StateOpen || store == nil || l.stateMap == nil || l.txMap == nil {
		l.mu.RUnlock()
		return ErrInvalidState
	}
	h := l.header
	stateMap, txMap := l.stateMap, l.txMap
	l.mu.RUnlock()
	if header.CalculateHash(h) != h.Hash {
		return errors.New("bounded read roots have an invalid ledger header")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	view := &executionView{
		state: &executionTree{store: store, kind: shamap.TypeState, base: h, root: h.AccountHash, sequence: h.LedgerIndex},
		transactions: &executionTree{store: store, kind: shamap.TypeTransaction, base: h, root: h.TxHash,
			sequence: h.LedgerIndex, count: uint32(txMap.Size())},
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil || l.stateMap != stateMap || l.txMap != txMap || l.header.Hash != h.Hash {
		return ErrInvalidState
	}
	l.execution = view
	l.stateMap, l.txMap = nil, nil
	return nil
}

// EnableBoundedExecution persists the imported maps before replacing them with
// root descriptors, then authenticates the complete destination closure once.
// A later target can carry that proof across incremental builds and promote its
// pending changed-node closure after the caller's durability barrier.
func (l *Ledger) EnableBoundedExecution(ctx context.Context, store *ExecutionStore) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.RLock()
	if l.execution != nil {
		writable := l.writable
		sameStore := store != nil && l.execution.state.store == store
		l.mu.RUnlock()
		if !sameStore {
			return errors.New("bounded execution is already attached to a different store")
		}
		if writable {
			return ErrInvalidState
		}
		return l.EstablishBoundedClosure(ctx)
	}
	if l.writable || l.state == StateOpen || store == nil || l.stateMap == nil || l.txMap == nil {
		l.mu.RUnlock()
		return ErrInvalidState
	}
	h := l.header
	stateMap, txMap := l.stateMap, l.txMap
	l.mu.RUnlock()
	if header.CalculateHash(h) != h.Hash {
		return errors.New("bounded execution header is invalid")
	}
	stateRoot, err := stateMap.Hash()
	if err != nil {
		return fmt.Errorf("hash imported state tree: %w", err)
	}
	txRoot, err := txMap.Hash()
	if err != nil {
		return fmt.Errorf("hash imported transaction tree: %w", err)
	}
	if stateRoot != h.AccountHash || txRoot != h.TxHash {
		return errors.New("bounded execution roots do not match ledger header")
	}
	for _, tree := range []*shamap.SHAMap{stateMap, txMap} {
		if err := tree.StoreDirtyBatched(ctx, store.config.Workspace, func(entries []shamap.FlushEntry) error {
			return store.config.Family.StoreBatch(ctx, entries)
		}); err != nil {
			return fmt.Errorf("persist imported execution tree: %w", err)
		}
	}
	view := &executionView{
		state:        &executionTree{store: store, kind: shamap.TypeState, base: h, root: stateRoot, sequence: h.LedgerIndex, closure: executionClosureUnknown},
		transactions: &executionTree{store: store, kind: shamap.TypeTransaction, base: h, root: txRoot, sequence: h.LedgerIndex, count: uint32(txMap.Size()), closure: executionClosureUnknown},
	}
	if err := verifyExecutionRoot(ctx, view.state); err != nil {
		return fmt.Errorf("verify imported state closure: %w", err)
	}
	if err := verifyExecutionRoot(ctx, view.transactions); err != nil {
		return fmt.Errorf("verify imported transaction closure: %w", err)
	}
	view.state.closure = executionClosureVerified
	view.transactions.closure = executionClosureVerified
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil || l.stateMap != stateMap || l.txMap != txMap || l.header.Hash != h.Hash {
		return ErrInvalidState
	}
	l.execution = view
	l.stateMap, l.txMap = nil, nil
	return nil
}

// Discard releases an abandoned mutable execution view. It is harmless after
// ConsumeState and for legacy in-memory ledgers.
func (l *Ledger) Discard() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil && l.writable {
		l.execution.close()
		l.execution = nil
		l.invalidateMutableStateLocked()
	}
}

func (l *Ledger) ExecutionJournalConfig() (overlay.Config, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.execution == nil {
		return overlay.Config{}, false
	}
	return l.execution.state.store.config.Overlay, true
}

func (l *Ledger) UsesBoundedExecution() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.execution != nil
}

func NewOpenFromParentHeader(ctx context.Context, parent *Ledger, hdr header.LedgerHeader) (*Ledger, error) {
	if parent == nil {
		return nil, errors.New("nil replay parent")
	}
	parent.mu.RLock()
	if parent.execution != nil {
		defer parent.mu.RUnlock()
		if parent.writable || parent.state == StateOpen {
			return nil, ErrInvalidState
		}
		view, err := parent.execution.successor(hdr.LedgerIndex)
		if err != nil {
			return nil, err
		}
		return &Ledger{execution: view, header: hdr, fees: parent.fees, rules: parent.rules, writable: true, state: StateOpen}, nil
	}
	parent.mu.RUnlock()
	state, err := parent.StateMapSnapshotContext(ctx)
	if err != nil {
		return nil, err
	}
	return NewOpenWithHeader(hdr, state, shamap.New(shamap.TypeTransaction), parent.Fees())
}

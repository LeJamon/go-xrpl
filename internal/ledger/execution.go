package ledger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"
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

func (s *ExecutionStore) GuardRoots(preserved func(rootlease.Root) bool) (func(), bool) {
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

type executionTree struct {
	store    *ExecutionStore
	kind     shamap.Type
	base     header.LedgerHeader
	root     [32]byte
	sequence uint32
	changes  *overlay.Overlay
	release  func()
	count    uint32
	hashMu   sync.Mutex
	cached   [32]byte
	haveHash bool
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
		defer v.Close()
		return v.Get(key)
	}
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
		defer v.Close()
		if after == nil {
			return v.ForEach(ctx, fn)
		}
		return v.ForEachFrom(ctx, *after, fn)
	}
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
	out := &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root, sequence: sequence, count: t.count}
	release, err := t.store.roots.Acquire(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		return nil, err
	}
	out.release = release
	out.changes, err = overlay.New(executionBase{tree: &base}, t.store.config.Overlay)
	if err != nil {
		release()
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
	release, err := t.store.roots.Acquire(rootlease.Root{Hash: t.root, Kind: t.kind})
	if err != nil {
		_ = changes.Close()
		return nil, err
	}
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: t.root,
		sequence: t.sequence, changes: changes, release: release, count: t.count}, nil
}

func (t *executionTree) close() {
	if t.changes != nil {
		_ = t.changes.Close()
		t.changes = nil
	}
	if t.release != nil {
		t.release()
		t.release = nil
	}
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
	t.changes, t.release, t.count = fork.changes, fork.release, fork.count
	fork.changes, fork.release = nil, nil
	t.haveHash = false
	return nil
}

func (t *executionTree) hash(ctx context.Context) ([32]byte, error) {
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
	result, err := shamap.BuildChanges(ctx, t.kind, t.root, t.sequence, t.store.config.Family,
		next, func(entry shamap.FlushEntry) error {
			return t.store.config.Family.StoreBatch(ctx, []shamap.FlushEntry{entry})
		}, shamap.WithCommitmentBudget(t.store.config.Workspace))
	if err != nil {
		return [32]byte{}, err
	}
	t.cached, t.haveHash = result.Root, true
	return result.Root, nil
}

func (t *executionTree) rooted(ctx context.Context) (*executionTree, error) {
	root, err := t.hash(ctx)
	if err != nil {
		return nil, err
	}
	return &executionTree{store: t.store, kind: t.kind, base: t.base, root: root, sequence: t.sequence, count: t.count}, nil
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

// EnableBoundedExecution persists the imported maps before replacing them with
// root descriptors. The caller must already have authenticated their contents.
func (l *Ledger) EnableBoundedExecution(ctx context.Context, store *ExecutionStore) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.execution != nil {
		return nil
	}
	if l.writable || store == nil {
		return ErrInvalidState
	}
	for _, tree := range []*shamap.SHAMap{l.stateMap, l.txMap} {
		if err := tree.StoreDirtyBatched(ctx, store.config.Workspace, func(entries []shamap.FlushEntry) error {
			return store.config.Family.StoreBatch(ctx, entries)
		}); err != nil {
			return fmt.Errorf("persist imported execution tree: %w", err)
		}
	}
	stateRoot, err := l.stateMap.Hash()
	if err != nil {
		return err
	}
	txRoot, err := l.txMap.Hash()
	if err != nil {
		return err
	}
	l.execution = &executionView{
		state:        &executionTree{store: store, kind: shamap.TypeState, base: l.header, root: stateRoot, sequence: l.header.LedgerIndex},
		transactions: &executionTree{store: store, kind: shamap.TypeTransaction, base: l.header, root: txRoot, sequence: l.header.LedgerIndex, count: uint32(l.txMap.Size())},
	}
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

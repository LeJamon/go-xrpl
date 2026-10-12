package overlay

import (
	"bytes"
	"context"
	"errors"
	"sync"
)

// Overlay is a mutable change layer over an immutable Reader. It is safe to
// read, fork, and close concurrently. Set and Delete operations are
// serialized with one another; callers may use a fork for independent writers.
type Overlay struct {
	mu sync.RWMutex

	base     baseReader
	cfg      Config
	budget   *budget
	root     *treeNode
	segments []*spillSegment // newest first
	closed   bool
}

// Snapshot is an immutable read lease. It keeps its captured overlay roots and
// spill segments alive until Close. The zero value is unusable.
type Snapshot struct {
	mu sync.Mutex

	base     baseReader
	budget   *budget
	root     *treeNode
	segments []*spillSegment
	closed   bool
}

type view struct {
	base     baseReader
	budget   *budget
	root     *treeNode
	segments []*spillSegment
}

// New creates an empty mutable overlay above base. A nil base is treated as an
// empty state. The base is immutable and owned by the caller for the lifetime
// of all derived overlays and snapshots.
func New(base baseReader, cfg Config) (*Overlay, error) {
	if base == nil {
		base = emptyReader{}
	}
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Overlay{base: base, cfg: cfg, budget: newBudget(cfg)}, nil
}

// Set writes an owned copy of value at key.
func (o *Overlay) Set(key [32]byte, value []byte) error {
	return o.SetContext(context.Background(), key, value)
}

// SetContext is Set with cancellation-aware spill and admission work.
func (o *Overlay) SetContext(ctx context.Context, key [32]byte, value []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return o.mutate(ctx, key, value, false)
}

// Delete masks key in the base and all older change layers.
func (o *Overlay) Delete(key [32]byte) error {
	return o.DeleteContext(context.Background(), key)
}

// DeleteContext is Delete with cancellation-aware spill and admission work.
func (o *Overlay) DeleteContext(ctx context.Context, key [32]byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return o.mutate(ctx, key, nil, true)
}

func (o *Overlay) mutate(ctx context.Context, key [32]byte, value []byte, deleted bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.closed {
		return ErrClosed
	}
	if existing := findTreeNode(o.root, key); existing != nil && sameTreeValue(existing, value, deleted) {
		return nil
	}

	if o.shouldSpillBeforeMutation(key, value, deleted) && o.root != nil {
		if err := o.spillRootLocked(ctx); err != nil {
			return err
		}
	}
	newRoot, err := insertTreeNode(o.budget, o.root, key, value, deleted)
	if err == nil {
		oldRoot := o.root
		o.root = newRoot
		releaseTreeNode(oldRoot)
		return nil
	}
	if !errors.Is(err, ErrMemoryBudget) {
		return err
	}

	// Admission can fail because a sibling fork retains the old root or because
	// the single value is larger than the configured resident-memory allowance.
	// Spill the private root first, then represent an oversized write directly as
	// one immutable run. Both paths preserve the failed mutation's atomicity.
	if o.root != nil {
		if spillErr := o.spillRootLocked(ctx); spillErr != nil {
			return spillErr
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !o.canFitOneValue(value) {
		if o.cfg.MaxDiskBytes == 0 || o.cfg.SpillDir == "" {
			return ErrSpillDisabled
		}
		segment, spillErr := writeOneSegment(ctx, o.budget, o.cfg.SpillDir, key, value, deleted)
		if spillErr != nil {
			return spillErr
		}
		return o.addSegmentLocked(ctx, segment)
	}
	newRoot, err = insertTreeNode(o.budget, nil, key, value, deleted)
	if err != nil {
		if o.cfg.MaxDiskBytes == 0 || o.cfg.SpillDir == "" {
			return err
		}
		segment, spillErr := writeOneSegment(ctx, o.budget, o.cfg.SpillDir, key, value, deleted)
		if spillErr != nil {
			return spillErr
		}
		return o.addSegmentLocked(ctx, segment)
	}
	o.root = newRoot
	return nil
}

func (o *Overlay) canFitOneValue(value []byte) bool {
	charge, ok := addCharge(nodeOverhead, blobOverhead+uint64(len(value)))
	if !ok {
		return false
	}
	memoryUsed := o.budget.memoryUsed()
	return o.cfg.MaxMemoryBytes == 0 || (memoryUsed <= o.cfg.MaxMemoryBytes && charge <= o.cfg.MaxMemoryBytes-memoryUsed)
}

func (o *Overlay) shouldSpillBeforeMutation(key [32]byte, value []byte, deleted bool) bool {
	if o.root == nil {
		return false
	}
	if o.cfg.MaxMemoryBytes == 0 {
		return false
	}
	depth := 0
	n := o.root
	for n != nil {
		depth++
		if key == n.key {
			break
		}
		if bytes.Compare(key[:], n.key[:]) < 0 {
			n = n.left
		} else {
			n = n.right
		}
	}
	newBlob := uint64(0)
	if n == nil || !sameTreeValue(n, value, deleted) {
		newBlob = blobOverhead + uint64(len(value))
	}
	needed, ok := addCharge(uint64(depth+4)*nodeOverhead, newBlob)
	if !ok {
		return true
	}
	// Larger values need descriptor headroom before the old tree can be
	// released. Small records have enough slack for the descriptor already.
	if o.cfg.MaxDiskBytes > 0 && o.cfg.SpillDir != "" && newBlob >= segmentOverhead {
		needed, ok = addCharge(needed, segmentOverhead)
		if !ok {
			return true
		}
	}
	memoryUsed := o.budget.memoryUsed()
	return needed > o.cfg.MaxMemoryBytes || memoryUsed > o.cfg.MaxMemoryBytes-needed
}

// Get resolves the newest change, then the immutable base. Returned bytes are
// owned by the caller and may be retained or mutated.
func (o *Overlay) Get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	v, err := o.capture()
	if err != nil {
		return nil, false, err
	}
	defer v.release()
	return v.get(ctx, key)
}

func (v *view) get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if n := findTreeNode(v.root, key); n != nil {
		if n.deleted {
			return nil, false, nil
		}
		value := make([]byte, len(n.value.data))
		copy(value, n.value.data)
		return value, true, nil
	}
	for _, segment := range v.segments {
		cursor, err := segment.cursor(ctx, v.budget, nil)
		if err != nil {
			return nil, false, err
		}
		value, found, err := cursor.get(ctx, key)
		closeErr := cursor.close()
		if err != nil {
			return nil, false, err
		}
		if closeErr != nil {
			return nil, false, closeErr
		}
		if found {
			if value == nil {
				return nil, false, nil
			}
			return value, true, nil
		}
	}
	value, found, err := v.base.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	owned := make([]byte, len(value))
	copy(owned, value)
	return owned, true, nil
}

// Iterate walks the visible state in ascending key order, beginning strictly
// after after. Callback data is borrowed until the callback returns.
func (o *Overlay) Iterate(ctx context.Context, after *[32]byte, fn func([32]byte, []byte) bool) error {
	if fn == nil {
		return errors.New("ledger overlay: nil iteration callback")
	}
	v, err := o.capture()
	if err != nil {
		return err
	}
	defer v.release()
	return v.iterate(ctx, after, true, func(key [32]byte, value []byte, deleted bool) bool {
		if deleted {
			return true
		}
		return fn(key, value)
	})
}

// Fork creates a mutable child sharing immutable tree nodes and spill runs.
// Later changes in either overlay use copy-on-write and cannot alter the other
// overlay's captured snapshot.
func (o *Overlay) Fork() (*Overlay, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrClosed
	}
	child := &Overlay{base: o.base, cfg: o.cfg, budget: o.budget, root: retainTreeNode(o.root)}
	child.segments = make([]*spillSegment, len(o.segments))
	for i, segment := range o.segments {
		child.segments[i] = retainSegment(segment)
	}
	return child, nil
}

// Snapshot captures an immutable read lease of the current state.
func (o *Overlay) Snapshot() (*Snapshot, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return nil, ErrClosed
	}
	snapshot := &Snapshot{base: o.base, budget: o.budget, root: retainTreeNode(o.root)}
	snapshot.segments = make([]*spillSegment, len(o.segments))
	for i, segment := range o.segments {
		snapshot.segments[i] = retainSegment(segment)
	}
	return snapshot, nil
}

// Close releases the overlay's root and spill ownership. It is idempotent.
func (o *Overlay) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	root, segments := o.root, o.segments
	o.root = nil
	o.segments = nil
	o.mu.Unlock()
	releaseTreeNode(root)
	for _, segment := range segments {
		releaseSegment(segment)
	}
	return nil
}

// Close releases a snapshot lease. It is idempotent.
func (s *Snapshot) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	root, segments := s.root, s.segments
	s.root = nil
	s.segments = nil
	s.mu.Unlock()
	releaseTreeNode(root)
	for _, segment := range segments {
		releaseSegment(segment)
	}
	return nil
}

func (o *Overlay) capture() (*view, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return nil, ErrClosed
	}
	v := &view{base: o.base, budget: o.budget, root: retainTreeNode(o.root)}
	v.segments = make([]*spillSegment, len(o.segments))
	for i, segment := range o.segments {
		v.segments[i] = retainSegment(segment)
	}
	return v, nil
}

func (s *Snapshot) capture() (*view, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	v := &view{base: s.base, budget: s.budget, root: retainTreeNode(s.root)}
	v.segments = make([]*spillSegment, len(s.segments))
	for i, segment := range s.segments {
		v.segments[i] = retainSegment(segment)
	}
	return v, nil
}

func (v *view) release() {
	if v == nil {
		return
	}
	releaseTreeNode(v.root)
	for _, segment := range v.segments {
		releaseSegment(segment)
	}
	v.root = nil
	v.segments = nil
}

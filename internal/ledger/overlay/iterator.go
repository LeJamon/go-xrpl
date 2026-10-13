package overlay

import (
	"bytes"
	"context"
	"errors"
	"sync"
)

type changeCursor interface {
	next(context.Context) ([32]byte, []byte, bool, bool, error)
	close() error
}

type treeChangeCursor struct {
	cursor *treeCursor
	closed bool
}

func (c *treeChangeCursor) next(ctx context.Context) ([32]byte, []byte, bool, bool, error) {
	var key [32]byte
	if c == nil || c.closed {
		return key, nil, false, false, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return key, nil, false, false, err
	}
	key, value, deleted, ok := c.cursor.next()
	return key, value, deleted, ok, nil
}

func (c *treeChangeCursor) close() error {
	if c != nil {
		c.closed = true
	}
	return nil
}

type pullBaseCursor struct {
	cursor  Cursor
	owner   *budget
	last    [32]byte
	hasLast bool
	current uint64
	closed  bool
}

func (c *pullBaseCursor) releaseCurrent() {
	if c.current != 0 {
		c.owner.releaseMemory(c.current)
		c.current = 0
	}
}

func (c *pullBaseCursor) next(ctx context.Context) ([32]byte, []byte, bool, bool, error) {
	var key [32]byte
	if c == nil || c.closed {
		return key, nil, false, false, ErrClosed
	}
	c.releaseCurrent()
	if err := ctx.Err(); err != nil {
		return key, nil, false, false, err
	}
	key, borrowed, ok, err := c.cursor.Next(ctx)
	if err != nil || !ok {
		return key, nil, false, ok, err
	}
	if c.hasLast && bytes.Compare(key[:], c.last[:]) <= 0 {
		return key, nil, false, false, ErrOrdering
	}
	charge, valid := addCharge(workspaceStream, uint64(len(borrowed)))
	if !valid || !c.owner.reserveMemory(charge) {
		return key, nil, false, false, ErrMemoryBudget
	}
	value := make([]byte, len(borrowed))
	copy(value, borrowed)
	c.current = charge
	c.last = key
	c.hasLast = true
	return key, value, false, true, nil
}

func (c *pullBaseCursor) close() error {
	if c == nil || c.closed {
		return nil
	}
	c.closed = true
	c.releaseCurrent()
	return c.cursor.Close()
}

type cursorItem struct {
	key     [32]byte
	value   []byte
	deleted bool
	valid   bool
}

func (v *view) openCursors(ctx context.Context, after *[32]byte, includeBase bool) ([]changeCursor, uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	count := len(v.segments)
	if v.root != nil {
		count++
	}
	if includeBase {
		count++
	}
	workspace, ok := addCharge(workspaceBase, uint64(count)*workspaceStream)
	if !ok || !v.budget.reserveMemory(workspace) {
		return nil, 0, ErrMemoryBudget
	}
	cursors := make([]changeCursor, 0, count)
	closeCursors := func() {
		for _, cursor := range cursors {
			_ = cursor.close()
		}
		v.budget.releaseMemory(workspace)
	}
	if v.root != nil {
		cursors = append(cursors, &treeChangeCursor{cursor: newTreeCursor(v.root, after)})
	}
	for _, segment := range v.segments {
		cursor, err := segment.cursor(ctx, v.budget, after)
		if err != nil {
			closeCursors()
			return nil, 0, err
		}
		cursors = append(cursors, cursor)
	}
	if includeBase {
		var cursorAfter *[32]byte
		if after != nil {
			afterCopy := *after
			cursorAfter = &afterCopy
		}
		cursor, err := v.base.OpenCursor(ctx, cursorAfter)
		if err != nil {
			closeCursors()
			return nil, 0, err
		}
		cursors = append(cursors, &pullBaseCursor{cursor: cursor, owner: v.budget})
	}
	return cursors, workspace, nil
}

func closeCursors(cursors []changeCursor, workspace uint64, owner *budget) error {
	var first error
	for _, cursor := range cursors {
		if err := cursor.close(); err != nil && first == nil {
			first = err
		}
	}
	owner.releaseMemory(workspace)
	return first
}

func nextItems(ctx context.Context, cursors []changeCursor, items []cursorItem) error {
	for i, cursor := range cursors {
		key, value, deleted, ok, err := cursor.next(ctx)
		if err != nil {
			return err
		}
		items[i] = cursorItem{key: key, value: value, deleted: deleted, valid: ok}
	}
	return nil
}

func advanceItem(ctx context.Context, cursor changeCursor, item *cursorItem) error {
	key, value, deleted, ok, err := cursor.next(ctx)
	if err != nil {
		return err
	}
	item.key, item.value, item.deleted, item.valid = key, value, deleted, ok
	return nil
}

func mergeCursors(ctx context.Context, cursors []changeCursor, fn func([32]byte, []byte, bool) bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	items := make([]cursorItem, len(cursors))
	if err := nextItems(ctx, cursors, items); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lowest := -1
		for i, item := range items {
			if !item.valid {
				continue
			}
			if lowest < 0 || bytes.Compare(item.key[:], items[lowest].key[:]) < 0 {
				lowest = i
			}
		}
		if lowest < 0 {
			return nil
		}
		key := items[lowest].key
		chosen := lowest
		for i := lowest + 1; i < len(items); i++ {
			if items[i].valid && items[i].key == key {
				// Sources are ordered newest to oldest, so the first source wins.
				if i < chosen {
					chosen = i
				}
			}
		}
		if !items[chosen].deleted && !fn(key, items[chosen].value, items[chosen].deleted) {
			return nil
		}
		if items[chosen].deleted && !fn(key, nil, true) {
			return nil
		}
		for i := range items {
			if items[i].valid && items[i].key == key {
				if err := advanceItem(ctx, cursors[i], &items[i]); err != nil {
					return err
				}
			}
		}
	}
}

func (v *view) iterate(ctx context.Context, after *[32]byte, includeBase bool, fn func([32]byte, []byte, bool) bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cursors, workspace, err := v.openCursors(ctx, after, includeBase)
	if err != nil {
		return err
	}
	mergeErr := mergeCursors(ctx, cursors, fn)
	closeErr := closeCursors(cursors, workspace, v.budget)
	if mergeErr != nil {
		return mergeErr
	}
	return closeErr
}

// Changes returns an explicit pull iterator over final coalesced overlay
// changes. Its sources are newest-first and no base values are included.
func (o *Overlay) Changes(ctx context.Context, after *[32]byte) (*ChangeIterator, error) {
	v, err := o.capture()
	if err != nil {
		return nil, err
	}
	return newChangeIterator(ctx, v, after, false)
}

// Iterate implements Reader for Snapshot.
func (s *Snapshot) Get(ctx context.Context, key [32]byte) ([]byte, bool, error) {
	v, err := s.capture()
	if err != nil {
		return nil, false, err
	}
	defer v.release()
	return v.get(ctx, key)
}

func (s *Snapshot) Iterate(ctx context.Context, after *[32]byte, fn func([32]byte, []byte) bool) error {
	if fn == nil {
		return errors.New("ledger overlay: nil iteration callback")
	}
	v, err := s.capture()
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

type ChangeIterator struct {
	mu sync.Mutex

	view      view
	cursors   []changeCursor
	items     []cursorItem
	workspace uint64
	owner     *budget
	closed    bool
	lastErr   error
}

func newChangeIterator(ctx context.Context, v view, after *[32]byte, includeBase bool) (*ChangeIterator, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cursors, workspace, err := v.openCursors(ctx, after, includeBase)
	if err != nil {
		v.release()
		return nil, err
	}
	items := make([]cursorItem, len(cursors))
	if err := nextItems(ctx, cursors, items); err != nil {
		_ = closeCursors(cursors, workspace, v.budget)
		v.release()
		return nil, err
	}
	return &ChangeIterator{view: v, cursors: cursors, items: items, workspace: workspace, owner: v.budget}, nil
}

// Next returns the next final overlay change. The returned value is owned by
// the caller. done is true after the final change.
func (it *ChangeIterator) Next(ctx context.Context) (change Change, done bool, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.closed {
		return Change{}, false, ErrClosed
	}
	if it.lastErr != nil {
		return Change{}, false, it.lastErr
	}
	if err := ctx.Err(); err != nil {
		it.lastErr = err
		return Change{}, false, err
	}
	lowest := -1
	for i, item := range it.items {
		if !item.valid {
			continue
		}
		if lowest < 0 || bytes.Compare(item.key[:], it.items[lowest].key[:]) < 0 {
			lowest = i
		}
	}
	if lowest < 0 {
		return Change{}, true, nil
	}
	key := it.items[lowest].key
	chosen := lowest
	for i := lowest + 1; i < len(it.items); i++ {
		if it.items[i].valid && it.items[i].key == key && i < chosen {
			chosen = i
		}
	}
	change.Key = key
	change.Deleted = it.items[chosen].deleted
	if !change.Deleted {
		change.Value = make([]byte, len(it.items[chosen].value))
		copy(change.Value, it.items[chosen].value)
	}
	for i := range it.items {
		if it.items[i].valid && it.items[i].key == key {
			if advanceErr := advanceItem(ctx, it.cursors[i], &it.items[i]); advanceErr != nil {
				it.lastErr = advanceErr
				return Change{}, false, advanceErr
			}
		}
	}
	return change, false, nil
}

// Close releases iterator buffers, source handles, workspace admission, and
// its snapshot lease. It is idempotent.
func (it *ChangeIterator) Close() error {
	if it == nil {
		return nil
	}
	it.mu.Lock()
	if it.closed {
		it.mu.Unlock()
		return nil
	}
	it.closed = true
	cursors, workspace, owner, captured := it.cursors, it.workspace, it.owner, it.view
	it.cursors, it.items = nil, nil
	it.view = view{}
	it.mu.Unlock()
	err := closeCursors(cursors, workspace, owner)
	captured.release()
	return err
}

var _ Reader = (*Overlay)(nil)
var _ Reader = (*Snapshot)(nil)

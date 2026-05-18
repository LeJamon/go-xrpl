package shamap

// lruElem is one entry in an intrusive doubly-linked list used by the
// in-package LRU caches. Inlining the prev/next pointers and avoiding
// the container/list any-typed Value field eliminates the per-Get type
// assertion and per-PushFront list.Element heap allocation that the
// original implementations of TreeNodeCache, FullBelowCache and
// CachingSyncFilter all paid on the hot path.
type lruElem[K comparable, V any] struct {
	key  K
	val  V
	prev *lruElem[K, V]
	next *lruElem[K, V]
}

// lruList implements an intrusive doubly-linked list of *lruElem.
// Sentinel head/tail nodes simplify the boundary cases; head.next is
// the most-recently-used entry, tail.prev the least.
type lruList[K comparable, V any] struct {
	head lruElem[K, V] // sentinel; head.next is MRU
	tail lruElem[K, V] // sentinel; tail.prev is LRU
	len  int
}

func newLRUList[K comparable, V any]() *lruList[K, V] {
	l := &lruList[K, V]{}
	l.head.next = &l.tail
	l.tail.prev = &l.head
	return l
}

// pushFront inserts an element at the front (MRU position).
func (l *lruList[K, V]) pushFront(e *lruElem[K, V]) {
	e.prev = &l.head
	e.next = l.head.next
	l.head.next.prev = e
	l.head.next = e
	l.len++
}

// moveToFront promotes an element to MRU.
func (l *lruList[K, V]) moveToFront(e *lruElem[K, V]) {
	if l.head.next == e {
		return
	}
	// Detach.
	e.prev.next = e.next
	e.next.prev = e.prev
	// Reinsert at front.
	e.prev = &l.head
	e.next = l.head.next
	l.head.next.prev = e
	l.head.next = e
}

// remove drops an element from the list.
func (l *lruList[K, V]) remove(e *lruElem[K, V]) {
	e.prev.next = e.next
	e.next.prev = e.prev
	e.prev = nil
	e.next = nil
	l.len--
}

// back returns the LRU entry, or nil if the list is empty.
func (l *lruList[K, V]) back() *lruElem[K, V] {
	if l.tail.prev == &l.head {
		return nil
	}
	return l.tail.prev
}

// front returns the MRU entry, or nil if the list is empty.
func (l *lruList[K, V]) front() *lruElem[K, V] {
	if l.head.next == &l.tail {
		return nil
	}
	return l.head.next
}

// next walks from MRU toward LRU; returns nil at the end.
func (l *lruList[K, V]) next(e *lruElem[K, V]) *lruElem[K, V] {
	if e == nil || e.next == &l.tail {
		return nil
	}
	return e.next
}

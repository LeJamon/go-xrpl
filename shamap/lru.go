package shamap

// lruElem is one entry in an intrusive doubly-linked list used by the
// in-package LRU caches.
type lruElem[K comparable, V any] struct {
	key  K
	val  V
	prev *lruElem[K, V]
	next *lruElem[K, V]
}

// lruList is an intrusive doubly-linked list with sentinel head/tail.
// head.next is MRU; tail.prev is LRU.
type lruList[K comparable, V any] struct {
	head lruElem[K, V]
	tail lruElem[K, V]
	len  int
}

func newLRUList[K comparable, V any]() *lruList[K, V] {
	l := &lruList[K, V]{}
	l.head.next = &l.tail
	l.tail.prev = &l.head
	return l
}

func (l *lruList[K, V]) pushFront(e *lruElem[K, V]) {
	e.prev = &l.head
	e.next = l.head.next
	l.head.next.prev = e
	l.head.next = e
	l.len++
}

func (l *lruList[K, V]) moveToFront(e *lruElem[K, V]) {
	if l.head.next == e {
		return
	}
	e.prev.next = e.next
	e.next.prev = e.prev
	e.prev = &l.head
	e.next = l.head.next
	l.head.next.prev = e
	l.head.next = e
}

func (l *lruList[K, V]) remove(e *lruElem[K, V]) {
	e.prev.next = e.next
	e.next.prev = e.prev
	e.prev = nil
	e.next = nil
	l.len--
}

func (l *lruList[K, V]) back() *lruElem[K, V] {
	if l.tail.prev == &l.head {
		return nil
	}
	return l.tail.prev
}

func (l *lruList[K, V]) front() *lruElem[K, V] {
	if l.head.next == &l.tail {
		return nil
	}
	return l.head.next
}

func (l *lruList[K, V]) next(e *lruElem[K, V]) *lruElem[K, V] {
	if e == nil || e.next == &l.tail {
		return nil
	}
	return e.next
}

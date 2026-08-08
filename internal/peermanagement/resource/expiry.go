package resource

import (
	"container/heap"
	"time"
)

type expiryKind uint8

const (
	expireEntry expiryKind = iota
	expireImport
)

type expiryItem struct {
	when   time.Time
	kind   expiryKind
	entry  *entry
	origin string
	index  int
}

type expiryQueue []*expiryItem

func (q expiryQueue) Len() int { return len(q) }

func (q expiryQueue) Less(i, j int) bool { return q[i].when.Before(q[j].when) }

func (q expiryQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *expiryQueue) Push(value any) {
	item := value.(*expiryItem)
	item.index = len(*q)
	*q = append(*q, item)
}

func (q *expiryQueue) Pop() any {
	old := *q
	last := len(old) - 1
	item := old[last]
	old[last] = nil
	item.index = -1
	*q = old[:last]
	return item
}

func (m *Manager) scheduleEntryExpiryLocked(e *entry, when time.Time) {
	if e.expiry == nil {
		e.expiry = &expiryItem{when: when, kind: expireEntry, entry: e, index: -1}
		heap.Push(&m.expiries, e.expiry)
		return
	}
	e.expiry.when = when
	heap.Fix(&m.expiries, e.expiry.index)
}

func (m *Manager) cancelEntryExpiryLocked(e *entry) {
	if e.expiry == nil {
		return
	}
	heap.Remove(&m.expiries, e.expiry.index)
	e.expiry = nil
}

func (m *Manager) expireLocked(now time.Time, limit int) {
	for limit > 0 && len(m.expiries) > 0 && !now.Before(m.expiries[0].when) {
		item := heap.Pop(&m.expiries).(*expiryItem)
		limit--
		switch item.kind {
		case expireEntry:
			e := item.entry
			if e.expiry != item {
				continue
			}
			e.expiry = nil
			if e.localRefs == 0 && e.importRefs == 0 {
				delete(m.entries, e.k)
				m.stats.evictions++
			}
		case expireImport:
			rec := m.imports[item.origin]
			if rec == nil || rec.expiry != item {
				continue
			}
			m.removeImportLocked(item.origin, rec, now)
		}
	}
}

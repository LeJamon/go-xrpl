package nodestore

import (
	"container/list"
	"sync"
	"time"
)

type negativeCacheEntry struct {
	hash      Hash256
	expiresAt time.Time
}

type negativeCache struct {
	mu      sync.Mutex
	entries map[Hash256]*list.Element
	order   list.List
	ttl     time.Duration
	maxSize int
}

func newNegativeCache(ttl time.Duration, maxSize int) *negativeCache {
	return &negativeCache{
		entries: make(map[Hash256]*list.Element),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (c *negativeCache) MarkMissing(hash Hash256) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.sweepExpiredLocked(now)
	if element, ok := c.entries[hash]; ok {
		entry := element.Value.(*negativeCacheEntry)
		entry.expiresAt = now.Add(c.ttl)
		c.order.MoveToBack(element)
		return
	}
	if c.maxSize > 0 && len(c.entries) >= c.maxSize {
		c.removeElementLocked(c.order.Front())
	}
	entry := &negativeCacheEntry{hash: hash, expiresAt: now.Add(c.ttl)}
	c.entries[hash] = c.order.PushBack(entry)
}

func (c *negativeCache) IsMissing(hash Hash256) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[hash]
	if !ok {
		return false
	}
	if time.Now().After(element.Value.(*negativeCacheEntry).expiresAt) {
		c.removeElementLocked(element)
		return false
	}
	return true
}

func (c *negativeCache) Remove(hash Hash256) {
	c.mu.Lock()
	if element, ok := c.entries[hash]; ok {
		c.removeElementLocked(element)
	}
	c.mu.Unlock()
}

func (c *negativeCache) Clear() {
	c.mu.Lock()
	clear(c.entries)
	c.order.Init()
	c.mu.Unlock()
}

func (c *negativeCache) Sweep() int {
	c.mu.Lock()
	removed := c.sweepExpiredLocked(time.Now())
	c.mu.Unlock()
	return removed
}

func (c *negativeCache) sweepExpiredLocked(now time.Time) int {
	removed := 0
	for element := c.order.Front(); element != nil; element = c.order.Front() {
		entry := element.Value.(*negativeCacheEntry)
		if now.Before(entry.expiresAt) {
			break
		}
		c.removeElementLocked(element)
		removed++
	}
	return removed
}

func (c *negativeCache) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	delete(c.entries, element.Value.(*negativeCacheEntry).hash)
	c.order.Remove(element)
}

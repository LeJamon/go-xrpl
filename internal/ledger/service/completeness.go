package service

import (
	"fmt"
	"sort"
	"strings"
)

type ledgerRange struct {
	start uint32
	end   uint32
}

func (r ledgerRange) contains(seq uint32) bool {
	return seq >= r.start && seq <= r.end
}

func (r ledgerRange) String() string {
	if r.start == r.end {
		return fmt.Sprintf("%d", r.start)
	}
	return fmt.Sprintf("%d-%d", r.start, r.end)
}

type completeLedgerSet struct {
	ranges []ledgerRange
}

func newCompleteLedgerSet() *completeLedgerSet {
	return &completeLedgerSet{}
}

func (c *completeLedgerSet) add(seq uint32) {
	c.addRange(seq, seq)
}

func (c *completeLedgerSet) addRange(start, end uint32) {
	if start > end {
		return
	}

	next := ledgerRange{start: start, end: end}
	merged := make([]ledgerRange, 0, len(c.ranges)+1)
	inserted := false
	for _, current := range c.ranges {
		if current.end < next.start && next.start-current.end > 1 {
			merged = append(merged, current)
			continue
		}
		if next.end < current.start && current.start-next.end > 1 {
			if !inserted {
				merged = append(merged, next)
				inserted = true
			}
			merged = append(merged, current)
			continue
		}
		next.start = min(next.start, current.start)
		next.end = max(next.end, current.end)
	}
	if !inserted {
		merged = append(merged, next)
	}
	c.ranges = merged
}

func (c *completeLedgerSet) remove(seq uint32) {
	c.removeRange(seq, seq)
}

func (c *completeLedgerSet) removeRange(start, end uint32) {
	if start > end {
		return
	}

	remaining := make([]ledgerRange, 0, len(c.ranges)+1)
	for _, current := range c.ranges {
		if current.end < start || current.start > end {
			remaining = append(remaining, current)
			continue
		}
		if current.start < start {
			remaining = append(remaining, ledgerRange{start: current.start, end: start - 1})
		}
		if current.end > end {
			remaining = append(remaining, ledgerRange{start: end + 1, end: current.end})
		}
	}
	c.ranges = remaining
}

func (c *completeLedgerSet) contains(seq uint32) bool {
	index := sort.Search(len(c.ranges), func(i int) bool {
		return c.ranges[i].end >= seq
	})
	return index < len(c.ranges) && c.ranges[index].contains(seq)
}

func (c *completeLedgerSet) String() string {
	if len(c.ranges) == 0 {
		return "empty"
	}

	parts := make([]string, len(c.ranges))
	for i, current := range c.ranges {
		parts[i] = current.String()
	}
	return strings.Join(parts, ",")
}

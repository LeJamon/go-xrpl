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
	_, ok := c.rangeContaining(seq)
	return ok
}

func (c *completeLedgerSet) rangeContaining(seq uint32) (ledgerRange, bool) {
	index := sort.Search(len(c.ranges), func(i int) bool {
		return c.ranges[i].end >= seq
	})
	if index >= len(c.ranges) || !c.ranges[index].contains(seq) {
		return ledgerRange{}, false
	}
	return c.ranges[index], true
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

// trimPendingValidatedRange removes ledgers whose persistence is still in
// progress from a complete validated range. The range keeps its widest
// contiguous tip first, then chooses the larger remaining side for interior
// gaps, matching rippled's validated-range lookup behavior.
func trimPendingValidatedRange(minVal, maxVal uint32, pending []uint32) (uint32, uint32) {
	if len(pending) == 0 || (minVal == 0 && maxVal == 0) {
		return minVal, maxVal
	}

	sort.Slice(pending, func(i, j int) bool { return pending[i] < pending[j] })
	isPending := func(seq uint32) bool {
		index := sort.Search(len(pending), func(i int) bool { return pending[i] >= seq })
		return index < len(pending) && pending[index] == seq
	}

	for maxVal > 0 && isPending(maxVal) {
		maxVal--
	}
	for minVal <= maxVal && isPending(minVal) {
		minVal++
	}
	if minVal > maxVal {
		return 0, 0
	}

	for _, seq := range pending {
		if seq < minVal || seq > maxVal {
			continue
		}
		if seq > minVal+(maxVal-minVal)/2 {
			maxVal = seq - 1
		} else {
			minVal = seq + 1
		}
		if minVal > maxVal {
			return 0, 0
		}
	}

	return minVal, maxVal
}

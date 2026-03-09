package manager

import (
	"fmt"
	"sort"
	"strings"
)

// LedgerRange represents an inclusive range of ledger sequence numbers
type LedgerRange struct {
	Start, End uint32
}

// Contains checks if a sequence number is within this range
func (r LedgerRange) Contains(seq uint32) bool {
	return seq >= r.Start && seq <= r.End
}

// Length returns the number of ledgers in this range
func (r LedgerRange) Length() uint32 {
	return r.End - r.Start + 1
}

// String returns a string representation of the range
func (r LedgerRange) String() string {
	if r.Start == r.End {
		return fmt.Sprintf("%d", r.Start)
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End)
}

// CompleteLedgerSet efficiently tracks which ledger sequences are available
// using a sorted list of non-overlapping ranges
type CompleteLedgerSet struct {
	ranges []LedgerRange
}

// NewCompleteLedgerSet creates a new empty completeness tracker
func NewCompleteLedgerSet() *CompleteLedgerSet {
	return &CompleteLedgerSet{
		ranges: make([]LedgerRange, 0),
	}
}

// Add marks a single ledger sequence as complete
func (c *CompleteLedgerSet) Add(seq uint32) {
	c.AddRange(seq, seq)
}

// AddRange marks a range of ledger sequences as complete
func (c *CompleteLedgerSet) AddRange(start, end uint32) {
	if start > end {
		return
	}

	newRange := LedgerRange{Start: start, End: end}
	c.ranges = c.mergeRange(c.ranges, newRange)
}

// Contains checks if a ledger sequence is marked as complete
func (c *CompleteLedgerSet) Contains(seq uint32) bool {
	// Binary search for efficiency
	idx := sort.Search(len(c.ranges), func(i int) bool {
		return c.ranges[i].End >= seq
	})

	return idx < len(c.ranges) && c.ranges[idx].Contains(seq)
}

// Range returns the overall min and max sequence numbers, and whether any exist
func (c *CompleteLedgerSet) Range() (min, max uint32, hasAny bool) {
	if len(c.ranges) == 0 {
		return 0, 0, false
	}

	return c.ranges[0].Start, c.ranges[len(c.ranges)-1].End, true
}

// FindMissing returns all missing sequence numbers in the given range
func (c *CompleteLedgerSet) FindMissing(start, end uint32) []uint32 {
	if start > end {
		return nil
	}

	var missing []uint32
	current := start

	for _, r := range c.ranges {
		// Skip ranges that end before our search range
		if r.End < start {
			continue
		}

		// Stop if this range starts after our search range
		if r.Start > end {
			break
		}

		// Add missing sequences before this range
		rangeStart := r.Start
		if rangeStart > end {
			rangeStart = end + 1 // Don't go beyond our search range
		}

		for current < rangeStart && current <= end {
			missing = append(missing, current)
			current++
		}

		// Skip past this complete range
		if r.End >= current {
			current = r.End + 1
		}
	}

	// Add any remaining missing sequences after the last range
	for current <= end {
		missing = append(missing, current)
		current++
	}

	return missing
}

// FindNextMissing finds the next missing sequence after the given sequence
func (c *CompleteLedgerSet) FindNextMissing(after uint32) (uint32, bool) {
	missing := c.FindMissing(after+1, after+1000) // Look ahead reasonably
	if len(missing) > 0 {
		return missing[0], true
	}
	return 0, false
}

// Count returns the total number of complete ledger sequences
func (c *CompleteLedgerSet) Count() uint32 {
	var total uint32
	for _, r := range c.ranges {
		total += r.Length()
	}
	return total
}

// Clear removes all completeness information
func (c *CompleteLedgerSet) Clear() {
	c.ranges = c.ranges[:0] // Keep underlying capacity
}

// String returns a human-readable representation of the complete ranges
func (c *CompleteLedgerSet) String() string {
	if len(c.ranges) == 0 {
		return "empty"
	}

	var parts []string
	for _, r := range c.ranges {
		parts = append(parts, r.String())
	}

	return strings.Join(parts, ",")
}

// mergeRange inserts a new range into the sorted list, merging overlapping ranges
func (c *CompleteLedgerSet) mergeRange(ranges []LedgerRange, newRange LedgerRange) []LedgerRange {
	if len(ranges) == 0 {
		return []LedgerRange{newRange}
	}

	var result []LedgerRange
	merged := false

	for i, existing := range ranges {
		// If new range comes before this range and doesn't overlap
		if !merged && newRange.End+1 < existing.Start {
			result = append(result, newRange)
			result = append(result, ranges[i:]...)
			merged = true
			break
		}

		// If ranges overlap or are adjacent, merge them
		if !merged && c.rangesOverlapOrAdjacent(newRange, existing) {
			// Start merging - extend newRange to include existing
			newRange.Start = min(newRange.Start, existing.Start)
			newRange.End = max(newRange.End, existing.End)

			// Continue merging with subsequent ranges if they overlap
			for j := i + 1; j < len(ranges) && c.rangesOverlapOrAdjacent(newRange, ranges[j]); j++ {
				newRange.End = max(newRange.End, ranges[j].End)
				i = j // Skip these ranges
			}

			result = append(result, newRange)
			result = append(result, ranges[i+1:]...)
			merged = true
			break
		}

		// No overlap, keep existing range
		result = append(result, existing)
	}

	// If we haven't merged yet, add new range at the end
	if !merged {
		result = append(result, newRange)
	}

	return result
}

// rangesOverlapOrAdjacent checks if two ranges overlap or are adjacent
func (c *CompleteLedgerSet) rangesOverlapOrAdjacent(a, b LedgerRange) bool {
	// Ranges overlap if one starts before the other ends (with 1-sequence adjacency)
	return a.Start <= b.End+1 && b.Start <= a.End+1
}

// min returns the minimum of two uint32 values
func min(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two uint32 values
func max(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

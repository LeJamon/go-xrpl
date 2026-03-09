package shamap

import (
	"fmt"
)

// DifferenceType represents the type of difference between two SHAMapItems
type DifferenceType int

const (
	DiffAdded DifferenceType = iota
	DiffRemoved
	DiffModified
)

// String returns a string representation of the difference type
func (dt DifferenceType) String() string {
	switch dt {
	case DiffAdded:
		return "added"
	case DiffRemoved:
		return "removed"
	case DiffModified:
		return "modified"
	default:
		return fmt.Sprintf("unknown(%d)", int(dt))
	}
}

// DifferenceItem represents a single difference between two SHAMaps
type DifferenceItem struct {
	Key        [32]byte
	Type       DifferenceType
	FirstItem  *Item // Item from first map (nil if added in second)
	SecondItem *Item // Item from second map (nil if removed from second)
}

// String returns a string representation of the difference item
func (di *DifferenceItem) String() string {
	return fmt.Sprintf("%s: key=%x", di.Type, di.Key)
}

// DifferenceSet contains all differences found between two SHAMaps
type DifferenceSet struct {
	Differences []DifferenceItem
	Complete    bool // true if all differences found, false if truncated due to maxCount
}

// Len returns the number of differences
func (ds *DifferenceSet) Len() int {
	return len(ds.Differences)
}

// IsEmpty returns true if there are no differences
func (ds *DifferenceSet) IsEmpty() bool {
	return len(ds.Differences) == 0
}

// String returns a human-readable representation of the differences
func (ds *DifferenceSet) String() string {
	result := fmt.Sprintf("DifferenceSet: %d differences", len(ds.Differences))
	if !ds.Complete {
		result += " (truncated)"
	}
	result += "\n"

	for i, diff := range ds.Differences {
		result += fmt.Sprintf("  [%d] %s\n", i, diff.String())
	}

	return result
}

// AddDifference adds a difference to the set
func (ds *DifferenceSet) AddDifference(key [32]byte, diffType DifferenceType, first, second *Item) {
	ds.Differences = append(ds.Differences, DifferenceItem{
		Key:        key,
		Type:       diffType,
		FirstItem:  first,
		SecondItem: second,
	})
}

// HasMore returns true if there might be more differences (i.e., truncated)
func (ds *DifferenceSet) HasMore() bool {
	return !ds.Complete
}

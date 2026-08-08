package statecompare

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// ComparableStateEntry is one decoded ledger-state entry used for comparison.
type ComparableStateEntry struct {
	Index   string
	DataHex string
	Decoded map[string]any
}

// StateModification describes one state entry whose serialized data changed.
type StateModification struct {
	Index       string
	OldDataHex  string
	NewDataHex  string
	OldDecoded  map[string]any
	NewDecoded  map[string]any
	ChangedKeys []string
}

// StateComparison contains stable, index-sorted state differences.
type StateComparison struct {
	Added     []ComparableStateEntry
	Removed   []ComparableStateEntry
	Modified  []StateModification
	Unchanged []ComparableStateEntry
}

// CompareStates compares two state snapshots by case-insensitive ledger index
// and serialized data. Case-colliding ledger indexes are rejected.
func CompareStates(before, after map[string]ComparableStateEntry) (StateComparison, error) {
	var err error
	before, err = normalizeStateEntries(before)
	if err != nil {
		return StateComparison{}, fmt.Errorf("normalizing before state: %w", err)
	}
	after, err = normalizeStateEntries(after)
	if err != nil {
		return StateComparison{}, fmt.Errorf("normalizing after state: %w", err)
	}

	var result StateComparison
	for key, newEntry := range after {
		oldEntry, exists := before[key]
		switch {
		case !exists:
			result.Added = append(result.Added, newEntry)
		case !strings.EqualFold(oldEntry.DataHex, newEntry.DataHex):
			result.Modified = append(result.Modified, StateModification{
				Index:       newEntry.Index,
				OldDataHex:  oldEntry.DataHex,
				NewDataHex:  newEntry.DataHex,
				OldDecoded:  oldEntry.Decoded,
				NewDecoded:  newEntry.Decoded,
				ChangedKeys: ChangedStateKeys(oldEntry.Decoded, newEntry.Decoded),
			})
		default:
			result.Unchanged = append(result.Unchanged, newEntry)
		}
	}
	for key, oldEntry := range before {
		if _, exists := after[key]; !exists {
			result.Removed = append(result.Removed, oldEntry)
		}
	}
	sort.Slice(result.Added, func(i, j int) bool { return result.Added[i].Index < result.Added[j].Index })
	sort.Slice(result.Removed, func(i, j int) bool { return result.Removed[i].Index < result.Removed[j].Index })
	sort.Slice(result.Modified, func(i, j int) bool { return result.Modified[i].Index < result.Modified[j].Index })
	sort.Slice(result.Unchanged, func(i, j int) bool { return result.Unchanged[i].Index < result.Unchanged[j].Index })
	return result, nil
}

func normalizeStateEntries(entries map[string]ComparableStateEntry) (map[string]ComparableStateEntry, error) {
	normalized := make(map[string]ComparableStateEntry, len(entries))
	for key, entry := range entries {
		canonical := strings.ToLower(key)
		if _, exists := normalized[canonical]; exists {
			return nil, fmt.Errorf("duplicate canonical ledger index %q", canonical)
		}
		normalized[canonical] = entry
	}
	return normalized, nil
}

// ChangedStateKeys returns the sorted top-level decoded fields that differ.
func ChangedStateKeys(old, new map[string]any) []string {
	if old == nil || new == nil {
		return nil
	}
	keys := make(map[string]struct{}, len(old)+len(new))
	for key := range old {
		keys[key] = struct{}{}
	}
	for key := range new {
		keys[key] = struct{}{}
	}
	changed := make([]string, 0)
	for key := range keys {
		oldValue, oldExists := old[key]
		newValue, newExists := new[key]
		if !oldExists || !newExists || !reflect.DeepEqual(oldValue, newValue) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

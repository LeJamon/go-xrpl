package replaytool

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
)

// ComparableStateEntry is a decoded ledger-state entry used by CompareStates.
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

// StateComparison is the typed result of comparing two ledger-state snapshots.
type StateComparison struct {
	Added     []ComparableStateEntry
	Removed   []ComparableStateEntry
	Modified  []StateModification
	Unchanged []ComparableStateEntry
}

// CompareStates compares two state snapshots by case-insensitive ledger index
// and serialized data, returning stable index-sorted result slices.
func CompareStates(before, after map[string]ComparableStateEntry) StateComparison {
	before = normalizeStateEntries(before)
	after = normalizeStateEntries(after)

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
	return result
}

func normalizeStateEntries(entries map[string]ComparableStateEntry) map[string]ComparableStateEntry {
	normalized := make(map[string]ComparableStateEntry, len(entries))
	for key, entry := range entries {
		normalized[strings.ToLower(key)] = entry
	}
	return normalized
}

// ChangedStateKeys returns the sorted top-level decoded fields whose values
// differ. If either entry is undecodable, it returns nil.
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

// computeStateDiff diffs two state snapshots — each keyed by lowercase-hex index
// with a hex-encoded entry as the value — into the added / modified / removed
// shape shared by the `replay` and `replay-range` debug dumps. Added and
// modified entries carry their decoded JSON for readability. Hex comparison is
// case-insensitive. This is the single source of the diff semantics the two
// dumpers (and `xrpld compare`) used to each maintain separately.
func computeStateDiff(pre, post map[string]string) map[string]any {
	before := make(map[string]ComparableStateEntry, len(pre))
	for key, dataHex := range pre {
		lower := strings.ToLower(key)
		before[lower] = ComparableStateEntry{
			Index:   lower,
			DataHex: dataHex,
			Decoded: decodeEntryData(dataHex),
		}
	}
	after := make(map[string]ComparableStateEntry, len(post))
	for key, dataHex := range post {
		after[strings.ToLower(key)] = ComparableStateEntry{
			Index:   key,
			DataHex: dataHex,
			Decoded: decodeEntryData(dataHex),
		}
	}
	comparison := CompareStates(before, after)

	diff := map[string]any{
		"added":    make([]map[string]any, 0),
		"modified": make([]map[string]any, 0),
		"removed":  make([]string, 0),
	}
	for _, added := range comparison.Added {
		entry := map[string]any{"index": added.Index, "data_hex": added.DataHex}
		if added.Decoded != nil {
			entry["decoded"] = added.Decoded
		}
		diff["added"] = append(diff["added"].([]map[string]any), entry)
	}
	for _, modified := range comparison.Modified {
		entry := map[string]any{
			"index":         modified.Index,
			"pre_data_hex":  modified.OldDataHex,
			"post_data_hex": modified.NewDataHex,
		}
		if modified.OldDecoded != nil {
			entry["pre_decoded"] = modified.OldDecoded
		}
		if modified.NewDecoded != nil {
			entry["post_decoded"] = modified.NewDecoded
		}
		diff["modified"] = append(diff["modified"].([]map[string]any), entry)
	}
	removed := make([]string, len(comparison.Removed))
	for i, entry := range comparison.Removed {
		removed[i] = entry.Index
	}
	diff["removed"] = removed
	return diff
}

// diffCounts returns the added/modified/removed cardinalities of a diff produced
// by computeStateDiff, for console summaries.
func diffCounts(diff map[string]any) (added, modified, removed int) {
	if a, ok := diff["added"].([]map[string]any); ok {
		added = len(a)
	}
	if m, ok := diff["modified"].([]map[string]any); ok {
		modified = len(m)
	}
	if r, ok := diff["removed"].([]string); ok {
		removed = len(r)
	}
	return
}

// postStateEntries renders a state snapshot (lowercase-hex index → hex data) as
// the sorted, decoded list written to post_state.json by the debug dumps.
func postStateEntries(post map[string]string) []map[string]any {
	keys := make([]string, 0, len(post))
	for k := range post {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		entry := map[string]any{"index": key, "data_hex": post[key]}
		if decoded := decodeEntryData(post[key]); decoded != nil {
			entry["decoded"] = decoded
		}
		out = append(out, entry)
	}
	return out
}

// writeJSONFile marshals v as indented JSON to path, returning any error so a
// failed debug-dump write is surfaced rather than silently dropped.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // G306: developer replay dump, world-readable by intent
}

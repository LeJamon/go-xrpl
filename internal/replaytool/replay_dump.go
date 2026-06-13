package replaytool

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// computeStateDiff diffs two state snapshots — each keyed by lowercase-hex index
// with a hex-encoded entry as the value — into the added / modified / removed
// shape shared by the `replay` and `replay-range` debug dumps. Added and
// modified entries carry their decoded JSON for readability. Hex comparison is
// case-insensitive. This is the single source of the diff semantics the two
// dumpers (and `xrpld compare`) used to each maintain separately.
func computeStateDiff(pre, post map[string]string) map[string]any {
	diff := map[string]any{
		"added":    make([]map[string]any, 0),
		"modified": make([]map[string]any, 0),
		"removed":  make([]string, 0),
	}

	// remaining starts as the pre-state and is whittled down as post keys are
	// matched; whatever is left was removed.
	remaining := make(map[string]string, len(pre))
	for k, v := range pre {
		remaining[strings.ToLower(k)] = v
	}

	postKeys := make([]string, 0, len(post))
	for k := range post {
		postKeys = append(postKeys, k)
	}
	sort.Strings(postKeys)

	for _, key := range postKeys {
		keyLower := strings.ToLower(key)
		postDataHex := post[key]
		preDataHex, exists := remaining[keyLower]
		switch {
		case !exists:
			entry := map[string]any{"index": key, "data_hex": postDataHex}
			if decoded := decodeEntryData(postDataHex); decoded != nil {
				entry["decoded"] = decoded
			}
			diff["added"] = append(diff["added"].([]map[string]any), entry)
		case !strings.EqualFold(preDataHex, postDataHex):
			entry := map[string]any{
				"index":         key,
				"pre_data_hex":  preDataHex,
				"post_data_hex": postDataHex,
			}
			if d := decodeEntryData(preDataHex); d != nil {
				entry["pre_decoded"] = d
			}
			if d := decodeEntryData(postDataHex); d != nil {
				entry["post_decoded"] = d
			}
			diff["modified"] = append(diff["modified"].([]map[string]any), entry)
		}
		delete(remaining, keyLower)
	}

	removed := make([]string, 0, len(remaining))
	for k := range remaining {
		removed = append(removed, k)
	}
	sort.Strings(removed)
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
	return os.WriteFile(path, data, 0o644)
}

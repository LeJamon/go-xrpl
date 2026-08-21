package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/spf13/cobra"
)

type stateFile struct {
	Entries json.RawMessage `json:"entries"`
}

type stateFileEntry struct {
	Index   string          `json:"index"`
	Data    json.RawMessage `json:"data"`
	DataHex json.RawMessage `json:"data_hex"`
	Decoded map[string]any  `json:"decoded,omitempty"`
}

type compareOptions struct {
	showAll     bool
	showDecoded bool
	filterType  string
	outputPath  string
}

func newCompareCommand() *cobra.Command {
	options := &compareOptions{}
	command := &cobra.Command{
		Use:   "compare <file1> <file2>",
		Short: "Compare two state dump files",
		Long: `Compare two state dump JSON files and show differences.

Supports multiple formats:
- Fixture state.json files (entries with index/data)
- Debug post_state.json files (entries with index/data_hex/decoded)
- Any JSON with entries array containing index and data fields

Shows:
- Added entries (in file2 but not file1)
- Removed entries (in file1 but not file2)
- Modified entries with field-by-field diff

Exits non-zero when any difference is found.

Examples:
    goxrpl compare state1.json state2.json
    goxrpl compare fixtures/ledger_100/state.json fixtures/ledger_101/state.json
    goxrpl compare debug/post_state.json expected_state.json --decoded
    goxrpl compare file1.json file2.json --filter AccountRoot
    goxrpl compare file1.json file2.json --all`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompare(cmd, args, options)
		},
	}
	command.Flags().BoolVarP(&options.showAll, "all", "a", false, "Show all entries, not just differences")
	command.Flags().BoolVarP(&options.showDecoded, "decoded", "d", true, "Show decoded JSON (default true)")
	command.Flags().StringVarP(&options.filterType, "filter", "f", "", "Filter by LedgerEntryType (e.g., AccountRoot, RippleState)")
	command.Flags().StringVarP(&options.outputPath, "output", "o", "", "Output diff to JSON file")
	return command
}

func runCompare(cmd *cobra.Command, args []string, options *compareOptions) error {
	w := cmd.OutOrStdout()
	file1Path := args[0]
	file2Path := args[1]

	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                         State Dump Comparison")
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintf(w, "File 1: %s\n", file1Path)
	fmt.Fprintf(w, "File 2: %s\n", file2Path)
	fmt.Fprintln(w)

	// Load both files
	map1, err := loadStateFile(file1Path)
	if err != nil {
		return fmt.Errorf("loading file1 %q: %w", file1Path, err)
	}

	map2, err := loadStateFile(file2Path)
	if err != nil {
		return fmt.Errorf("loading file2 %q: %w", file2Path, err)
	}

	fmt.Fprintf(w, "File 1: %d entries\n", len(map1))
	fmt.Fprintf(w, "File 2: %d entries\n", len(map2))
	fmt.Fprintln(w)

	// Find differences
	added, removed, modified, unchanged, err := compareStates(map1, map2)
	if err != nil {
		return fmt.Errorf("comparing states: %w", err)
	}

	// Apply filter if specified
	if options.filterType != "" {
		added = filterByType(added, options.filterType)
		removed = filterByType(removed, options.filterType)
		modified = filterModifiedByType(modified, options.filterType)
		unchanged = filterByType(unchanged, options.filterType)
		fmt.Fprintf(w, "Filtered by type: %s\n\n", options.filterType)
	}

	// Print summary
	fmt.Fprintln(w, "--- Summary ---")
	fmt.Fprintf(w, "Added:     %d entries (in file2 but not file1)\n", len(added))
	fmt.Fprintf(w, "Removed:   %d entries (in file1 but not file2)\n", len(removed))
	fmt.Fprintf(w, "Modified:  %d entries\n", len(modified))
	fmt.Fprintf(w, "Unchanged: %d entries\n", len(unchanged))
	fmt.Fprintln(w)

	// Print details
	if len(added) > 0 {
		printAddedEntries(w, added, options)
	}

	if len(removed) > 0 {
		printRemovedEntries(w, removed, options)
	}

	if len(modified) > 0 {
		printModifiedEntries(w, modified, options)
	}

	if options.showAll && len(unchanged) > 0 {
		printUnchangedEntries(w, unchanged)
	}

	// Output to file if requested
	if options.outputPath != "" {
		if err := writeDiffJSON(w, options.outputPath, added, removed, modified); err != nil {
			return err
		}
	}

	// Signal a non-zero exit when there are differences; the report above is
	// the user-facing output, so use the already-reported sentinel.
	if len(added) > 0 || len(removed) > 0 || len(modified) > 0 {
		return cmdexit.ErrReported
	}
	return nil
}

func loadStateFile(path string) (map[string]stateEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	entries, err := parseStateEntries(data)
	if err != nil {
		return nil, err
	}
	return normalizeStateEntries(entries)
}

func parseStateEntries(data []byte) ([]stateFileEntry, error) {
	var topLevel json.RawMessage
	if err := json.Unmarshal(data, &topLevel); err != nil {
		return nil, fmt.Errorf("decoding JSON: %w", err)
	}
	topLevel = bytes.TrimSpace(topLevel)

	var entries []stateFileEntry
	switch {
	case len(topLevel) > 0 && topLevel[0] == '{':
		var snapshot stateFile
		if err := json.Unmarshal(topLevel, &snapshot); err != nil {
			return nil, fmt.Errorf("decoding state snapshot: %w", err)
		}
		if snapshot.Entries == nil {
			return nil, fmt.Errorf("state snapshot is missing entries")
		}
		snapshot.Entries = bytes.TrimSpace(snapshot.Entries)
		if len(snapshot.Entries) == 0 || snapshot.Entries[0] != '[' {
			return nil, fmt.Errorf("state snapshot entries must be an array")
		}
		if err := json.Unmarshal(snapshot.Entries, &entries); err != nil {
			return nil, fmt.Errorf("decoding state entries: %w", err)
		}
	case len(topLevel) > 0 && topLevel[0] == '[':
		if err := json.Unmarshal(topLevel, &entries); err != nil {
			return nil, fmt.Errorf("decoding state entries: %w", err)
		}
	default:
		return nil, fmt.Errorf("expected a state snapshot object or entry array")
	}
	return entries, nil
}

type stateEntry = statecompare.ComparableStateEntry

func normalizeStateEntries(entries []stateFileEntry) (map[string]stateEntry, error) {
	result := make(map[string]stateEntry, len(entries))
	originalIndexes := make(map[string]string, len(entries))
	for i, entry := range entries {
		index := strings.TrimSpace(entry.Index)
		if index == "" {
			return nil, fmt.Errorf("entry %d has no index", i+1)
		}
		key := strings.ToLower(index)
		if original, exists := originalIndexes[key]; exists {
			return nil, fmt.Errorf("entry %d index %q duplicates %q", i+1, index, original)
		}
		originalIndexes[key] = index

		if entry.Data != nil && entry.DataHex != nil {
			return nil, fmt.Errorf("entry %q has both data and data_hex", index)
		}

		var rawData json.RawMessage
		switch {
		case entry.Data != nil:
			rawData = entry.Data
		case entry.DataHex != nil:
			rawData = entry.DataHex
		default:
			return nil, fmt.Errorf("entry %q has no raw data", index)
		}

		var dataHex string
		if err := json.Unmarshal(rawData, &dataHex); err != nil {
			return nil, fmt.Errorf("entry %q raw data must be a string: %w", index, err)
		}
		if dataHex == "" {
			return nil, fmt.Errorf("entry %q has no raw data", index)
		}
		if _, err := hex.DecodeString(dataHex); err != nil {
			return nil, fmt.Errorf("entry %q has invalid raw data: %w", index, err)
		}

		decoded := decodeStateData(dataHex)
		if decoded == nil {
			decoded = entry.Decoded
		}

		result[key] = stateEntry{
			Index:   index,
			DataHex: dataHex,
			Decoded: decoded,
		}
	}
	return result, nil
}

func decodeStateData(hexData string) map[string]any {
	decoded, err := binarycodec.Decode(hexData)
	if err != nil {
		return nil
	}
	return decoded
}

type modifiedEntry = statecompare.StateModification

func compareStates(map1, map2 map[string]stateEntry) (added, removed []stateEntry, modified []modifiedEntry, unchanged []stateEntry, err error) {
	comparison, err := statecompare.CompareStates(map1, map2)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return comparison.Added, comparison.Removed, comparison.Modified, comparison.Unchanged, nil
}

func filterByType(entries []stateEntry, entryType string) []stateEntry {
	result := make([]stateEntry, 0)
	for _, e := range entries {
		if e.Decoded != nil {
			if t, ok := e.Decoded["LedgerEntryType"].(string); ok {
				if strings.EqualFold(t, entryType) {
					result = append(result, e)
				}
			}
		}
	}
	return result
}

func filterModifiedByType(entries []modifiedEntry, entryType string) []modifiedEntry {
	result := make([]modifiedEntry, 0)
	for _, e := range entries {
		if e.NewDecoded != nil {
			if t, ok := e.NewDecoded["LedgerEntryType"].(string); ok {
				if strings.EqualFold(t, entryType) {
					result = append(result, e)
				}
			}
		}
	}
	return result
}

func printAddedEntries(w io.Writer, entries []stateEntry, options *compareOptions) {
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                              ADDED ENTRIES")
	fmt.Fprintln(w, "================================================================================")

	for i, e := range entries {
		fmt.Fprintf(w, "\n[+] Entry %d: %s\n", i+1, e.Index)
		printEntryDetails(w, e.Decoded, options)
	}
	fmt.Fprintln(w)
}

func printRemovedEntries(w io.Writer, entries []stateEntry, options *compareOptions) {
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                             REMOVED ENTRIES")
	fmt.Fprintln(w, "================================================================================")

	for i, e := range entries {
		fmt.Fprintf(w, "\n[-] Entry %d: %s\n", i+1, e.Index)
		printEntryDetails(w, e.Decoded, options)
	}
	fmt.Fprintln(w)
}

func printModifiedEntries(w io.Writer, entries []modifiedEntry, options *compareOptions) {
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                            MODIFIED ENTRIES")
	fmt.Fprintln(w, "================================================================================")

	for i, e := range entries {
		fmt.Fprintf(w, "\n[~] Entry %d: %s\n", i+1, e.Index)

		if e.NewDecoded != nil {
			if t, ok := e.NewDecoded["LedgerEntryType"].(string); ok {
				fmt.Fprintf(w, "    Type: %s\n", t)
			}
		}

		if len(e.ChangedKeys) > 0 {
			fmt.Fprintf(w, "    Changed fields: %v\n", e.ChangedKeys)
		}

		fmt.Fprintln(w, "    ---")

		// Show field-by-field diff
		if options.showDecoded && e.OldDecoded != nil && e.NewDecoded != nil {
			printFieldDiff(w, e.OldDecoded, e.NewDecoded, e.ChangedKeys)
		}
	}
	fmt.Fprintln(w)
}

func printUnchangedEntries(w io.Writer, entries []stateEntry) {
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                           UNCHANGED ENTRIES")
	fmt.Fprintln(w, "================================================================================")

	for i, e := range entries {
		entryType := "Unknown"
		if e.Decoded != nil {
			if t, ok := e.Decoded["LedgerEntryType"].(string); ok {
				entryType = t
			}
		}
		fmt.Fprintf(w, "[=] %d: %s (%s)\n", i+1, truncateID(e.Index, 32), entryType)
	}
	fmt.Fprintln(w)
}

func printEntryDetails(w io.Writer, decoded map[string]any, options *compareOptions) {
	if decoded == nil {
		fmt.Fprintln(w, "    (unable to decode)")
		return
	}

	if t, ok := decoded["LedgerEntryType"].(string); ok {
		fmt.Fprintf(w, "    Type: %s\n", t)
	}

	if options.showDecoded {
		// Print key fields based on entry type
		printKeyFields(w, decoded)

		// Optionally print full JSON
		if options.showAll {
			prettyJSON, _ := json.MarshalIndent(decoded, "    ", "  ")
			fmt.Fprintf(w, "    Full data:\n    %s\n", string(prettyJSON))
		}
	}
}

func printKeyFields(w io.Writer, decoded map[string]any) {
	entryType, _ := decoded["LedgerEntryType"].(string)

	switch entryType {
	case "AccountRoot":
		printField(w, decoded, "Account")
		printField(w, decoded, "Balance")
		printField(w, decoded, "Sequence")
		printField(w, decoded, "OwnerCount")
		printField(w, decoded, "Flags")
	case "RippleState":
		printField(w, decoded, "Balance")
		printField(w, decoded, "LowLimit")
		printField(w, decoded, "HighLimit")
		printField(w, decoded, "Flags")
	case "Offer":
		printField(w, decoded, "Account")
		printField(w, decoded, "TakerGets")
		printField(w, decoded, "TakerPays")
		printField(w, decoded, "Sequence")
	case "DirectoryNode":
		printField(w, decoded, "Owner")
		printField(w, decoded, "RootIndex")
	case "FeeSettings":
		printField(w, decoded, "BaseFee")
		printField(w, decoded, "ReserveBase")
		printField(w, decoded, "ReserveIncrement")
		printField(w, decoded, "BaseFeeDrops")
		printField(w, decoded, "ReserveBaseDrops")
		printField(w, decoded, "ReserveIncrementDrops")
	case "Amendments":
		if amendments, ok := decoded["Amendments"].([]any); ok {
			fmt.Fprintf(w, "    Amendments: %d enabled\n", len(amendments))
		}
	default:
		fields := make([]string, 0, len(decoded))
		for field := range decoded {
			if field != "LedgerEntryType" {
				fields = append(fields, field)
			}
		}
		sort.Strings(fields)
		for _, field := range fields {
			fmt.Fprintf(w, "    %s: %v\n", field, formatValue(decoded[field]))
		}
	}
}

func printField(w io.Writer, decoded map[string]any, field string) {
	if val, ok := decoded[field]; ok {
		fmt.Fprintf(w, "    %s: %v\n", field, formatValue(val))
	}
}

func formatValue(v any) string {
	switch val := v.(type) {
	case map[string]any:
		// Likely an Amount object
		if currency, ok := val["currency"].(string); ok {
			if value, ok := val["value"].(string); ok {
				if issuer, ok := val["issuer"].(string); ok {
					return fmt.Sprintf("%s %s (%s...)", value, currency, truncate(issuer, 8))
				}
				return fmt.Sprintf("%s %s", value, currency)
			}
		}
		jsonBytes, _ := json.Marshal(val)
		return string(jsonBytes)
	case []any:
		return fmt.Sprintf("[%d items]", len(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

func printFieldDiff(w io.Writer, old, new map[string]any, changedKeys []string) {
	for _, key := range changedKeys {
		oldVal := old[key]
		newVal := new[key]

		fmt.Fprintf(w, "    %s:\n", key)
		fmt.Fprintf(w, "      - %v\n", formatValue(oldVal))
		fmt.Fprintf(w, "      + %v\n", formatValue(newVal))
	}
}

func writeDiffJSON(w io.Writer, path string, added, removed []stateEntry, modified []modifiedEntry) error {
	output := map[string]any{
		"added":    make([]map[string]any, 0),
		"removed":  make([]map[string]any, 0),
		"modified": make([]map[string]any, 0),
	}

	for _, e := range added {
		output["added"] = append(output["added"].([]map[string]any), map[string]any{
			"index":    e.Index,
			"data_hex": e.DataHex,
			"decoded":  e.Decoded,
		})
	}

	for _, e := range removed {
		output["removed"] = append(output["removed"].([]map[string]any), map[string]any{
			"index":    e.Index,
			"data_hex": e.DataHex,
			"decoded":  e.Decoded,
		})
	}

	for _, e := range modified {
		output["modified"] = append(output["modified"].([]map[string]any), map[string]any{
			"index":        e.Index,
			"changed_keys": e.ChangedKeys,
			"old_data_hex": e.OldDataHex,
			"new_data_hex": e.NewDataHex,
			"old":          e.OldDecoded,
			"new":          e.NewDecoded,
		})
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding diff: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil { //nolint:gosec // G306: developer CLI diff output, world-readable by intent
		return fmt.Errorf("writing diff file %q: %w", path, err)
	}
	fmt.Fprintf(w, "Diff written to: %s\n", path)
	return nil
}

// truncate returns s shortened to at most n bytes (no ellipsis); safe on short
// strings. truncateID appends "..." when it shortens.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

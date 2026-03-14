package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/spf13/cobra"
)

// StateFile represents a state dump file (either from fixtures or debug output)
type StateFile struct {
	LedgerIndex uint32                   `json:"ledger_index,omitempty"`
	AccountHash string                   `json:"account_hash,omitempty"`
	Entries     []StateFileEntry         `json:"entries,omitempty"`
	State       []map[string]interface{} `json:"state,omitempty"` // Alternative format from debug dumps
}

// StateFileEntry represents a state entry that could come from different formats
type StateFileEntry struct {
	Index   string                 `json:"index"`
	Data    string                 `json:"data,omitempty"`     // From fixture state.json
	DataHex string                 `json:"data_hex,omitempty"` // From debug post_state.json
	Decoded map[string]interface{} `json:"decoded,omitempty"`  // Pre-decoded data
}

var (
	compareShowAll      bool
	compareShowDecoded  bool
	compareFilterType   string
	compareOutputFormat string
)

// compareCmd represents the compare command
var compareCmd = &cobra.Command{
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

Examples:
    xrpld compare state1.json state2.json
    xrpld compare fixtures/ledger_100/state.json fixtures/ledger_101/state.json
    xrpld compare debug/post_state.json expected_state.json --decoded
    xrpld compare file1.json file2.json --filter AccountRoot
    xrpld compare file1.json file2.json --all`,
	Args: cobra.ExactArgs(2),
	Run:  runCompare,
}

func init() {
	rootCmd.AddCommand(compareCmd)

	compareCmd.Flags().BoolVarP(&compareShowAll, "all", "a", false, "Show all entries, not just differences")
	compareCmd.Flags().BoolVarP(&compareShowDecoded, "decoded", "d", true, "Show decoded JSON (default true)")
	compareCmd.Flags().StringVarP(&compareFilterType, "filter", "f", "", "Filter by LedgerEntryType (e.g., AccountRoot, RippleState)")
	compareCmd.Flags().StringVarP(&compareOutputFormat, "output", "o", "", "Output diff to JSON file")
}

func runCompare(cmd *cobra.Command, args []string) {
	file1Path := args[0]
	file2Path := args[1]

	fmt.Println("================================================================================")
	fmt.Println("                         State Dump Comparison")
	fmt.Println("================================================================================")
	fmt.Printf("File 1: %s\n", file1Path)
	fmt.Printf("File 2: %s\n", file2Path)
	fmt.Println()

	// Load both files
	state1, err := loadStateFile(file1Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to load file1: %v\n", err)
		os.Exit(1)
	}

	state2, err := loadStateFile(file2Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to load file2: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("File 1: %d entries\n", len(state1))
	fmt.Printf("File 2: %d entries\n", len(state2))
	fmt.Println()

	// Build maps for comparison
	map1 := buildStateMap(state1)
	map2 := buildStateMap(state2)

	// Find differences
	added, removed, modified, unchanged := compareStates(map1, map2)

	// Apply filter if specified
	if compareFilterType != "" {
		added = filterByType(added, compareFilterType)
		removed = filterByType(removed, compareFilterType)
		modified = filterModifiedByType(modified, compareFilterType)
		unchanged = filterByType(unchanged, compareFilterType)
		fmt.Printf("Filtered by type: %s\n\n", compareFilterType)
	}

	// Print summary
	fmt.Println("--- Summary ---")
	fmt.Printf("Added:     %d entries (in file2 but not file1)\n", len(added))
	fmt.Printf("Removed:   %d entries (in file1 but not file2)\n", len(removed))
	fmt.Printf("Modified:  %d entries\n", len(modified))
	fmt.Printf("Unchanged: %d entries\n", len(unchanged))
	fmt.Println()

	// Print details
	if len(added) > 0 {
		printAddedEntries(added)
	}

	if len(removed) > 0 {
		printRemovedEntries(removed)
	}

	if len(modified) > 0 {
		printModifiedEntries(modified)
	}

	if compareShowAll && len(unchanged) > 0 {
		printUnchangedEntries(unchanged)
	}

	// Output to file if requested
	if compareOutputFormat != "" {
		writeDiffJSON(compareOutputFormat, added, removed, modified)
	}

	// Exit with error if there are differences
	if len(added) > 0 || len(removed) > 0 || len(modified) > 0 {
		os.Exit(1)
	}
}

func loadStateFile(path string) ([]StateFileEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Try parsing as StateFile first
	var stateFile StateFile
	if err := json.Unmarshal(data, &stateFile); err == nil {
		if len(stateFile.Entries) > 0 {
			return stateFile.Entries, nil
		}
	}

	// Try parsing as array of entries directly (debug post_state.json format)
	var entries []StateFileEntry
	if err := json.Unmarshal(data, &entries); err == nil {
		return entries, nil
	}

	// Try parsing as array of maps
	var mapEntries []map[string]interface{}
	if err := json.Unmarshal(data, &mapEntries); err == nil {
		entries := make([]StateFileEntry, 0, len(mapEntries))
		for _, m := range mapEntries {
			entry := StateFileEntry{}
			if idx, ok := m["index"].(string); ok {
				entry.Index = idx
			}
			if data, ok := m["data"].(string); ok {
				entry.Data = data
			}
			if dataHex, ok := m["data_hex"].(string); ok {
				entry.DataHex = dataHex
			}
			if decoded, ok := m["decoded"].(map[string]interface{}); ok {
				entry.Decoded = decoded
			}
			if entry.Index != "" {
				entries = append(entries, entry)
			}
		}
		return entries, nil
	}

	return nil, fmt.Errorf("unrecognized file format")
}

type stateEntry struct {
	Index   string
	DataHex string
	Decoded map[string]interface{}
}

func buildStateMap(entries []StateFileEntry) map[string]stateEntry {
	result := make(map[string]stateEntry)
	for _, e := range entries {
		key := strings.ToLower(e.Index)
		dataHex := e.Data
		if dataHex == "" {
			dataHex = e.DataHex
		}

		decoded := e.Decoded
		if decoded == nil && dataHex != "" {
			decoded = decodeStateData(dataHex)
		}

		result[key] = stateEntry{
			Index:   e.Index,
			DataHex: dataHex,
			Decoded: decoded,
		}
	}
	return result
}

func decodeStateData(hexData string) map[string]interface{} {
	decoded, err := binarycodec.Decode(hexData)
	if err != nil {
		return nil
	}
	return decoded
}

type modifiedEntry struct {
	Index       string
	OldDataHex  string
	NewDataHex  string
	OldDecoded  map[string]interface{}
	NewDecoded  map[string]interface{}
	ChangedKeys []string
}

func compareStates(map1, map2 map[string]stateEntry) (added, removed []stateEntry, modified []modifiedEntry, unchanged []stateEntry) {
	// Find added and modified
	for key, entry2 := range map2 {
		entry1, exists := map1[key]
		if !exists {
			added = append(added, entry2)
		} else if strings.ToLower(entry1.DataHex) != strings.ToLower(entry2.DataHex) {
			changedKeys := findChangedKeys(entry1.Decoded, entry2.Decoded)
			modified = append(modified, modifiedEntry{
				Index:       entry2.Index,
				OldDataHex:  entry1.DataHex,
				NewDataHex:  entry2.DataHex,
				OldDecoded:  entry1.Decoded,
				NewDecoded:  entry2.Decoded,
				ChangedKeys: changedKeys,
			})
		} else {
			unchanged = append(unchanged, entry2)
		}
	}

	// Find removed
	for key, entry1 := range map1 {
		if _, exists := map2[key]; !exists {
			removed = append(removed, entry1)
		}
	}

	// Sort for consistent output
	sort.Slice(added, func(i, j int) bool { return added[i].Index < added[j].Index })
	sort.Slice(removed, func(i, j int) bool { return removed[i].Index < removed[j].Index })
	sort.Slice(modified, func(i, j int) bool { return modified[i].Index < modified[j].Index })
	sort.Slice(unchanged, func(i, j int) bool { return unchanged[i].Index < unchanged[j].Index })

	return
}

func findChangedKeys(old, new map[string]interface{}) []string {
	if old == nil || new == nil {
		return nil
	}

	changed := make([]string, 0)
	allKeys := make(map[string]bool)

	for k := range old {
		allKeys[k] = true
	}
	for k := range new {
		allKeys[k] = true
	}

	for k := range allKeys {
		oldVal, oldExists := old[k]
		newVal, newExists := new[k]

		if !oldExists || !newExists {
			changed = append(changed, k)
		} else if !reflect.DeepEqual(oldVal, newVal) {
			changed = append(changed, k)
		}
	}

	sort.Strings(changed)
	return changed
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

func printAddedEntries(entries []stateEntry) {
	fmt.Println("================================================================================")
	fmt.Println("                              ADDED ENTRIES")
	fmt.Println("================================================================================")

	for i, e := range entries {
		fmt.Printf("\n[+] Entry %d: %s\n", i+1, e.Index)
		printEntryDetails(e.Decoded)
	}
	fmt.Println()
}

func printRemovedEntries(entries []stateEntry) {
	fmt.Println("================================================================================")
	fmt.Println("                             REMOVED ENTRIES")
	fmt.Println("================================================================================")

	for i, e := range entries {
		fmt.Printf("\n[-] Entry %d: %s\n", i+1, e.Index)
		printEntryDetails(e.Decoded)
	}
	fmt.Println()
}

func printModifiedEntries(entries []modifiedEntry) {
	fmt.Println("================================================================================")
	fmt.Println("                            MODIFIED ENTRIES")
	fmt.Println("================================================================================")

	for i, e := range entries {
		fmt.Printf("\n[~] Entry %d: %s\n", i+1, e.Index)

		if e.NewDecoded != nil {
			if t, ok := e.NewDecoded["LedgerEntryType"].(string); ok {
				fmt.Printf("    Type: %s\n", t)
			}
		}

		if len(e.ChangedKeys) > 0 {
			fmt.Printf("    Changed fields: %v\n", e.ChangedKeys)
		}

		fmt.Println("    ---")

		// Show field-by-field diff
		if compareShowDecoded && e.OldDecoded != nil && e.NewDecoded != nil {
			printFieldDiff(e.OldDecoded, e.NewDecoded, e.ChangedKeys)
		}
	}
	fmt.Println()
}

func printUnchangedEntries(entries []stateEntry) {
	fmt.Println("================================================================================")
	fmt.Println("                           UNCHANGED ENTRIES")
	fmt.Println("================================================================================")

	for i, e := range entries {
		entryType := "Unknown"
		if e.Decoded != nil {
			if t, ok := e.Decoded["LedgerEntryType"].(string); ok {
				entryType = t
			}
		}
		fmt.Printf("[=] %d: %s (%s)\n", i+1, e.Index[:32]+"...", entryType)
	}
	fmt.Println()
}

func printEntryDetails(decoded map[string]interface{}) {
	if decoded == nil {
		fmt.Println("    (unable to decode)")
		return
	}

	if t, ok := decoded["LedgerEntryType"].(string); ok {
		fmt.Printf("    Type: %s\n", t)
	}

	if compareShowDecoded {
		// Print key fields based on entry type
		printKeyFields(decoded)

		// Optionally print full JSON
		if compareShowAll {
			prettyJSON, _ := json.MarshalIndent(decoded, "    ", "  ")
			fmt.Printf("    Full data:\n    %s\n", string(prettyJSON))
		}
	}
}

func printKeyFields(decoded map[string]interface{}) {
	entryType, _ := decoded["LedgerEntryType"].(string)

	switch entryType {
	case "AccountRoot":
		printField(decoded, "Account")
		printField(decoded, "Balance")
		printField(decoded, "Sequence")
		printField(decoded, "OwnerCount")
		printField(decoded, "Flags")
	case "RippleState":
		printField(decoded, "Balance")
		printField(decoded, "LowLimit")
		printField(decoded, "HighLimit")
		printField(decoded, "Flags")
	case "Offer":
		printField(decoded, "Account")
		printField(decoded, "TakerGets")
		printField(decoded, "TakerPays")
		printField(decoded, "Sequence")
	case "DirectoryNode":
		printField(decoded, "Owner")
		printField(decoded, "RootIndex")
	case "FeeSettings":
		printField(decoded, "BaseFee")
		printField(decoded, "ReserveBase")
		printField(decoded, "ReserveIncrement")
		printField(decoded, "BaseFeeDrops")
		printField(decoded, "ReserveBaseDrops")
		printField(decoded, "ReserveIncrementDrops")
	case "Amendments":
		if amendments, ok := decoded["Amendments"].([]interface{}); ok {
			fmt.Printf("    Amendments: %d enabled\n", len(amendments))
		}
	default:
		// Print all fields for unknown types
		for k, v := range decoded {
			if k != "LedgerEntryType" {
				fmt.Printf("    %s: %v\n", k, formatValue(v))
			}
		}
	}
}

func printField(decoded map[string]interface{}, field string) {
	if val, ok := decoded[field]; ok {
		fmt.Printf("    %s: %v\n", field, formatValue(val))
	}
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		// Likely an Amount object
		if currency, ok := val["currency"].(string); ok {
			if value, ok := val["value"].(string); ok {
				if issuer, ok := val["issuer"].(string); ok {
					return fmt.Sprintf("%s %s (%s...)", value, currency, issuer[:8])
				}
				return fmt.Sprintf("%s %s", value, currency)
			}
		}
		jsonBytes, _ := json.Marshal(val)
		return string(jsonBytes)
	case []interface{}:
		return fmt.Sprintf("[%d items]", len(val))
	default:
		return fmt.Sprintf("%v", val)
	}
}

func printFieldDiff(old, new map[string]interface{}, changedKeys []string) {
	for _, key := range changedKeys {
		oldVal := old[key]
		newVal := new[key]

		fmt.Printf("    %s:\n", key)
		fmt.Printf("      - %v\n", formatValue(oldVal))
		fmt.Printf("      + %v\n", formatValue(newVal))
	}
}

func writeDiffJSON(path string, added, removed []stateEntry, modified []modifiedEntry) {
	output := map[string]interface{}{
		"added":    make([]map[string]interface{}, 0),
		"removed":  make([]map[string]interface{}, 0),
		"modified": make([]map[string]interface{}, 0),
	}

	for _, e := range added {
		output["added"] = append(output["added"].([]map[string]interface{}), map[string]interface{}{
			"index":   e.Index,
			"decoded": e.Decoded,
		})
	}

	for _, e := range removed {
		output["removed"] = append(output["removed"].([]map[string]interface{}), map[string]interface{}{
			"index":   e.Index,
			"decoded": e.Decoded,
		})
	}

	for _, e := range modified {
		output["modified"] = append(output["modified"].([]map[string]interface{}), map[string]interface{}{
			"index":        e.Index,
			"changed_keys": e.ChangedKeys,
			"old":          e.OldDecoded,
			"new":          e.NewDecoded,
		})
	}

	data, _ := json.MarshalIndent(output, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Printf("ERROR: Failed to write diff file: %v\n", err)
	} else {
		fmt.Printf("Diff written to: %s\n", path)
	}
}

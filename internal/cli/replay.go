package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/tx"
	_ "github.com/LeJamon/goXRPLd/internal/tx/all"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/spf13/cobra"
)

// Fixture file structures matching xrpl-state-compare export format

// StateFixture represents state.json - the pre-state at ledger N
type StateFixture struct {
	LedgerIndex uint32       `json:"ledger_index"`
	AccountHash string       `json:"account_hash"`
	Entries     []StateEntry `json:"entries"`
}

// StateEntry represents a single state entry
type StateEntry struct {
	Index string `json:"index"` // 32-byte hex key
	Data  string `json:"data"`  // Binary data as hex
}

// EnvFixture represents env.json - the execution context
type EnvFixture struct {
	LedgerIndex         uint32     `json:"ledger_index"`
	ParentHash          string     `json:"parent_hash"`
	ParentCloseTime     int64      `json:"parent_close_time"`
	CloseTime           int64      `json:"close_time"`
	CloseTimeResolution uint32     `json:"close_time_resolution"`
	CloseFlags          uint8      `json:"close_flags"`
	TotalCoins          string     `json:"total_coins"`
	Fees                FeesConfig `json:"fees"`
	Amendments          []string   `json:"amendments"`
}

// FeesConfig represents fee settings
type FeesConfig struct {
	BaseFee          uint64 `json:"base_fee"`
	ReserveBase      uint64 `json:"reserve_base"`
	ReserveIncrement uint64 `json:"reserve_increment"`
}

// TxsFixture represents txs.json - transactions to execute
type TxsFixture struct {
	Transactions []TxEntry `json:"transactions"`
}

// TxEntry represents a single transaction
type TxEntry struct {
	Index  int    `json:"index"`
	Hash   string `json:"hash"`
	TxBlob string `json:"tx_blob"` // Binary transaction as hex
}

// ExpectedFixture represents expected.json - expected results
type ExpectedFixture struct {
	LedgerIndex     uint32            `json:"ledger_index"`
	LedgerHash      string            `json:"ledger_hash"`
	AccountHash     string            `json:"account_hash"`
	TransactionHash string            `json:"transaction_hash"`
	TotalCoins      string            `json:"total_coins"`
	Transactions    []ExpectedTxEntry `json:"transactions"`
}

// ExpectedTxEntry represents expected transaction result
type ExpectedTxEntry struct {
	Index    int    `json:"index"`
	Hash     string `json:"hash"`
	MetaBlob string `json:"meta_blob"` // Binary metadata as hex
}

// TxApplyInfo stores detailed transaction application info
type TxApplyInfo struct {
	Index      int
	Hash       string
	TxType     string
	Account    string
	Result     string
	ResultCode int
	Applied    bool
	Fee        uint64
	DecodedTx  map[string]interface{}
	Metadata   *tx.Metadata
	Error      string
}

// ReplayResult contains the results of the replay
type ReplayResult struct {
	Success         bool
	LedgerHash      [32]byte
	AccountHash     [32]byte
	TransactionHash [32]byte
	TotalCoins      uint64
	Errors          []string
	TxResults       []TxApplyInfo
	PreStateCount   int
	PostStateCount  int
	PostState       map[string][]byte // key -> data for debugging
	Duration        time.Duration
}

var (
	fixtureDir    string
	outputResult  string
	verboseReplay bool
	dumpState     bool
	dumpDir       string
	showDecoded   bool
)

// replayCmd represents the replay command
var replayCmd = &cobra.Command{
	Use:   "replay [fixture-dir]",
	Short: "Replay transactions from fixtures for state transition testing",
	Long: `Replay executes state transition tests using fixture files.

It loads pre-state from state.json, execution context from env.json,
transactions from txs.json, and compares results against expected.json.

This enables validation of the transaction engine against known-good
state transitions captured from rippled.

Example:
    xrpld replay ./fixtures/ledger_32750
    xrpld replay ./fixtures/ledger_32750 -v
    xrpld replay ./fixtures/ledger_32750 --dump --dump-dir ./debug
    xrpld replay ./fixtures/ledger_32750 --decoded`,
	Args: cobra.ExactArgs(1),
	Run:  runReplay,
}

func init() {
	rootCmd.AddCommand(replayCmd)

	replayCmd.Flags().StringVarP(&outputResult, "output", "o", "", "Output file for results (JSON)")
	replayCmd.Flags().BoolVarP(&verboseReplay, "verbose", "v", false, "Verbose output")
	replayCmd.Flags().BoolVar(&dumpState, "dump", false, "Dump full state on failure (or always with -v)")
	replayCmd.Flags().StringVar(&dumpDir, "dump-dir", "", "Directory to write state dumps (default: fixture-dir/debug)")
	replayCmd.Flags().BoolVar(&showDecoded, "decoded", false, "Show decoded JSON for transactions and state entries")
}

func runReplay(cmd *cobra.Command, args []string) {
	fixtureDir = args[0]
	startTime := time.Now()

	fmt.Println("================================================================================")
	fmt.Println("                        XRPL State Transition Replay")
	fmt.Println("================================================================================")
	fmt.Printf("Fixture directory: %s\n", fixtureDir)
	fmt.Printf("Started at:        %s\n", startTime.Format(time.RFC3339))
	fmt.Println()

	// Load fixtures
	state, env, txs, expected, err := loadFixtures(fixtureDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to load fixtures: %v\n", err)
		os.Exit(1)
	}

	printFixtureInfo(state, env, txs, expected)

	// Execute replay
	result, openLedger, err := executeReplayVerbose(state, env, txs, expected)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Replay execution failed: %v\n", err)
		os.Exit(1)
	}

	result.Duration = time.Since(startTime)

	// Print detailed results
	printDetailedResults(result, expected, state)

	// Dump state if requested or on failure
	shouldDump := dumpState || (verboseReplay && !result.Success) || !result.Success
	if shouldDump && openLedger != nil {
		dumpDebugInfo(result, state, expected, openLedger)
	}

	// Write output if requested
	if outputResult != "" {
		if err := writeResultJSON(outputResult, result); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to write output: %v\n", err)
		} else {
			fmt.Printf("\nResults written to: %s\n", outputResult)
		}
	}

	fmt.Println()
	fmt.Printf("Duration: %v\n", result.Duration)

	if !result.Success {
		os.Exit(1)
	}
}

func printFixtureInfo(state *StateFixture, env *EnvFixture, txs *TxsFixture, expected *ExpectedFixture) {
	fmt.Println("--- Fixture Summary ---")
	fmt.Printf("Pre-state ledger:     %d\n", state.LedgerIndex)
	fmt.Printf("Pre-state entries:    %d\n", len(state.Entries))
	fmt.Printf("Pre-state hash:       %s\n", state.AccountHash)
	fmt.Println()
	fmt.Printf("Target ledger:        %d\n", env.LedgerIndex)
	fmt.Printf("Transactions:         %d\n", len(txs.Transactions))
	fmt.Printf("Parent hash:          %s\n", env.ParentHash)
	fmt.Printf("Close time:           %d\n", env.CloseTime)
	fmt.Printf("Close time res:       %d\n", env.CloseTimeResolution)
	fmt.Println()
	fmt.Println("Fee settings:")
	fmt.Printf("  Base fee:           %d drops\n", env.Fees.BaseFee)
	fmt.Printf("  Reserve base:       %d drops (%d XRP)\n", env.Fees.ReserveBase, env.Fees.ReserveBase/1_000_000)
	fmt.Printf("  Reserve increment:  %d drops (%d XRP)\n", env.Fees.ReserveIncrement, env.Fees.ReserveIncrement/1_000_000)
	fmt.Println()
	fmt.Printf("Expected ledger hash: %s\n", expected.LedgerHash)
	fmt.Printf("Expected state hash:  %s\n", expected.AccountHash)
	fmt.Printf("Expected tx hash:     %s\n", expected.TransactionHash)
	fmt.Printf("Expected total coins: %s\n", expected.TotalCoins)
	fmt.Println()
}

func loadFixtures(dir string) (*StateFixture, *EnvFixture, *TxsFixture, *ExpectedFixture, error) {
	state := &StateFixture{}
	if err := loadJSON(filepath.Join(dir, "state.json"), state); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading state.json: %w", err)
	}

	env := &EnvFixture{}
	if err := loadJSON(filepath.Join(dir, "env.json"), env); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading env.json: %w", err)
	}

	txs := &TxsFixture{}
	if err := loadJSON(filepath.Join(dir, "txs.json"), txs); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading txs.json: %w", err)
	}

	expected := &ExpectedFixture{}
	if err := loadJSON(filepath.Join(dir, "expected.json"), expected); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("loading expected.json: %w", err)
	}

	return state, env, txs, expected, nil
}

func loadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func executeReplayVerbose(state *StateFixture, env *EnvFixture, txs *TxsFixture, expected *ExpectedFixture) (*ReplayResult, *ledger.Ledger, error) {
	result := &ReplayResult{
		Success:       true,
		Errors:        make([]string, 0),
		TxResults:     make([]TxApplyInfo, 0),
		PreStateCount: len(state.Entries),
		PostState:     make(map[string][]byte),
	}

	fmt.Println("--- Execution ---")

	// Step 1: Create state map and inject pre-state
	fmt.Printf("[1/5] Injecting %d pre-state entries...\n", len(state.Entries))

	stateMap, err := shamap.New(shamap.TypeState)
	if err != nil {
		return nil, nil, fmt.Errorf("creating state map: %w", err)
	}

	for i, entry := range state.Entries {
		key, err := hexToHash32(entry.Index)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing entry %d key: %w", i, err)
		}

		data, err := hex.DecodeString(entry.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing entry %d data: %w", i, err)
		}

		if err := stateMap.Put(key, data); err != nil {
			return nil, nil, fmt.Errorf("inserting entry %d: %w", i, err)
		}

		if verboseReplay && showDecoded && i < 5 {
			decoded := decodeEntryData(entry.Data)
			fmt.Printf("      Entry %d: %s\n", i, entry.Index[:16]+"...")
			if decoded != nil {
				if entryType, ok := decoded["LedgerEntryType"]; ok {
					fmt.Printf("        Type: %v\n", entryType)
				}
			}
		}
	}

	preStateHash, _ := stateMap.Hash()
	fmt.Printf("      Pre-state hash: %s\n", hex.EncodeToString(preStateHash[:]))

	// Step 2: Create transaction map
	fmt.Println("[2/5] Creating transaction map...")
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return nil, nil, fmt.Errorf("creating tx map: %w", err)
	}

	// Step 3: Parse environment
	fmt.Println("[3/5] Setting up ledger environment...")
	totalCoins, err := parseDrops(env.TotalCoins)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing total_coins: %w", err)
	}

	parentHash, err := hexToHash32(env.ParentHash)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing parent_hash: %w", err)
	}

	closeTime := time.Unix(protocol.RippleEpochUnix+int64(env.CloseTime), 0).UTC()
	parentCloseTime := time.Unix(protocol.RippleEpochUnix+int64(env.ParentCloseTime), 0).UTC()

	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         env.LedgerIndex,
		ParentHash:          parentHash,
		ParentCloseTime:     parentCloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: env.CloseTimeResolution,
		CloseFlags:          env.CloseFlags,
		Drops:               totalCoins,
	}

	// Extract fees from state (use defaults if not found)
	fees := extractFeesFromState(state.Entries)

	// Use NewOpenWithHeader to create open ledger directly with the exact header values
	// This avoids the sequence increment that NewOpen would do
	openLedger := ledger.NewOpenWithHeader(ledgerHeader, stateMap, txMap, fees)

	fmt.Printf("      Ledger sequence: %d\n", env.LedgerIndex)
	fmt.Printf("      Total coins:     %d drops\n", totalCoins)

	// Step 4: Apply transactions
	fmt.Printf("[4/5] Applying %d transactions...\n", len(txs.Transactions))

	// Build amendment rules from the amendments list in the fixture
	rules := buildRulesFromAmendments(env.Amendments)

	engineConfig := tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            env.LedgerIndex,
		SkipSignatureVerification: true,
		Standalone:                true,
		Rules:                     rules,
	}

	engine := tx.NewEngine(openLedger, engineConfig)
	blockProcessor := tx.NewBlockProcessor(engine)

	for _, txEntry := range txs.Transactions {
		txInfo := TxApplyInfo{
			Index: txEntry.Index,
			Hash:  txEntry.Hash,
		}

		txBlob, err := hex.DecodeString(txEntry.TxBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to decode blob: %v", err)
			txInfo.Applied = false
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.Index, txInfo.Error))
			result.Success = false
			continue
		}

		// Decode transaction for display
		txInfo.DecodedTx = decodeEntryData(txEntry.TxBlob)
		if txInfo.DecodedTx != nil {
			if txType, ok := txInfo.DecodedTx["TransactionType"].(string); ok {
				txInfo.TxType = txType
			}
			if account, ok := txInfo.DecodedTx["Account"].(string); ok {
				txInfo.Account = account
			}
		}

		// Parse and prepare the transaction
		parsedTx, err := tx.ParseAndPrepare(txBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to parse: %v", err)
			txInfo.Applied = false
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.Index, txInfo.Error))
			result.Success = false
			continue
		}

		// Apply the transaction using the BlockProcessor
		// This handles: applying, setting transaction index, creating tx+meta blob
		blockTxResult, err := blockProcessor.ApplyTransaction(parsedTx.Transaction, parsedTx.RawBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to apply: %v", err)
			txInfo.Applied = false
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.Index, txInfo.Error))
			result.Success = false
			continue
		}

		applyResult := blockTxResult.ApplyResult
		txInfo.Hash = hex.EncodeToString(blockTxResult.Hash[:])
		txInfo.Result = applyResult.Result.String()
		txInfo.ResultCode = int(applyResult.Result)
		txInfo.Applied = applyResult.Applied
		txInfo.Fee = applyResult.Fee
		txInfo.Metadata = applyResult.Metadata

		result.TxResults = append(result.TxResults, txInfo)

		// Print transaction result
		statusStr := "APPLIED"
		if !applyResult.Applied {
			statusStr = "REJECTED"
		}
		fmt.Printf("      [%d] %-20s %-12s %s (fee=%d)\n",
			txEntry.Index, txInfo.TxType, applyResult.Result.String(), statusStr, applyResult.Fee)

		if verboseReplay && showDecoded {
			fmt.Printf("           Account: %s\n", txInfo.Account)
			fmt.Printf("           Hash:    %s\n", txEntry.Hash)
			if applyResult.Metadata != nil && len(applyResult.Metadata.AffectedNodes) > 0 {
				fmt.Printf("           Affected nodes: %d\n", len(applyResult.Metadata.AffectedNodes))
				for _, node := range applyResult.Metadata.AffectedNodes {
					fmt.Printf("             - %s: %s (%s)\n", node.NodeType, node.LedgerEntryType, node.LedgerIndex[:16]+"...")
				}
			}
		}

		// Add transaction to ledger using the pre-computed hash and blob from BlockProcessor
		if err := openLedger.AddTransactionWithMeta(blockTxResult.Hash, blockTxResult.TxWithMetaBlob); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: failed to add to ledger: %v", txEntry.Index, err))
		}
	}

	// Step 5: Update skip list (LedgerHashes entry)
	fmt.Println("[5/6] Updating skip list (LedgerHashes)...")
	if err := updateSkipList(openLedger, parentHash, env.LedgerIndex); err != nil {
		fmt.Printf("      WARNING: Failed to update skip list: %v\n", err)
		// Don't fail - this is not critical for basic replay testing
	} else {
		fmt.Printf("      Updated LedgerHashes with parent hash, LastLedgerSequence=%d\n", env.LedgerIndex-1)
	}

	// Step 6: Close the ledger
	fmt.Println("[6/6] Closing ledger...")
	if err := openLedger.Close(closeTime, env.CloseFlags); err != nil {
		return nil, nil, fmt.Errorf("closing ledger: %w", err)
	}

	// Get result hashes
	result.LedgerHash = openLedger.Hash()
	result.AccountHash, _ = openLedger.StateMapHash()
	result.TransactionHash, _ = openLedger.TxMapHash()
	result.TotalCoins = openLedger.TotalDrops()

	// Capture post-state for debugging
	openLedger.ForEach(func(key [32]byte, data []byte) bool {
		result.PostState[hex.EncodeToString(key[:])] = data
		return true
	})
	result.PostStateCount = len(result.PostState)

	fmt.Printf("      Post-state entries: %d\n", result.PostStateCount)
	fmt.Println()

	return result, openLedger, nil
}

func printDetailedResults(result *ReplayResult, expected *ExpectedFixture, preState *StateFixture) {
	fmt.Println("================================================================================")
	fmt.Println("                              RESULTS")
	fmt.Println("================================================================================")

	// Hash comparisons
	expectedLedgerHash, _ := hexToHash32(expected.LedgerHash)
	expectedAccountHash, _ := hexToHash32(expected.AccountHash)
	expectedTxHash, _ := hexToHash32(expected.TransactionHash)
	expectedCoins, _ := parseDrops(expected.TotalCoins)

	ledgerHashMatch := result.LedgerHash == expectedLedgerHash
	accountHashMatch := result.AccountHash == expectedAccountHash
	txHashMatch := result.TransactionHash == expectedTxHash
	coinsMatch := result.TotalCoins == expectedCoins

	fmt.Println()
	fmt.Println("Hash Comparison:")
	fmt.Println("-----------------")
	printHashRow("Ledger Hash", result.LedgerHash, expectedLedgerHash, ledgerHashMatch)
	printHashRow("Account Hash", result.AccountHash, expectedAccountHash, accountHashMatch)
	printHashRow("Transaction Hash", result.TransactionHash, expectedTxHash, txHashMatch)
	fmt.Println()

	fmt.Println("State Comparison:")
	fmt.Println("-----------------")
	fmt.Printf("Pre-state entries:  %d\n", result.PreStateCount)
	fmt.Printf("Post-state entries: %d\n", result.PostStateCount)
	fmt.Printf("Difference:         %+d entries\n", result.PostStateCount-result.PreStateCount)
	fmt.Println()

	fmt.Println("Coins Comparison:")
	fmt.Println("-----------------")
	fmt.Printf("Got:      %d drops\n", result.TotalCoins)
	fmt.Printf("Expected: %d drops\n", expectedCoins)
	fmt.Printf("Diff:     %d drops %s\n", int64(result.TotalCoins)-int64(expectedCoins), statusEmoji(coinsMatch))
	fmt.Println()

	// Transaction summary
	fmt.Println("Transaction Summary:")
	fmt.Println("--------------------")
	appliedCount := 0
	rejectedCount := 0
	errorCount := 0
	for _, txr := range result.TxResults {
		if txr.Error != "" {
			errorCount++
		} else if txr.Applied {
			appliedCount++
		} else {
			rejectedCount++
		}
	}
	fmt.Printf("Total:    %d\n", len(result.TxResults))
	fmt.Printf("Applied:  %d\n", appliedCount)
	fmt.Printf("Rejected: %d\n", rejectedCount)
	fmt.Printf("Errors:   %d\n", errorCount)
	fmt.Println()

	// Errors
	if len(result.Errors) > 0 {
		fmt.Println("Errors:")
		fmt.Println("-------")
		for _, err := range result.Errors {
			fmt.Printf("  - %s\n", err)
		}
		fmt.Println()
	}

	// Overall result
	allMatch := ledgerHashMatch && accountHashMatch && txHashMatch && coinsMatch && len(result.Errors) == 0
	result.Success = allMatch

	fmt.Println("================================================================================")
	if allMatch {
		fmt.Println("                         PASS - All checks passed")
	} else {
		fmt.Println("                         FAIL - Mismatch detected")
		fmt.Println()
		if !ledgerHashMatch {
			fmt.Println("  [X] Ledger hash mismatch")
		}
		if !accountHashMatch {
			fmt.Println("  [X] Account hash mismatch (state tree root differs)")
		}
		if !txHashMatch {
			fmt.Println("  [X] Transaction hash mismatch")
		}
		if !coinsMatch {
			fmt.Println("  [X] Total coins mismatch")
		}
		if len(result.Errors) > 0 {
			fmt.Printf("  [X] %d errors during execution\n", len(result.Errors))
		}
	}
	fmt.Println("================================================================================")
}

func printHashRow(name string, got, expected [32]byte, match bool) {
	gotHex := hex.EncodeToString(got[:])
	expectedHex := hex.EncodeToString(expected[:])
	status := statusEmoji(match)

	fmt.Printf("%s:\n", name)
	fmt.Printf("  Got:      %s %s\n", gotHex, status)
	if !match {
		fmt.Printf("  Expected: %s\n", expectedHex)
	}
}

func statusEmoji(match bool) string {
	if match {
		return "[OK]"
	}
	return "[MISMATCH]"
}

func dumpDebugInfo(result *ReplayResult, preState *StateFixture, expected *ExpectedFixture, openLedger *ledger.Ledger) {
	dir := dumpDir
	if dir == "" {
		dir = filepath.Join(fixtureDir, "debug")
	}

	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Println("                           DEBUG DUMP")
	fmt.Println("================================================================================")
	fmt.Printf("Writing debug files to: %s\n", dir)

	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("ERROR: Failed to create dump directory: %v\n", err)
		return
	}

	// Dump post-state
	fmt.Println()
	fmt.Println("Post-state entries:")
	fmt.Println("-------------------")

	postStateFile := filepath.Join(dir, "post_state.json")
	postStateData := make([]map[string]interface{}, 0)

	// Sort keys for consistent output
	keys := make([]string, 0, len(result.PostState))
	for k := range result.PostState {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, key := range keys {
		data := result.PostState[key]
		dataHex := hex.EncodeToString(data)

		entry := map[string]interface{}{
			"index":    key,
			"data_hex": dataHex,
		}

		// Try to decode
		decoded := decodeEntryData(dataHex)
		if decoded != nil {
			entry["decoded"] = decoded
			entryType := ""
			if t, ok := decoded["LedgerEntryType"]; ok {
				entryType = fmt.Sprintf("%v", t)
			}

			if verboseReplay || i < 20 {
				fmt.Printf("[%d] %s\n", i, key)
				fmt.Printf("    Type: %s\n", entryType)
				if showDecoded {
					prettyJSON, _ := json.MarshalIndent(decoded, "    ", "  ")
					fmt.Printf("    Data: %s\n", string(prettyJSON))
				}
			}
		}

		postStateData = append(postStateData, entry)
	}

	if len(keys) > 20 && !verboseReplay {
		fmt.Printf("... and %d more entries\n", len(keys)-20)
	}

	// Write post-state JSON
	postStateJSON, _ := json.MarshalIndent(postStateData, "", "  ")
	if err := os.WriteFile(postStateFile, postStateJSON, 0644); err != nil {
		fmt.Printf("ERROR: Failed to write post_state.json: %v\n", err)
	} else {
		fmt.Printf("\nWrote %s (%d entries)\n", postStateFile, len(postStateData))
	}

	// Dump state diff
	fmt.Println()
	fmt.Println("State differences:")
	fmt.Println("------------------")

	preStateMap := make(map[string]string)
	for _, entry := range preState.Entries {
		preStateMap[strings.ToLower(entry.Index)] = entry.Data
	}

	diffFile := filepath.Join(dir, "state_diff.json")
	diff := map[string]interface{}{
		"added":    make([]map[string]interface{}, 0),
		"modified": make([]map[string]interface{}, 0),
		"removed":  make([]string, 0),
	}

	addedCount := 0
	modifiedCount := 0

	for _, key := range keys {
		keyLower := strings.ToLower(key)
		postDataHex := hex.EncodeToString(result.PostState[key])

		preDataHex, exists := preStateMap[keyLower]
		if !exists {
			// Added entry
			addedCount++
			entry := map[string]interface{}{
				"index":    key,
				"data_hex": postDataHex,
			}
			if decoded := decodeEntryData(postDataHex); decoded != nil {
				entry["decoded"] = decoded
			}
			diff["added"] = append(diff["added"].([]map[string]interface{}), entry)

			if addedCount <= 10 || verboseReplay {
				fmt.Printf("ADDED: %s\n", key)
				if decoded := decodeEntryData(postDataHex); decoded != nil {
					if t, ok := decoded["LedgerEntryType"]; ok {
						fmt.Printf("  Type: %v\n", t)
					}
				}
			}
		} else if strings.ToLower(preDataHex) != strings.ToLower(postDataHex) {
			// Modified entry
			modifiedCount++
			entry := map[string]interface{}{
				"index":         key,
				"pre_data_hex":  preDataHex,
				"post_data_hex": postDataHex,
			}
			if preDec := decodeEntryData(preDataHex); preDec != nil {
				entry["pre_decoded"] = preDec
			}
			if postDec := decodeEntryData(postDataHex); postDec != nil {
				entry["post_decoded"] = postDec
			}
			diff["modified"] = append(diff["modified"].([]map[string]interface{}), entry)

			if modifiedCount <= 10 || verboseReplay {
				fmt.Printf("MODIFIED: %s\n", key)
				if preDec := decodeEntryData(preDataHex); preDec != nil {
					if t, ok := preDec["LedgerEntryType"]; ok {
						fmt.Printf("  Type: %v\n", t)
					}
				}
			}
		}
		delete(preStateMap, keyLower)
	}

	// Remaining entries in preStateMap are removed
	removedKeys := make([]string, 0)
	for key := range preStateMap {
		removedKeys = append(removedKeys, key)
	}
	sort.Strings(removedKeys)
	diff["removed"] = removedKeys

	fmt.Printf("\nSummary: +%d added, ~%d modified, -%d removed\n", addedCount, modifiedCount, len(removedKeys))

	// Write diff JSON
	diffJSON, _ := json.MarshalIndent(diff, "", "  ")
	if err := os.WriteFile(diffFile, diffJSON, 0644); err != nil {
		fmt.Printf("ERROR: Failed to write state_diff.json: %v\n", err)
	} else {
		fmt.Printf("Wrote %s\n", diffFile)
	}

	// Dump transaction results
	txResultsFile := filepath.Join(dir, "tx_results.json")
	txResultsJSON, _ := json.MarshalIndent(result.TxResults, "", "  ")
	if err := os.WriteFile(txResultsFile, txResultsJSON, 0644); err != nil {
		fmt.Printf("ERROR: Failed to write tx_results.json: %v\n", err)
	} else {
		fmt.Printf("Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))
	}

	fmt.Println()
}

func decodeEntryData(hexData string) map[string]interface{} {
	decoded, err := binarycodec.Decode(hexData)
	if err != nil {
		return nil
	}
	return decoded
}

// buildRulesFromAmendments creates amendment rules from a list of amendment names or IDs.
// If the list is empty, returns empty rules (no amendments enabled).
func buildRulesFromAmendments(amendments []string) *amendment.Rules {
	if len(amendments) == 0 {
		return amendment.EmptyRules()
	}

	builder := amendment.NewRulesBuilder()
	for _, amendmentStr := range amendments {
		// Try to find by name first
		feature := amendment.GetFeatureByName(amendmentStr)
		if feature != nil {
			builder.Enable(feature.ID)
			continue
		}

		// Try to parse as hex ID
		if len(amendmentStr) == 64 {
			var id [32]byte
			decoded, err := hex.DecodeString(amendmentStr)
			if err == nil && len(decoded) == 32 {
				copy(id[:], decoded)
				builder.Enable(id)
			}
		}
	}
	return builder.Build()
}

func hexToHash32(s string) ([32]byte, error) {
	var hash [32]byte
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return hash, err
	}
	if len(decoded) != 32 {
		return hash, fmt.Errorf("expected 32 bytes, got %d", len(decoded))
	}
	copy(hash[:], decoded)
	return hash, nil
}

func parseDrops(s string) (uint64, error) {
	var drops uint64
	_, err := fmt.Sscanf(s, "%d", &drops)
	return drops, err
}

func writeResultJSON(path string, result *ReplayResult) error {
	output := map[string]interface{}{
		"success":           result.Success,
		"ledger_hash":       hex.EncodeToString(result.LedgerHash[:]),
		"account_hash":      hex.EncodeToString(result.AccountHash[:]),
		"transaction_hash":  hex.EncodeToString(result.TransactionHash[:]),
		"total_coins":       fmt.Sprintf("%d", result.TotalCoins),
		"pre_state_count":   result.PreStateCount,
		"post_state_count":  result.PostStateCount,
		"duration_ms":       result.Duration.Milliseconds(),
		"errors":            result.Errors,
		"transaction_count": len(result.TxResults),
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// updateSkipList updates the LedgerHashes entries (skip lists) with the parent hash.
// This mirrors rippled's Ledger::updateSkipList() function.
// There are TWO skip lists:
//  1. "Every 256th ledger" skip list: Updated only when (prevIndex & 0xff) == 0,
//     using keylet::skip(prevIndex). Records a hash of every 256th ledger.
//  2. "Rolling 256" skip list: Always updated, using keylet::skip().
//     Maintains a rolling window of the most recent 256 ledger hashes.
func updateSkipList(l *ledger.Ledger, parentHash [32]byte, currentSeq uint32) error {
	if currentSeq == 0 {
		// Genesis ledger has no parent
		return nil
	}

	prevIndex := currentSeq - 1

	// 1. Update record of every 256th ledger (only when prevIndex is a multiple of 256)
	if (prevIndex & 0xff) == 0 {
		k := keylet.LedgerHashesForSeq(prevIndex)
		if err := updateOrCreateSkipListEntry(l, k, parentHash, prevIndex, false); err != nil {
			return fmt.Errorf("updating every-256th skip list: %w", err)
		}
	}

	// 2. Update record of past 256 ledgers (always)
	k := keylet.LedgerHashes()
	if err := updateOrCreateSkipListEntry(l, k, parentHash, prevIndex, true); err != nil {
		return fmt.Errorf("updating rolling-256 skip list: %w", err)
	}

	return nil
}

// updateOrCreateSkipListEntry updates or creates a skip list entry.
// If rolling is true, it removes the oldest hash when at 256 hashes (rolling window).
// If rolling is false, it just appends (for the every-256th ledger skip list).
func updateOrCreateSkipListEntry(l *ledger.Ledger, k keylet.Keylet, parentHash [32]byte, prevIndex uint32, rolling bool) error {
	// Read the existing entry
	data, err := l.Read(k)
	if err != nil {
		// Entry doesn't exist - create a new one
		return createSkipListEntry(l, k, parentHash, prevIndex)
	}

	// Decode the binary data
	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return fmt.Errorf("decoding LedgerHashes: %w", err)
	}

	// Get the current hashes array
	var hashes []string
	if hashesRaw, ok := decoded["Hashes"]; ok {
		switch arr := hashesRaw.(type) {
		case []string:
			// Binary codec returns []string directly
			hashes = make([]string, len(arr))
			copy(hashes, arr)
		case []interface{}:
			// Handle []interface{} case (e.g., from JSON unmarshaling)
			hashes = make([]string, 0, len(arr))
			for _, h := range arr {
				if hashStr, ok := h.(string); ok {
					hashes = append(hashes, hashStr)
				}
			}
		}
	}

	// If rolling and we have 256 hashes, remove the oldest (first)
	if rolling && len(hashes) >= 256 {
		hashes = hashes[1:]
	}

	// Add the new parent hash (uppercase hex)
	hashes = append(hashes, strings.ToUpper(hex.EncodeToString(parentHash[:])))

	// Update the decoded map
	decoded["Hashes"] = hashes
	decoded["LastLedgerSequence"] = prevIndex

	// Encode back to binary
	encoded, err := binarycodec.Encode(decoded)
	if err != nil {
		return fmt.Errorf("encoding LedgerHashes: %w", err)
	}

	// Decode hex to bytes
	newData, err := hex.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decoding encoded hex: %w", err)
	}

	// Update the entry
	return l.Update(k, newData)
}

// createSkipListEntry creates a new LedgerHashes entry when one doesn't exist.
func createSkipListEntry(l *ledger.Ledger, k keylet.Keylet, parentHash [32]byte, prevIndex uint32) error {
	// Create a new LedgerHashes entry
	entry := map[string]interface{}{
		"LedgerEntryType":    "LedgerHashes",
		"Flags":              uint32(0),
		"Hashes":             []string{strings.ToUpper(hex.EncodeToString(parentHash[:]))},
		"LastLedgerSequence": prevIndex,
	}

	// Encode to binary
	encoded, err := binarycodec.Encode(entry)
	if err != nil {
		return fmt.Errorf("encoding new LedgerHashes: %w", err)
	}

	// Decode hex to bytes
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decoding encoded hex: %w", err)
	}

	// Insert the new entry
	return l.Insert(k, data)
}

// extractFeesFromState extracts fee settings from state entries.
// Returns default fees if FeeSettings not found.
func extractFeesFromState(entries []StateEntry) drops.Fees {
	// FeeSettings keylet index (keylet::fees())
	feeSettingsIndex := [32]byte{}
	feeSettingsIndexBytes, _ := hex.DecodeString("4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A651")
	copy(feeSettingsIndex[:], feeSettingsIndexBytes)

	for _, entry := range entries {
		// Convert hex string index to [32]byte
		var entryIndex [32]byte
		indexBytes, err := hex.DecodeString(entry.Index)
		if err != nil || len(indexBytes) != 32 {
			continue
		}
		copy(entryIndex[:], indexBytes)

		if entryIndex == feeSettingsIndex {
			// Decode the entry
			decoded, err := binarycodec.Decode(entry.Data)
			if err != nil {
				break
			}

			fees := drops.Fees{}

			// Modern format (XRPFees amendment)
			if baseFeeDrops, ok := decoded["BaseFeeDrops"].(string); ok {
				if val, err := parseHexOrDecimal(baseFeeDrops); err == nil {
					fees.Base = drops.XRPAmount(val)
				}
			}
			if reserveBaseDrops, ok := decoded["ReserveBaseDrops"].(string); ok {
				if val, err := parseHexOrDecimal(reserveBaseDrops); err == nil {
					fees.Reserve = drops.XRPAmount(val)
				}
			}
			if reserveIncrementDrops, ok := decoded["ReserveIncrementDrops"].(string); ok {
				if val, err := parseHexOrDecimal(reserveIncrementDrops); err == nil {
					fees.Increment = drops.XRPAmount(val)
				}
			}

			// Legacy format (pre-XRPFees)
			if baseFee, ok := decoded["BaseFee"].(string); ok && fees.Base == 0 {
				if val, err := parseHexOrDecimal(baseFee); err == nil {
					fees.Base = drops.XRPAmount(val)
				}
			}
			if reserveBase, ok := decoded["ReserveBase"].(uint32); ok && fees.Reserve == 0 {
				fees.Reserve = drops.XRPAmount(reserveBase)
			}
			if reserveInc, ok := decoded["ReserveIncrement"].(uint32); ok && fees.Increment == 0 {
				fees.Increment = drops.XRPAmount(reserveInc)
			}

			// Use defaults for any unset values
			if fees.Base == 0 {
				fees.Base = 10
			}
			if fees.Reserve == 0 {
				fees.Reserve = 10_000_000
			}
			if fees.Increment == 0 {
				fees.Increment = 2_000_000
			}

			return fees
		}
	}

	// Return defaults
	return drops.Fees{
		Base:      10,
		Reserve:   10_000_000,
		Increment: 2_000_000,
	}
}

// parseHexOrDecimal parses a string that could be hex or decimal.
func parseHexOrDecimal(s string) (uint64, error) {
	// Try hex first (starts with 0x or is all hex chars)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		var val uint64
		_, err := fmt.Sscanf(s, "0x%x", &val)
		return val, err
	}
	// Try decimal
	var val uint64
	_, err := fmt.Sscanf(s, "%d", &val)
	return val, err
}

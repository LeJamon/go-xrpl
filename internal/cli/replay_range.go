package cli

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/header"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/statecompare"
	"github.com/spf13/cobra"
)

var (
	replayRangeFrom    uint32
	replayRangeTo      uint32
	replayRangeDumpDir string
	replayRangeVerbose bool
	replayRangeDecoded bool
)

// replayRangeCmd represents the replay-range command
var replayRangeCmd = &cobra.Command{
	Use:   "replay-range",
	Short: "Continuously replay transactions from a range of ledgers",
	Long: `Replay-range executes continuous state transition tests by reading
directly from the xrpl-state-compare PostgreSQL database.

It loads the initial state at ledger --from, then continuously applies
transactions from subsequent ledgers up to --to, keeping state in memory
between blocks for faster execution.

At each block, it verifies:
- ledger_hash
- account_hash (state tree root)
- transaction_hash (tx tree root)

On any mismatch, it stops immediately and dumps debug information.

Database configuration is read from environment variables:
- POSTGRES_HOST (default: localhost)
- POSTGRES_PORT (default: 5432)
- POSTGRES_DB (default: xrpl_state)
- POSTGRES_USER (default: postgres)
- POSTGRES_PASSWORD (default: postgres)

Example:
    xrpld replay-range --from 32750 --to 32800
    xrpld replay-range --from 32750 --to 32800 -v
    xrpld replay-range --from 32750 --to 32800 --dump-dir ./debug`,
	Run: runReplayRange,
}

func init() {
	rootCmd.AddCommand(replayRangeCmd)

	replayRangeCmd.Flags().Uint32Var(&replayRangeFrom, "from", 0, "Starting ledger index (pre-state)")
	replayRangeCmd.Flags().Uint32Var(&replayRangeTo, "to", 0, "Ending ledger index (last block to process)")
	replayRangeCmd.Flags().StringVar(&replayRangeDumpDir, "dump-dir", "", "Directory for debug output on failure")
	replayRangeCmd.Flags().BoolVarP(&replayRangeVerbose, "verbose", "v", false, "Verbose output")
	replayRangeCmd.Flags().BoolVar(&replayRangeDecoded, "decoded", false, "Show decoded JSON for entries")

	replayRangeCmd.MarkFlagRequired("from")
	replayRangeCmd.MarkFlagRequired("to")
}

// RangeReplayStats holds statistics for the replay run
type RangeReplayStats struct {
	BlocksProcessed   int
	BlocksSuccessful  int
	TotalTransactions int
	TotalDuration     time.Duration
	FailedAtBlock     uint32
	FailureReason     string
}

func runReplayRange(cmd *cobra.Command, args []string) {
	if replayRangeFrom >= replayRangeTo {
		fmt.Fprintf(os.Stderr, "ERROR: --from must be less than --to\n")
		os.Exit(1)
	}

	ctx := context.Background()
	startTime := time.Now()

	fmt.Println("================================================================================")
	fmt.Println("                    XRPL Continuous State Replay")
	fmt.Println("================================================================================")
	fmt.Printf("Range:      %d -> %d (%d blocks)\n", replayRangeFrom, replayRangeTo, replayRangeTo-replayRangeFrom)
	fmt.Printf("Started at: %s\n", startTime.Format(time.RFC3339))
	fmt.Println()

	// Connect to database
	fmt.Println("[1/3] Connecting to database...")
	client, err := statecompare.NewClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()
	fmt.Println("      Connected to PostgreSQL")

	// Validate range exists
	fmt.Println("[2/3] Validating ledger range...")
	valid, missingLedger, err := client.ValidateRange(ctx, replayRangeFrom, replayRangeTo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to validate range: %v\n", err)
		os.Exit(1)
	}
	if !valid {
		fmt.Fprintf(os.Stderr, "ERROR: Ledger %d not found in database\n", missingLedger)
		fmt.Fprintf(os.Stderr, "       Run 'python main.py sync-range %d %d' first\n", replayRangeFrom, replayRangeTo)
		os.Exit(1)
	}
	fmt.Printf("      All %d ledgers present in database\n", replayRangeTo-replayRangeFrom+1)

	// Load initial state
	fmt.Printf("[3/3] Loading initial state at ledger %d...\n", replayRangeFrom)
	stateMap, preSnapshot, fees, err := loadInitialState(ctx, client, replayRangeFrom)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to load initial state: %v\n", err)
		os.Exit(1)
	}

	stateCount, _ := client.GetStateEntryCount(ctx, replayRangeFrom)
	fmt.Printf("      Loaded %d state entries\n", stateCount)
	fmt.Println()

	// Start continuous replay
	fmt.Println("--- Starting Continuous Replay ---")
	fmt.Println()

	stats := &RangeReplayStats{}
	currentStateMap := stateMap
	previousSnapshot := preSnapshot

	for targetLedger := replayRangeFrom + 1; targetLedger <= replayRangeTo; targetLedger++ {
		blockStart := time.Now()

		// Process this block
		result, newStateMap, err := processBlock(ctx, client, currentStateMap, previousSnapshot, targetLedger, fees)
		if err != nil {
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = err.Error()
			fmt.Printf("[%d] ERROR: %v\n", targetLedger, err)
			break
		}

		blockDuration := time.Since(blockStart)
		stats.BlocksProcessed++
		stats.TotalTransactions += result.TxCount

		// Check hashes
		if !result.Success {
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = "hash mismatch"

			fmt.Printf("[%d] %3d txs | FAIL | %v\n", targetLedger, result.TxCount, blockDuration.Round(time.Millisecond))
			fmt.Println()

			// Dump debug info
			dumpRangeDebugInfo(targetLedger, result, currentStateMap)

			printRangeFailure(targetLedger, result)
			break
		}

		stats.BlocksSuccessful++

		// Print progress
		if replayRangeVerbose {
			fmt.Printf("[%d] %3d txs | OK   | %v\n", targetLedger, result.TxCount, blockDuration.Round(time.Millisecond))
		} else {
			// Compact output: show every 10 blocks or last block
			if stats.BlocksProcessed%10 == 0 || targetLedger == replayRangeTo {
				elapsed := time.Since(startTime)
				blocksPerSec := float64(stats.BlocksProcessed) / elapsed.Seconds()
				fmt.Printf("[%d] %d blocks processed | %.1f blk/s\n", targetLedger, stats.BlocksProcessed, blocksPerSec)
			}
		}

		// Update state for next iteration
		currentStateMap = newStateMap
		previousSnapshot = result.PostSnapshot

		// Update fees from the new state (in case a SetFee transaction was processed)
		fees = ExtractFeesFromSHAMap(currentStateMap)
	}

	stats.TotalDuration = time.Since(startTime)

	// Print summary
	fmt.Println()
	printRangeSummary(stats)

	if stats.FailedAtBlock > 0 {
		os.Exit(1)
	}
}

// BlockResult holds the result of processing a single block
type BlockResult struct {
	Success                 bool
	TxCount                 int
	LedgerHash              [32]byte
	AccountHash             [32]byte
	TransactionHash         [32]byte
	TotalCoins              uint64
	ExpectedLedgerHash      [32]byte
	ExpectedAccountHash     [32]byte
	ExpectedTransactionHash [32]byte
	ExpectedTotalCoins      uint64
	PostSnapshot            *statecompare.LedgerSnapshot
	PostState               map[string][]byte
	PreState                map[string][]byte
	TxResults               []TxApplyInfo
	Errors                  []string
}

func loadInitialState(ctx context.Context, client *statecompare.Client, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	// Get snapshot
	snapshot, err := client.GetSnapshot(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	// Get state entries
	entries, err := client.GetStateEntries(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting state entries: %w", err)
	}

	// Create state map
	stateMap, err := shamap.New(shamap.TypeState)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("creating state map: %w", err)
	}

	// Inject entries
	for _, entry := range entries {
		if err := stateMap.Put(entry.Index, entry.Data); err != nil {
			return nil, nil, drops.Fees{}, fmt.Errorf("injecting entry: %w", err)
		}
	}

	// Extract fees from state (use defaults if not found)
	fees := ExtractFeesFromState(entries)

	return stateMap, snapshot, fees, nil
}

func ExtractFeesFromState(entries []statecompare.StateEntry) drops.Fees {
	// FeeSettings keylet index
	feeSettingsIndex := [32]byte{}
	feeSettingsIndexBytes, _ := hex.DecodeString("4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A651")
	copy(feeSettingsIndex[:], feeSettingsIndexBytes)

	for _, entry := range entries {
		if entry.Index == feeSettingsIndex {
			// Decode the entry
			decoded, err := binarycodec.Decode(hex.EncodeToString(entry.Data))
			if err != nil {
				break
			}

			fees := drops.Fees{}

			if baseFee, ok := decoded["BaseFee"].(string); ok {
				if val, err := parseHexOrDecimal(baseFee); err == nil {
					fees.Base = drops.XRPAmount(val)
				}
			}
			if reserveBase, ok := decoded["ReserveBase"].(uint32); ok {
				fees.Reserve = drops.XRPAmount(reserveBase)
			}
			if reserveInc, ok := decoded["ReserveIncrement"].(uint32); ok {
				fees.Increment = drops.XRPAmount(reserveInc)
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

// extractFeesFromSHAMap extracts fee settings from a state SHAMap.
// Returns default fees if FeeSettings not found.
func ExtractFeesFromSHAMap(stateMap *shamap.SHAMap) drops.Fees {
	// FeeSettings keylet index (keylet::fees())
	feeSettingsIndex := [32]byte{}
	feeSettingsIndexBytes, _ := hex.DecodeString("4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A651")
	copy(feeSettingsIndex[:], feeSettingsIndexBytes)

	// Try to get the FeeSettings entry from the state map
	item, found, err := stateMap.Get(feeSettingsIndex)
	if err != nil || !found || item == nil {
		// Return defaults if not found
		return drops.Fees{
			Base:      10,
			Reserve:   10_000_000,
			Increment: 2_000_000,
		}
	}

	// Get the data from the item
	data := item.Data()

	// Decode the entry
	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return drops.Fees{
			Base:      10,
			Reserve:   10_000_000,
			Increment: 2_000_000,
		}
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

func processBlock(
	ctx context.Context,
	client *statecompare.Client,
	preStateMap *shamap.SHAMap,
	preSnapshot *statecompare.LedgerSnapshot,
	targetLedger uint32,
	fees drops.Fees,
) (*BlockResult, *shamap.SHAMap, error) {
	result := &BlockResult{
		PostState: make(map[string][]byte),
		PreState:  make(map[string][]byte),
		TxResults: make([]TxApplyInfo, 0),
		Errors:    make([]string, 0),
	}

	// Capture pre-state for debugging
	_ = preStateMap.ForEach(func(item *shamap.Item) bool {
		key := item.Key()
		result.PreState[hex.EncodeToString(key[:])] = item.Data()
		return true
	})

	// Get expected values for this ledger
	postSnapshot, err := client.GetSnapshot(ctx, targetLedger)
	if err != nil {
		return nil, nil, fmt.Errorf("getting target snapshot: %w", err)
	}
	result.PostSnapshot = postSnapshot
	result.ExpectedLedgerHash = postSnapshot.LedgerHash
	result.ExpectedAccountHash = postSnapshot.AccountHash
	result.ExpectedTransactionHash = postSnapshot.TransactionHash
	result.ExpectedTotalCoins = postSnapshot.TotalCoins

	// Get transactions for this ledger
	txs, err := client.GetTransactions(ctx, targetLedger)
	if err != nil {
		return nil, nil, fmt.Errorf("getting transactions: %w", err)
	}
	result.TxCount = len(txs)

	// Create transaction map
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return nil, nil, fmt.Errorf("creating tx map: %w", err)
	}

	// Setup ledger header
	rippleEpoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	closeTime := rippleEpoch.Add(time.Duration(postSnapshot.CloseTime) * time.Second)
	parentCloseTime := rippleEpoch.Add(time.Duration(preSnapshot.CloseTime) * time.Second)

	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         targetLedger,
		ParentHash:          preSnapshot.LedgerHash,
		ParentCloseTime:     parentCloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: postSnapshot.CloseTimeResolution,
		CloseFlags:          postSnapshot.CloseFlags,
		Drops:               preSnapshot.TotalCoins, // Start with parent's total coins
	}

	// Create open ledger with current state
	openLedger := ledger.NewOpenWithHeader(ledgerHeader, preStateMap, txMap, fees)

	// Setup engine
	// TODO: Load amendments from the Amendments ledger entry in the state.
	// For now, use EmptyRules() which is correct for early historical ledgers
	// before amendments were enabled on mainnet.
	engineConfig := tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            targetLedger,
		SkipSignatureVerification: true,
		Standalone:                true,
		Rules:                     amendment.EmptyRules(),
	}

	engine := tx.NewEngine(openLedger, engineConfig)
	blockProcessor := tx.NewBlockProcessor(engine)

	// Apply transactions
	for _, txEntry := range txs {
		txInfo := TxApplyInfo{
			Index: txEntry.TxIndex,
			Hash:  hex.EncodeToString(txEntry.TxHash[:]),
		}

		// Decode for display
		txInfo.DecodedTx = decodeEntryData(hex.EncodeToString(txEntry.TxBlob))
		if txInfo.DecodedTx != nil {
			if txType, ok := txInfo.DecodedTx["TransactionType"].(string); ok {
				txInfo.TxType = txType
			}
			if account, ok := txInfo.DecodedTx["Account"].(string); ok {
				txInfo.Account = account
			}
		}

		// Parse transaction
		parsedTx, err := tx.ParseAndPrepare(txEntry.TxBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to parse: %v", err)
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.TxIndex, txInfo.Error))
			continue
		}

		// Apply transaction
		blockTxResult, err := blockProcessor.ApplyTransaction(parsedTx.Transaction, parsedTx.RawBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to apply: %v", err)
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.TxIndex, txInfo.Error))
			continue
		}

		applyResult := blockTxResult.ApplyResult
		txInfo.Result = applyResult.Result.String()
		txInfo.ResultCode = int(applyResult.Result)
		txInfo.Applied = applyResult.Applied
		txInfo.Fee = applyResult.Fee
		txInfo.Metadata = applyResult.Metadata

		result.TxResults = append(result.TxResults, txInfo)

		// Add to ledger
		if err := openLedger.AddTransactionWithMeta(blockTxResult.Hash, blockTxResult.TxWithMetaBlob); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: failed to add to ledger: %v", txEntry.TxIndex, err))
		}

		if replayRangeVerbose && replayRangeDecoded {
			fmt.Printf("        [%d] %-20s %-12s\n", txEntry.TxIndex, txInfo.TxType, txInfo.Result)
		}
	}

	// Update skip list
	if err := updateSkipList(openLedger, preSnapshot.LedgerHash, targetLedger); err != nil {
		// Log but don't fail
		if replayRangeVerbose {
			fmt.Printf("      WARNING: Failed to update skip list: %v\n", err)
		}
	}

	// Close ledger
	if err := openLedger.Close(closeTime, postSnapshot.CloseFlags); err != nil {
		return nil, nil, fmt.Errorf("closing ledger: %w", err)
	}

	// Get result hashes
	result.LedgerHash = openLedger.Hash()
	result.AccountHash, _ = openLedger.StateMapHash()
	result.TransactionHash, _ = openLedger.TxMapHash()
	result.TotalCoins = openLedger.TotalDrops()

	// Capture post-state
	_ = openLedger.ForEach(func(key [32]byte, data []byte) bool {
		result.PostState[hex.EncodeToString(key[:])] = data
		return true
	})

	// Check all three hashes
	ledgerHashMatch := result.LedgerHash == result.ExpectedLedgerHash
	accountHashMatch := result.AccountHash == result.ExpectedAccountHash
	txHashMatch := result.TransactionHash == result.ExpectedTransactionHash

	result.Success = ledgerHashMatch && accountHashMatch && txHashMatch && len(result.Errors) == 0

	// Get the new state map for next iteration
	newStateMap, err := openLedger.StateMapSnapshot()
	if err != nil {
		return nil, nil, fmt.Errorf("getting state snapshot: %w", err)
	}

	return result, newStateMap, nil
}

func dumpRangeDebugInfo(ledgerIndex uint32, result *BlockResult, preStateMap *shamap.SHAMap) {
	dir := replayRangeDumpDir
	if dir == "" {
		dir = fmt.Sprintf("./debug/ledger_%d", ledgerIndex)
	} else {
		dir = filepath.Join(dir, fmt.Sprintf("ledger_%d", ledgerIndex))
	}

	fmt.Printf("Writing debug files to: %s\n", dir)

	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Printf("ERROR: Failed to create dump directory: %v\n", err)
		return
	}

	// Dump post-state
	postStateFile := filepath.Join(dir, "post_state.json")
	postStateData := make([]map[string]interface{}, 0)

	keys := make([]string, 0, len(result.PostState))
	for k := range result.PostState {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		data := result.PostState[key]
		dataHex := hex.EncodeToString(data)

		entry := map[string]interface{}{
			"index":    key,
			"data_hex": dataHex,
		}

		if decoded := decodeEntryData(dataHex); decoded != nil {
			entry["decoded"] = decoded
		}

		postStateData = append(postStateData, entry)
	}

	postStateJSON, _ := json.MarshalIndent(postStateData, "", "  ")
	os.WriteFile(postStateFile, postStateJSON, 0644)
	fmt.Printf("  Wrote %s (%d entries)\n", postStateFile, len(postStateData))

	// Dump state diff
	diffFile := filepath.Join(dir, "state_diff.json")
	diff := map[string]interface{}{
		"added":    make([]map[string]interface{}, 0),
		"modified": make([]map[string]interface{}, 0),
		"removed":  make([]string, 0),
	}

	// Build pre-state map for comparison
	preStateKeys := make(map[string]string)
	for key, data := range result.PreState {
		preStateKeys[strings.ToLower(key)] = hex.EncodeToString(data)
	}

	for _, key := range keys {
		keyLower := strings.ToLower(key)
		postDataHex := hex.EncodeToString(result.PostState[key])

		preDataHex, exists := preStateKeys[keyLower]
		if !exists {
			entry := map[string]interface{}{
				"index":    key,
				"data_hex": postDataHex,
			}
			if decoded := decodeEntryData(postDataHex); decoded != nil {
				entry["decoded"] = decoded
			}
			diff["added"] = append(diff["added"].([]map[string]interface{}), entry)
		} else if strings.ToLower(preDataHex) != strings.ToLower(postDataHex) {
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
		}
		delete(preStateKeys, keyLower)
	}

	removedKeys := make([]string, 0)
	for key := range preStateKeys {
		removedKeys = append(removedKeys, key)
	}
	sort.Strings(removedKeys)
	diff["removed"] = removedKeys

	diffJSON, _ := json.MarshalIndent(diff, "", "  ")
	os.WriteFile(diffFile, diffJSON, 0644)
	fmt.Printf("  Wrote %s\n", diffFile)

	// Dump transaction results
	txResultsFile := filepath.Join(dir, "tx_results.json")
	txResultsJSON, _ := json.MarshalIndent(result.TxResults, "", "  ")
	os.WriteFile(txResultsFile, txResultsJSON, 0644)
	fmt.Printf("  Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))
}

func printRangeFailure(ledgerIndex uint32, result *BlockResult) {
	fmt.Println()
	fmt.Println("================================================================================")
	fmt.Printf("                      FAILED at ledger %d\n", ledgerIndex)
	fmt.Println("================================================================================")
	fmt.Println()

	ledgerHashMatch := result.LedgerHash == result.ExpectedLedgerHash
	accountHashMatch := result.AccountHash == result.ExpectedAccountHash
	txHashMatch := result.TransactionHash == result.ExpectedTransactionHash

	fmt.Println("Hash Comparison:")
	fmt.Println("-----------------")

	printRangeHashRow("Ledger Hash", result.LedgerHash, result.ExpectedLedgerHash, ledgerHashMatch)
	printRangeHashRow("Account Hash", result.AccountHash, result.ExpectedAccountHash, accountHashMatch)
	printRangeHashRow("Transaction Hash", result.TransactionHash, result.ExpectedTransactionHash, txHashMatch)

	fmt.Println()
	fmt.Printf("Total Coins: got %d, expected %d\n", result.TotalCoins, result.ExpectedTotalCoins)

	if len(result.Errors) > 0 {
		fmt.Println()
		fmt.Println("Errors:")
		for _, err := range result.Errors {
			fmt.Printf("  - %s\n", err)
		}
	}

	fmt.Println()
	fmt.Println("Use 'xrpld compare' to analyze state differences.")
	fmt.Println("================================================================================")
}

func printRangeHashRow(name string, got, expected [32]byte, match bool) {
	gotHex := hex.EncodeToString(got[:])
	expectedHex := hex.EncodeToString(expected[:])

	status := "[OK]"
	if !match {
		status = "[MISMATCH]"
	}

	fmt.Printf("%s: %s\n", name, status)
	fmt.Printf("  Got:      %s\n", gotHex)
	if !match {
		fmt.Printf("  Expected: %s\n", expectedHex)
	}
}

func printRangeSummary(stats *RangeReplayStats) {
	fmt.Println("================================================================================")
	if stats.FailedAtBlock > 0 {
		fmt.Printf("FAILED at block %d: %s\n", stats.FailedAtBlock, stats.FailureReason)
	} else {
		fmt.Println("SUCCESS: All blocks replayed successfully")
	}
	fmt.Println("================================================================================")
	fmt.Printf("Blocks processed:    %d\n", stats.BlocksProcessed)
	fmt.Printf("Blocks successful:   %d\n", stats.BlocksSuccessful)
	fmt.Printf("Total transactions:  %d\n", stats.TotalTransactions)
	fmt.Printf("Total time:          %v\n", stats.TotalDuration.Round(time.Millisecond))
	if stats.TotalDuration.Seconds() > 0 {
		fmt.Printf("Average speed:       %.1f blocks/sec\n", float64(stats.BlocksProcessed)/stats.TotalDuration.Seconds())
	}
	fmt.Println("================================================================================")
}

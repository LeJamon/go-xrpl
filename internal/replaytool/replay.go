package replaytool

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	"github.com/spf13/cobra"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
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
	DecodedTx  map[string]any
	Metadata   *tx.Metadata
	Error      string
	RawBlob    []byte `json:"-"`
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

// replayRunner holds one `replay` invocation's flags and output sink. Flags bind
// to its fields (not package globals), so each NewCommands() call is fully
// isolated and the printers can be tested by pointing out at a buffer.
type replayRunner struct {
	out io.Writer

	fixtureDir   string
	outputResult string
	verbose      bool
	dumpState    bool
	dumpDir      string
	showDecoded  bool
}

// newReplayCmd builds the `replay` command and its flags.
func newReplayCmd() *cobra.Command {
	r := &replayRunner{}
	cmd := &cobra.Command{
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
		RunE: func(cmd *cobra.Command, args []string) error {
			r.out = cmd.OutOrStdout()
			r.fixtureDir = args[0]
			return r.run()
		},
	}

	cmd.Flags().StringVarP(&r.outputResult, "output", "o", "", "Output file for results (JSON)")
	cmd.Flags().BoolVarP(&r.verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&r.dumpState, "dump", false, "Dump full state on failure (or always with -v)")
	cmd.Flags().StringVar(&r.dumpDir, "dump-dir", "", "Directory to write state dumps (default: fixture-dir/debug)")
	cmd.Flags().BoolVar(&r.showDecoded, "decoded", false, "Show decoded JSON for transactions and state entries")

	return cmd
}

func (r *replayRunner) run() error {
	startTime := time.Now()

	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                        XRPL State Transition Replay")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Fixture directory: %s\n", r.fixtureDir)
	fmt.Fprintf(r.out, "Started at:        %s\n", startTime.Format(time.RFC3339))
	fmt.Fprintln(r.out)

	// Load fixtures
	state, env, txs, expected, err := loadFixtures(r.fixtureDir)
	if err != nil {
		return fmt.Errorf("loading fixtures: %w", err)
	}

	r.printFixtureInfo(state, env, txs, expected)

	// Execute replay
	result, openLedger, err := r.executeReplayVerbose(state, env, txs)
	if err != nil {
		return fmt.Errorf("replay execution failed: %w", err)
	}

	result.Duration = time.Since(startTime)

	result.Success = computeReplaySuccess(result, expected)

	// Print detailed results
	r.printDetailedResults(result, expected)

	if (r.dumpState || !result.Success) && openLedger != nil {
		r.dumpDebugInfo(result, state)
	}

	// Write output if requested
	if r.outputResult != "" {
		if err := writeResultJSON(r.outputResult, result); err != nil {
			fmt.Fprintf(r.out, "ERROR: Failed to write output: %v\n", err)
		} else {
			fmt.Fprintf(r.out, "\nResults written to: %s\n", r.outputResult)
		}
	}

	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "Duration: %v\n", result.Duration)

	if !result.Success {
		return cmdexit.ErrReported
	}
	return nil
}

// computeReplaySuccess reports whether the replayed ledger matches the expected
// fixture on every checked hash, total coins, and with no execution errors.
func computeReplaySuccess(result *ReplayResult, expected *ExpectedFixture) bool {
	expectedLedgerHash, _ := hexToHash32(expected.LedgerHash)
	expectedAccountHash, _ := hexToHash32(expected.AccountHash)
	expectedTxHash, _ := hexToHash32(expected.TransactionHash)
	expectedCoins, _ := parseDrops(expected.TotalCoins)

	return result.LedgerHash == expectedLedgerHash &&
		result.AccountHash == expectedAccountHash &&
		result.TransactionHash == expectedTxHash &&
		result.TotalCoins == expectedCoins &&
		len(result.Errors) == 0
}

func (r *replayRunner) printFixtureInfo(state *StateFixture, env *EnvFixture, txs *TxsFixture, expected *ExpectedFixture) {
	fmt.Fprintln(r.out, "--- Fixture Summary ---")
	fmt.Fprintf(r.out, "Pre-state ledger:     %d\n", state.LedgerIndex)
	fmt.Fprintf(r.out, "Pre-state entries:    %d\n", len(state.Entries))
	fmt.Fprintf(r.out, "Pre-state hash:       %s\n", state.AccountHash)
	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "Target ledger:        %d\n", env.LedgerIndex)
	fmt.Fprintf(r.out, "Transactions:         %d\n", len(txs.Transactions))
	fmt.Fprintf(r.out, "Parent hash:          %s\n", env.ParentHash)
	fmt.Fprintf(r.out, "Close time:           %d\n", env.CloseTime)
	fmt.Fprintf(r.out, "Close time res:       %d\n", env.CloseTimeResolution)
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "Fee settings:")
	fmt.Fprintf(r.out, "  Base fee:           %d drops\n", env.Fees.BaseFee)
	fmt.Fprintf(r.out, "  Reserve base:       %d drops (%d XRP)\n", env.Fees.ReserveBase, env.Fees.ReserveBase/1_000_000)
	fmt.Fprintf(r.out, "  Reserve increment:  %d drops (%d XRP)\n", env.Fees.ReserveIncrement, env.Fees.ReserveIncrement/1_000_000)
	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "Expected ledger hash: %s\n", expected.LedgerHash)
	fmt.Fprintf(r.out, "Expected state hash:  %s\n", expected.AccountHash)
	fmt.Fprintf(r.out, "Expected tx hash:     %s\n", expected.TransactionHash)
	fmt.Fprintf(r.out, "Expected total coins: %s\n", expected.TotalCoins)
	fmt.Fprintln(r.out)
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

func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (r *replayRunner) executeReplayVerbose(state *StateFixture, env *EnvFixture, txs *TxsFixture) (*ReplayResult, *ledger.Ledger, error) {
	result := &ReplayResult{
		Success:       true,
		Errors:        make([]string, 0),
		TxResults:     make([]TxApplyInfo, 0),
		PreStateCount: len(state.Entries),
		PostState:     make(map[string][]byte),
	}

	fmt.Fprintln(r.out, "--- Execution ---")

	// Step 1: Create state map and inject pre-state
	fmt.Fprintf(r.out, "[1/5] Injecting %d pre-state entries...\n", len(state.Entries))

	stateMap := shamap.New(shamap.TypeState)

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

		if r.verbose && r.showDecoded && i < 5 {
			decoded := decodeEntryData(entry.Data)
			fmt.Fprintf(r.out, "      Entry %d: %s\n", i, shortHex(entry.Index, 16))
			if decoded != nil {
				if entryType, ok := decoded["LedgerEntryType"]; ok {
					fmt.Fprintf(r.out, "        Type: %v\n", entryType)
				}
			}
		}
	}

	preStateHash, _ := stateMap.Hash()
	fmt.Fprintf(r.out, "      Pre-state hash: %s\n", hex.EncodeToString(preStateHash[:]))

	// Step 2: Create transaction map
	fmt.Fprintln(r.out, "[2/5] Creating transaction map...")
	txMap := shamap.New(shamap.TypeTransaction)

	// Step 3: Parse environment
	fmt.Fprintln(r.out, "[3/5] Setting up ledger environment...")
	totalCoins, err := parseDrops(env.TotalCoins)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing total_coins: %w", err)
	}

	parentHash, err := hexToHash32(env.ParentHash)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing parent_hash: %w", err)
	}

	closeTime := time.Unix(protocol.RippleEpochUnix+env.CloseTime, 0).UTC()
	parentCloseTime := time.Unix(protocol.RippleEpochUnix+env.ParentCloseTime, 0).UTC()

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

	fmt.Fprintf(r.out, "      Ledger sequence: %d\n", env.LedgerIndex)
	fmt.Fprintf(r.out, "      Total coins:     %d drops\n", totalCoins)

	// Step 4: Apply transactions
	fmt.Fprintf(r.out, "[4/5] Applying %d transactions...\n", len(txs.Transactions))

	// Build amendment rules from the amendments list in the fixture
	rules := buildRulesFromAmendments(env.Amendments)

	engineConfig := tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            env.LedgerIndex,
		ParentCloseTime:           uint32(env.ParentCloseTime),
		SkipSignatureVerification: true,
		Standalone:                true,
		Rules:                     rules,
	}

	engine := txengine.NewEngine(openLedger, engineConfig)
	blockProcessor := txengine.NewBlockProcessor(engine)

	// The full per-tx decode feeds verbose/decoded output and the JSON result
	// artifact; the apply path never needs it, and the on-failure dump backfills
	// it on demand from the retained blob.
	wantTxDetail := r.verbose || r.dumpState || r.showDecoded || r.outputResult != ""
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

		// Parse and prepare the transaction
		parsedTx, err := txengine.ParseAndPrepare(txBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to parse: %v", err)
			txInfo.Applied = false
			fillTxDisplay(&txInfo, txBlob, nil, wantTxDetail)
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.Index, txInfo.Error))
			result.Success = false
			continue
		}
		fillTxDisplay(&txInfo, txBlob, parsedTx.Transaction, wantTxDetail)

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
		fmt.Fprintf(r.out, "      [%d] %-20s %-12s %s (fee=%d)\n",
			txEntry.Index, txInfo.TxType, applyResult.Result.String(), statusStr, applyResult.Fee)

		if r.verbose && r.showDecoded {
			fmt.Fprintf(r.out, "           Account: %s\n", txInfo.Account)
			fmt.Fprintf(r.out, "           Hash:    %s\n", txEntry.Hash)
			if applyResult.Metadata != nil && len(applyResult.Metadata.AffectedNodes) > 0 {
				fmt.Fprintf(r.out, "           Affected nodes: %d\n", len(applyResult.Metadata.AffectedNodes))
				for _, node := range applyResult.Metadata.AffectedNodes {
					fmt.Fprintf(r.out, "             - %s: %s (%s)\n", node.NodeType, node.LedgerEntryType, shortHex(node.LedgerIndex, 16))
				}
			}
		}

		// Add transaction to ledger using the pre-computed hash and blob from BlockProcessor
		if err := openLedger.AddTransactionWithMeta(blockTxResult.Hash, blockTxResult.TxWithMetaBlob); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: failed to add to ledger: %v", txEntry.Index, err))
		}
	}

	// Step 5: Close the ledger. Close() updates the LedgerHashes skip lists
	// from the header's ParentHash before sealing state, so there is no
	// separate skip-list pass — running one would double-append the hash.
	fmt.Fprintln(r.out, "[5/5] Closing ledger...")
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

	fmt.Fprintf(r.out, "      Post-state entries: %d\n", result.PostStateCount)
	fmt.Fprintln(r.out)

	return result, openLedger, nil
}

func (r *replayRunner) printDetailedResults(result *ReplayResult, expected *ExpectedFixture) {
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                              RESULTS")
	fmt.Fprintln(r.out, "================================================================================")

	// Hash comparisons
	expectedLedgerHash, _ := hexToHash32(expected.LedgerHash)
	expectedAccountHash, _ := hexToHash32(expected.AccountHash)
	expectedTxHash, _ := hexToHash32(expected.TransactionHash)
	expectedCoins, _ := parseDrops(expected.TotalCoins)

	ledgerHashMatch := result.LedgerHash == expectedLedgerHash
	accountHashMatch := result.AccountHash == expectedAccountHash
	txHashMatch := result.TransactionHash == expectedTxHash
	coinsMatch := result.TotalCoins == expectedCoins

	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "Hash Comparison:")
	fmt.Fprintln(r.out, "-----------------")
	r.printHashRow("Ledger Hash", result.LedgerHash, expectedLedgerHash, ledgerHashMatch)
	r.printHashRow("Account Hash", result.AccountHash, expectedAccountHash, accountHashMatch)
	r.printHashRow("Transaction Hash", result.TransactionHash, expectedTxHash, txHashMatch)
	fmt.Fprintln(r.out)

	fmt.Fprintln(r.out, "State Comparison:")
	fmt.Fprintln(r.out, "-----------------")
	fmt.Fprintf(r.out, "Pre-state entries:  %d\n", result.PreStateCount)
	fmt.Fprintf(r.out, "Post-state entries: %d\n", result.PostStateCount)
	fmt.Fprintf(r.out, "Difference:         %+d entries\n", result.PostStateCount-result.PreStateCount)
	fmt.Fprintln(r.out)

	fmt.Fprintln(r.out, "Coins Comparison:")
	fmt.Fprintln(r.out, "-----------------")
	fmt.Fprintf(r.out, "Got:      %d drops\n", result.TotalCoins)
	fmt.Fprintf(r.out, "Expected: %d drops\n", expectedCoins)
	fmt.Fprintf(r.out, "Diff:     %d drops %s\n", int64(result.TotalCoins)-int64(expectedCoins), statusEmoji(coinsMatch))
	fmt.Fprintln(r.out)

	// Transaction summary
	fmt.Fprintln(r.out, "Transaction Summary:")
	fmt.Fprintln(r.out, "--------------------")
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
	fmt.Fprintf(r.out, "Total:    %d\n", len(result.TxResults))
	fmt.Fprintf(r.out, "Applied:  %d\n", appliedCount)
	fmt.Fprintf(r.out, "Rejected: %d\n", rejectedCount)
	fmt.Fprintf(r.out, "Errors:   %d\n", errorCount)
	fmt.Fprintln(r.out)

	// Errors
	if len(result.Errors) > 0 {
		fmt.Fprintln(r.out, "Errors:")
		fmt.Fprintln(r.out, "-------")
		for _, err := range result.Errors {
			fmt.Fprintf(r.out, "  - %s\n", err)
		}
		fmt.Fprintln(r.out)
	}

	fmt.Fprintln(r.out, "================================================================================")
	if result.Success {
		fmt.Fprintln(r.out, "                         PASS - All checks passed")
	} else {
		fmt.Fprintln(r.out, "                         FAIL - Mismatch detected")
		fmt.Fprintln(r.out)
		if !ledgerHashMatch {
			fmt.Fprintln(r.out, "  [X] Ledger hash mismatch")
		}
		if !accountHashMatch {
			fmt.Fprintln(r.out, "  [X] Account hash mismatch (state tree root differs)")
		}
		if !txHashMatch {
			fmt.Fprintln(r.out, "  [X] Transaction hash mismatch")
		}
		if !coinsMatch {
			fmt.Fprintln(r.out, "  [X] Total coins mismatch")
		}
		if len(result.Errors) > 0 {
			fmt.Fprintf(r.out, "  [X] %d errors during execution\n", len(result.Errors))
		}
	}
	fmt.Fprintln(r.out, "================================================================================")
}

func (r *replayRunner) printHashRow(name string, got, expected [32]byte, match bool) {
	gotHex := hex.EncodeToString(got[:])
	expectedHex := hex.EncodeToString(expected[:])
	status := statusEmoji(match)

	fmt.Fprintf(r.out, "%s:\n", name)
	fmt.Fprintf(r.out, "  Got:      %s %s\n", gotHex, status)
	if !match {
		fmt.Fprintf(r.out, "  Expected: %s\n", expectedHex)
	}
}

func statusEmoji(match bool) string {
	if match {
		return "[OK]"
	}
	return "[MISMATCH]"
}

func (r *replayRunner) dumpDebugInfo(result *ReplayResult, preState *StateFixture) {
	dir := r.dumpDir
	if dir == "" {
		dir = filepath.Join(r.fixtureDir, "debug")
	}

	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                           DEBUG DUMP")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Writing debug files to: %s\n", dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(r.out, "ERROR: Failed to create dump directory: %v\n", err)
		return
	}

	// Normalize pre- and post-state to lowercase-hex index → hex data so the
	// shared diff helper can compare them.
	post := make(map[string]string, len(result.PostState))
	for key, data := range result.PostState {
		post[key] = hex.EncodeToString(data)
	}
	pre := make(map[string]string, len(preState.Entries))
	for _, entry := range preState.Entries {
		pre[strings.ToLower(entry.Index)] = entry.Data
	}

	postStateFile := filepath.Join(dir, "post_state.json")
	postStateData := postStateEntries(post)
	if err := writeJSONFile(postStateFile, postStateData); err != nil {
		fmt.Fprintf(r.out, "ERROR: Failed to write post_state.json: %v\n", err)
	} else {
		fmt.Fprintf(r.out, "Wrote %s (%d entries)\n", postStateFile, len(postStateData))
	}

	diff := computeStateDiff(pre, post)
	added, modified, removed := diffCounts(diff)
	diffFile := filepath.Join(dir, "state_diff.json")
	if err := writeJSONFile(diffFile, diff); err != nil {
		fmt.Fprintf(r.out, "ERROR: Failed to write state_diff.json: %v\n", err)
	} else {
		fmt.Fprintf(r.out, "Wrote %s\n", diffFile)
	}
	fmt.Fprintf(r.out, "State diff: +%d added, ~%d modified, -%d removed\n", added, modified, removed)

	txResultsFile := filepath.Join(dir, "tx_results.json")
	materializeDecoded(result.TxResults)
	if err := writeJSONFile(txResultsFile, result.TxResults); err != nil {
		fmt.Fprintf(r.out, "ERROR: Failed to write tx_results.json: %v\n", err)
	} else {
		fmt.Fprintf(r.out, "Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))
	}

	fmt.Fprintln(r.out)
}

func decodeEntryData(hexData string) map[string]any {
	decoded, err := binarycodec.Decode(hexData)
	if err != nil {
		return nil
	}
	return decoded
}

// decodeEntryBytes decodes an already-binary blob directly, skipping the hex
// round-trip decodeEntryData would impose.
func decodeEntryBytes(blob []byte) map[string]any {
	decoded, err := binarycodec.DecodeBytes(blob)
	if err != nil {
		return nil
	}
	return decoded
}

// materializeDecoded fills DecodedTx for any result still missing it, decoding
// from the retained raw blob. The apply path leaves DecodedTx nil on the hot
// success path (the ledger hashes never read it); the on-failure debug dump
// calls this so tx_results.json is complete even on a run that did not request
// per-tx detail up front.
func materializeDecoded(results []TxApplyInfo) {
	for i := range results {
		if results[i].DecodedTx == nil && len(results[i].RawBlob) > 0 {
			results[i].DecodedTx = decodeEntryBytes(results[i].RawBlob)
		}
	}
}

// fillTxDisplay populates txInfo's display/diagnostic fields. TxType and Account
// are read straight from the already-parsed transaction, so the hot path never
// decodes the blob a second time (the engine's ParseAndPrepare already decoded
// it). The full DecodedTx map — read only by verbose output and the on-failure
// debug dump, never by the three ledger hashes — is materialized lazily: when
// wantDetail is set, or when parsed is nil (a parse failure, where a best-effort
// decode is the only way to label the tx). The raw blob is retained so a dump
// triggered by a late failure can still materialize DecodedTx on demand (see
// materializeDecoded) without the hot path paying for the decode.
func fillTxDisplay(txInfo *TxApplyInfo, blob []byte, parsed tx.Transaction, wantDetail bool) {
	txInfo.RawBlob = blob
	if parsed != nil {
		c := parsed.GetCommon()
		txInfo.TxType = c.TransactionType
		txInfo.Account = c.Account
		if !wantDetail {
			return
		}
	}
	decoded := decodeEntryBytes(blob)
	if decoded == nil {
		return
	}
	txInfo.DecodedTx = decoded
	if parsed == nil {
		if t, ok := decoded["TransactionType"].(string); ok {
			txInfo.TxType = t
		}
		if a, ok := decoded["Account"].(string); ok {
			txInfo.Account = a
		}
	}
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

// shortHex returns the first n characters of a hex string with a trailing
// ellipsis, never panicking on inputs shorter than n.
func shortHex(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseDrops parses an unsigned decimal drops amount, rejecting trailing
// garbage (unlike fmt.Sscanf).
func parseDrops(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
}

func writeResultJSON(path string, result *ReplayResult) error {
	output := map[string]any{
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

	return os.WriteFile(path, data, 0o644)
}

// extractFeesFromState reads the fee schedule from the FeeSettings entry in a
// fixture's pre-state entries, falling back to the default schedule when it is
// absent or undecodable.
func extractFeesFromState(entries []StateEntry) drops.Fees {
	feeKey := keylet.Fees().Key
	for _, entry := range entries {
		idx, err := hexToHash32(entry.Index)
		if err != nil || idx != feeKey {
			continue
		}
		decoded, err := binarycodec.Decode(entry.Data)
		if err != nil {
			return defaultFees()
		}
		return feesFromDecoded(decoded)
	}
	return defaultFees()
}

// parseHexOrDecimal parses a string that could be hex (0x-prefixed) or decimal,
// rejecting trailing garbage (unlike fmt.Sscanf).
func parseHexOrDecimal(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return strconv.ParseUint(s[2:], 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

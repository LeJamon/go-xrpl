package replaytool

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Fixture file structures matching xrpl-state-compare export format

type stateFixture struct {
	LedgerIndex uint32              `json:"ledger_index"`
	AccountHash string              `json:"account_hash"`
	Entries     []fixtureStateEntry `json:"entries"`
}

type fixtureStateEntry struct {
	Index string `json:"index"` // 32-byte hex key
	Data  string `json:"data"`  // Binary data as hex
}

type envFixture struct {
	LedgerIndex         uint32     `json:"ledger_index"`
	ParentHash          string     `json:"parent_hash"`
	ParentCloseTime     int64      `json:"parent_close_time"`
	CloseTime           int64      `json:"close_time"`
	CloseTimeResolution uint32     `json:"close_time_resolution"`
	CloseFlags          uint8      `json:"close_flags"`
	TotalCoins          string     `json:"total_coins"`
	Fees                feesConfig `json:"fees"`
	Amendments          []string   `json:"amendments"`
}

type feesConfig struct {
	BaseFee          uint64 `json:"base_fee"`
	ReserveBase      uint64 `json:"reserve_base"`
	ReserveIncrement uint64 `json:"reserve_increment"`
}

type txsFixture struct {
	Transactions []fixtureTxEntry `json:"transactions"`
}

type fixtureTxEntry struct {
	Index  int    `json:"index"`
	Hash   string `json:"hash"`
	TxBlob string `json:"tx_blob"` // Binary transaction as hex
}

type expectedFixture struct {
	LedgerIndex     uint32            `json:"ledger_index"`
	LedgerHash      string            `json:"ledger_hash"`
	AccountHash     string            `json:"account_hash"`
	TransactionHash string            `json:"transaction_hash"`
	TotalCoins      string            `json:"total_coins"`
	Transactions    []expectedTxEntry `json:"transactions"`
}

type expectedTxEntry struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
	Meta  string `json:"meta"`
}

type txApplyInfo struct {
	Index      int            `json:"index"`
	Hash       string         `json:"hash"`
	TxType     string         `json:"type,omitempty"`
	Account    string         `json:"account,omitempty"`
	Result     string         `json:"result,omitempty"`
	ResultCode int            `json:"result_code"`
	Applied    bool           `json:"applied"`
	Fee        uint64         `json:"fee"`
	DecodedTx  map[string]any `json:"decoded,omitempty"`
	MetaBlob   []byte         `json:"-"`
	Error      string         `json:"error,omitempty"`
	RawBlob    []byte         `json:"-"`
}

type replayResult struct {
	Success         bool
	LedgerHash      [32]byte
	AccountHash     [32]byte
	TransactionHash [32]byte
	TotalCoins      uint64
	Errors          []string
	TxResults       []txApplyInfo
	PreStateCount   int
	PostStateCount  int
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
    goxrpl replay ./fixtures/ledger_32750
    goxrpl replay ./fixtures/ledger_32750 -v
    goxrpl replay ./fixtures/ledger_32750 --dump --dump-dir ./debug
    goxrpl replay ./fixtures/ledger_32750 --decoded`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r.out = cmd.OutOrStdout()
			r.fixtureDir = args[0]
			return r.run(cmd.Context())
		},
	}

	cmd.Flags().StringVarP(&r.outputResult, "output", "o", "", "Output file for results (JSON)")
	cmd.Flags().BoolVarP(&r.verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&r.dumpState, "dump", false, "Dump full state after replay")
	cmd.Flags().StringVar(&r.dumpDir, "dump-dir", "", "Directory to write state dumps (default: fixture-dir/debug)")
	cmd.Flags().BoolVar(&r.showDecoded, "decoded", false, "Show decoded transaction JSON")

	return cmd
}

func (r *replayRunner) run(ctx context.Context) error {
	startTime := time.Now()

	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                        XRPL State Transition Replay")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Fixture directory: %s\n", r.fixtureDir)
	fmt.Fprintf(r.out, "Started at:        %s\n", startTime.Format(time.RFC3339))
	fmt.Fprintln(r.out)

	// Load fixtures
	fixture, err := loadValidatedFixture(ctx, r.fixtureDir)
	if err != nil {
		return fmt.Errorf("loading fixtures: %w", err)
	}

	r.printFixtureInfo(fixture.State, fixture.Env, fixture.Txs, fixture.Expected)

	fixture.Execution.WantTxDetail = r.showDecoded
	executed, err := executeBlock(ctx, fixture.Execution)
	if err != nil {
		return fmt.Errorf("replay execution failed: %w", err)
	}
	result := &replayResult{
		LedgerHash:      executed.LedgerHash,
		AccountHash:     executed.AccountHash,
		TransactionHash: executed.TransactionHash,
		TotalCoins:      executed.TotalCoins,
		Errors:          executed.Errors,
		TxResults:       executed.TxResults,
		PreStateCount:   len(fixture.State.Entries),
		PostStateCount:  executed.PostStateCount,
	}
	for _, txInfo := range result.TxResults {
		status := "REJECTED"
		if txInfo.Applied {
			status = "APPLIED"
		}
		fmt.Fprintf(r.out, "      [%d] %-20s %-12s %s (fee=%d)\n",
			txInfo.Index, txInfo.TxType, txInfo.Result, status, txInfo.Fee)
	}
	if r.showDecoded {
		encoder := json.NewEncoder(r.out)
		encoder.SetIndent("      ", "  ")
		for i := range result.TxResults {
			fmt.Fprintf(r.out, "      Transaction %d decoded:\n", result.TxResults[i].Index)
			if err := encoder.Encode(result.TxResults[i].DecodedTx); err != nil {
				return fmt.Errorf("writing decoded transaction: %w", err)
			}
		}
	}

	result.Duration = time.Since(startTime)

	result.Success = computeReplaySuccess(result, &fixture.Want)

	// Print detailed results
	r.printDetailedResults(result, fixture.Expected)

	if r.dumpState || !result.Success {
		if err := r.dumpDebugInfo(ctx, result, fixture.Execution.StateMap, executed.StateMap); err != nil {
			if !result.Success {
				return errors.Join(cmdexit.ErrReported, err)
			}
			return err
		}
	}

	// Write output if requested
	if r.outputResult != "" {
		if err := writeResultJSON(r.outputResult, result); err != nil {
			return fmt.Errorf("writing requested output: %w", err)
		}
		fmt.Fprintf(r.out, "\nResults written to: %s\n", r.outputResult)
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
func computeReplaySuccess(result *replayResult, expected *expectedBlock) bool {
	if len(result.TxResults) != len(expected.Transactions) {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"transaction result count %d does not match expected count %d",
			len(result.TxResults), len(expected.Transactions),
		))
	} else {
		for i := range expected.Transactions {
			got := result.TxResults[i]
			want := expected.Transactions[i]
			gotHash, err := protocol.Hash256FromHex(got.Hash)
			if err != nil || got.Index != want.Index || gotHash != want.Hash || !bytes.Equal(got.MetaBlob, want.MetaBlob) {
				result.Errors = append(result.Errors, fmt.Sprintf("transaction %d hash or metadata does not match expected", i))
			}
		}
	}
	return result.LedgerHash == expected.LedgerHash &&
		result.AccountHash == expected.AccountHash &&
		result.TransactionHash == expected.TransactionHash &&
		result.TotalCoins == expected.TotalCoins &&
		len(result.Errors) == 0
}

func (r *replayRunner) printFixtureInfo(state *stateFixture, env *envFixture, txs *txsFixture, expected *expectedFixture) {
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

func (r *replayRunner) printDetailedResults(result *replayResult, expected *expectedFixture) {
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                              RESULTS")
	fmt.Fprintln(r.out, "================================================================================")

	// Hash comparisons
	expectedLedgerHash, _ := protocol.Hash256FromHex(expected.LedgerHash)
	expectedAccountHash, _ := protocol.Hash256FromHex(expected.AccountHash)
	expectedTxHash, _ := protocol.Hash256FromHex(expected.TransactionHash)
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

func (r *replayRunner) dumpDebugInfo(ctx context.Context, result *replayResult, preStateMap, postStateMap *shamap.SHAMap) error {
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
		return fmt.Errorf("creating dump directory: %w", err)
	}

	postStateFile := filepath.Join(dir, "post_state.json")
	postStateCount, err := writeStateArtifact(ctx, postStateFile, postStateMap)
	if err != nil {
		return fmt.Errorf("writing post_state.json: %w", err)
	}
	fmt.Fprintf(r.out, "Wrote %s (%d entries)\n", postStateFile, postStateCount)

	diffFile := filepath.Join(dir, "state_diff.json")
	counts, err := writeStateDiffArtifact(ctx, diffFile, preStateMap, postStateMap)
	if err != nil {
		return fmt.Errorf("writing state_diff.json: %w", err)
	}
	fmt.Fprintf(r.out, "Wrote %s\n", diffFile)
	fmt.Fprintf(r.out, "State diff: +%d added, ~%d modified, -%d removed\n", counts.Added, counts.Modified, counts.Removed)

	txResultsFile := filepath.Join(dir, "tx_results.json")
	materializeDecoded(result.TxResults)
	if err := writeJSONFile(txResultsFile, result.TxResults); err != nil {
		return fmt.Errorf("writing tx_results.json: %w", err)
	}
	fmt.Fprintf(r.out, "Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))

	fmt.Fprintln(r.out)
	return nil
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
func materializeDecoded(results []txApplyInfo) {
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
func fillTxDisplay(txInfo *txApplyInfo, blob []byte, parsed tx.Transaction, wantDetail bool) {
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

// parseDrops parses an unsigned decimal drops amount, rejecting trailing
// garbage (unlike fmt.Sscanf).
func parseDrops(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
}

func replayCloseTime(seconds int64) (time.Time, error) {
	if seconds < 0 || seconds > math.MaxUint32 {
		return time.Time{}, fmt.Errorf("XRPL close time %d is outside uint32 range", seconds)
	}
	return protocol.FromRippleTime(uint32(seconds)), nil
}

func writeResultJSON(path string, result *replayResult) error {
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
		"transactions":      result.TxResults,
	}
	return writeAtomicJSON(path, output)
}

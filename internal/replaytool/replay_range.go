package replaytool

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

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/spf13/cobra"
)

var (
	replayRangeFrom                 uint32
	replayRangeTo                   uint32
	replayRangeDumpDir              string
	replayRangeVerbose              bool
	replayRangeDecoded              bool
	replayRangeCheckpointDir        string
	replayRangeCheckpointInterval   uint32
	replayRangeResumeFrom           uint32
	replayRangeNodestoreDir         string
	replayRangeContinueOnDivergence bool
	replayRangeFindingsOut          string
	replayRangeGoXRPLCommit         string
)

// newReplayRangeCmd builds the `replay-range` command and its flags.
func newReplayRangeCmd() *cobra.Command {
	cmd := &cobra.Command{
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

On any mismatch, it stops immediately and dumps debug information, unless
--continue-on-divergence is set (see below).

The active amendment set is loaded from the Amendments ledger entry in the
seed state and evolves automatically as flag-ledger EnableAmendment
pseudo-transactions are applied, so modern (post-amendment) ranges replay
correctly. The seed state's tree root is verified against the known
account_hash before replay starts, so an incomplete or corrupt import fails
fast instead of looking like an execution bug at from+1.

By default the whole state tree is held in RAM (~6-12 GB for a mainnet
checkpoint). With --nodestore-dir the seed state is instead held lazily in a
node-local pebble nodestore: a shared read-only base built once per checkpoint
plus a per-run copy-on-write overlay for the segment's mutations. Re-seeding
the same checkpoint then opens the nodestore instead of rebuilding the tree.

With --continue-on-divergence the worker does not stop at the first hash
mismatch: it records a structured, commit-tagged finding (--findings-out),
resets to mainnet's ground-truth post-state reconstructed from the ledger's
transaction metadata, and continues — so one pass surveys every divergence in
the range. The reset is gated on the reconstructed state's account_hash, so
replay only continues from a byte-exact state.

Long runs can be checkpointed to disk (--checkpoint-dir) and resumed
(--resume-from) so a crash or stop does not force a restart from --from.

Database configuration is read from environment variables:
- POSTGRES_HOST (default: localhost)
- POSTGRES_PORT (default: 5432)
- POSTGRES_DB (default: xrpl_state)
- POSTGRES_USER (default: postgres)
- POSTGRES_PASSWORD (default: postgres)

Example:
    xrpld replay-range --from 32750 --to 32800
    xrpld replay-range --from 32750 --to 32800 -v
    xrpld replay-range --from 32750 --to 32800 --dump-dir ./debug
    xrpld replay-range --from 99226370 --to 99236370 --checkpoint-dir ./ckpt
    xrpld replay-range --from 99226370 --to 99236370 --checkpoint-dir ./ckpt --resume-from 99230000`,
		Run: runReplayRange,
	}

	cmd.Flags().Uint32Var(&replayRangeFrom, "from", 0, "Starting ledger index (pre-state)")
	cmd.Flags().Uint32Var(&replayRangeTo, "to", 0, "Ending ledger index (last block to process)")
	cmd.Flags().StringVar(&replayRangeDumpDir, "dump-dir", "", "Directory for debug output on failure")
	cmd.Flags().BoolVarP(&replayRangeVerbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&replayRangeDecoded, "decoded", false, "Show decoded JSON for entries")
	cmd.Flags().StringVar(&replayRangeCheckpointDir, "checkpoint-dir", "", "Directory for periodic state checkpoints (enables checkpoint/resume)")
	cmd.Flags().Uint32Var(&replayRangeCheckpointInterval, "checkpoint-interval", 10000, "Write a checkpoint every N ledgers (requires --checkpoint-dir)")
	cmd.Flags().Uint32Var(&replayRangeResumeFrom, "resume-from", 0, "Resume from the checkpoint at this ledger seq (requires --checkpoint-dir)")
	cmd.Flags().StringVar(&replayRangeNodestoreDir, "nodestore-dir", "", "Node-local directory for the lazy pebble nodestore (shared read-only checkpoint base + per-run overlay). When set, seed state is held lazily instead of fully in RAM.")
	cmd.Flags().BoolVar(&replayRangeContinueOnDivergence, "continue-on-divergence", false, "On a hash mismatch, record a finding and reset to mainnet ground truth, then continue (survey all divergences) instead of stopping")
	cmd.Flags().StringVar(&replayRangeFindingsOut, "findings-out", "", "Path to the findings JSONL file (default <dump-dir>/findings.jsonl or ./debug/findings.jsonl); used with --continue-on-divergence")
	cmd.Flags().StringVar(&replayRangeGoXRPLCommit, "goxrpl-commit", "", "Commit/image tag recorded in findings (default: VCS revision from build info)")

	cmd.MarkFlagRequired("from")
	cmd.MarkFlagRequired("to")

	return cmd
}

// RangeReplayStats holds statistics for the replay run
type RangeReplayStats struct {
	BlocksProcessed   int
	BlocksSuccessful  int
	TotalTransactions int
	Divergences       int
	TotalDuration     time.Duration
	FailedAtBlock     uint32
	FailureReason     string
}

func runReplayRange(cmd *cobra.Command, args []string) {
	if replayRangeFrom >= replayRangeTo {
		fmt.Fprintf(os.Stderr, "ERROR: --from must be less than --to\n")
		os.Exit(1)
	}

	// Effective starting point. With --resume-from we seed from an on-disk
	// checkpoint at that seq instead of loading the full state at --from.
	startLedger := replayRangeFrom
	if replayRangeResumeFrom > 0 {
		if replayRangeCheckpointDir == "" {
			fmt.Fprintf(os.Stderr, "ERROR: --resume-from requires --checkpoint-dir\n")
			os.Exit(1)
		}
		if replayRangeResumeFrom <= replayRangeFrom || replayRangeResumeFrom >= replayRangeTo {
			fmt.Fprintf(os.Stderr, "ERROR: --resume-from must be within (%d, %d)\n", replayRangeFrom, replayRangeTo)
			os.Exit(1)
		}
		if _, err := os.Stat(checkpointPath(replayRangeCheckpointDir, replayRangeResumeFrom)); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: no checkpoint for ledger %d in %s; --resume-from must equal a ledger seq checkpointed in a prior run (a multiple of --checkpoint-interval)\n", replayRangeResumeFrom, replayRangeCheckpointDir)
			os.Exit(1)
		}
		startLedger = replayRangeResumeFrom
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
	valid, missingLedger, err := client.ValidateRange(ctx, startLedger, replayRangeTo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to validate range: %v\n", err)
		os.Exit(1)
	}
	if !valid {
		fmt.Fprintf(os.Stderr, "ERROR: Ledger %d not found in database\n", missingLedger)
		fmt.Fprintf(os.Stderr, "       Run 'python main.py sync-range %d %d' first\n", startLedger, replayRangeTo)
		os.Exit(1)
	}
	fmt.Printf("      All %d ledgers present in database\n", replayRangeTo-startLedger+1)

	source, err := newStateSource(client, replayRangeNodestoreDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to initialize state source: %v\n", err)
		os.Exit(1)
	}
	defer source.Close()

	var findings *findingsWriter
	if replayRangeContinueOnDivergence {
		findings, err = newFindingsWriter(findingsPath())
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to open findings file: %v\n", err)
			os.Exit(1)
		}
		defer findings.Close()
	}

	var stateMap *shamap.SHAMap
	var preSnapshot *statecompare.LedgerSnapshot
	var fees drops.Fees
	if replayRangeResumeFrom > 0 {
		// Checkpoint-file resume seeds from goXRPL's own computed state, which
		// is held in RAM; nodestore-lazy seeding applies to fresh --from loads.
		fmt.Printf("[3/3] Resuming from checkpoint at ledger %d...\n", startLedger)
		stateMap, preSnapshot, fees, err = resumeFromCheckpoint(ctx, client, replayRangeCheckpointDir, startLedger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to resume from checkpoint: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("[3/3] Loading initial state at ledger %d...\n", startLedger)
		stateMap, preSnapshot, fees, err = source.Load(ctx, startLedger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to load initial state: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("      Loaded seed state at ledger %d (root verified against account_hash)\n", startLedger)
	fmt.Println()

	// Start continuous replay
	fmt.Println("--- Starting Continuous Replay ---")
	fmt.Println()

	stats := &RangeReplayStats{}
	commit := goxrplCommit(replayRangeGoXRPLCommit)
	currentStateMap := stateMap
	previousSnapshot := preSnapshot

	for targetLedger := startLedger + 1; targetLedger <= replayRangeTo; targetLedger++ {
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
			fmt.Printf("[%d] %3d txs | FAIL | %v\n", targetLedger, result.TxCount, blockDuration.Round(time.Millisecond))

			if replayRangeContinueOnDivergence {
				resumed, err := recordDivergenceAndReset(ctx, client, findings, commit, targetLedger, previousSnapshot.LedgerHash, result, currentStateMap, newStateMap)
				if err != nil {
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = err.Error()
					fmt.Printf("[%d] ERROR recording divergence: %v\n", targetLedger, err)
					break
				}
				stats.Divergences++
				if resumed == nil {
					// The ground-truth reconstruction did not match mainnet's
					// account_hash, so continuing would build on a corrupt
					// state; stop with the finding already recorded.
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = "divergence; mainnet ground-truth reconstruction did not match account_hash, cannot continue"
					fmt.Printf("[%d] divergence recorded; cannot reconstruct mainnet state, stopping\n", targetLedger)
					break
				}
				fmt.Printf("[%d] divergence recorded; reset to mainnet ground truth, continuing\n", targetLedger)
				currentStateMap = resumed
				previousSnapshot = result.PostSnapshot
				fees = ExtractFeesFromSHAMap(currentStateMap)
				maybeCheckpoint(targetLedger, currentStateMap)
				continue
			}

			stats.FailedAtBlock = targetLedger
			stats.FailureReason = "hash mismatch"
			fmt.Println()
			dumpRangeDebugInfo(targetLedger, result, currentStateMap, newStateMap)
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

		// Periodically checkpoint so a crash or stop can resume mid-range.
		maybeCheckpoint(targetLedger, currentStateMap)
	}

	stats.TotalDuration = time.Since(startTime)

	// Print summary
	fmt.Println()
	printRangeSummary(stats)

	if stats.FailedAtBlock > 0 {
		os.Exit(1)
	}
}

// maybeCheckpoint writes a checkpoint when checkpointing is enabled and the
// ledger seq lands on the configured interval.
func maybeCheckpoint(seq uint32, stateMap *shamap.SHAMap) {
	if replayRangeCheckpointDir == "" || replayRangeCheckpointInterval == 0 ||
		seq%replayRangeCheckpointInterval != 0 {
		return
	}
	if err := writeCheckpoint(replayRangeCheckpointDir, seq, stateMap); err != nil {
		fmt.Printf("      WARNING: failed to write checkpoint at %d: %v\n", seq, err)
	} else if replayRangeVerbose {
		fmt.Printf("      checkpoint written at ledger %d\n", seq)
	}
}

// findingsPath resolves where divergence findings are written: an explicit
// --findings-out, else findings.jsonl under the dump dir (or ./debug).
func findingsPath() string {
	if replayRangeFindingsOut != "" {
		return replayRangeFindingsOut
	}
	dir := replayRangeDumpDir
	if dir == "" {
		dir = "./debug"
	}
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "findings.jsonl")
}

// recordDivergenceAndReset writes a finding for a divergent block and returns
// the mainnet ground-truth post-state to continue from, or nil when that state
// could not be reconstructed byte-exactly (in which case replay must stop).
func recordDivergenceAndReset(
	ctx context.Context,
	client *statecompare.Client,
	findings *findingsWriter,
	commit string,
	ledgerIndex uint32,
	parentHash [32]byte,
	result *BlockResult,
	preState, goxrplPost *shamap.SHAMap,
) (*shamap.SHAMap, error) {
	corrected, verified, err := reconstructMainnetState(ctx, client, preState, ledgerIndex, result.ExpectedAccountHash)
	if err != nil {
		return nil, fmt.Errorf("reconstructing mainnet state: %w", err)
	}
	diverging, err := divergingObjects(goxrplPost, corrected)
	if err != nil {
		return nil, fmt.Errorf("computing diverging objects: %w", err)
	}
	finding := buildFinding(commit, ledgerIndex, parentHash, result, verified, diverging)
	if err := findings.Write(finding); err != nil {
		return nil, fmt.Errorf("writing finding: %w", err)
	}
	if !verified {
		return nil, nil
	}
	return corrected, nil
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

	// Verify the imported tree root against the known account_hash. The SHAMap
	// root is a Merkle commitment over the whole state, so a match proves the
	// import is complete and correct; a mismatch means a partial or corrupt
	// seed and is failed fast so it is not misread as an execution bug at
	// from+1.
	if err := verifyStateRoot(stateMap, snapshot.AccountHash, ledgerIndex); err != nil {
		return nil, nil, drops.Fees{}, err
	}

	// Seed fees from the verified state. ExtractFeesFromSHAMap honors both the
	// modern XRPFees format and the legacy FeeSettings fields, so post-amendment
	// ranges seed the correct fees instead of silently falling back to defaults.
	fees := ExtractFeesFromSHAMap(stateMap)

	return stateMap, snapshot, fees, nil
}

// verifyStateRoot fails if the state map's tree root does not match the
// expected account_hash for the given ledger.
func verifyStateRoot(stateMap *shamap.SHAMap, expected [32]byte, ledgerIndex uint32) error {
	root, err := stateMap.Hash()
	if err != nil {
		return fmt.Errorf("computing state root hash: %w", err)
	}
	if root != expected {
		return fmt.Errorf("seed state account_hash mismatch at ledger %d: imported root %s != expected %s (incomplete or corrupt state import)",
			ledgerIndex, hex.EncodeToString(root[:]), hex.EncodeToString(expected[:]))
	}
	return nil
}

// resumeFromCheckpoint loads the seed state from an on-disk checkpoint at seq,
// validates its root against the known account_hash, and returns the snapshot
// and fees needed to continue replay from seq+1.
func resumeFromCheckpoint(ctx context.Context, client *statecompare.Client, dir string, seq uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	path := checkpointPath(dir, seq)
	stateMap, ckptSeq, err := loadCheckpoint(path)
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}
	if ckptSeq != seq {
		return nil, nil, drops.Fees{}, fmt.Errorf("checkpoint %s holds ledger %d, expected %d", path, ckptSeq, seq)
	}

	snapshot, err := client.GetSnapshot(ctx, seq)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	if err := verifyStateRoot(stateMap, snapshot.AccountHash, seq); err != nil {
		return nil, nil, drops.Fees{}, err
	}

	fees := ExtractFeesFromSHAMap(stateMap)
	return stateMap, snapshot, fees, nil
}

// loadRulesFromState builds the amendment Rules from the Amendments singleton
// entry in the given state map. An absent entry means no amendments are
// enabled (pre-amendment / genesis ledgers), which yields EmptyRules().
func loadRulesFromState(stateMap *shamap.SHAMap) (*amendment.Rules, error) {
	item, found, err := stateMap.Get(keylet.Amendments().Key)
	if err != nil {
		return nil, fmt.Errorf("reading amendments entry: %w", err)
	}
	if !found || item == nil {
		return amendment.EmptyRules(), nil
	}
	rules, err := ledger.LoadAmendmentsFromLedgerEntry(item.Data())
	if err != nil {
		return nil, fmt.Errorf("parsing amendments entry: %w", err)
	}
	return rules, nil
}

// defaultFees is the fallback fee schedule used when a ledger has no readable
// FeeSettings entry.
func defaultFees() drops.Fees {
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
		return defaultFees()
	}

	// Get the data from the item
	data := item.Data()

	// Decode the entry
	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return defaultFees()
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
	d := defaultFees()
	if fees.Base == 0 {
		fees.Base = d.Base
	}
	if fees.Reserve == 0 {
		fees.Reserve = d.Reserve
	}
	if fees.Increment == 0 {
		fees.Increment = d.Increment
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
		TxResults: make([]TxApplyInfo, 0),
		Errors:    make([]string, 0),
	}

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
	closeTime := time.Unix(protocol.RippleEpochUnix+postSnapshot.CloseTime, 0).UTC()
	parentCloseTime := time.Unix(protocol.RippleEpochUnix+preSnapshot.CloseTime, 0).UTC()

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

	// Derive the active amendment set from the parent (pre) state's Amendments
	// entry, mirroring rippled, where a ledger's rules come from its parent.
	// Flag-ledger EnableAmendment pseudo-transactions applied in this block
	// update the Amendments entry in the post state, so the rule set evolves
	// automatically as the range advances.
	rules, err := loadRulesFromState(preStateMap)
	if err != nil {
		return nil, nil, fmt.Errorf("loading amendments: %w", err)
	}

	// Setup engine
	engineConfig := tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            targetLedger,
		SkipSignatureVerification: true,
		Standalone:                true,
		Rules:                     rules,
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

func dumpRangeDebugInfo(ledgerIndex uint32, result *BlockResult, preStateMap, postStateMap *shamap.SHAMap) {
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

	// Materializing a nodestore-lazy map would walk millions of nodes; skip
	// the full state/diff dump and rely on --continue-on-divergence for
	// targeted, object-level findings instead.
	if preStateMap.IsBacked() || postStateMap.IsBacked() {
		fmt.Printf("  Skipping full state/diff dump for nodestore-lazy state; use --continue-on-divergence for object-level findings\n")
		dumpTxResults(dir, result)
		return
	}

	preState := materializeState(preStateMap)
	postState := materializeState(postStateMap)

	// Dump post-state
	postStateFile := filepath.Join(dir, "post_state.json")
	postStateData := make([]map[string]any, 0)

	keys := make([]string, 0, len(postState))
	for k := range postState {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		data := postState[key]
		dataHex := hex.EncodeToString(data)

		entry := map[string]any{
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
	diff := map[string]any{
		"added":    make([]map[string]any, 0),
		"modified": make([]map[string]any, 0),
		"removed":  make([]string, 0),
	}

	// Build pre-state map for comparison
	preStateKeys := make(map[string]string)
	for key, data := range preState {
		preStateKeys[strings.ToLower(key)] = hex.EncodeToString(data)
	}

	for _, key := range keys {
		keyLower := strings.ToLower(key)
		postDataHex := hex.EncodeToString(postState[key])

		preDataHex, exists := preStateKeys[keyLower]
		if !exists {
			entry := map[string]any{
				"index":    key,
				"data_hex": postDataHex,
			}
			if decoded := decodeEntryData(postDataHex); decoded != nil {
				entry["decoded"] = decoded
			}
			diff["added"] = append(diff["added"].([]map[string]any), entry)
		} else if !strings.EqualFold(preDataHex, postDataHex) {
			entry := map[string]any{
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
			diff["modified"] = append(diff["modified"].([]map[string]any), entry)
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

	dumpTxResults(dir, result)
}

// materializeState walks a state SHAMap into a key-hex -> data map. Only safe
// for fully in-memory maps; a nodestore-lazy map would fetch the whole tree.
func materializeState(stateMap *shamap.SHAMap) map[string][]byte {
	out := make(map[string][]byte)
	_ = stateMap.ForEach(func(item *shamap.Item) bool {
		key := item.Key()
		out[hex.EncodeToString(key[:])] = item.Data()
		return true
	})
	return out
}

// dumpTxResults writes the per-transaction apply results for a block.
func dumpTxResults(dir string, result *BlockResult) {
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
	} else if stats.Divergences > 0 {
		fmt.Printf("COMPLETED with %d divergence(s) recorded\n", stats.Divergences)
	} else {
		fmt.Println("SUCCESS: All blocks replayed successfully")
	}
	fmt.Println("================================================================================")
	fmt.Printf("Blocks processed:    %d\n", stats.BlocksProcessed)
	fmt.Printf("Blocks successful:   %d\n", stats.BlocksSuccessful)
	fmt.Printf("Divergences found:   %d\n", stats.Divergences)
	fmt.Printf("Total transactions:  %d\n", stats.TotalTransactions)
	fmt.Printf("Total time:          %v\n", stats.TotalDuration.Round(time.Millisecond))
	if stats.TotalDuration.Seconds() > 0 {
		fmt.Printf("Average speed:       %.1f blocks/sec\n", float64(stats.BlocksProcessed)/stats.TotalDuration.Seconds())
	}
	fmt.Println("================================================================================")
}

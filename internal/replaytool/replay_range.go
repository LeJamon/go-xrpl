package replaytool

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/observability"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	"github.com/spf13/cobra"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// replayRangeRunner holds one `replay-range` invocation's flags and output sink.
// Flags bind to its fields (not package globals), so each NewCommands() call is
// fully isolated.
type replayRangeRunner struct {
	out io.Writer

	from                 uint32
	to                   uint32
	dumpDir              string
	verbose              bool
	decoded              bool
	checkpointDir        string
	checkpointInterval   uint32
	resumeFrom           uint32
	nodestoreDir         string
	baseCacheMB          int
	overlayCacheMB       int
	gogc                 int
	continueOnDivergence bool
	findingsOut          string
	goxrplCommit         string
}

// newReplayRangeCmd builds the `replay-range` command and its flags.
func newReplayRangeCmd() *cobra.Command {
	r := &replayRangeRunner{}
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
		RunE: func(cmd *cobra.Command, args []string) error {
			r.out = cmd.OutOrStdout()
			return r.run()
		},
	}

	cmd.Flags().Uint32Var(&r.from, "from", 0, "Starting ledger index (pre-state)")
	cmd.Flags().Uint32Var(&r.to, "to", 0, "Ending ledger index (last block to process)")
	cmd.Flags().StringVar(&r.dumpDir, "dump-dir", "", "Directory for debug output on failure")
	cmd.Flags().BoolVarP(&r.verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&r.decoded, "decoded", false, "Show decoded JSON for entries")
	cmd.Flags().StringVar(&r.checkpointDir, "checkpoint-dir", "", "Directory for periodic state checkpoints (enables checkpoint/resume)")
	cmd.Flags().Uint32Var(&r.checkpointInterval, "checkpoint-interval", 10000, "Write a checkpoint every N ledgers (requires --checkpoint-dir)")
	cmd.Flags().Uint32Var(&r.resumeFrom, "resume-from", 0, "Resume from the checkpoint at this ledger seq (requires --checkpoint-dir)")
	cmd.Flags().StringVar(&r.nodestoreDir, "nodestore-dir", "", "Node-local directory for the lazy pebble nodestore (shared read-only checkpoint base + per-run overlay). When set, seed state is held lazily instead of fully in RAM.")
	cmd.Flags().IntVar(&r.baseCacheMB, "base-cache-mb", 1024, "Pebble block-cache size (MiB) for the shared read-only nodestore base (only used with --nodestore-dir)")
	cmd.Flags().IntVar(&r.overlayCacheMB, "overlay-cache-mb", 256, "Pebble block-cache size (MiB) for the per-run nodestore overlay (only used with --nodestore-dir)")
	cmd.Flags().IntVar(&r.gogc, "gogc", 0, "If >0, set GOGC for this run, raising the GC trigger to cut collection frequency on the default in-memory state path (which keeps the whole tree live). 0 leaves Go's default.")
	cmd.Flags().BoolVar(&r.continueOnDivergence, "continue-on-divergence", false, "On a hash mismatch, record a finding and reset to mainnet ground truth, then continue (survey all divergences) instead of stopping")
	cmd.Flags().StringVar(&r.findingsOut, "findings-out", "", "Path to the findings JSONL file (default <dump-dir>/findings.jsonl or ./debug/findings.jsonl); used with --continue-on-divergence")
	cmd.Flags().StringVar(&r.goxrplCommit, "goxrpl-commit", "", "Commit/image tag recorded in findings (default: VCS revision from build info)")

	// MarkFlagRequired only errors if the flag does not exist — a construction
	// bug, so fail fast rather than ignoring the error.
	for _, name := range []string{"from", "to"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("replay-range: marking flag %q required: %v", name, err))
		}
	}

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

	// Per-phase wall-clock accumulated across every processed block, so each
	// run self-reports its bottleneck without a profiler (issue #1084 Step 0):
	//   - FetchDuration:    the two Postgres round-trips (snapshot + txs)
	//   - ApplyDuration:    rule/fee setup + per-tx parse and engine apply
	//   - FinalizeDuration: Close (rehash) + the three root hashes + snapshot
	FetchDuration    time.Duration
	ApplyDuration    time.Duration
	FinalizeDuration time.Duration
}

func (r *replayRangeRunner) run() error {
	if r.from >= r.to {
		return fmt.Errorf("--from must be less than --to")
	}

	// Effective starting point. With --resume-from we seed from an on-disk
	// checkpoint at that seq instead of loading the full state at --from.
	startLedger := r.from
	if r.resumeFrom > 0 {
		if r.checkpointDir == "" {
			return fmt.Errorf("--resume-from requires --checkpoint-dir")
		}
		if r.resumeFrom <= r.from || r.resumeFrom >= r.to {
			return fmt.Errorf("--resume-from must be within (%d, %d)", r.from, r.to)
		}
		if _, err := os.Stat(checkpointPath(r.checkpointDir, r.resumeFrom)); err != nil {
			return fmt.Errorf("no checkpoint for ledger %d in %s; --resume-from must equal a ledger seq checkpointed in a prior run (a multiple of --checkpoint-interval)", r.resumeFrom, r.checkpointDir)
		}
		startLedger = r.resumeFrom
	}

	ctx := context.Background()
	startTime := time.Now()

	// Opt-in profiling: GOXRPL_PPROF=:6060 exposes pprof for this run so the
	// CPU-vs-IO split of a replay can be measured. Off by default.
	if addr := os.Getenv("GOXRPL_PPROF"); addr != "" {
		go func() {
			if err := observability.StartPProf(addr); err != nil {
				fmt.Fprintf(r.out, "      WARNING: pprof server failed on %s: %v\n", addr, err)
			}
		}()
		fmt.Fprintf(r.out, "pprof enabled on %s\n", addr)
	}

	// The default in-memory state path keeps the whole tree live for the run, so
	// a higher GC trigger trades RAM for fewer full marks of a mostly-static set.
	if r.gogc > 0 {
		debug.SetGCPercent(r.gogc)
	}

	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                    XRPL Continuous State Replay")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Range:      %d -> %d (%d blocks)\n", r.from, r.to, r.to-r.from)
	fmt.Fprintf(r.out, "Started at: %s\n", startTime.Format(time.RFC3339))
	fmt.Fprintln(r.out)

	// Connect to database
	fmt.Fprintln(r.out, "[1/3] Connecting to database...")
	client, err := statecompare.NewClientFromEnv()
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()
	fmt.Fprintln(r.out, "      Connected to PostgreSQL")

	// Validate range exists
	fmt.Fprintln(r.out, "[2/3] Validating ledger range...")
	valid, missingLedger, err := client.ValidateRange(ctx, startLedger, r.to)
	if err != nil {
		return fmt.Errorf("validating range: %w", err)
	}
	if !valid {
		return fmt.Errorf("ledger %d not found in database; run 'python main.py sync-range %d %d' first", missingLedger, startLedger, r.to)
	}
	fmt.Fprintf(r.out, "      All %d ledgers present in database\n", r.to-startLedger+1)

	source, err := newStateSource(client, r.nodestoreDir, r.baseCacheMB, r.overlayCacheMB)
	if err != nil {
		return fmt.Errorf("initializing state source: %w", err)
	}
	defer source.Close()

	var findings *findingsWriter
	if r.continueOnDivergence {
		findings, err = newFindingsWriter(r.findingsPath())
		if err != nil {
			return fmt.Errorf("opening findings file: %w", err)
		}
		defer findings.Close()
	}

	var stateMap *shamap.SHAMap
	var preSnapshot *statecompare.LedgerSnapshot
	var fees drops.Fees
	if r.resumeFrom > 0 {
		// Checkpoint-file resume seeds from goXRPL's own computed state, which
		// is held in RAM; nodestore-lazy seeding applies to fresh --from loads.
		fmt.Fprintf(r.out, "[3/3] Resuming from checkpoint at ledger %d...\n", startLedger)
		stateMap, preSnapshot, fees, err = resumeFromCheckpoint(ctx, client, r.checkpointDir, startLedger)
		if err != nil {
			return fmt.Errorf("resuming from checkpoint: %w", err)
		}
	} else {
		fmt.Fprintf(r.out, "[3/3] Loading initial state at ledger %d...\n", startLedger)
		stateMap, preSnapshot, fees, err = source.Load(ctx, startLedger)
		if err != nil {
			return fmt.Errorf("loading initial state: %w", err)
		}
	}

	fmt.Fprintf(r.out, "      Loaded seed state at ledger %d (root verified against account_hash)\n", startLedger)
	fmt.Fprintln(r.out)

	// Start continuous replay
	fmt.Fprintln(r.out, "--- Starting Continuous Replay ---")
	fmt.Fprintln(r.out)

	stats := &RangeReplayStats{}
	commit := goxrplCommit(r.goxrplCommit)
	currentStateMap := stateMap
	previousSnapshot := preSnapshot

	for targetLedger := startLedger + 1; targetLedger <= r.to; targetLedger++ {
		blockStart := time.Now()

		// Process this block
		result, newStateMap, err := r.processBlock(ctx, client, currentStateMap, previousSnapshot, targetLedger, fees)
		if err != nil {
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = err.Error()
			fmt.Fprintf(r.out, "[%d] ERROR: %v\n", targetLedger, err)
			break
		}

		blockDuration := time.Since(blockStart)
		stats.BlocksProcessed++
		stats.TotalTransactions += result.TxCount
		stats.FetchDuration += result.FetchDur
		stats.ApplyDuration += result.ApplyDur
		stats.FinalizeDuration += result.FinalizeDur

		// Check hashes
		if !result.Success {
			fmt.Fprintf(r.out, "[%d] %3d txs | FAIL | %v\n", targetLedger, result.TxCount, blockDuration.Round(time.Millisecond))

			if r.continueOnDivergence {
				resumed, err := recordDivergenceAndReset(ctx, client, findings, commit, targetLedger, previousSnapshot.LedgerHash, result, currentStateMap, newStateMap)
				if err != nil {
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = err.Error()
					fmt.Fprintf(r.out, "[%d] ERROR recording divergence: %v\n", targetLedger, err)
					break
				}
				stats.Divergences++
				if resumed == nil {
					// The ground-truth reconstruction did not match mainnet's
					// account_hash, so continuing would build on a corrupt
					// state; stop with the finding already recorded.
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = "divergence; mainnet ground-truth reconstruction did not match account_hash, cannot continue"
					fmt.Fprintf(r.out, "[%d] divergence recorded; cannot reconstruct mainnet state, stopping\n", targetLedger)
					break
				}
				fmt.Fprintf(r.out, "[%d] divergence recorded; reset to mainnet ground truth, continuing\n", targetLedger)
				currentStateMap = resumed
				previousSnapshot = result.PostSnapshot
				fees = extractFeesFromSHAMap(currentStateMap)
				r.maybeCheckpoint(targetLedger, currentStateMap)
				continue
			}

			stats.FailedAtBlock = targetLedger
			stats.FailureReason = "hash mismatch"
			fmt.Fprintln(r.out)
			r.dumpRangeDebugInfo(targetLedger, result, currentStateMap, newStateMap)
			r.printRangeFailure(targetLedger, result)
			break
		}

		stats.BlocksSuccessful++

		// Print progress
		if r.verbose {
			fmt.Fprintf(r.out, "[%d] %3d txs | OK   | %v\n", targetLedger, result.TxCount, blockDuration.Round(time.Millisecond))
		} else {
			// Compact output: show every 10 blocks or last block
			if stats.BlocksProcessed%10 == 0 || targetLedger == r.to {
				elapsed := time.Since(startTime)
				blocksPerSec := float64(stats.BlocksProcessed) / elapsed.Seconds()
				fmt.Fprintf(r.out, "[%d] %d blocks processed | %.1f blk/s\n", targetLedger, stats.BlocksProcessed, blocksPerSec)
			}
		}

		// Update state for next iteration
		currentStateMap = newStateMap
		previousSnapshot = result.PostSnapshot

		// Update fees from the new state (in case a SetFee transaction was processed)
		fees = extractFeesFromSHAMap(currentStateMap)

		// Periodically checkpoint so a crash or stop can resume mid-range.
		r.maybeCheckpoint(targetLedger, currentStateMap)
	}

	stats.TotalDuration = time.Since(startTime)

	// Print summary
	fmt.Fprintln(r.out)
	r.printRangeSummary(stats)

	if stats.FailedAtBlock > 0 {
		// The failure is already reported above; only the exit code is left.
		return cmdexit.ErrReported
	}
	return nil
}

// maybeCheckpoint writes a checkpoint when checkpointing is enabled and the
// ledger seq lands on the configured interval.
func (r *replayRangeRunner) maybeCheckpoint(seq uint32, stateMap *shamap.SHAMap) {
	if r.checkpointDir == "" || r.checkpointInterval == 0 ||
		seq%r.checkpointInterval != 0 {
		return
	}
	if err := writeCheckpoint(r.checkpointDir, seq, stateMap); err != nil {
		fmt.Fprintf(r.out, "      WARNING: failed to write checkpoint at %d: %v\n", seq, err)
	} else if r.verbose {
		fmt.Fprintf(r.out, "      checkpoint written at ledger %d\n", seq)
	}
}

// findingsPath resolves where divergence findings are written: an explicit
// --findings-out, else findings.jsonl under the dump dir (or ./debug).
func (r *replayRangeRunner) findingsPath() string {
	if r.findingsOut != "" {
		return r.findingsOut
	}
	dir := r.dumpDir
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

	// Per-phase wall-clock for this block (fetch / apply / finalize), summed
	// into RangeReplayStats so printRangeSummary can report the bottleneck.
	FetchDur    time.Duration
	ApplyDur    time.Duration
	FinalizeDur time.Duration
}

func loadInitialState(ctx context.Context, client *statecompare.Client, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	// Get snapshot
	snapshot, err := client.GetSnapshot(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	// Stream the state pack into the map so the whole pack and the full entry
	// slice are never materialized in RAM at once.
	stateMap := shamap.New(shamap.TypeState)
	if err := client.StreamStateEntries(ctx, ledgerIndex, func(entry statecompare.StateEntry) error {
		if err := stateMap.Put(entry.Index, entry.Data); err != nil {
			return fmt.Errorf("injecting entry: %w", err)
		}
		return nil
	}); err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting state entries: %w", err)
	}

	// Verify the imported tree root against the known account_hash. The SHAMap
	// root is a Merkle commitment over the whole state, so a match proves the
	// import is complete and correct; a mismatch means a partial or corrupt
	// seed and is failed fast so it is not misread as an execution bug at
	// from+1.
	if err := verifyStateRoot(stateMap, snapshot.AccountHash, ledgerIndex); err != nil {
		return nil, nil, drops.Fees{}, err
	}

	// Seed fees from the verified state. extractFeesFromSHAMap honors both the
	// modern XRPFees format and the legacy FeeSettings fields, so post-amendment
	// ranges seed the correct fees instead of silently falling back to defaults.
	fees := extractFeesFromSHAMap(stateMap)

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

	fees := extractFeesFromSHAMap(stateMap)
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

// feesFromDecoded reads a decoded FeeSettings entry into a drops.Fees, honoring
// both the modern XRPFees fields (BaseFeeDrops/ReserveBaseDrops/...) and the
// legacy fields, filling any unset value from the default schedule. Shared by
// the fixture-entry and SHAMap fee extractors.
func feesFromDecoded(decoded map[string]any) drops.Fees {
	fees := drops.Fees{}

	// Modern format (XRPFees amendment)
	if v, ok := decoded["BaseFeeDrops"].(string); ok {
		if n, err := parseHexOrDecimal(v); err == nil {
			fees.Base = drops.XRPAmount(n)
		}
	}
	if v, ok := decoded["ReserveBaseDrops"].(string); ok {
		if n, err := parseHexOrDecimal(v); err == nil {
			fees.Reserve = drops.XRPAmount(n)
		}
	}
	if v, ok := decoded["ReserveIncrementDrops"].(string); ok {
		if n, err := parseHexOrDecimal(v); err == nil {
			fees.Increment = drops.XRPAmount(n)
		}
	}

	// Legacy format (pre-XRPFees)
	if v, ok := decoded["BaseFee"].(string); ok && fees.Base == 0 {
		if n, err := parseHexOrDecimal(v); err == nil {
			fees.Base = drops.XRPAmount(n)
		}
	}
	if v, ok := decoded["ReserveBase"].(uint32); ok && fees.Reserve == 0 {
		fees.Reserve = drops.XRPAmount(v)
	}
	if v, ok := decoded["ReserveIncrement"].(uint32); ok && fees.Increment == 0 {
		fees.Increment = drops.XRPAmount(v)
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

// extractFeesFromSHAMap extracts the fee schedule from the FeeSettings entry of
// a state SHAMap, falling back to the default schedule when it is absent or
// undecodable.
func extractFeesFromSHAMap(stateMap *shamap.SHAMap) drops.Fees {
	item, found, err := stateMap.Get(keylet.Fees().Key)
	if err != nil || !found || item == nil {
		return defaultFees()
	}

	decoded, err := binarycodec.Decode(hex.EncodeToString(item.Data()))
	if err != nil {
		return defaultFees()
	}
	return feesFromDecoded(decoded)
}

func (r *replayRangeRunner) processBlock(
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

	// Phase 1 (fetch): the two synchronous Postgres round-trips, plus the
	// ~1-in-1000 cold-pack blob download that GetTransactions triggers.
	fetchStart := time.Now()

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
	result.FetchDur = time.Since(fetchStart)

	// Phase 2 (apply): amendment-rule/fee setup, then per-tx parse and engine
	// apply. State-node reads from the lazy nodestore happen here, during the
	// SHAMap descents inside each Apply.
	applyStart := time.Now()

	// Create transaction map
	txMap := shamap.New(shamap.TypeTransaction)

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

	// Flag-ledger NegativeUNL transition: on a flag ledger (seq % 256 == 0) with
	// featureNegativeUNL enabled, apply the pending ValidatorToDisable /
	// ValidatorToReEnable transitions to the NegativeUNL entry BEFORE creating the
	// tx-apply engine and applying txs, mirroring rippled BuildLedger.cpp:48-53
	// (updateNegativeUNL runs on the freshly-built ledger before the OpenView
	// tx-apply accum) and the catchup path (inbound/replay_delta.go:616-620). A
	// UNLModify pseudo-tx sets the pending transition at one flag ledger; the next
	// flag ledger moves it into DisabledValidators. Without this the 256th ledger
	// after a UNLModify forks account_hash even though every transaction matches.
	if targetLedger%256 == 0 && rules != nil && rules.Enabled(amendment.FeatureNegativeUNL) {
		if err := openLedger.UpdateNegativeUNL(); err != nil {
			return nil, nil, fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
		}
	}

	// Setup engine
	engineConfig := tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            targetLedger,
		ParentHash:                preSnapshot.LedgerHash,
		ParentCloseTime:           uint32(preSnapshot.CloseTime),
		SkipSignatureVerification: true,
		Standalone:                true,
		Rules:                     rules,
	}

	engine := txengine.NewEngine(openLedger, engineConfig)
	blockProcessor := txengine.NewBlockProcessor(engine)

	// Apply transactions. The hot success path skips the full per-tx decode: the
	// three ledger hashes never read it, verbose output gates it, and the
	// on-failure dump materializes it on demand from the retained blob.
	wantTxDetail := r.verbose || r.dumpDir != ""
	for _, txEntry := range txs {
		txInfo := TxApplyInfo{
			Index: txEntry.TxIndex,
			Hash:  hex.EncodeToString(txEntry.TxHash[:]),
		}

		// Parse transaction
		parsedTx, err := txengine.ParseAndPrepare(txEntry.TxBlob)
		if err != nil {
			txInfo.Error = fmt.Sprintf("failed to parse: %v", err)
			fillTxDisplay(&txInfo, txEntry.TxBlob, nil, wantTxDetail)
			result.TxResults = append(result.TxResults, txInfo)
			result.Errors = append(result.Errors, fmt.Sprintf("tx %d: %s", txEntry.TxIndex, txInfo.Error))
			continue
		}
		fillTxDisplay(&txInfo, txEntry.TxBlob, parsedTx.Transaction, wantTxDetail)

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

		if r.verbose && r.decoded {
			fmt.Fprintf(r.out, "        [%d] %-20s %-12s\n", txEntry.TxIndex, txInfo.TxType, txInfo.Result)
		}
	}
	result.ApplyDur = time.Since(applyStart)

	// Phase 3 (finalize): Close (incremental rehash of the touched nodes), the
	// three root hashes, and the COW state snapshot handed to the next block.
	finalizeStart := time.Now()

	// Close ledger. Close() updates the LedgerHashes skip lists from the
	// header's ParentHash as its first step, so no separate skip-list pass is
	// needed here — doing one would double-append the parent hash.
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
	result.FinalizeDur = time.Since(finalizeStart)

	return result, newStateMap, nil
}

func (r *replayRangeRunner) dumpRangeDebugInfo(ledgerIndex uint32, result *BlockResult, preStateMap, postStateMap *shamap.SHAMap) {
	dir := r.dumpDir
	if dir == "" {
		dir = fmt.Sprintf("./debug/ledger_%d", ledgerIndex)
	} else {
		dir = filepath.Join(dir, fmt.Sprintf("ledger_%d", ledgerIndex))
	}

	fmt.Fprintf(r.out, "Writing debug files to: %s\n", dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(r.out, "ERROR: Failed to create dump directory: %v\n", err)
		return
	}

	// Materializing a nodestore-lazy map would walk millions of nodes; skip
	// the full state/diff dump and rely on --continue-on-divergence for
	// targeted, object-level findings instead.
	if preStateMap.IsBacked() || postStateMap.IsBacked() {
		fmt.Fprintf(r.out, "  Skipping full state/diff dump for nodestore-lazy state; use --continue-on-divergence for object-level findings\n")
		r.writeTxResults(dir, result)
		return
	}

	pre := hexStateMap(preStateMap)
	post := hexStateMap(postStateMap)

	postStateFile := filepath.Join(dir, "post_state.json")
	postStateData := postStateEntries(post)
	if err := writeJSONFile(postStateFile, postStateData); err != nil {
		fmt.Fprintf(r.out, "  ERROR: Failed to write post_state.json: %v\n", err)
	} else {
		fmt.Fprintf(r.out, "  Wrote %s (%d entries)\n", postStateFile, len(postStateData))
	}

	diffFile := filepath.Join(dir, "state_diff.json")
	diff := computeStateDiff(pre, post)
	if err := writeJSONFile(diffFile, diff); err != nil {
		fmt.Fprintf(r.out, "  ERROR: Failed to write state_diff.json: %v\n", err)
	} else {
		fmt.Fprintf(r.out, "  Wrote %s\n", diffFile)
	}

	r.writeTxResults(dir, result)
}

// hexStateMap walks a fully in-memory state SHAMap into a lowercase-hex index →
// hex-data map. Only safe for non-backed maps; a nodestore-lazy map would fetch
// the whole tree.
func hexStateMap(stateMap *shamap.SHAMap) map[string]string {
	out := make(map[string]string)
	_ = stateMap.ForEach(func(item *shamap.Item) bool {
		key := item.Key()
		out[hex.EncodeToString(key[:])] = hex.EncodeToString(item.Data())
		return true
	})
	return out
}

// writeTxResults writes the per-transaction apply results for a block.
func (r *replayRangeRunner) writeTxResults(dir string, result *BlockResult) {
	txResultsFile := filepath.Join(dir, "tx_results.json")
	materializeDecoded(result.TxResults)
	if err := writeJSONFile(txResultsFile, result.TxResults); err != nil {
		fmt.Fprintf(r.out, "  ERROR: Failed to write tx_results.json: %v\n", err)
		return
	}
	fmt.Fprintf(r.out, "  Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))
}

func (r *replayRangeRunner) printRangeFailure(ledgerIndex uint32, result *BlockResult) {
	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "                      FAILED at ledger %d\n", ledgerIndex)
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out)

	ledgerHashMatch := result.LedgerHash == result.ExpectedLedgerHash
	accountHashMatch := result.AccountHash == result.ExpectedAccountHash
	txHashMatch := result.TransactionHash == result.ExpectedTransactionHash

	fmt.Fprintln(r.out, "Hash Comparison:")
	fmt.Fprintln(r.out, "-----------------")

	r.printRangeHashRow("Ledger Hash", result.LedgerHash, result.ExpectedLedgerHash, ledgerHashMatch)
	r.printRangeHashRow("Account Hash", result.AccountHash, result.ExpectedAccountHash, accountHashMatch)
	r.printRangeHashRow("Transaction Hash", result.TransactionHash, result.ExpectedTransactionHash, txHashMatch)

	fmt.Fprintln(r.out)
	fmt.Fprintf(r.out, "Total Coins: got %d, expected %d\n", result.TotalCoins, result.ExpectedTotalCoins)

	if len(result.Errors) > 0 {
		fmt.Fprintln(r.out)
		fmt.Fprintln(r.out, "Errors:")
		for _, err := range result.Errors {
			fmt.Fprintf(r.out, "  - %s\n", err)
		}
	}

	fmt.Fprintln(r.out)
	fmt.Fprintln(r.out, "Use 'xrpld compare' to analyze state differences.")
	fmt.Fprintln(r.out, "================================================================================")
}

func (r *replayRangeRunner) printRangeHashRow(name string, got, expected [32]byte, match bool) {
	gotHex := hex.EncodeToString(got[:])
	expectedHex := hex.EncodeToString(expected[:])

	status := "[OK]"
	if !match {
		status = "[MISMATCH]"
	}

	fmt.Fprintf(r.out, "%s: %s\n", name, status)
	fmt.Fprintf(r.out, "  Got:      %s\n", gotHex)
	if !match {
		fmt.Fprintf(r.out, "  Expected: %s\n", expectedHex)
	}
}

func (r *replayRangeRunner) printRangeSummary(stats *RangeReplayStats) {
	fmt.Fprintln(r.out, "================================================================================")
	if stats.FailedAtBlock > 0 {
		fmt.Fprintf(r.out, "FAILED at block %d: %s\n", stats.FailedAtBlock, stats.FailureReason)
	} else if stats.Divergences > 0 {
		fmt.Fprintf(r.out, "COMPLETED with %d divergence(s) recorded\n", stats.Divergences)
	} else {
		fmt.Fprintln(r.out, "SUCCESS: All blocks replayed successfully")
	}
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Blocks processed:    %d\n", stats.BlocksProcessed)
	fmt.Fprintf(r.out, "Blocks successful:   %d\n", stats.BlocksSuccessful)
	fmt.Fprintf(r.out, "Divergences found:   %d\n", stats.Divergences)
	fmt.Fprintf(r.out, "Total transactions:  %d\n", stats.TotalTransactions)
	fmt.Fprintf(r.out, "Total time:          %v\n", stats.TotalDuration.Round(time.Millisecond))
	if stats.TotalDuration.Seconds() > 0 {
		fmt.Fprintf(r.out, "Average speed:       %.1f blocks/sec\n", float64(stats.BlocksProcessed)/stats.TotalDuration.Seconds())
	}

	// Per-phase breakdown: report each phase's share of measured in-block time
	// so every run self-reports where it spends its time (issue #1084 Step 0).
	// The percentages are of fetch+apply+finalize, not TotalDuration, which
	// also covers seed load, checkpointing, and per-block bookkeeping.
	phaseTotal := stats.FetchDuration + stats.ApplyDuration + stats.FinalizeDuration
	if phaseTotal > 0 {
		pct := func(d time.Duration) float64 { return 100 * float64(d) / float64(phaseTotal) }
		fmt.Fprintln(r.out, "--------------------------------------------------------------------------------")
		fmt.Fprintf(r.out, "Phase breakdown (in-block, %% of fetch+apply+finalize):\n")
		fmt.Fprintf(r.out, "  fetch    (db snapshot+txs):   %10v  %5.1f%%\n", stats.FetchDuration.Round(time.Millisecond), pct(stats.FetchDuration))
		fmt.Fprintf(r.out, "  apply    (parse+engine):      %10v  %5.1f%%\n", stats.ApplyDuration.Round(time.Millisecond), pct(stats.ApplyDuration))
		fmt.Fprintf(r.out, "  finalize (close+hash+snap):   %10v  %5.1f%%\n", stats.FinalizeDuration.Round(time.Millisecond), pct(stats.FinalizeDuration))
	}
	fmt.Fprintln(r.out, "================================================================================")
}

package replaytool

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"

	"github.com/spf13/cobra"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/keylet"
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
	legacyPayChanDirGate bool
	payChanDirFirstFixed uint32
}

var replayGCTuning sync.Mutex

// newReplayRangeCmd builds the `replay-range` command and its flags.
func newReplayRangeCmd() *cobra.Command {
	return newReplayRangeCmdWithRun(func(ctx context.Context, runner *replayRangeRunner) error {
		return runner.run(ctx)
	})
}

func newReplayRangeCmdWithRun(run func(context.Context, *replayRangeRunner) error) *cobra.Command {
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

Retired amendment IDs may remain in or be absent from that ledger entry, so its
membership cannot identify their historical behavior. Replay uses rippled
v3.2.0 post-fix fixPayChanRecipientOwnerDir semantics for every target ledger
by default.
--legacy-paychan-owner-dir-gate explicitly uses pre-fix semantics for every
target ledger. With that flag, --paychan-owner-dir-first-fixed-ledger defines the
first target ledger that uses fixed semantics; target ledgers below it remain
pre-fix. A first-fixed ledger of zero leaves the entire selected range pre-fix.

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
    goxrpl replay-range --from 32750 --to 32800
    goxrpl replay-range --from 32750 --to 32800 -v
    goxrpl replay-range --from 32750 --to 32800 --dump-dir ./debug
    goxrpl replay-range --from 3100000 --to 3278999 --legacy-paychan-owner-dir-gate
    goxrpl replay-range --from 3275000 --to 3285000 --legacy-paychan-owner-dir-gate --paychan-owner-dir-first-fixed-ledger 3280000
    goxrpl replay-range --from 99226370 --to 99236370 --checkpoint-dir ./ckpt
    goxrpl replay-range --from 99226370 --to 99236370 --checkpoint-dir ./ckpt --resume-from 99230000`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r.out = cmd.OutOrStdout()
			return run(cmd.Context(), r)
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
	cmd.Flags().BoolVar(&r.legacyPayChanDirGate, "legacy-paychan-owner-dir-gate", false, "Use pre-fix recipient owner-directory semantics for every target ledger unless --paychan-owner-dir-first-fixed-ledger sets a transition")
	cmd.Flags().Uint32Var(&r.payChanDirFirstFixed, "paychan-owner-dir-first-fixed-ledger", 0, "First target ledger that uses fixed recipient owner-directory semantics; a nonzero value requires --legacy-paychan-owner-dir-gate (0 keeps the entire range pre-fix when legacy mode is enabled)")

	// MarkFlagRequired only errors if the flag does not exist — a construction
	// bug, so fail fast rather than ignoring the error.
	for _, name := range []string{"from", "to"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(fmt.Sprintf("replay-range: marking flag %q required: %v", name, err))
		}
	}

	return cmd
}

type rangeReplayStats struct {
	BlocksProcessed   int
	BlocksSuccessful  int
	TotalTransactions int
	Divergences       int
	TotalDuration     time.Duration
	FailedAtBlock     uint32
	FailureReason     string
}

func (r *replayRangeRunner) run(parentCtx context.Context) (runErr error) {
	if err := r.validateFlags(); err != nil {
		return err
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

	ctx, cancel := context.WithCancelCause(parentCtx)
	defer cancel(nil)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}

	profiler, err := startReplayPProf(ctx, cancel)
	if err != nil {
		return err
	}
	if profiler != nil {
		fmt.Fprintf(r.out, "pprof enabled on %s\n", profiler.Addr())
		defer func() {
			runErr = errors.Join(runErr, profiler.Shutdown())
		}()
	}

	startTime := time.Now()

	// The default in-memory state path keeps the whole tree live for the run, so
	// a higher GC trigger trades RAM for fewer full marks of a mostly-static set.
	if r.gogc > 0 {
		replayGCTuning.Lock()
		previousGCPercent := debug.SetGCPercent(r.gogc)
		defer func() {
			debug.SetGCPercent(previousGCPercent)
			replayGCTuning.Unlock()
		}()
	}

	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintln(r.out, "                    XRPL Continuous State Replay")
	fmt.Fprintln(r.out, "================================================================================")
	fmt.Fprintf(r.out, "Range:      %d -> %d (%d blocks)\n", r.from, r.to, r.to-r.from)
	switch {
	case !r.legacyPayChanDirGate:
		fmt.Fprintln(r.out, "Compatibility: post-fix fixPayChanRecipientOwnerDir semantics for every target ledger (rippled v3.2.0)")
	case r.payChanDirFirstFixed == 0:
		fmt.Fprintln(r.out, "Compatibility: pre-fix fixPayChanRecipientOwnerDir semantics for every target ledger")
	default:
		fmt.Fprintf(r.out, "Compatibility: pre-fix fixPayChanRecipientOwnerDir semantics before target ledger %d; fixed semantics at and after it\n", r.payChanDirFirstFixed)
	}
	fmt.Fprintf(r.out, "Started at: %s\n", startTime.Format(time.RFC3339))
	fmt.Fprintln(r.out)

	// Connect to database
	fmt.Fprintln(r.out, "[1/3] Connecting to database...")
	client, err := statecompare.NewClientFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("closing database client: %w", err))
		}
	}()
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

	var findings *findingsWriter
	if r.continueOnDivergence {
		findings, err = newFindingsWriter(r.findingsPath())
		if err != nil {
			return fmt.Errorf("opening findings file: %w", err)
		}
		defer func() {
			if err := findings.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("closing findings: %w", err))
			}
		}()
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
		source, err := newStateSource(client, r.nodestoreDir, r.baseCacheMB, r.overlayCacheMB)
		if err != nil {
			return fmt.Errorf("initializing state source: %w", err)
		}
		defer func() {
			if err := source.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("closing state source: %w", err))
			}
		}()
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

	stats := &rangeReplayStats{}
	var artifactErr error
	commit := goxrplCommit(r.goxrplCommit)
	currentStateMap := stateMap
	previousSnapshot := preSnapshot

	for sequence := uint64(startLedger) + 1; sequence <= uint64(r.to); sequence++ {
		targetLedger := uint32(sequence)
		blockStart := time.Now()

		// Process this block
		result, newStateMap, err := r.processBlock(ctx, client, currentStateMap, previousSnapshot, targetLedger, fees)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = err.Error()
			fmt.Fprintf(r.out, "[%d] ERROR: %v\n", targetLedger, err)
			break
		}

		blockDuration := time.Since(blockStart)
		stats.BlocksProcessed++
		stats.TotalTransactions += result.TxCount

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
				fees, err = feesFromStateMap(currentStateMap)
				if err != nil {
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = err.Error()
					fmt.Fprintf(r.out, "[%d] ERROR loading fees: %v\n", targetLedger, err)
					break
				}
				if err := r.maybeCheckpoint(ctx, targetLedger, currentStateMap); err != nil {
					stats.FailedAtBlock = targetLedger
					stats.FailureReason = err.Error()
					fmt.Fprintf(r.out, "[%d] ERROR: %v\n", targetLedger, err)
					break
				}
				continue
			}

			stats.FailedAtBlock = targetLedger
			stats.FailureReason = "hash mismatch"
			fmt.Fprintln(r.out)
			artifactErr = r.dumpRangeDebugInfo(ctx, targetLedger, result, currentStateMap, newStateMap)
			if artifactErr != nil {
				stats.FailureReason = errors.Join(errors.New(stats.FailureReason), artifactErr).Error()
				fmt.Fprintf(r.out, "ERROR writing requested diagnostics: %v\n", artifactErr)
			}
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
		fees, err = feesFromStateMap(currentStateMap)
		if err != nil {
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = err.Error()
			fmt.Fprintf(r.out, "[%d] ERROR loading fees: %v\n", targetLedger, err)
			break
		}

		// Periodically checkpoint so a crash or stop can resume mid-range.
		if err := r.maybeCheckpoint(ctx, targetLedger, currentStateMap); err != nil {
			stats.FailedAtBlock = targetLedger
			stats.FailureReason = err.Error()
			fmt.Fprintf(r.out, "[%d] ERROR: %v\n", targetLedger, err)
			break
		}
	}

	stats.TotalDuration = time.Since(startTime)

	// Print summary
	fmt.Fprintln(r.out)
	r.printRangeSummary(stats)

	if stats.FailedAtBlock > 0 {
		// The failure is already reported above; only the exit code is left.
		return errors.Join(cmdexit.ErrReported, artifactErr)
	}
	return nil
}

func (r *replayRangeRunner) validateFlags() error {
	if r.from >= r.to {
		return fmt.Errorf("--from must be less than --to")
	}
	if r.payChanDirFirstFixed != 0 && !r.legacyPayChanDirGate {
		return errors.New("--paychan-owner-dir-first-fixed-ledger requires --legacy-paychan-owner-dir-gate")
	}
	return nil
}

// maybeCheckpoint writes a checkpoint when checkpointing is enabled and the
// ledger seq lands on the configured interval.
func (r *replayRangeRunner) maybeCheckpoint(ctx context.Context, seq uint32, stateMap *shamap.SHAMap) error {
	if r.checkpointDir == "" || r.checkpointInterval == 0 ||
		seq%r.checkpointInterval != 0 {
		return nil
	}
	if err := writeCheckpoint(ctx, r.checkpointDir, seq, stateMap); err != nil {
		return fmt.Errorf("writing checkpoint at ledger %d: %w", seq, err)
	}
	if r.verbose {
		fmt.Fprintf(r.out, "      checkpoint written at ledger %d\n", seq)
	}
	return nil
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
	result *blockResult,
	preState, goxrplPost *shamap.SHAMap,
) (*shamap.SHAMap, error) {
	writeFailedFinding := func(verified bool, stage string, stageErr error) error {
		finding := buildFinding(commit, ledgerIndex, parentHash, result, verified, nil, false)
		finding.Errors = append(finding.Errors, fmt.Sprintf("%s: %v", stage, stageErr))
		if err := findings.Write(finding); err != nil {
			return errors.Join(stageErr, fmt.Errorf("writing failed finding: %w", err))
		}
		return stageErr
	}

	corrected, verified, err := reconstructMainnetState(ctx, client, preState, result.PostSnapshot, result.Rules)
	if err != nil {
		return nil, fmt.Errorf("reconstructing mainnet state: %w", writeFailedFinding(false, "reconstruction failed", err))
	}
	var diverging []divergingObject
	divergingComplete := true
	if verified {
		diverging, divergingComplete, err = divergingObjectsContext(ctx, goxrplPost, corrected)
		if err != nil {
			return nil, fmt.Errorf("computing diverging objects: %w", writeFailedFinding(true, "diagnostics failed", err))
		}
	}
	finding := buildFinding(commit, ledgerIndex, parentHash, result, verified, diverging, divergingComplete)
	if err := findings.Write(finding); err != nil {
		return nil, fmt.Errorf("writing finding: %w", err)
	}
	if !verified {
		return nil, nil
	}
	return corrected, nil
}

type blockResult struct {
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
	TxResults               []txApplyInfo
	Errors                  []string
	Rules                   *amendment.Rules
}

func loadInitialState(ctx context.Context, client *statecompare.Client, ledgerIndex uint32) (*shamap.SHAMap, *statecompare.LedgerSnapshot, drops.Fees, error) {
	// Get snapshot
	snapshot, err := client.Snapshot(ctx, ledgerIndex)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	// Stream the state pack into the map so the whole pack and the full entry
	// slice are never materialized in RAM at once.
	stateMap := shamap.New(shamap.TypeState)
	if err := client.StreamStateEntries(ctx, snapshot, func(entry statecompare.StateEntry) error {
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

	// Seed fees from the verified state, failing if a present FeeSettings entry
	// is malformed.
	fees, err := feesFromStateMap(stateMap)
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}

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
	stateMap, ckptSeq, err := loadCheckpoint(ctx, path)
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}
	if ckptSeq != seq {
		return nil, nil, drops.Fees{}, fmt.Errorf("checkpoint %s holds ledger %d, expected %d", path, ckptSeq, seq)
	}

	snapshot, err := client.Snapshot(ctx, seq)
	if err != nil {
		return nil, nil, drops.Fees{}, fmt.Errorf("getting snapshot: %w", err)
	}

	if err := verifyStateRoot(stateMap, snapshot.AccountHash, seq); err != nil {
		return nil, nil, drops.Fees{}, err
	}

	fees, err := feesFromStateMap(stateMap)
	if err != nil {
		return nil, nil, drops.Fees{}, err
	}
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
		return amendment.NewRules(amendment.PermanentlyEnabledIDs()), nil
	}
	rules, err := ledger.LoadAmendmentsFromLedgerEntry(item.Data())
	if err != nil {
		return nil, fmt.Errorf("parsing amendments entry: %w", err)
	}
	return rules, nil
}

func replayPreFixPayChanRecipientOwnerDir(targetLedger uint32, legacyGate bool, firstFixedLedger uint32) bool {
	return legacyGate && (firstFixedLedger == 0 || targetLedger < firstFixedLedger)
}

func (r *replayRangeRunner) processBlock(
	ctx context.Context,
	client *statecompare.Client,
	preStateMap *shamap.SHAMap,
	preSnapshot *statecompare.LedgerSnapshot,
	targetLedger uint32,
	fees drops.Fees,
) (*blockResult, *shamap.SHAMap, error) {
	return r.processBlockShared(ctx, client, preStateMap, preSnapshot, targetLedger, fees)
}

func validateReplaySnapshotLink(preSnapshot, postSnapshot *statecompare.LedgerSnapshot, targetLedger uint32) error {
	if preSnapshot == nil || postSnapshot == nil {
		return errors.New("replay snapshot cannot be nil")
	}
	if postSnapshot.LedgerIndex != targetLedger {
		return fmt.Errorf("target snapshot ledger index: got %d, expected %d", postSnapshot.LedgerIndex, targetLedger)
	}
	if targetLedger == 0 {
		return errors.New("target ledger cannot be zero")
	}
	if preSnapshot.LedgerIndex != targetLedger-1 {
		return fmt.Errorf("parent snapshot ledger index: got %d, expected %d", preSnapshot.LedgerIndex, targetLedger-1)
	}
	if postSnapshot.ParentHash != preSnapshot.LedgerHash {
		return fmt.Errorf("ledger %d parent hash does not match ledger %d hash", targetLedger, preSnapshot.LedgerIndex)
	}
	return nil
}

func (r *replayRangeRunner) dumpRangeDebugInfo(ctx context.Context, ledgerIndex uint32, result *blockResult, preStateMap, postStateMap *shamap.SHAMap) error {
	dir := r.dumpDir
	if dir == "" {
		dir = fmt.Sprintf("./debug/ledger_%d", ledgerIndex)
	} else {
		dir = filepath.Join(dir, fmt.Sprintf("ledger_%d", ledgerIndex))
	}

	fmt.Fprintf(r.out, "Writing debug files to: %s\n", dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating dump directory: %w", err)
	}

	// Materializing a nodestore-lazy map would walk millions of nodes; skip
	// the full state/diff dump and rely on --continue-on-divergence for
	// targeted, object-level findings instead.
	if preStateMap.IsBacked() || postStateMap.IsBacked() {
		fmt.Fprintf(r.out, "  Skipping full state/diff dump for nodestore-lazy state; use --continue-on-divergence for object-level findings\n")
		return r.writeTxResults(dir, result)
	}

	postStateFile := filepath.Join(dir, "post_state.json")
	postStateCount, err := writeStateArtifact(ctx, postStateFile, postStateMap)
	if err != nil {
		return fmt.Errorf("writing post_state.json: %w", err)
	}
	fmt.Fprintf(r.out, "  Wrote %s (%d entries)\n", postStateFile, postStateCount)

	diffFile := filepath.Join(dir, "state_diff.json")
	if _, err := writeStateDiffArtifact(ctx, diffFile, preStateMap, postStateMap); err != nil {
		return fmt.Errorf("writing state_diff.json: %w", err)
	}
	fmt.Fprintf(r.out, "  Wrote %s\n", diffFile)

	return r.writeTxResults(dir, result)
}

// writeTxResults writes the per-transaction apply results for a block.
func (r *replayRangeRunner) writeTxResults(dir string, result *blockResult) error {
	txResultsFile := filepath.Join(dir, "tx_results.json")
	materializeDecoded(result.TxResults)
	if err := writeJSONFile(txResultsFile, result.TxResults); err != nil {
		return fmt.Errorf("writing tx_results.json: %w", err)
	}
	fmt.Fprintf(r.out, "  Wrote %s (%d transactions)\n", txResultsFile, len(result.TxResults))
	return nil
}

func (r *replayRangeRunner) printRangeFailure(ledgerIndex uint32, result *blockResult) {
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
	fmt.Fprintln(r.out, "Use 'goxrpl compare' to analyze state differences.")
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

func (r *replayRangeRunner) printRangeSummary(stats *rangeReplayStats) {
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
	fmt.Fprintln(r.out, "================================================================================")
}

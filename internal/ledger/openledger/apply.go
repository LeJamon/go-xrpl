package openledger

import (
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// Total/retry pass counts mirror rippled OpenLedger.h:40 (LEDGER_TOTAL_PASSES=3)
// and OpenLedger.h:44 (LEDGER_RETRY_PASSES=1).
const (
	totalPasses = 3
	retryPasses = 1
)

func nextRetryState(certainRetry bool, changes, pass int) bool {
	return certainRetry && changes != 0 && pass < retryPasses
}

// Mode controls how tec results are classified during apply.
//
// OpenLedgerMode mirrors rippled OpenLedger::apply_one
// (OpenLedger.cpp:170-189): tec always classifies as Success and commits
// with metadata, because result.applied = isTesSuccess || isTecClaim
// (Transactor.cpp:1108-1218). This is the per-tx ingress path
// (OpenLedger.Submit) and the Accept-replay path (OpenLedger.Accept).
//
// BuildLedgerMode mirrors rippled BuildLedger.cpp's apply loop: tec
// results classify as Retry on retriable passes (certainRetry=true) and
// commit as Success on the final non-retry pass. This is the consensus-
// build path used by Service.AcceptConsensusResult.
type Mode int

const (
	// OpenLedgerMode is the zero value so unset cfg.Mode defaults to the
	// open-ledger semantics expected on the ingress / replay paths.
	OpenLedgerMode Mode = iota
	BuildLedgerMode
)

// ApplyConfig captures the engine inputs shared across the 3-pass loop.
// BaseFee / ReserveBase / ReserveIncrement should be read by the caller
// from the ledger's FeeSettings SLE.
type ApplyConfig struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
	LedgerSequence   uint32
	NetworkID        uint32
	// ParentCloseTime is the close time of the parent ledger in
	// Ripple-epoch seconds. Pseudo-transactions like EnableAmendment
	// stamp this onto sfMajorities entries (Change.cpp:309-310), so
	// leaving it at 0 forks the AmendmentsSLE at the first flag
	// ledger that records a majority. Inbound replay sets the
	// equivalent EngineConfig field; this struct lets the
	// consensus-build path do the same.
	ParentCloseTime uint32
	// ApplicationCloseTime is the provisional successor time exposed while a
	// closed ledger is built. Open-ledger callers leave it unset.
	ApplicationCloseTime    uint32
	ApplicationCloseTimeSet bool
	Logger                  xrpllog.Logger
	// SkipSignatureVerification forces signature checks off on every
	// pass (mirrors AcceptLedger's standalone path where
	// SkipSignatureVerification = s.config.Standalone). When false,
	// pass 0 verifies signatures and later passes skip — matching
	// AcceptConsensusResult.
	SkipSignatureVerification bool
	// Mode selects rippled-faithful tec classification (see Mode docs).
	// Zero value = OpenLedgerMode. The consensus-build call site must
	// set BuildLedgerMode explicitly.
	Mode Mode
	// Rules is the amendment rule-set in effect for the parent ledger.
	// Plumbed into tx.EngineConfig.Rules so threading and other
	// amendment-gated transactor behaviour respects the on-ledger
	// Amendments SLE. Nil falls back to tx.Engine.rules() default
	// (all-amendments-on), which silently desyncs the engine from
	// the validated ledger state — production callers must set this.
	// Reference: rippled Application::buildLedger reads
	// previousLedger->rules() and threads it through; no equivalent
	// "all-on" fallback exists there.
	Rules                 *amendment.Rules
	NumberContextOverride *state.NumberContext
	// ApplyFlags is the engine ApplyFlags driving this submission.
	// Mirrors rippled NetworkOPs::apply which threads its flags into
	// TxQ::canBeHeld (TxQ.cpp:393-399 rejects tapFAIL_HARD).
	// Default zero — fail_hard rejection only fires when callers
	// explicitly set the bit.
	ApplyFlags tx.ApplyFlags
	// FeeTrack is the node-local LoadFeeTrack. Threaded into
	// tx.EngineConfig so the open-ledger fee floor reflects local /
	// cluster / global load (scaleFeeLoad), matching rippled's
	// Transactor::minimumFee. Nil leaves the floor at the raw base fee.
	// Consulted on open-ledger applies (gated on OpenLedger) and on the
	// TxQ apply/accept paths while load is elevated (gated on
	// EnforceLoadFee, set by TxqAdapter.ApplyTransaction); the
	// consensus-build path leaves it ignored.
	FeeTrack *feetrack.LoadFeeTrack
	// RetrySalt orders the shared retry set when it is supplied by a consensus
	// acceptance. Rippled keeps this salt on the CanonicalTXSet that carries
	// build leftovers and newly retriable open-ledger transactions.
	RetrySalt *[32]byte
}

// ApplyTxs runs rippled's open-ledger 3-pass apply against view, which
// is mutated in place. retries (if non-nil) is filled with PendingTxs whose
// final classification is Retry — caller decides whether to hold them for
// the next ledger. Seeded retries are part of the same shared queue as
// candidates returned by the initial pass.
//
// The shared retry queue is ordered by ApplyConfig.RetrySalt when supplied.
// Without it, insertion order is preserved. The consensus build path
// canonical-sorts with the agreed-set SHAMap-root salt per RCLConsensus.cpp:
// 512; the future OpenLedger.Modify will NOT sort.
//
// Mirrors OpenLedger::apply (OpenLedger.h:209-270) and apply_one
// (OpenLedger.cpp:170-189). The "skip txs already in parent" guard from
// BuildLedger.cpp:125-129 is folded in here so every caller benefits.
//
// ApplyTxs is the bulk-replay path (consensus build + Accept retries-
// first). The per-tx ingress path is OpenLedger.Submit, which routes
// through TxQ.Apply so terQUEUED is treated as Success per
// OpenLedger.cpp:183. ApplyTxs does not invoke TxQ inline: queued txs
// belong to the Accept modifier hook (OpenLedger.cpp:113-115), and
// inline-queueing during a replay would re-enter the queue path we are
// supposed to be draining.

// applyAndClassify runs a single transaction through bp. Applied results are
// successful regardless of TER; tef/tem/tel results fail; all other non-applied
// results retry. The engine's apply flags decide whether a tec result is applied.
func applyAndClassify(bp *txengine.BlockProcessor, transaction tx.Transaction, blob []byte, mode Mode, logger xrpllog.Logger) (Result, error) {
	var result txengine.BlockTxResult
	var applyErr error
	if mode == BuildLedgerMode {
		result, applyErr = bp.ApplyLedgerTransaction(transaction, blob)
	} else {
		result, applyErr = bp.ApplyTransaction(transaction, blob)
	}
	if applyErr != nil {
		logger.Warn("apply error — staged transaction discarded",
			"mode", mode,
			"hash", fmt.Sprintf("%x", result.Hash[:8]),
			"err", applyErr)
		return ResultFailure, applyErr
	}
	engineResult := result.ApplyResult.Result
	switch {
	case result.ApplyResult.Applied:
		return ResultSuccess, nil
	case engineResult.IsTef(), engineResult.IsTem(), engineResult.IsTel():
		return ResultFailure, nil
	default:
		return ResultRetry, nil
	}
}

// applyOneSingle is the single-tx convenience that mirrors apply_one
// (OpenLedger.cpp:170-189). It builds a one-shot engine + BlockProcessor
// against view and classifies the outcome. retry=true mirrors apply_one's
// retry parameter (sets tapRETRY so tec results land in retries instead of
// committing).
func applyOneSingle(view *ledger.Ledger, transaction tx.Transaction, blob []byte, retry bool, cfg ApplyConfig) Result {
	engineConfig := tx.EngineConfig{
		BaseFee:                 cfg.BaseFee,
		ReserveBase:             cfg.ReserveBase,
		ReserveIncrement:        cfg.ReserveIncrement,
		LedgerSequence:          cfg.LedgerSequence,
		NetworkID:               cfg.NetworkID,
		ParentCloseTime:         cfg.ParentCloseTime,
		ApplicationCloseTime:    cfg.ApplicationCloseTime,
		ApplicationCloseTimeSet: cfg.ApplicationCloseTimeSet,
		// Real parent hash drives pseudo-account derivation (AMMCreate);
		// the zero value forks the derived account ID from the network.
		ParentHash:                view.ParentHash(),
		Logger:                    cfg.Logger,
		SkipSignatureVerification: cfg.SkipSignatureVerification,
		Rules:                     cfg.Rules,
		NumberContextOverride:     cfg.NumberContextOverride,
		ApplyFlags:                cfg.ApplyFlags,
		FeeTrack:                  cfg.FeeTrack,
		ViewOpen:                  cfg.Mode == OpenLedgerMode,
	}
	if retry {
		engineConfig.ApplyFlags |= tx.TapRETRY
	}
	engine := txengine.NewEngine(view, engineConfig)
	// Seed the engine's txCount from the view so the TransactionIndex assigned
	// to this tx reflects all txs already in the open view — mirrors rippled's
	// OpenView::txCount() = baseTxCount_ + txs_.size(). Without this seed, a
	// non-TxQ Submit path hitting applyOneSingle twice in a row on the same
	// view would assign TransactionIndex=0 to both txs.
	engine.SetBaseTxCount(view.TxCount())
	bp := txengine.NewBlockProcessor(engine)
	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	result, _ := applyAndClassify(bp, transaction, blob, cfg.Mode, logger)
	return result
}

func ApplyTxs(view *ledger.Ledger, txs []PendingTx, retries *[]PendingTx, cfg ApplyConfig) error {
	return applyTxs(view, txs, retries, cfg, true, true)
}

type retryCandidate struct {
	pending PendingTx
	parsed  tx.Transaction
}

func appendRetryCandidate(
	queue []retryCandidate,
	seen map[[32]byte]struct{},
	ptx PendingTx,
	parsed tx.Transaction,
) []retryCandidate {
	if _, exists := seen[ptx.Hash]; exists {
		return queue
	}
	seen[ptx.Hash] = struct{}{}
	return append(queue, retryCandidate{pending: ptx, parsed: parsed})
}

func orderRetryCandidates(queue []retryCandidate, salt *[32]byte) {
	if salt == nil || len(queue) < 2 {
		return
	}
	ordered := make([]PendingTx, len(queue))
	byHash := make(map[[32]byte]retryCandidate, len(queue))
	for i, candidate := range queue {
		ordered[i] = candidate.pending
		byHash[candidate.pending.Hash] = candidate
	}
	CanonicalSort(ordered, *salt)
	for i, ptx := range ordered {
		queue[i] = byHash[ptx.Hash]
	}
}

func applyTxs(
	view *ledger.Ledger,
	txs []PendingTx,
	retries *[]PendingTx,
	cfg ApplyConfig,
	checkMembership bool,
	includeSeededRetries bool,
) error {
	if view == nil {
		return errors.New("openledger.ApplyTxs: view is nil")
	}
	if len(txs) == 0 && (!includeSeededRetries || retries == nil || len(*retries) == 0) {
		return nil
	}
	if cfg.Rules == nil {
		return errors.New("openledger.ApplyTxs: amendment rules are required")
	}
	if cfg.Mode != OpenLedgerMode && cfg.Mode != BuildLedgerMode {
		return fmt.Errorf("openledger.ApplyTxs: invalid mode %d", cfg.Mode)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}

	parsed := make([]tx.Transaction, len(txs))
	eligible := make([]bool, len(txs))
	for i, ptx := range txs {
		t, err := tx.ParseFromBinary(ptx.Blob)
		if err != nil {
			return fmt.Errorf("openledger.ApplyTxs: parse transaction %x: %w", ptx.Hash, err)
		}
		t.SetRawBytes(ptx.Blob)
		parsed[i] = t
		if checkMembership {
			exists, err := view.TxExists(ptx.Hash)
			if err != nil {
				return fmt.Errorf("openledger.ApplyTxs: check transaction %x: %w", ptx.Hash, err)
			}
			eligible[i] = !exists
		} else {
			eligible[i] = true
		}
	}

	// retrySet is rippled's shared `OrderedTxs retries`: it starts with any
	// seeded candidates and receives candidates returned by the prior-current
	// pass. Seeded candidates are deliberately not applied in the initial txs
	// pass; retriesFirst controls whether this shared loop runs before the
	// previous open view is replayed.
	retrySet := make([]retryCandidate, 0, len(txs))
	retrySeen := make(map[[32]byte]struct{}, len(txs))
	if includeSeededRetries && retries != nil {
		for _, ptx := range *retries {
			parsedRetry, err := tx.ParseFromBinary(ptx.Blob)
			if err != nil {
				return fmt.Errorf("openledger.ApplyTxs: parse seeded retry %x: %w", ptx.Hash, err)
			}
			parsedRetry.SetRawBytes(ptx.Blob)
			retrySet = appendRetryCandidate(retrySet, retrySeen, ptx, parsedRetry)
		}
	}

	buildEngine := func(certainRetry, skipSig bool) *txengine.BlockProcessor {
		engineConfig := tx.EngineConfig{
			BaseFee:                 cfg.BaseFee,
			ReserveBase:             cfg.ReserveBase,
			ReserveIncrement:        cfg.ReserveIncrement,
			LedgerSequence:          cfg.LedgerSequence,
			NetworkID:               cfg.NetworkID,
			ParentCloseTime:         cfg.ParentCloseTime,
			ApplicationCloseTime:    cfg.ApplicationCloseTime,
			ApplicationCloseTimeSet: cfg.ApplicationCloseTimeSet,
			// Real parent hash drives pseudo-account derivation (AMMCreate);
			// the zero value forks the derived account ID from the network.
			ParentHash:                view.ParentHash(),
			Logger:                    cfg.Logger,
			SkipSignatureVerification: skipSig,
			Rules:                     cfg.Rules,
			NumberContextOverride:     cfg.NumberContextOverride,
			ApplyFlags:                cfg.ApplyFlags,
			FeeTrack:                  cfg.FeeTrack,
			ViewOpen:                  cfg.Mode == OpenLedgerMode,
		}
		if certainRetry {
			engineConfig.ApplyFlags |= tx.TapRETRY
		}
		engine := txengine.NewEngine(view, engineConfig)
		// Issue #470: the per-pass engine's txCount starts at 0. Without
		// re-seeding from the view's current tx count, txs committed on a
		// retry pass would re-use TxIndex values already assigned to txs
		// from the initial pass, producing duplicate TransactionIndex
		// values in metadata — observable as identical TxIndex on
		// different txs in the same ledger, which forks the SHAMap
		// tx+meta root from rippled. Mirrors rippled OpenView::txCount()
		// = baseTxCount_ + txs_.size() where baseTxCount_ accumulates
		// across the build's apply passes.
		engine.SetBaseTxCount(view.TxCount())
		return txengine.NewBlockProcessor(engine)
	}

	// Initial single pass over txs (OpenLedger.h:220-238). retry=true on
	// this pass so tec results stay retriable rather than committing.
	bp := buildEngine(true, cfg.SkipSignatureVerification)
	initialChanges := 0
	for i, ptx := range txs {
		if parsed[i] == nil || !eligible[i] {
			continue
		}
		class, err := applyAndClassify(bp, parsed[i], ptx.Blob, cfg.Mode, logger)
		if err != nil {
			return fmt.Errorf("openledger.ApplyTxs: apply transaction %x on initial pass: %w", ptx.Hash, err)
		}
		switch class {
		case ResultSuccess:
			initialChanges++
		case ResultRetry:
			retrySet = appendRetryCandidate(retrySet, retrySeen, ptx, parsed[i])
		}
	}
	orderRetryCandidates(retrySet, cfg.RetrySalt)

	retryLoopCount := totalPasses
	certainRetry := true
	passOffset := 0
	if cfg.Mode == BuildLedgerMode {
		retryLoopCount--
		passOffset = 1
		certainRetry = nextRetryState(certainRetry, initialChanges, 0)
	}
	for pass := 0; pass < retryLoopCount && len(retrySet) > 0; pass++ {
		// Signatures were verified on the initial pass; retry passes
		// normally skip. Seeded retries have no initial tx pass, so verify
		// them on the first shared retry pass before allowing later passes
		// to use the cached verdict.
		bp = buildEngine(certainRetry, cfg.SkipSignatureVerification || pass > 0)

		changes := 0
		// Reuse retrySet's backing array; the range length is fixed before
		// appends and each candidate is read before its slot is reused.
		nextRetries := retrySet[:0]
		for _, candidate := range retrySet {
			ptx := candidate.pending
			class, err := applyAndClassify(bp, candidate.parsed, ptx.Blob, cfg.Mode, logger)
			if err != nil {
				return fmt.Errorf(
					"openledger.ApplyTxs: apply transaction %x on retry pass %d: %w",
					ptx.Hash,
					pass+1,
					err,
				)
			}
			switch class {
			case ResultSuccess:
				changes++
			case ResultRetry:
				nextRetries = append(nextRetries, candidate)
			}
		}
		retrySet = nextRetries

		// rippled OpenLedger.h:259-260: a non-retry pass that made no
		// changes bails. retryPasses below caps the retry-enabled passes.
		if changes == 0 && !certainRetry {
			break
		}
		certainRetry = nextRetryState(certainRetry, changes, pass+passOffset)
	}

	if retries != nil {
		if includeSeededRetries {
			*retries = (*retries)[:0]
		}
		seen := make(map[[32]byte]struct{}, len(retrySet)+len(*retries))
		for _, ptx := range *retries {
			seen[ptx.Hash] = struct{}{}
		}
		for _, candidate := range retrySet {
			if _, ok := seen[candidate.pending.Hash]; ok {
				continue
			}
			seen[candidate.pending.Hash] = struct{}{}
			*retries = append(*retries, candidate.pending)
		}
	}

	return nil
}

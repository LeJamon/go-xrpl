package openledger

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/tx"
	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// Total/retry pass counts mirror rippled OpenLedger.h:40 (LEDGER_TOTAL_PASSES=3)
// and OpenLedger.h:44 (LEDGER_RETRY_PASSES=1).
const (
	totalPasses = 3
	retryPasses = 1
)

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
	Logger          xrpllog.Logger
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
	Rules *amendment.Rules
	// ApplyFlags is the engine ApplyFlags driving this submission.
	// Mirrors rippled NetworkOPs::apply which threads its flags into
	// TxQ::canBeHeld (TxQ.cpp:393-399 rejects tapFAIL_HARD).
	// Default zero — fail_hard rejection only fires when callers
	// explicitly set the bit.
	ApplyFlags tx.ApplyFlags
}

// ApplyTxs runs rippled's open-ledger 3-pass apply against view, which
// is mutated in place. retries (if non-nil) is filled, in input order,
// with PendingTxs whose final classification is Retry — caller decides
// whether to hold them for the next ledger.
//
// Caller is responsible for any canonical sort (the consensus build
// path canonical-sorts with the agreed-set SHAMap-root salt per
// RCLConsensus.cpp:512; the future OpenLedger.Modify will NOT sort).
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

// applyAndClassify runs a single tx through bp against view and classifies
// the engine result per the selected Mode.
//
// OpenLedgerMode: tec is always Success+commit, matching
// OpenLedger::apply_one (OpenLedger.cpp:170-189) where
// result.applied = isTesSuccess || isTecClaim (Transactor.cpp:1108-1218).
//
// BuildLedgerMode: tec is Retry on retriable passes (certainRetry=true)
// and Success+commit on the final non-retry pass — mirrors
// BuildLedger.cpp's apply loop.
//
// Shared by ApplyTxs's per-pass inner loop and OpenLedger.Submit so the
// success/tec/retry classification lives in exactly one place.
func applyAndClassify(view *ledger.Ledger, bp *tx.BlockProcessor, transaction tx.Transaction, blob []byte, certainRetry bool, mode Mode, logger xrpllog.Logger) Result {
	result, applyErr := bp.ApplyTransaction(transaction, blob)
	if applyErr != nil {
		return ResultFailure
	}
	engineResult := result.ApplyResult.Result
	switch {
	case engineResult.IsSuccess():
		if err := view.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob); err != nil {
			logger.Warn("AddTransactionWithMeta failed for committed tx (tree out of sync with state)",
				"hash", fmt.Sprintf("%x", result.Hash[:8]),
				"ter", engineResult.String(),
				"err", err)
		}
		return ResultSuccess
	case engineResult.IsTec():
		if mode == BuildLedgerMode && certainRetry {
			return ResultRetry
		}
		if err := view.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob); err != nil {
			logger.Warn("AddTransactionWithMeta failed for committed tec tx (tree out of sync with state)",
				"hash", fmt.Sprintf("%x", result.Hash[:8]),
				"ter", engineResult.String(),
				"err", err)
		}
		return ResultSuccess
	case engineResult.ShouldRetry():
		return ResultRetry
	default:
		return ResultFailure
	}
}

// applyOneSingle is the single-tx convenience that mirrors apply_one
// (OpenLedger.cpp:170-189). It builds a one-shot engine + BlockProcessor
// against view and classifies the outcome. retry=true mirrors apply_one's
// retry parameter (sets tapRETRY so tec results land in retries instead of
// committing).
func applyOneSingle(view *ledger.Ledger, transaction tx.Transaction, blob []byte, retry bool, cfg ApplyConfig) Result {
	engineConfig := tx.EngineConfig{
		BaseFee:                   cfg.BaseFee,
		ReserveBase:               cfg.ReserveBase,
		ReserveIncrement:          cfg.ReserveIncrement,
		LedgerSequence:            cfg.LedgerSequence,
		NetworkID:                 cfg.NetworkID,
		ParentCloseTime:           cfg.ParentCloseTime,
		Logger:                    cfg.Logger,
		SkipSignatureVerification: cfg.SkipSignatureVerification,
		Rules:                     cfg.Rules,
	}
	if retry {
		engineConfig.ApplyFlags |= tx.TapRETRY
	}
	engine := tx.NewEngine(view, engineConfig)
	// Seed the engine's txCount from the view so the TransactionIndex assigned
	// to this tx reflects all txs already in the open view — mirrors rippled's
	// OpenView::txCount() = baseTxCount_ + txs_.size(). Without this seed, a
	// non-TxQ Submit path hitting applyOneSingle twice in a row on the same
	// view would assign TransactionIndex=0 to both txs.
	engine.SetBaseTxCount(view.TxCount())
	bp := tx.NewBlockProcessor(engine)
	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	return applyAndClassify(view, bp, transaction, blob, retry, cfg.Mode, logger)
}

func ApplyTxs(view *ledger.Ledger, txs []PendingTx, retries *[]PendingTx, cfg ApplyConfig) error {
	if view == nil || len(txs) == 0 {
		return nil
	}

	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}

	parsed := make([]tx.Transaction, len(txs))
	for i, ptx := range txs {
		t, err := tx.ParseFromBinary(ptx.Blob)
		if err != nil {
			logger.Warn("openledger: dropping malformed tx in replay",
				"hash", ptx.Hash, "err", err)
			continue
		}
		t.SetRawBytes(ptx.Blob)
		parsed[i] = t
	}

	// retrySet tracks the canonical retry queue (rippled's `OrderedTxs
	// retries`). Each tx index either lives here (Retry classification on
	// the previous pass) or has already been settled (Success/Failure).
	retrySet := make([]int, 0, len(txs))

	buildEngine := func(certainRetry, skipSig bool) *tx.BlockProcessor {
		engineConfig := tx.EngineConfig{
			BaseFee:                   cfg.BaseFee,
			ReserveBase:               cfg.ReserveBase,
			ReserveIncrement:          cfg.ReserveIncrement,
			LedgerSequence:            cfg.LedgerSequence,
			NetworkID:                 cfg.NetworkID,
			ParentCloseTime:           cfg.ParentCloseTime,
			Logger:                    cfg.Logger,
			SkipSignatureVerification: skipSig,
			Rules:                     cfg.Rules,
		}
		if certainRetry {
			engineConfig.ApplyFlags |= tx.TapRETRY
		}
		engine := tx.NewEngine(view, engineConfig)
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
		return tx.NewBlockProcessor(engine)
	}

	// Initial single pass over txs (OpenLedger.h:220-238). retry=true on
	// this pass so tec results stay retriable rather than committing.
	bp := buildEngine(true, cfg.SkipSignatureVerification)
	for i, ptx := range txs {
		if parsed[i] == nil {
			continue
		}
		if view.TxExists(ptx.Hash) {
			continue
		}
		switch applyAndClassify(view, bp, parsed[i], ptx.Blob, true, cfg.Mode, logger) {
		case ResultRetry:
			retrySet = append(retrySet, i)
		}
	}

	// Retry passes (OpenLedger.h:240-264). retry stays true while
	// `certainRetry` and the pass index is below LEDGER_RETRY_PASSES;
	// thereafter the final pass commits any tec leftover.
	certainRetry := true
	for pass := 0; pass < totalPasses && len(retrySet) > 0; pass++ {
		// Signatures were verified on the initial pass; retry passes
		// always skip.
		bp = buildEngine(certainRetry, true)

		changes := 0
		// Reuse retrySet's backing array; the inner loop reads each idx
		// before appending, so aliasing is safe.
		nextRetries := retrySet[:0]
		for _, idx := range retrySet {
			ptx := txs[idx]
			if parsed[idx] == nil {
				continue
			}
			switch applyAndClassify(view, bp, parsed[idx], ptx.Blob, certainRetry, cfg.Mode, logger) {
			case ResultSuccess:
				changes++
			case ResultRetry:
				nextRetries = append(nextRetries, idx)
			}
		}
		retrySet = nextRetries

		// rippled OpenLedger.h:259-260: a non-retry pass that made no
		// changes bails. retryPasses below caps the retry-enabled passes.
		if changes == 0 && !certainRetry {
			break
		}
		if changes == 0 || pass >= retryPasses {
			certainRetry = false
		}
	}

	if retries != nil && len(retrySet) > 0 {
		// Dedup against entries already in *retries — Accept calls
		// ApplyTxs over multiple phases (retries-first, prior-current,
		// locals) with the same slice, so the same hash can land in
		// retrySet across phases.
		seen := make(map[[32]byte]struct{}, len(retrySet)+len(*retries))
		for _, ptx := range *retries {
			seen[ptx.Hash] = struct{}{}
		}
		for _, idx := range retrySet {
			if _, ok := seen[txs[idx].Hash]; ok {
				continue
			}
			seen[txs[idx].Hash] = struct{}{}
			*retries = append(*retries, txs[idx])
		}
	}

	return nil
}

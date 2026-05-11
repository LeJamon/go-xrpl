package openledger

import (
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
	Logger           xrpllog.Logger
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
// Note: rippled's apply_one classifies `applied || terQUEUED` as
// Success. goXRPL does not invoke TxQ inline here (the original two
// service.go blocks did not either), so terQUEUED — being a ter code —
// falls through to ShouldRetry. If/when the open-ledger filter gains
// TxQ integration this branch will need to mirror OpenLedger.cpp:183.

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
func applyAndClassify(view *ledger.Ledger, bp *tx.BlockProcessor, transaction tx.Transaction, blob []byte, certainRetry bool, mode Mode) Result {
	result, applyErr := bp.ApplyTransaction(transaction, blob)
	if applyErr != nil {
		return ResultFailure
	}
	engineResult := result.ApplyResult.Result
	switch {
	case engineResult.IsSuccess():
		view.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
		return ResultSuccess
	case engineResult.IsTec():
		if mode == BuildLedgerMode && certainRetry {
			return ResultRetry
		}
		view.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
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
		Logger:                    cfg.Logger,
		SkipSignatureVerification: cfg.SkipSignatureVerification,
	}
	if retry {
		engineConfig.ApplyFlags |= tx.TapRETRY
	}
	engine := tx.NewEngine(view, engineConfig)
	bp := tx.NewBlockProcessor(engine)
	return applyAndClassify(view, bp, transaction, blob, retry, cfg.Mode)
}

func ApplyTxs(view *ledger.Ledger, txs []PendingTx, retries *[]PendingTx, cfg ApplyConfig) error {
	if view == nil || len(txs) == 0 {
		return nil
	}

	type txStatus int
	const (
		txPending txStatus = iota
		txSucceeded
		txRetry
		txFailed
	)

	statuses := make(map[[32]byte]txStatus, len(txs))

	// Parse blobs up front so we don't pay the cost per pass. Anything
	// that fails to parse is permanently dropped (mirrors apply_one's
	// tem/tef/tel branch for genuinely malformed input).
	parsed := make([]tx.Transaction, len(txs))
	for i, ptx := range txs {
		t, err := tx.ParseFromBinary(ptx.Blob)
		if err == nil {
			t.SetRawBytes(ptx.Blob)
			parsed[i] = t
		} else {
			statuses[ptx.Hash] = txFailed
		}
	}

	// Skip txs already in the view — rippled BuildLedger.cpp:125-129 and
	// OpenLedger.h:226-228 both pre-filter against the parent.
	for _, ptx := range txs {
		if view.TxExists(ptx.Hash) {
			statuses[ptx.Hash] = txFailed
		}
	}

	engineConfig := tx.EngineConfig{
		BaseFee:          cfg.BaseFee,
		ReserveBase:      cfg.ReserveBase,
		ReserveIncrement: cfg.ReserveIncrement,
		LedgerSequence:   cfg.LedgerSequence,
		NetworkID:        cfg.NetworkID,
		Logger:           cfg.Logger,
	}

	certainRetry := true
	for pass := 0; pass < totalPasses; pass++ {
		// pass>0 = signatures already verified on pass 0. Callers that
		// pre-skip sigs (standalone replay) keep it off on every pass.
		engineConfig.SkipSignatureVerification = cfg.SkipSignatureVerification || pass > 0
		// tapRETRY on retriable passes; cleared on the final pass so any
		// leftover tec commits. BuildLedger.cpp:131-132.
		if certainRetry {
			engineConfig.ApplyFlags |= tx.TapRETRY
		} else {
			engineConfig.ApplyFlags &^= tx.TapRETRY
		}
		engine := tx.NewEngine(view, engineConfig)
		blockProcessor := tx.NewBlockProcessor(engine)

		changes := 0
		hasRetry := false

		for i, ptx := range txs {
			st := statuses[ptx.Hash]
			// Succeeded txs are already in the view; failed txs are out.
			if st == txFailed || st == txSucceeded {
				continue
			}
			// On retry passes, retry txs are handled by the dedicated
			// sub-loop below to match the build-path behavior.
			if pass > 0 && st == txRetry {
				continue
			}

			transaction := parsed[i]
			if transaction == nil {
				statuses[ptx.Hash] = txFailed
				continue
			}

			switch applyAndClassify(view, blockProcessor, transaction, ptx.Blob, certainRetry, cfg.Mode) {
			case ResultSuccess:
				changes++
				statuses[ptx.Hash] = txSucceeded
			case ResultRetry:
				statuses[ptx.Hash] = txRetry
				hasRetry = true
			default:
				statuses[ptx.Hash] = txFailed
			}
		}

		// Retry sub-loop: on retry passes, re-run anything classified as
		// txRetry above. Matches the original service.go two-sub-loop
		// shape (and rippled's per-pass retry behavior).
		if pass > 0 {
			for i, ptx := range txs {
				if statuses[ptx.Hash] != txRetry {
					continue
				}
				transaction := parsed[i]
				if transaction == nil {
					statuses[ptx.Hash] = txFailed
					continue
				}

				switch applyAndClassify(view, blockProcessor, transaction, ptx.Blob, certainRetry, cfg.Mode) {
				case ResultSuccess:
					changes++
					statuses[ptx.Hash] = txSucceeded
				case ResultRetry:
					hasRetry = true
				default:
					statuses[ptx.Hash] = txFailed
				}
			}
		}

		if !hasRetry {
			break
		}
		if changes == 0 && !certainRetry {
			break
		}
		if changes == 0 || pass >= retryPasses {
			certainRetry = false
		}
	}

	if retries != nil {
		for _, ptx := range txs {
			if statuses[ptx.Hash] == txRetry {
				*retries = append(*retries, ptx)
			}
		}
	}

	return nil
}

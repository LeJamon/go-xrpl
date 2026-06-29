package openledger

import (
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// Config carries the bits needed by Submit/Accept to build ApplyConfig.
type Config struct {
	NetworkID uint32
	Logger    xrpllog.Logger
}

// OpenLedger is goxrpl's open-ledger view.
// Invariants:
//   - current is never nil after construction.
//   - Current() reads under currentMu.RLock — never blocked by long Modify apply.
//   - Modify serialises writers via modifyMu and atomically publishes
//     a new current pointer under currentMu.Lock.
type OpenLedger struct {
	cfg       Config
	logger    xrpllog.Logger
	modifyMu  sync.Mutex
	currentMu sync.RWMutex
	current   *ledger.Ledger
	// cachedTxs memoises CurrentTxs for the published view; nil'd at every
	// publish point. Guarded by currentMu.
	cachedTxs [][]byte
}

// New creates a fresh OpenLedger anchored on closed; the initial Current() view is
// an open ledger built on top of closed via ledger.NewOpen.
func New(closed *ledger.Ledger, cfg Config) (*OpenLedger, error) {
	if closed == nil {
		return nil, errors.New("openledger.New: closed parent is nil")
	}
	initial, err := ledger.NewOpen(closed, time.Now())
	if err != nil {
		return nil, err
	}
	return &OpenLedger{
		cfg:     cfg,
		logger:  cfg.Logger,
		current: initial,
	}, nil
}

// Current returns the latest published view. Safe for concurrent callers.
func (o *OpenLedger) Current() *ledger.Ledger {
	o.currentMu.RLock()
	defer o.currentMu.RUnlock()
	return o.current
}

// Modify clones current, runs fn against the clone, and atomically publishes the
// clone iff fn returns true.
//
// modifyMu serialises writers so two concurrent Modify calls don't race on the
// same parent; readers calling Current() take only currentMu and see either the
// pre- or post-Modify pointer, never a partial clone.
//
// fn runs UNDER modifyMu, so it serialises against every Modify (and every Submit,
// which funnels through Modify): a slow fn stalls all concurrent submissions.
// Callers MUST keep fn short and CPU-bound — no I/O, locks, or long computation.
func (o *OpenLedger) Modify(fn func(*ledger.Ledger) bool) bool {
	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

	o.currentMu.RLock()
	parent := o.current
	o.currentMu.RUnlock()

	if !parent.IsOpen() {
		if o.logger != nil {
			o.logger.Error("openledger.Modify: current view is not open — refusing to apply")
		}
		return false
	}

	next, err := parent.MutableSnapshot()
	if err != nil {
		if o.logger != nil {
			o.logger.Error("openledger: MutableSnapshot failed", "err", err)
		}
		return false
	}

	changed := fn(next)
	if !changed {
		return false
	}

	o.currentMu.Lock()
	o.current = next
	o.cachedTxs = nil
	o.currentMu.Unlock()
	return true
}

// Accept rebuilds the working view from newLCL, replaying (optionally) retries
// first, then the prior view's txs, then the modifier, then locals; any PendingTx
// still in Retry on the final pass is appended to *retries.
//
// Locking: the retries-first apply runs OUTSIDE modifyMu so concurrent Submits
// aren't blocked by disputed-tx replay; modifyMu covers prior-current replay +
// modifier + locals + relay + publish; currentMu guards the final pointer swap.
//
// cfg is the per-call ApplyConfig (LedgerSequence is overridden to the working
// view's; Logger / NetworkID filled from the OpenLedger if unset).
//
// queue (if non-nil) routes locals through TxQ.Apply so an under-fee local lands
// in the queue rather than being dropped; nil falls back to direct ApplyTxs
// (tests / standalone).
//
// modifier (if non-nil) runs against the rebuilt view after replay and before
// locals — the TxQ-promotion hook so locals see the post-promotion fee level.
//
// relay (if non-nil) is invoked once per tx in the final view, skipping
// inner-batch txs; nil in tests / paths that should not re-broadcast.
func (o *OpenLedger) Accept(
	newLCL *ledger.Ledger,
	locals []PendingTx,
	retriesFirst bool,
	retries *[]PendingTx,
	cfg ApplyConfig,
	queue *txq.TxQ,
	modifier func(*ledger.Ledger),
	relay func(hash [32]byte, blob []byte),
) error {
	if newLCL == nil {
		return errors.New("openledger.Accept: newLCL is nil")
	}

	next, err := ledger.NewOpen(newLCL, time.Now())
	if err != nil {
		return err
	}

	applyCfg := cfg
	applyCfg.LedgerSequence = next.Sequence()
	applyCfg.NetworkID = o.cfg.NetworkID
	if applyCfg.Logger == nil {
		applyCfg.Logger = o.logger
	}
	// Force OpenLedger mode so we don't inherit a stray BuildLedgerMode from cfg.
	applyCfg.Mode = OpenLedgerMode

	// 1. retriesFirst — replay disputed/held txs first, OUTSIDE modifyMu.
	if retriesFirst && retries != nil && len(*retries) > 0 {
		held := append([]PendingTx(nil), (*retries)...)
		*retries = (*retries)[:0]
		if err := ApplyTxs(next, held, retries, applyCfg); err != nil {
			return err
		}
	}

	// Block concurrent Submits while we replay, modify, relay, and publish.
	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

	// 2. Replay prior current's txs.
	o.currentMu.RLock()
	curTxs := collectTxs(o.current, o.logger)
	o.currentMu.RUnlock()
	if len(curTxs) > 0 {
		if err := ApplyTxs(next, curTxs, retries, applyCfg); err != nil {
			return err
		}
	}

	// 3. Modifier hook (TxQ promotion) — runs before locals so they see the
	// post-promotion fee level.
	if modifier != nil {
		modifier(next)
	}

	// 4. Replay locals via TxQ.Apply so an under-fee local lands in the queue.
	if len(locals) > 0 {
		if queue != nil {
			viewCfg := applyCfg
			adapter := NewTxqAdapter(next, viewCfg)
			for _, lt := range locals {
				if next.TxExists(lt.Hash) {
					continue
				}
				parsed, perr := tx.ParseFromBinary(lt.Blob)
				if perr != nil {
					if o.logger != nil {
						o.logger.Debug("openledger.Accept: dropping malformed local tx", "hash", lt.Hash, "err", perr)
					}
					continue
				}
				parsed.SetRawBytes(lt.Blob)
				_ = queue.Apply(adapter, parsed, lt.Hash, lt.Account)
			}
		} else if err := ApplyTxs(next, locals, retries, applyCfg); err != nil {
			return err
		}
	}

	// 5. Relay recovered txs (skipping inner-batch); the caller's callback owns
	// the HashRouter + overlay, we just iterate.
	if relay != nil {
		_ = next.ForEachTransaction(func(hash [32]byte, data []byte) bool {
			rawBlob, _, splitErr := tx.SplitTxWithMetaBlob(data)
			if splitErr != nil {
				return true
			}
			if parsed, perr := tx.ParseFromBinary(rawBlob); perr == nil {
				if common := parsed.GetCommon(); common != nil && common.Flags != nil {
					if *common.Flags&tx.TfInnerBatchTxn != 0 {
						return true
					}
				}
			}
			relay(hash, rawBlob)
			return true
		})
	}

	// 6. Atomic publish.
	o.currentMu.Lock()
	o.current = next
	o.cachedTxs = nil
	o.currentMu.Unlock()
	return nil
}

// snapshotCurrentTxs returns the cached tx-blob slice for the published view,
// building and memoising it on first access. The returned slice is the internal
// cache pointer and MUST NOT be exposed directly; CurrentTxs wraps it.
func (o *OpenLedger) snapshotCurrentTxs() [][]byte {
	o.currentMu.RLock()
	if o.cachedTxs != nil {
		cached := o.cachedTxs
		o.currentMu.RUnlock()
		return cached
	}
	view := o.current
	o.currentMu.RUnlock()
	if view == nil {
		return nil
	}

	var built [][]byte
	_ = view.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			return true
		}
		built = append(built, raw)
		return true
	})

	o.currentMu.Lock()
	if o.current == view && o.cachedTxs == nil {
		o.cachedTxs = built
	}
	o.currentMu.Unlock()
	return built
}

// CurrentTxs returns a snapshot of the raw tx blobs in the published view. The
// outer slice is fresh per call (safe to re-order); the inner byte slices are
// shared with the view and must not be mutated.
func (o *OpenLedger) CurrentTxs() [][]byte {
	cached := o.snapshotCurrentTxs()
	out := make([][]byte, len(cached))
	copy(out, cached)
	return out
}

// collectTxs parses each tx blob in view into a PendingTx, skipping (and logging)
// malformed entries so one bad record doesn't poison the replay.
func collectTxs(v *ledger.Ledger, logger xrpllog.Logger) []PendingTx {
	if v == nil {
		return nil
	}
	if logger == nil {
		logger = xrpllog.Discard()
	}
	var out []PendingTx
	_ = v.ForEachTransaction(func(itemKey [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			logger.Warn("openledger: skipping unsplittable tx item in replay",
				"item", itemKey, "err", err)
			return true
		}
		ptx, err := ParsePendingTx(raw)
		if err != nil {
			logger.Warn("openledger: skipping unparseable tx in replay",
				"item", itemKey, "err", err)
			return true
		}
		out = append(out, ptx)
		return true
	})
	return out
}

// Submit is the convenience entry point for tx ingress, wrapping Modify with a
// single-tx apply attempt. Returns (changed, result) where changed reflects
// whether the open-view pointer advanced.
//
// When queue is non-nil (production wiring) classification is delegated to
// TxQ.Apply, which decides whether to apply to the view or hold the tx. When TxQ
// holds a tx (terQUEUED) result is Success but changed is false. The nil-queue
// branch is for unit tests / standalone callers without a wired TxQ.
func (o *OpenLedger) Submit(ptx PendingTx, cfg ApplyConfig, queue *txq.TxQ) (bool, Result) {
	out := o.SubmitDetailed(ptx, cfg, queue)
	return out.Changed, out.Class
}

// SubmitOutcome is the detailed result of routing one tx through TxQ.Apply. Class
// is the coarse Success/Failure/Retry bucket the relay decision keys off; the
// other fields carry the engine detail the RPC submit response needs.
type SubmitOutcome struct {
	// Changed reports whether the open-view pointer advanced (the tx was
	// applied to the view). False for terQUEUED (in flight in the queue)
	// and for every rejection.
	Changed bool
	// Class is the OpenLedger 3-pass classification (Success/Failure/Retry).
	Class Result
	// Result is the engine TER, terQUEUED, or the rejection code.
	Result ter.Result
	// Applied is true only when the tx was committed to the open view.
	Applied bool
	// Queued is true when TxQ held the tx for a later ledger (terQUEUED).
	Queued bool
	// Fee is the drops charged by the engine on apply (0 when queued/rejected).
	Fee uint64
	// Metadata is the engine metadata on apply (nil when queued/rejected).
	Metadata *tx.Metadata
	// Message is the human-readable result message.
	Message string
}

// SubmitDetailed is the rich-result ingress entry point: like Submit but returns
// full engine detail so RPC submit can report engine_result / queued / fee. Both
// the RPC and network ingress paths funnel through here.
//
// When queue is non-nil (production wiring) classification is delegated to
// TxQ.Apply; terQUEUED is treated as Success/in-flight. The nil-queue branch is
// for unit tests / standalone callers without a wired TxQ.
func (o *OpenLedger) SubmitDetailed(ptx PendingTx, cfg ApplyConfig, queue *txq.TxQ) SubmitOutcome {
	// Per-tx ingress is always OpenLedger semantics; cfg.Mode is ignored.
	cfg.Mode = OpenLedgerMode
	var out SubmitOutcome
	out.Class = ResultFailure
	out.Result = ter.TefINTERNAL
	out.Changed = o.Modify(func(view *ledger.Ledger) bool {
		// Pre-filter: tx already in view → tefALREADY, so callers can report
		// the duplicate distinctly from a generic failure.
		if view.TxExists(ptx.Hash) {
			out.Class = ResultFailure
			out.Result = ter.TefALREADY
			out.Message = ter.TefALREADY.Message()
			return false
		}
		// Reuse the parse from ingress when present: it avoids re-decoding the
		// blob under the apply mutex and carries any off-strand signature verdict
		// (PrewarmSignature) through to the in-strand check. Fall back to parsing
		// for PendingTx values built without ParsePendingTx.
		parsed := ptx.Parsed
		if parsed == nil {
			p, err := tx.ParseFromBinary(ptx.Blob)
			if err != nil {
				out.Class = ResultFailure
				out.Result = ter.TemMALFORMED
				out.Message = ter.TemMALFORMED.Message()
				return false
			}
			p.SetRawBytes(ptx.Blob)
			parsed = p
		}

		if queue != nil {
			adapter := NewTxqAdapter(view, cfg)
			applyRes := queue.Apply(adapter, parsed, ptx.Hash, ptx.Account)
			out.Result = applyRes.Result
			if last := adapter.LastApplyResult(); last != nil {
				out.Fee = last.Fee
				out.Metadata = last.Metadata
				out.Message = last.Message
			}
			if out.Message == "" {
				out.Message = applyRes.Result.Message()
			}
			switch {
			case applyRes.Applied:
				out.Applied = true
				out.Class = ResultSuccess
				return true
			case applyRes.Result == ter.TerQUEUED:
				// Held for a later ledger — view unchanged but in flight, so
				// classify as Success. Nothing was charged: drop any
				// Fee/Metadata/Message from a failed direct-apply attempt.
				out.Queued = true
				out.Fee = 0
				out.Metadata = nil
				out.Message = applyRes.Result.Message()
				out.Class = ResultSuccess
				return false
			case applyRes.Result.IsTef() || applyRes.Result.IsTem() || applyRes.Result.IsTel():
				out.Class = ResultFailure
				return false
			default:
				out.Class = ResultRetry
				return false
			}
		}

		// No TxQ wired — fall back to direct apply with tapNONE (not tapRETRY:
		// tapRETRY would suppress tapFAIL_HARD interactions and shift
		// open-ledger fee throttling).
		out.Class = applyOneSingle(view, parsed, ptx.Blob, false, cfg)
		out.Applied = out.Class == ResultSuccess
		return out.Applied
	})
	return out
}

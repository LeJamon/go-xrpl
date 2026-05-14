package openledger

import (
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/txq"
	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// Config carries the bits needed by Submit/Accept to build ApplyConfig.
type Config struct {
	NetworkID uint32
	Logger    xrpllog.Logger
}

// OpenLedger is goxrpl's port of rippled's app/ledger/OpenLedger.
// Invariants:
//   - current is never nil after construction.
//   - Current() reads under currentMu.RLock — never blocked by long Modify apply.
//   - Modify serialises writers via modifyMu and atomically publishes
//     a new current pointer under currentMu.Lock.
type OpenLedger struct {
	cfg       Config
	logger    xrpllog.Logger
	modifyMu  sync.Mutex   // OpenLedger.cpp:56 modify_mutex_
	currentMu sync.RWMutex // OpenLedger.cpp:57 current_mutex_
	current   *ledger.Ledger
	// cachedTxs memoises the result of CurrentTxs against the currently
	// published view. Invalidated (set to nil) at every publish point.
	// Guarded by currentMu.
	cachedTxs [][]byte
}

// New creates a fresh OpenLedger anchored on closed. The initial Current()
// view is an open ledger built on top of closed via ledger.NewOpen,
// mirroring rippled's create() helper (OpenLedger.cpp:159-168).
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
// Mirrors OpenLedger::current() (OpenLedger.cpp:50-55).
func (o *OpenLedger) Current() *ledger.Ledger {
	o.currentMu.RLock()
	defer o.currentMu.RUnlock()
	return o.current
}

// Modify clones current, runs fn against the clone, and atomically
// publishes the clone iff fn returns true. Mirrors OpenLedger::modify()
// (OpenLedger.cpp:57-69).
//
// Concurrency: modifyMu serialises writers so two concurrent Modify calls
// do not race on top of the same parent (matching rippled's
// modify_mutex_). Readers calling Current() take only the currentMu read
// lock and so are never blocked by an in-flight fn — they either see the
// pre-Modify pointer or the post-Modify pointer, never a partially-
// constructed clone.
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

// Accept rebuilds the working view from newLCL, optionally replaying
// retries first, then prior current view's txs, then running the
// modifier (TxQ promotion), then replaying locals via TxQ.apply. Any
// PendingTxs that ended in Retry on the final pass are appended to
// *retries for the caller.
//
// Mirrors OpenLedger::accept (OpenLedger.cpp:71-155).
//
// Locking matches rippled (OpenLedger.cpp:85-94): the retries-first
// apply runs OUTSIDE modifyMu so concurrent Submits aren't blocked by
// disputed-tx replay against the freshly-closed ledger; modifyMu is
// acquired only for prior-current replay + modifier + locals + relay
// + publish. currentMu is taken for the final pointer swap.
//
// cfg carries the per-call ApplyConfig — the caller has just computed
// fees from newLCL via readFeesFromLedger. LedgerSequence is overridden
// to the working view's sequence, and Logger / NetworkID are filled in
// from the OpenLedger if not already set.
//
// queue (if non-nil) routes locals through TxQ.Apply so each local
// re-enters the queue path (rippled OpenLedger.cpp:117-118 calls
// `app.getTxQ().apply(app, *next, item.second, flags, j_)` per local).
// Without queue, locals fall back to direct ApplyTxs (used only by
// tests / standalone-mode replay).
//
// modifier (if non-nil) runs against the freshly built next view after
// retries-and-prior-current replay and BEFORE locals. This is the hook
// rippled uses at OpenLedger.cpp:113 to call
// `app_.getTxQ().accept(app_, view)` — promoting queued txs into the
// new open view so the post-promotion fee level shapes which locals can
// land. Pass nil when no TxQ promotion is desired.
//
// relay (if non-nil) is invoked once per tx in the final post-replay
// view (skipping inner-batch txs, mirroring OpenLedger.cpp:120-150).
// Callers thread their overlay handle through this callback. Pass nil
// in unit tests / paths that should not re-broadcast.
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
	// Accept-replay is OpenLedger semantics, not BuildLedger. Force the
	// mode here so we don't inherit a stray BuildLedgerMode from cfg.
	applyCfg.Mode = OpenLedgerMode

	// 1. retriesFirst — replay disputed/held txs first, OUTSIDE modifyMu
	// so concurrent Submits aren't blocked (OpenLedger.cpp:85-90).
	if retriesFirst && retries != nil && len(*retries) > 0 {
		held := append([]PendingTx(nil), (*retries)...)
		*retries = (*retries)[:0]
		if err := ApplyTxs(next, held, retries, applyCfg); err != nil {
			return err
		}
	}

	// Block concurrent Submits while we read prior-current, run the
	// modifier, replay locals, relay, and publish (OpenLedger.cpp:94).
	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

	// 2. Replay prior current's txs (OpenLedger.cpp:96-112).
	o.currentMu.RLock()
	curTxs := collectTxs(o.current, o.logger)
	o.currentMu.RUnlock()
	if len(curTxs) > 0 {
		if err := ApplyTxs(next, curTxs, retries, applyCfg); err != nil {
			return err
		}
	}

	// 3. Modifier hook — rippled OpenLedger.cpp:113-115 calls
	// app_.getTxQ().accept(app_, view) here to drain queued txs into
	// the freshly rebuilt open view BEFORE locals replay, so locals
	// see the post-promotion fee level.
	if modifier != nil {
		modifier(next)
	}

	// 4. Replay locals via TxQ.Apply (OpenLedger.cpp:117-118). Each
	// local re-enters the queue path so a local that does not meet the
	// current fee level lands in the queue rather than being dropped.
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

	// 5. Relay recovered txs — rippled OpenLedger.cpp:120-150 iterates
	// the rebuilt view and re-broadcasts any non-inner-batch tx whose
	// HashRouter::shouldRelay() permits it. Caller's relay callback
	// owns the HashRouter + overlay; we just iterate.
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

// CurrentTxs returns a snapshot of the raw tx blobs in the currently
// published view. Callers receive a fresh top-level slice that is safe
// to retain and re-order; the underlying tx-blob byte slices, however,
// are shared with the open-ledger view and MUST NOT be mutated.
//
// Under the hood we memoise the per-view []byte slice (one walk per
// publish, not per call) and copy the outer slice header on each call
// so an accidental append by a caller cannot bleed into another
// reader. Mirrors RCLConsensus.cpp:333-349 reading
// openLedger().current()->txs.
func (o *OpenLedger) CurrentTxs() [][]byte {
	o.currentMu.RLock()
	if o.cachedTxs != nil {
		out := make([][]byte, len(o.cachedTxs))
		copy(out, o.cachedTxs)
		o.currentMu.RUnlock()
		return out
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
	out := make([][]byte, len(built))
	copy(out, built)
	return out
}

// collectTxs extracts the raw tx blobs from view's tx map and parses
// each into a PendingTx. Malformed entries are skipped so a single bad
// record does not poison the whole replay, but each is logged at Warn
// so an operator can see that a peer fed unparseable data.
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

// Submit is the convenience entry point for tx ingress. It wraps Modify
// with a single-tx apply attempt mirroring apply_one
// (OpenLedger.cpp:170-189). Returns (changed, result) where changed
// reflects whether the open-view pointer was advanced; result is the
// per-tx classification.
//
// Mirrors NetworkOPsImp::apply calling openLedger().modify with a
// single-tx body (NetworkOPs.cpp:1507). When queue is non-nil (the
// production wiring) all classification is delegated to TxQ.Apply,
// which itself decides whether to apply directly to the view or hold
// the tx in the queue. Note: when TxQ holds a tx (terQUEUED) the
// result is Success but changed is false — the tx is in flight in the
// queue, not in the open view.
//
// The nil-queue branch exists only for unit tests / standalone-mode
// callers where TxQ is not wired; production wiring always passes a
// non-nil queue via service.go.
func (o *OpenLedger) Submit(ptx PendingTx, cfg ApplyConfig, queue *txq.TxQ) (bool, Result) {
	// Per-tx ingress is OpenLedger semantics by definition (BuildLedger
	// only applies inside consensus close). cfg.Mode is ignored.
	cfg.Mode = OpenLedgerMode
	result := ResultFailure
	changed := o.Modify(func(view *ledger.Ledger) bool {
		// Pre-filter: tx already in view → drop (BuildLedger.cpp:125-129).
		if view.TxExists(ptx.Hash) {
			result = ResultFailure
			return false
		}
		parsed, err := tx.ParseFromBinary(ptx.Blob)
		if err != nil {
			result = ResultFailure
			return false
		}
		parsed.SetRawBytes(ptx.Blob)

		if queue != nil {
			adapter := NewTxqAdapter(view, cfg)
			applyRes := queue.Apply(adapter, parsed, ptx.Hash, ptx.Account)
			switch {
			case applyRes.Applied:
				result = ResultSuccess
				return true
			case applyRes.Result == tx.TerQUEUED:
				// Held for a later ledger — view is unchanged but the
				// tx is in flight, so classify as Success (matches
				// OpenLedger.cpp:183 treating terQUEUED as applied).
				result = ResultSuccess
				return false
			case applyRes.Result.IsTef() || applyRes.Result.IsTem() || applyRes.Result.IsTel():
				result = ResultFailure
				return false
			default:
				result = ResultRetry
				return false
			}
		}

		// No TxQ wired — fall back to direct apply with tapNONE. Rippled's
		// per-tx ingress path is NetworkOPs::processTransaction →
		// TxQ.apply (NetworkOPs.cpp:1483-1530), which uses tapNONE — not
		// OpenLedger::apply_one(retry=true). Setting tapRETRY here would
		// suppress tapFAIL_HARD interactions (Transactor.cpp:1114-1124)
		// and shift open-ledger fee throttling.
		result = applyOneSingle(view, parsed, ptx.Blob, false, cfg)
		return result == ResultSuccess
	})
	return changed, result
}

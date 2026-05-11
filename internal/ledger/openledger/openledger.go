package openledger

import (
	"errors"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/tx"
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
	o.currentMu.Unlock()
	return true
}

// Accept rebuilds the working view from newLCL, optionally replaying
// retries first, then prior current view's txs, then locals. Any
// PendingTxs that ended in Retry on the final pass are appended to
// *retries for the caller.
//
// Mirrors OpenLedger::accept (OpenLedger.cpp:71-155).
//
// Locking: holds modifyMu for the entire rebuild — concurrent Submits
// are serialised behind this. currentMu is taken only for the final
// pointer swap. This is slightly stricter than rippled (which releases
// modify_mutex_ around the retries-first apply) but eliminates an
// observable race that rippled's design tolerates because of its
// implicit Application-locking discipline.
//
// cfg carries the per-call ApplyConfig — the caller has just computed
// fees from newLCL via readFeesFromLedger. LedgerSequence is overridden
// to the working view's sequence, and Logger / NetworkID are filled in
// from the OpenLedger if not already set.
func (o *OpenLedger) Accept(
	newLCL *ledger.Ledger,
	locals []PendingTx,
	retriesFirst bool,
	retries *[]PendingTx,
	cfg ApplyConfig,
) error {
	if newLCL == nil {
		return errors.New("openledger.Accept: newLCL is nil")
	}

	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

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

	// 1. retriesFirst — replay disputed/held txs first
	// (OpenLedger.cpp:85-90). We drain the caller's slice up front and
	// let ApplyTxs re-fill it with any final-pass Retry classifications.
	if retriesFirst && retries != nil && len(*retries) > 0 {
		held := append([]PendingTx(nil), (*retries)...)
		*retries = (*retries)[:0]
		if err := ApplyTxs(next, held, retries, applyCfg); err != nil {
			return err
		}
	}

	// 2. Replay prior current's txs (OpenLedger.cpp:96-112). The
	// parent-skip guard inside ApplyTxs drops anything already in
	// newLCL.
	o.currentMu.RLock()
	curTxs := collectTxs(o.current)
	o.currentMu.RUnlock()
	if len(curTxs) > 0 {
		if err := ApplyTxs(next, curTxs, retries, applyCfg); err != nil {
			return err
		}
	}

	// 3. Replay locals (OpenLedger.cpp:117-118).
	if len(locals) > 0 {
		if err := ApplyTxs(next, locals, retries, applyCfg); err != nil {
			return err
		}
	}

	// 4. Atomic publish.
	o.currentMu.Lock()
	o.current = next
	o.currentMu.Unlock()
	return nil
}

// collectTxs extracts the raw tx blobs from view's tx map and parses
// each into a PendingTx. Malformed entries are silently skipped so a
// single bad record does not poison the whole replay.
func collectTxs(v *ledger.Ledger) []PendingTx {
	if v == nil {
		return nil
	}
	var out []PendingTx
	_ = v.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			return true
		}
		ptx, err := ParsePendingTx(raw)
		if err != nil {
			return true
		}
		out = append(out, ptx)
		return true
	})
	return out
}

// Submit is the convenience entry point for tx ingress. It wraps Modify
// with a single-tx apply attempt mirroring apply_one
// (OpenLedger.cpp:170-189). Returns (changed, result) where changed is
// the Modify return value and result is the per-tx classification.
//
// Mirrors NetworkOPsImp::apply calling openLedger().modify with a
// single-tx body (NetworkOPs.cpp:1507).
func (o *OpenLedger) Submit(ptx PendingTx, cfg ApplyConfig) (bool, Result) {
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
		// retry=true matches OpenLedger::apply's call into apply_one for
		// the per-tx initial attempt (OpenLedger.h:229).
		result = applyOneSingle(view, parsed, ptx.Blob, true, cfg)
		return result == ResultSuccess
	})
	return changed, result
}

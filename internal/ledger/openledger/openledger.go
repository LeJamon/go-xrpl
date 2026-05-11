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

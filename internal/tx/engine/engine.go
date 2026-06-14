package engine

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"

	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// Engine processes transactions against a ledger.
//
// Engine instances are NOT safe for concurrent Apply/ApplyPseudo calls. A
// single Engine is meant to drive a single open ledger's transaction stream
// in order (matching rippled's OpenView, which is also single-writer). The
// only field that may be touched off-thread is txCount (read via TxCount,
// reset via SetBaseTxCount), which is atomic to make those accessors safe
// for observers — it is not a license to call Apply concurrently.
type Engine struct {
	// View provides access to ledger state
	view txcore.LedgerView

	// Config holds engine configuration
	config txcore.EngineConfig

	// logger is the scoped logger for the Tx partition.
	// Always non-nil; falls back to xrpllog.Discard() when not configured.
	logger xrpllog.Logger

	// txCount tracks the number of applied transactions for TransactionIndex.
	// Each applied transaction (tesSUCCESS or tec) gets the current count as
	// its TransactionIndex, then the counter increments.
	// Reference: rippled OpenView::txCount() = baseTxCount_ + txs_.size()
	txCount atomic.Uint32

	// invariantViolationHook, when non-nil, lets tests force an invariant
	// violation for a given (result, table). Production always leaves it nil,
	// so runInvariantsOnTable behaves exactly as the real checkers dictate.
	invariantViolationHook func(result ter.Result, table *txcore.ApplyStateTable) *invariants.InvariantViolation
}

// rulesView wraps a LedgerView so Rules() reports a known rule set. The engine's
// base view (e.g. a Ledger) returns nil from Rules(), but rippled's preclaim view
// always carries the parent ledger's rules. Wrapping the view for the Preclaimer
// dispatch keeps rules-gated reads (e.g. accountFunds' frozen-LP-token check)
// working at the preclaim stage, matching the rules visible during apply.
type rulesView struct {
	txcore.LedgerView
	rules *amendment.Rules
}

func (v rulesView) Rules() *amendment.Rules { return v.rules }

// NewEngine creates a new transaction engine
func NewEngine(view txcore.LedgerView, config txcore.EngineConfig) *Engine {
	logger := config.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	return &Engine{
		view:   view,
		config: config,
		logger: logger.Named(xrpllog.PartitionTx),
	}
}

// InvariantViolationValue describes a detected invariant violation. Exported so
// test hooks can construct one without importing the invariants package.
type InvariantViolationValue = invariants.InvariantViolation

// InvariantViolationHook is a test-only override that forces an invariant
// violation for a given (result, table). It is consulted by the invariant pass
// on both the tes and tec apply paths after the real checkers pass cleanly.
type InvariantViolationHook = func(result ter.Result, table *txcore.ApplyStateTable) *InvariantViolationValue

// SetInvariantViolationHookForTest installs a test-only hook that forces an
// invariant violation, used to exercise the tec→tecINVARIANT_FAILED→
// tefINVARIANT_FAILED escalation without crafting a state that trips a real
// checker. Production never calls this, so the hook stays nil and the real
// checkers alone decide.
func (e *Engine) SetInvariantViolationHookForTest(hook InvariantViolationHook) {
	e.invariantViolationHook = hook
}

// NewInvariantViolation builds an invariant violation value for tests that drive
// SetInvariantViolationHookForTest, without exposing the invariants package to
// test callers.
func NewInvariantViolation(name, message string) *invariants.InvariantViolation {
	return &invariants.InvariantViolation{Name: name, Message: message}
}

// rules returns the amendment rules for this engine. EngineConfig.Rules
// MUST be set by the caller — typically from the parent ledger's
// Amendments SLE via ledger.LoadAmendmentsFromLedger (production) or
// amendment.AllSupportedRules / EmptyRules (tests / genesis).
//
// Mirrors rippled's `Rules() = delete` (include/xrpl/protocol/Rules.h:57)
// where there is no default constructor — every Rules instance is
// explicitly built from a ledger via makeRulesGivenLedger. A silent
// fallback (whether AllSupportedRules or EmptyRules) desyncs the engine
// from on-chain state: AllSupportedRules treats every amendment as
// enabled even when not on the ledger (the #401/#418 wedge); EmptyRules
// treats everything as disabled even when amendments ARE on the ledger
// (which broke the soak in the opposite direction). Panicking forces
// every call site to plumb the real rules.
func (e *Engine) rules() *amendment.Rules {
	return e.config.GetRules()
}

// TxCount returns the current transaction count (for batch baseTxCount).
// Reference: rippled OpenView::txCount()
func (e *Engine) TxCount() uint32 {
	return e.txCount.Load()
}

// Preflight runs the preflight pipeline (syntax, signature, tx-type
// validation) against the engine's rules and returns the TER. Used by
// TxQ.Apply to reject structurally invalid submissions before they are
// held in the queue, mirroring rippled's preflight at TxQ.cpp:743-745.
func (e *Engine) Preflight(tx txcore.Transaction) ter.Result {
	return e.preflight(tx)
}

// Preclaim runs the full preclaim pipeline against the engine's view
// and returns the TER. Used by TxQ's multiTxn path (TxQ.cpp:1167-1170)
// which runs preclaim against a cloned view with adjusted AccountRoot
// fields to detect terINSUF_FEE_B / terPRE_SEQ before queueing.
func (e *Engine) Preclaim(tx txcore.Transaction, txHash [32]byte) ter.Result {
	return e.preclaim(tx, txHash)
}

// SetBaseTxCount sets the base transaction count for batch inner transactions.
// Inner transactions start numbering from this value.
// Reference: rippled OpenView::baseTxCount_ initialized from parent view
func (e *Engine) SetBaseTxCount(count uint32) {
	e.txCount.Store(count)
}

// adjustOwnerCountOnView modifies an account's OwnerCount on a LedgerView.
// Used by the engine for tecOVERSIZE offer cleanup after the sandbox is discarded.
// Reference: rippled removeUnfundedOffers() adjusts owner count on the base view.
func adjustOwnerCountOnView(view txcore.LedgerView, account [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) {
	_ = txcore.AdjustOwnerCountWithTx(view, account, delta, txHash, ledgerSeq)
}

// deleteNFTokenOfferOnView deletes an NFTokenOffer from the ledger view,
// removing it from owner directory, NFTBuys/NFTSells directory, and erasing the SLE.
// Used for tecEXPIRED re-deletion of expired NFToken offers.
// Reference: rippled NFTokenUtils.cpp deleteTokenOffer
func deleteNFTokenOfferOnView(view txcore.LedgerView, offerKL keylet.Keylet, txHash [32]byte, ledgerSeq uint32) {
	offerData, err := view.Read(offerKL)
	if err != nil || offerData == nil {
		return
	}

	offer, err := state.ParseNFTokenOffer(offerData)
	if err != nil {
		return
	}

	ownerDirKey := keylet.OwnerDir(offer.Owner)
	state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false)

	// Remove from NFTBuys or NFTSells directory
	isSellOffer := offer.Flags&entry.LsfSellNFToken != 0
	var tokenDirKey keylet.Keylet
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(offer.NFTokenID)
	} else {
		tokenDirKey = keylet.NFTBuys(offer.NFTokenID)
	}
	state.DirRemove(view, tokenDirKey, offer.NFTokenOfferNode, offerKL.Key, false)

	_ = view.Erase(offerKL)
	adjustOwnerCountOnView(view, offer.Owner, -1, txHash, ledgerSeq)
}

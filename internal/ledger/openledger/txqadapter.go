package openledger

import (
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// TxqAdapter bridges *ledger.Ledger + ApplyConfig to the interfaces
// txq.ApplyContext, txq.AcceptContext, and txq.ClosedLedgerContext
// without taking a direct dependency on the txq package.
//
// Each adapter is bound to a specific open-ledger view and an
// ApplyConfig. It is constructed:
//   - inside OpenLedger.Submit, where the view is the just-cloned snapshot
//     under Modify (so every Submit gets a fresh adapter against the
//     working clone).
//   - inside the modifier closure passed to OpenLedger.Accept, where the
//     view is the newly built next-open snapshot.
//
// Rippled equivalent: NetworkOPs.cpp:1483-1530 builds an OpenView
// directly; the adapter pattern keeps the cycle out of go's import graph.
type TxqAdapter struct {
	view *ledger.Ledger
	cfg  ApplyConfig
}

// NewTxqAdapter constructs an adapter over the given view + apply config.
// The caller is responsible for owning the view's lifetime (typically the
// closure body inside Modify / Accept's modifier hook).
func NewTxqAdapter(view *ledger.Ledger, cfg ApplyConfig) *TxqAdapter {
	return &TxqAdapter{view: view, cfg: cfg}
}

// GetLedgerSequence returns the view's current sequence (open-ledger seq).
func (a *TxqAdapter) GetLedgerSequence() uint32 {
	if a.view == nil {
		return 0
	}
	return a.view.Sequence()
}

// GetParentHash returns the view's parent (LCL) hash. Used by TxQ to
// pseudo-randomly order same-fee candidates deterministically across
// validators.
func (a *TxqAdapter) GetParentHash() [32]byte {
	if a.view == nil {
		return [32]byte{}
	}
	return a.view.ParentHash()
}

// GetTxInLedger returns the number of transactions already applied to the
// open view. Used by FeeMetrics::ScaleFeeLevel to compute the escalated
// open-ledger fee level.
func (a *TxqAdapter) GetTxInLedger() uint32 {
	if a.view == nil {
		return 0
	}
	var count uint32
	_ = a.view.ForEachTransaction(func(_ [32]byte, _ []byte) bool {
		count++
		return true
	})
	return count
}

// GetAccountSequence reads the AccountRoot.Sequence for accountID. Returns
// 0 when the account does not exist (matching rippled's behavior:
// unknown account → caller will then hit AccountExists=false and bail).
func (a *TxqAdapter) GetAccountSequence(accountID [20]byte) uint32 {
	ar, ok := a.readAccountRoot(accountID)
	if !ok {
		return 0
	}
	return ar.Sequence
}

// AccountExists reports whether an AccountRoot SLE exists for accountID.
func (a *TxqAdapter) AccountExists(accountID [20]byte) bool {
	if a.view == nil {
		return false
	}
	exists, err := a.view.Exists(keylet.Account(accountID))
	if err != nil {
		return false
	}
	return exists
}

// TicketExists reports whether a Ticket SLE exists for (accountID, ticketSeq).
func (a *TxqAdapter) TicketExists(accountID [20]byte, ticketSeq uint32) bool {
	if a.view == nil {
		return false
	}
	exists, err := a.view.Exists(keylet.Ticket(accountID, ticketSeq))
	if err != nil {
		return false
	}
	return exists
}

// GetAccountBalance returns the account's XRP balance in drops. Returns 0
// when the account does not exist.
func (a *TxqAdapter) GetAccountBalance(accountID [20]byte) uint64 {
	ar, ok := a.readAccountRoot(accountID)
	if !ok {
		return 0
	}
	return ar.Balance
}

// GetAccountReserve returns the reserve requirement for an account at the
// given ownerCount: reserveBase + ownerCount * reserveIncrement.
//
// Rippled: View::accountReserve in View.h:285.
func (a *TxqAdapter) GetAccountReserve(ownerCount uint32) uint64 {
	return a.cfg.ReserveBase + uint64(ownerCount)*a.cfg.ReserveIncrement
}

// GetBaseFee returns the base fee in drops for txn. Per-tx-type
// adjustments (e.g. multisign multipliers) are folded into
// txq.ToFeeLevel by the caller, so returning cfg.BaseFee here is correct.
func (a *TxqAdapter) GetBaseFee(_ tx.Transaction) uint64 {
	return a.cfg.BaseFee
}

// ApplyTransaction is the engine call used by TxQ.Apply / TxQ.Accept.
// It constructs a one-shot Engine + BlockProcessor over the adapter's
// view and applies txn, returning (engineResult, applied) where
// applied = isTesSuccess || isTecClaim (matches rippled
// Transactor.cpp:1108-1218). On applied=true the tx+meta is written to
// the view's tx map so subsequent GetTxInLedger / TxExists reflect it.
func (a *TxqAdapter) ApplyTransaction(txn tx.Transaction) (tx.Result, bool) {
	if a.view == nil || txn == nil {
		return tx.TefINTERNAL, false
	}

	blob := txn.GetRawBytes()
	if len(blob) == 0 {
		return tx.TefINTERNAL, false
	}

	engineCfg := tx.EngineConfig{
		BaseFee:                   a.cfg.BaseFee,
		ReserveBase:               a.cfg.ReserveBase,
		ReserveIncrement:          a.cfg.ReserveIncrement,
		LedgerSequence:            a.view.Sequence(),
		NetworkID:                 a.cfg.NetworkID,
		Logger:                    a.cfg.Logger,
		SkipSignatureVerification: a.cfg.SkipSignatureVerification,
	}
	engine := tx.NewEngine(a.view, engineCfg)
	bp := tx.NewBlockProcessor(engine)

	result, err := bp.ApplyTransaction(txn, blob)
	if err != nil {
		return tx.TefINTERNAL, false
	}
	engineResult := result.ApplyResult.Result
	applied := engineResult.IsSuccess() || engineResult.IsTec()
	if applied {
		_ = a.view.AddTransactionWithMeta(result.Hash, result.TxWithMetaBlob)
	}
	return engineResult, applied
}

// PreclaimTransaction runs a preclaim-style check for the multiTxn path
// in TxQ.Apply. Rippled (TxQ.cpp:1127-1170) clones the open view,
// adjusts the account's Sequence and Balance to reflect the in-flight
// queued txs, then runs preclaim. If preclaim succeeds, returns
// tesSUCCESS (likely to claim fee).
//
// Faithful implementation here is non-trivial: it requires a fully
// isolated mutable snapshot of the view with the AccountRoot fields
// overwritten before the engine runs. Until that lands we return
// tesSUCCESS — same effect as rippled's "fee level is enough so
// preclaim probably passes" common case, at the cost of allowing some
// queued txs that would otherwise be rejected with terINSUF_FEE_B.
// Tracked as a follow-up; see TxQ.cpp:1167-1170.
func (a *TxqAdapter) PreclaimTransaction(_ tx.Transaction, _ [20]byte, _ uint64, _ uint32) tx.Result {
	return tx.TesSUCCESS
}

// readAccountRoot is the shared helper for the SLE reads above.
func (a *TxqAdapter) readAccountRoot(accountID [20]byte) (*state.AccountRoot, bool) {
	if a.view == nil {
		return nil, false
	}
	key := keylet.Account(accountID)
	exists, err := a.view.Exists(key)
	if err != nil || !exists {
		return nil, false
	}
	data, err := a.view.Read(key)
	if err != nil {
		return nil, false
	}
	ar, err := state.ParseAccountRoot(data)
	if err != nil {
		return nil, false
	}
	return ar, true
}

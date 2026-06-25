package engine

import (
	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

// PseudoPreclaim is implemented by pseudo-transaction types that need
// rule-aware preclaim gating beyond the generic open-ledger check.
// Reference: rippled Change::preclaim per-tx-type switch (Change.cpp:93-139).
type PseudoPreclaim interface {
	PreclaimPseudo(rules *amendment.Rules) ter.Result
}

// pseudoPreflight enforces the gates that rippled's Change::preflight runs on
// every pseudo-transaction type before dispatching to type-specific logic.
// It also mirrors the two preflight0 guards that fire for pseudo-tx —
// tfInnerBatchTxn rejection (Transactor.cpp:46-51) and the NetworkID check
// when sfNetworkID is present (Transactor.cpp:53-75).
// Reference: rippled Change.cpp:36-80, Transactor.cpp:42-87.
func (e *Engine) pseudoPreflight(tx txcore.Transaction, rules *amendment.Rules) ter.Result {
	common := tx.GetCommon()

	// preflight0 inner-batch gate: a pseudo-tx with the tfInnerBatchTxn flag
	// is structurally invalid because only the batch executor sets that flag.
	// Reference: Transactor.cpp:46-51.
	if common.Flags != nil && *common.Flags&txcore.TfInnerBatchTxn != 0 {
		return ter.TemINVALID_FLAG
	}

	// preflight0 NetworkID gate: rippled checks NetworkID for non-pseudo
	// always, and for pseudo only when sfNetworkID is present. We are on the
	// pseudo path, so the check fires only when the field is set; an absent
	// NetworkID on a pseudo-tx is always legal.
	// Reference: Transactor.cpp:53-75.
	if common.NetworkID != nil {
		if e.config.NetworkID <= txcore.LegacyNetworkIDThreshold {
			return ter.TelNETWORK_ID_MAKES_TX_NON_CANONICAL
		}
		if *common.NetworkID != e.config.NetworkID {
			return ter.TelWRONG_NETWORK
		}
	}

	// Account must decode to AccountID(0). Rippled reads it as
	// getAccountID(sfAccount), which returns beast::zero both when sfAccount is
	// present-zero AND when it decodes to the default (zero) account. sfAccount is
	// a required common field, so an on-ledger UNL_MODIFY pseudo-tx that never
	// assigns it still carries a present, default-valued sfAccount; a default
	// AccountID serializes as a zero-length blob, which goXRPL decodes to an empty
	// Account. Go pseudo-tx constructors stamp protocol.ZeroAccount, so accept
	// either the empty (default → zero) form or the canonical zero address; any
	// other (non-zero) account is rejected, mirroring rippled's
	// `account != beast::zero` check.
	// Reference: Change.cpp:43-48 (getAccountID(sfAccount) defaults to zero).
	if common.Account != "" && common.Account != protocol.ZeroAccount {
		return ter.TemBAD_SRC_ACCOUNT
	}

	// Fee must decode to zero drops. Compare against the typed value rather
	// than a literal "0" so encodings like "00" or an absent Fee field map
	// to the same beast::zero check rippled performs after parse.
	// Reference: Change.cpp:50-56.
	if !isZeroFee(common.Fee) {
		return ter.TemBAD_FEE
	}

	// No signing fields are permitted.
	// Reference: Change.cpp:58-63.
	if common.SigningPubKey != "" || common.TxnSignature != "" || len(common.Signers) > 0 {
		return ter.TemBAD_SIGNATURE
	}

	// Sequence must be zero. Rippled also rejects sfPreviousTxnID here, but
	// go-xrpl's Common has no such field. sfTicketSequence is NOT consulted in
	// Change::preflight — rippled rejects it at the structural tx-format layer
	// (Transactor::preflight1), so we do not gate on it here either.
	// Reference: Change.cpp:65-69.
	if common.Sequence != nil && *common.Sequence != 0 {
		return ter.TemBAD_SEQUENCE
	}

	// NegativeUNL amendment must be enabled for UNL_MODIFY pseudo-tx.
	// Reference: Change.cpp:72-77.
	if tx.TxType() == txcore.TypeUNLModify && (rules == nil || !rules.NegativeUNLEnabled()) {
		return ter.TemDISABLED
	}

	return ter.TesSUCCESS
}

// pseudoPreclaim enforces rippled Change::preclaim: pseudo-transactions are
// only valid against a closed ledger, plus the per-type field-set gates each
// pseudo type registers via PseudoPreclaim. The default branch returns
// temUNKNOWN to mirror rippled's Change.cpp:137-139 — a new pseudo type added
// without a PreclaimPseudo implementation must fail loudly, not silently.
// Reference: Change.cpp:82-140.
func (e *Engine) pseudoPreclaim(tx txcore.Transaction, rules *amendment.Rules) ter.Result {
	if e.config.OpenLedger {
		return ter.TemINVALID
	}
	switch tx.TxType() {
	case txcore.TypeAmendment, txcore.TypeUNLModify:
		if p, ok := tx.(PseudoPreclaim); ok {
			return p.PreclaimPseudo(rules)
		}
		return ter.TesSUCCESS
	case txcore.TypeFee:
		p, ok := tx.(PseudoPreclaim)
		if !ok {
			return ter.TemUNKNOWN
		}
		return p.PreclaimPseudo(rules)
	default:
		return ter.TemUNKNOWN
	}
}

// isZeroFee reports whether a Common.Fee decimal-drops string decodes to zero.
// Empty, an explicit "0", any number of leading zeros, and surrounding ASCII
// whitespace all count. Non-decimal input (signs, "0x" prefixes, etc.) is
// rejected by returning false, which surfaces upstream as temBAD_FEE.
func isZeroFee(s string) bool {
	lo, hi := 0, len(s)
	for lo < hi && (s[lo] == ' ' || s[lo] == '\t') {
		lo++
	}
	for hi > lo && (s[hi-1] == ' ' || s[hi-1] == '\t') {
		hi--
	}
	if lo == hi {
		return true
	}
	for i := lo; i < hi; i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

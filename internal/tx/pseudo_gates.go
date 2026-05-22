package tx

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/protocol"
)

// PseudoPreclaim is implemented by pseudo-transaction types that need
// rule-aware preclaim gating beyond the generic open-ledger check.
// Reference: rippled Change::preclaim per-tx-type switch (Change.cpp:93-139).
type PseudoPreclaim interface {
	PreclaimPseudo(rules *amendment.Rules) Result
}

// pseudoPreflight enforces the gates that rippled's Change::preflight runs on
// every pseudo-transaction type before dispatching to type-specific logic.
// It also mirrors the two preflight0 guards that fire for pseudo-tx —
// tfInnerBatchTxn rejection (Transactor.cpp:46-51) and the NetworkID check
// when sfNetworkID is present (Transactor.cpp:53-75).
// Reference: rippled Change.cpp:36-80, Transactor.cpp:42-87.
func (e *Engine) pseudoPreflight(tx Transaction, rules *amendment.Rules) Result {
	common := tx.GetCommon()

	// preflight0 inner-batch gate: a pseudo-tx with the tfInnerBatchTxn flag
	// is structurally invalid because only the batch executor sets that flag.
	// Reference: Transactor.cpp:46-51.
	if common.Flags != nil && *common.Flags&TfInnerBatchTxn != 0 {
		return TemINVALID_FLAG
	}

	// preflight0 NetworkID gate: rippled checks NetworkID for non-pseudo
	// always, and for pseudo only when sfNetworkID is present. We are on the
	// pseudo path, so the check fires only when the field is set; an absent
	// NetworkID on a pseudo-tx is always legal.
	// Reference: Transactor.cpp:53-75.
	if common.NetworkID != nil {
		if e.config.NetworkID <= LegacyNetworkIDThreshold {
			return TelNETWORK_ID_MAKES_TX_NON_CANONICAL
		}
		if *common.NetworkID != e.config.NetworkID {
			return TelWRONG_NETWORK
		}
	}

	// Account must be the canonical zero address. Rippled relies on the
	// tx-format requiring sfAccount to be present and getAccountID(sfAccount)
	// to decode to AccountID(0). The Go pseudo-tx constructors stamp
	// protocol.ZeroAccount; anything else here is a caller bug.
	// Reference: Change.cpp:43-48.
	if common.Account != protocol.ZeroAccount {
		return TemBAD_SRC_ACCOUNT
	}

	// Fee must decode to zero drops. Compare against the typed value rather
	// than a literal "0" so encodings like "00" or an absent Fee field map
	// to the same beast::zero check rippled performs after parse.
	// Reference: Change.cpp:50-56.
	if !isZeroFee(common.Fee) {
		return TemBAD_FEE
	}

	// No signing fields are permitted.
	// Reference: Change.cpp:58-63.
	if common.SigningPubKey != "" || common.TxnSignature != "" || len(common.Signers) > 0 {
		return TemBAD_SIGNATURE
	}

	// Sequence must be zero. Rippled also rejects sfPreviousTxnID here, but
	// goXRPL's Common has no such field. sfTicketSequence is NOT consulted in
	// Change::preflight — rippled rejects it at the structural tx-format layer
	// (Transactor::preflight1), so we do not gate on it here either.
	// Reference: Change.cpp:65-69.
	if common.Sequence != nil && *common.Sequence != 0 {
		return TemBAD_SEQUENCE
	}

	// NegativeUNL amendment must be enabled for UNL_MODIFY pseudo-tx.
	// Reference: Change.cpp:72-77.
	if tx.TxType() == TypeUNLModify && (rules == nil || !rules.NegativeUNLEnabled()) {
		return TemDISABLED
	}

	return TesSUCCESS
}

// pseudoPreclaim enforces rippled Change::preclaim: pseudo-transactions are
// only valid against a closed ledger, plus the per-type field-set gates each
// pseudo type registers via PseudoPreclaim. The default branch returns
// temUNKNOWN to mirror rippled's Change.cpp:137-139 — a new pseudo type added
// without a PreclaimPseudo implementation must fail loudly, not silently.
// Reference: Change.cpp:82-140.
func (e *Engine) pseudoPreclaim(tx Transaction, rules *amendment.Rules) Result {
	if e.config.OpenLedger {
		return TemINVALID
	}
	switch tx.TxType() {
	case TypeAmendment, TypeUNLModify:
		if p, ok := tx.(PseudoPreclaim); ok {
			return p.PreclaimPseudo(rules)
		}
		return TesSUCCESS
	case TypeFee:
		p, ok := tx.(PseudoPreclaim)
		if !ok {
			return TemUNKNOWN
		}
		return p.PreclaimPseudo(rules)
	default:
		return TemUNKNOWN
	}
}

// isZeroFee reports whether a Common.Fee decimal-drops string decodes to zero.
// Empty, an explicit "0", any number of leading zeros, and surrounding ASCII
// whitespace all count. Non-decimal input (signs, "0x" prefixes, etc.) is
// rejected by returning false, which surfaces upstream as temBAD_FEE.
func isZeroFee(s string) bool {
	// Strip ASCII whitespace.
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

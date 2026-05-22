package tx

import "github.com/LeJamon/goXRPLd/amendment"

// zeroAccountAddress is the base58-encoded XRPL account with all 20 ID bytes set
// to zero — the only Account value accepted on a pseudo-transaction.
// Reference: rippled beast::zero compare in Change::preflight.
const zeroAccountAddress = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// PseudoPreclaim is implemented by pseudo-transaction types that need
// rule-aware preclaim gating beyond the generic open-ledger check.
// Reference: rippled Change::preclaim per-tx-type switch (Change.cpp:93-139).
type PseudoPreclaim interface {
	PreclaimPseudo(rules *amendment.Rules) Result
}

// pseudoPreflight enforces the gates that rippled's Change::preflight runs on
// every pseudo-transaction type before dispatching to type-specific logic.
// Reference: rippled Change.cpp:36-80 (preflight + preflight0).
func pseudoPreflight(tx Transaction, rules *amendment.Rules) Result {
	common := tx.GetCommon()

	// Account must be zero. Both empty (Go-default) and the canonical
	// zero-address spelling are accepted; any other value is rejected.
	// Reference: Change.cpp:43-48.
	if common.Account != "" && common.Account != zeroAccountAddress {
		return TemBAD_SRC_ACCOUNT
	}

	// Fee must be zero drops. The Go form is a decimal-drops string, so both
	// the empty string and "0" map to rippled's beast::zero check.
	// Reference: Change.cpp:50-56.
	if common.Fee != "" && common.Fee != "0" {
		return TemBAD_FEE
	}

	// No signing fields are permitted.
	// Reference: Change.cpp:58-63.
	if common.SigningPubKey != "" || common.TxnSignature != "" || len(common.Signers) > 0 {
		return TemBAD_SIGNATURE
	}

	// Sequence must be zero and no PreviousTxnID / TicketSequence may be
	// present (Common has no PreviousTxnID, but TicketSequence is the
	// goXRPL equivalent guard against sequence-replacing fields).
	// Reference: Change.cpp:65-69.
	if common.Sequence != nil && *common.Sequence != 0 {
		return TemBAD_SEQUENCE
	}
	if common.TicketSequence != nil {
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
// only valid against a closed ledger, plus any per-type gating registered via
// the PseudoPreclaim interface (e.g. SetFee's XRPFees field-set check).
// Reference: Change.cpp:82-140.
func (e *Engine) pseudoPreclaim(tx Transaction, rules *amendment.Rules) Result {
	if e.config.OpenLedger {
		return TemINVALID
	}
	if p, ok := tx.(PseudoPreclaim); ok {
		return p.PreclaimPseudo(rules)
	}
	return TesSUCCESS
}

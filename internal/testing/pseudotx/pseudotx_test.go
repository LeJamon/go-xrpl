// Package pseudotx_test tests pseudo-transaction handling.
// Reference: rippled/src/test/app/PseudoTx_test.cpp
package pseudotx_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/stretchr/testify/require"
)

// TestPseudoTx_IsPseudoTransaction verifies that the type system correctly
// identifies pseudo-transaction types (Amendment, SetFee, UNLModify) vs
// real transaction types (AccountSet, Payment, etc.).
// Reference: rippled PseudoTx_test.cpp testAllowed() + testPrevented()
func TestPseudoTx_IsPseudoTransaction(t *testing.T) {
	// Pseudo-transaction types
	t.Run("Amendment is pseudo", func(t *testing.T) {
		require.True(t, tx.TypeAmendment.IsPseudoTransaction())
	})

	t.Run("SetFee is pseudo", func(t *testing.T) {
		require.True(t, tx.TypeFee.IsPseudoTransaction())
	})

	t.Run("UNLModify is pseudo", func(t *testing.T) {
		require.True(t, tx.TypeUNLModify.IsPseudoTransaction())
	})

	// Real transaction types should NOT be pseudo
	t.Run("AccountSet is not pseudo", func(t *testing.T) {
		require.False(t, tx.TypeAccountSet.IsPseudoTransaction())
	})

	t.Run("Payment is not pseudo", func(t *testing.T) {
		require.False(t, tx.TypePayment.IsPseudoTransaction())
	})

	t.Run("OfferCreate is not pseudo", func(t *testing.T) {
		require.False(t, tx.TypeOfferCreate.IsPseudoTransaction())
	})

	t.Run("TrustSet is not pseudo", func(t *testing.T) {
		require.False(t, tx.TypeTrustSet.IsPseudoTransaction())
	})
}

// TestPseudoTx_Prevented tests that pseudo-transactions cannot be submitted
// through the normal transaction submission path.
// Reference: rippled PseudoTx_test.cpp testPrevented()
//
// In rippled, passesLocalChecks() rejects pseudo-transactions before they reach
// the engine. In the Go engine, pseudo-transactions are routed through
// applyPseudoTransaction() which applies them without normal preflight/preclaim.
// The rejection should happen at the RPC/submission layer, not the engine.
func TestPseudoTx_Prevented(t *testing.T) {
	t.Skip("Pseudo-transaction rejection requires RPC submission layer local checks (not implemented)")
}

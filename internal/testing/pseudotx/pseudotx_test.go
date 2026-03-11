// Package pseudotx_test tests pseudo-transaction handling.
// Reference: rippled/src/test/app/PseudoTx_test.cpp
package pseudotx_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/stretchr/testify/require"

	_ "github.com/LeJamon/goXRPLd/internal/tx/all"
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

// makePseudoAmendmentHash creates a deterministic 64-char hex hash for testing.
func makePseudoAmendmentHash(id byte) string {
	var hash [32]byte
	hash[0] = id
	return hex.EncodeToString(hash[:])
}

// TestPseudoTx_Prevented tests that pseudo-transactions cannot be submitted
// through the normal transaction submission path.
// Reference: rippled PseudoTx_test.cpp testPrevented()
//
// In rippled, passesLocalChecks() rejects pseudo-transactions before they reach
// the engine. In the Go engine, Apply() rejects pseudo-transactions with temINVALID.
// Pseudo-transactions can only be applied via ApplyPseudo() (used by block processor
// and consensus code).
func TestPseudoTx_Prevented(t *testing.T) {
	env := jtx.NewTestEnv(t)

	// Create an engine with the current ledger
	engineConfig := tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		LedgerSequence:            env.LedgerSeq(),
		SkipSignatureVerification: true,
	}
	engine := tx.NewEngine(env.Ledger(), engineConfig)

	// Test that each pseudo-transaction type is rejected by Apply()
	// Reference: rippled PseudoTx_test.cpp line 97:
	//   BEAST_EXPECT(!result.applied && result.ter == temINVALID);
	t.Run("EnableAmendment rejected", func(t *testing.T) {
		amendTx := &pseudo.EnableAmendment{
			BaseTx: *tx.NewBaseTx(tx.TypeAmendment, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"),
		}
		amendTx.Amendment = makePseudoAmendmentHash(1)
		amendTx.Common.Fee = "0"
		amendTx.Common.SigningPubKey = ""
		seq := uint32(0)
		amendTx.Common.Sequence = &seq

		result := engine.Apply(amendTx)
		require.False(t, result.Applied, "Pseudo-transaction should not be applied")
		require.Equal(t, "temINVALID", result.Result.String(),
			"Pseudo-transaction should be rejected with temINVALID")
	})

	t.Run("SetFee rejected", func(t *testing.T) {
		feeTx := pseudo.NewSetFee()
		feeTx.BaseFee = "A"
		feeTx.Common.Fee = "0"
		feeTx.Common.SigningPubKey = ""
		seq := uint32(0)
		feeTx.Common.Sequence = &seq
		reserveBase := uint32(200_000_000)
		reserveInc := uint32(50_000_000)
		refUnits := uint32(10)
		feeTx.ReserveBase = &reserveBase
		feeTx.ReserveIncrement = &reserveInc
		feeTx.ReferenceFeeUnits = &refUnits

		result := engine.Apply(feeTx)
		require.False(t, result.Applied, "Pseudo-transaction should not be applied")
		require.Equal(t, "temINVALID", result.Result.String(),
			"Pseudo-transaction should be rejected with temINVALID")
	})

	t.Run("UNLModify rejected", func(t *testing.T) {
		unlTx := &pseudo.UNLModify{
			BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"),
		}
		unlTx.Common.Fee = "0"
		unlTx.Common.SigningPubKey = ""
		seq := uint32(0)
		unlTx.Common.Sequence = &seq

		result := engine.Apply(unlTx)
		require.False(t, result.Applied, "Pseudo-transaction should not be applied")
		require.Equal(t, "temINVALID", result.Result.String(),
			"Pseudo-transaction should be rejected with temINVALID")
	})

	// Verify that ApplyPseudo() DOES accept pseudo-transactions
	// (this is how pseudo-txs are applied during consensus/block processing)
	t.Run("EnableAmendment allowed via ApplyPseudo", func(t *testing.T) {
		amendTx := &pseudo.EnableAmendment{
			BaseTx: *tx.NewBaseTx(tx.TypeAmendment, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"),
		}
		amendTx.Amendment = makePseudoAmendmentHash(2)
		amendTx.Common.Fee = "0"
		amendTx.Common.SigningPubKey = ""
		seq := uint32(0)
		amendTx.Common.Sequence = &seq

		result := engine.ApplyPseudo(amendTx)
		// ApplyPseudo should succeed (or at least not return temINVALID)
		require.NotEqual(t, "temINVALID", result.Result.String(),
			"ApplyPseudo should not reject pseudo-transactions")
	})
}

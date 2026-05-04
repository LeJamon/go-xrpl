// Package pseudotx_test tests pseudo-transaction handling.
// Reference: rippled/src/xrpld/app/tx/detail/Change.cpp applyAmendment()
package pseudotx_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"
)

// makeAmendmentHash returns the uppercase hex hash for a known amendment name.
func makeAmendmentHash(name string) string {
	feat := amendment.GetFeatureByName(name)
	if feat != nil {
		return strings.ToUpper(hex.EncodeToString(feat.ID[:]))
	}
	// Fallback: create a simple hash from the name for tests
	var hash [32]byte
	copy(hash[:], []byte(name))
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

// newEnableAmendment creates an EnableAmendment pseudo-tx with the given hash and flags.
// Pseudo-transactions have zero account, zero fee, zero sequence, and no signatures.
func newEnableAmendment(amendmentHash string, flags uint32) *pseudo.EnableAmendment {
	ea := &pseudo.EnableAmendment{
		BaseTx: *tx.NewBaseTx(tx.TypeAmendment, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"), // zero account in base58
	}
	ea.Amendment = amendmentHash
	ea.Common.Fee = "0"
	ea.Common.SigningPubKey = ""
	// Set flags on Common — pseudo-txs use flags for gotMajority/lostMajority
	ea.Common.Flags = &flags
	// Pseudo-tx: zero sequence
	seq := uint32(0)
	ea.Common.Sequence = &seq
	return ea
}

// TestEnableAmendment_Enable tests that an amendment with no flags gets enabled.
// Reference: rippled Change.cpp applyAmendment() lines 318-335
func TestEnableAmendment_Enable(t *testing.T) {
	env := jtx.NewTestEnv(t)

	// Use a real amendment hash
	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")

	// Enable the amendment (no flags = activation)
	ea := newEnableAmendment(amendmentHash, 0)
	result := env.SubmitPseudo(ea)
	jtx.RequireTxSuccess(t, result)

	// Verify the Amendments SLE was created with the amendment
	amendmentsKey := keylet.Amendments()
	data, err := env.Ledger().Read(amendmentsKey)
	require.NoError(t, err)
	require.NotNil(t, data, "Amendments SLE should exist after enabling an amendment")

	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Amendments, 1, "Should have exactly 1 enabled amendment")

	var expectedHash [32]byte
	b, _ := hex.DecodeString(amendmentHash)
	copy(expectedHash[:], b)
	require.Equal(t, expectedHash, sle.Amendments[0])
}

// TestEnableAmendment_EnableMultiple tests enabling multiple amendments sequentially.
func TestEnableAmendment_EnableMultiple(t *testing.T) {
	env := jtx.NewTestEnv(t)

	hash1 := makeAmendmentHash("fixNFTokenPageLinks")
	hash2 := makeAmendmentHash("AMM")

	// Enable first amendment
	result := env.SubmitPseudo(newEnableAmendment(hash1, 0))
	jtx.RequireTxSuccess(t, result)

	// Enable second amendment
	result = env.SubmitPseudo(newEnableAmendment(hash2, 0))
	jtx.RequireTxSuccess(t, result)

	// Verify both are enabled
	data, err := env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Amendments, 2)
}

// TestEnableAmendment_AlreadyEnabled tests that enabling an already-enabled amendment
// returns tefALREADY.
// Reference: rippled Change.cpp applyAmendment() lines 265-267
func TestEnableAmendment_AlreadyEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")

	// Enable the amendment
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, 0))
	jtx.RequireTxSuccess(t, result)

	// Try to enable again → tefALREADY
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, 0))
	jtx.RequireTxFail(t, result, "tefALREADY")
}

// TestEnableAmendment_GotMajority tests that tfGotMajority adds to the majorities tracking.
// Reference: rippled Change.cpp applyAmendment() lines 303-317
func TestEnableAmendment_GotMajority(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")

	// GotMajority flag
	const tfGotMajority uint32 = 0x00010000
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Verify the Amendments SLE has a majority entry
	data, err := env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)

	// Should NOT be in the enabled list yet
	require.Len(t, sle.Amendments, 0, "Amendment should not be enabled yet")

	// Should be in the majorities list
	require.Len(t, sle.Majorities, 1, "Should have 1 majority entry")

	var expectedHash [32]byte
	b, _ := hex.DecodeString(amendmentHash)
	copy(expectedHash[:], b)
	require.Equal(t, expectedHash, sle.Majorities[0].Amendment)
	require.Greater(t, sle.Majorities[0].CloseTime, uint32(0), "CloseTime should be set")
}

// TestEnableAmendment_GotMajorityAlready tests that gotMajority on an amendment
// already in the majorities list returns tefALREADY.
// Reference: rippled Change.cpp applyAmendment() lines 288-289
func TestEnableAmendment_GotMajorityAlready(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfGotMajority uint32 = 0x00010000

	// First gotMajority → success
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Second gotMajority → tefALREADY
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxFail(t, result, "tefALREADY")
}

// TestEnableAmendment_LostMajority tests that tfLostMajority removes from the majorities list.
// Reference: rippled Change.cpp applyAmendment() lines 300-301, filtering logic
func TestEnableAmendment_LostMajority(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfGotMajority uint32 = 0x00010000
	const tfLostMajority uint32 = 0x00020000

	// First, add to majorities
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Then lose majority
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfLostMajority))
	jtx.RequireTxSuccess(t, result)

	// Verify the majority entry is gone
	data, err := env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Majorities, 0, "Majorities should be empty after lostMajority")
	require.Len(t, sle.Amendments, 0, "Amendment should not be enabled")
}

// TestEnableAmendment_LostMajorityNotInList tests that lostMajority on an amendment
// NOT in the majorities list returns tefALREADY.
// Reference: rippled Change.cpp applyAmendment() lines 300-301
func TestEnableAmendment_LostMajorityNotInList(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfLostMajority uint32 = 0x00020000

	// LostMajority on a non-existent majority → tefALREADY
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfLostMajority))
	jtx.RequireTxFail(t, result, "tefALREADY")
}

// TestEnableAmendment_BothFlags tests that setting both gotMajority and lostMajority
// returns temINVALID_FLAG.
// Reference: rippled Change.cpp applyAmendment() lines 274-275
func TestEnableAmendment_BothFlags(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfGotMajority uint32 = 0x00010000
	const tfLostMajority uint32 = 0x00020000

	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority|tfLostMajority))
	jtx.RequireTxFail(t, result, "temINVALID_FLAG")
}

// TestEnableAmendment_GotMajorityOnEnabled tests that gotMajority on an already-enabled
// amendment returns tefALREADY.
// Reference: rippled Change.cpp applyAmendment() lines 265-267
func TestEnableAmendment_GotMajorityOnEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfGotMajority uint32 = 0x00010000

	// Enable the amendment first
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, 0))
	jtx.RequireTxSuccess(t, result)

	// GotMajority on already-enabled → tefALREADY
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxFail(t, result, "tefALREADY")
}

// TestEnableAmendment_LostMajorityOnEnabled tests that lostMajority on an already-enabled
// amendment returns tefALREADY.
// Reference: rippled Change.cpp applyAmendment() lines 265-267
func TestEnableAmendment_LostMajorityOnEnabled(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfLostMajority uint32 = 0x00020000

	// Enable the amendment first
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, 0))
	jtx.RequireTxSuccess(t, result)

	// LostMajority on already-enabled → tefALREADY
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfLostMajority))
	jtx.RequireTxFail(t, result, "tefALREADY")
}

// TestEnableAmendment_FullLifecycle tests the complete amendment lifecycle:
// gotMajority → lostMajority → gotMajority → enable
func TestEnableAmendment_FullLifecycle(t *testing.T) {
	env := jtx.NewTestEnv(t)

	amendmentHash := makeAmendmentHash("fixNFTokenPageLinks")
	const tfGotMajority uint32 = 0x00010000
	const tfLostMajority uint32 = 0x00020000

	// Step 1: Got majority
	result := env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Step 2: Lost majority
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfLostMajority))
	jtx.RequireTxSuccess(t, result)

	// Step 3: Got majority again
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Step 4: Enable (no flags)
	result = env.SubmitPseudo(newEnableAmendment(amendmentHash, 0))
	jtx.RequireTxSuccess(t, result)

	// Verify final state
	data, err := env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Amendments, 1, "Amendment should be enabled")
	require.Len(t, sle.Majorities, 0, "No majorities should remain after activation")
}

// TestEnableAmendment_MajoritiesPassThrough tests that lostMajority only removes
// the targeted amendment and leaves others intact.
// Reference: rippled Change.cpp applyAmendment() lines 282-297
func TestEnableAmendment_MajoritiesPassThrough(t *testing.T) {
	env := jtx.NewTestEnv(t)

	hash1 := makeAmendmentHash("fixNFTokenPageLinks")
	hash2 := makeAmendmentHash("AMM")
	const tfGotMajority uint32 = 0x00010000
	const tfLostMajority uint32 = 0x00020000

	// Add two amendments to majorities
	result := env.SubmitPseudo(newEnableAmendment(hash1, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	result = env.SubmitPseudo(newEnableAmendment(hash2, tfGotMajority))
	jtx.RequireTxSuccess(t, result)

	// Verify both are in majorities
	data, err := env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err := pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Majorities, 2)

	// Lose majority for hash1 only
	result = env.SubmitPseudo(newEnableAmendment(hash1, tfLostMajority))
	jtx.RequireTxSuccess(t, result)

	// Verify hash2 remains
	data, err = env.Ledger().Read(keylet.Amendments())
	require.NoError(t, err)
	sle, err = pseudo.ParseAmendmentsSLE(data)
	require.NoError(t, err)
	require.Len(t, sle.Majorities, 1, "Only hash2 should remain")

	var expectedHash2 [32]byte
	b, _ := hex.DecodeString(hash2)
	copy(expectedHash2[:], b)
	require.Equal(t, expectedHash2, sle.Majorities[0].Amendment)
}

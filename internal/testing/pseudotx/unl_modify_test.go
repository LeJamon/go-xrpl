// Package pseudotx_test tests UNLModify pseudo-transaction handling.
// Reference: rippled/src/xrpld/app/tx/detail/Change.cpp applyUNLModify()
package pseudotx_test

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"

	_ "github.com/LeJamon/goXRPLd/internal/tx/all"
)

// Fake ED25519 validator public key (33 bytes: 0xED prefix + 32 bytes)
var (
	validatorKey1 = makeValidatorKey(1)
	validatorKey2 = makeValidatorKey(2)
)

func makeValidatorKey(id byte) string {
	key := make([]byte, 33)
	key[0] = 0xED // ED25519 prefix
	key[32] = id
	return hex.EncodeToString(key)
}

// newUNLModify creates a UNLModify pseudo-tx.
// disabling: 1 to disable a validator, 0 to re-enable.
// seq: must match the current ledger sequence.
// validatorKey: hex-encoded public key.
func newUNLModify(disabling uint8, seq uint32, validatorKey string) *pseudo.UNLModify {
	u := &pseudo.UNLModify{
		BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"),
	}
	u.Common.Fee = "0"
	u.Common.SigningPubKey = ""
	s := uint32(0)
	u.Common.Sequence = &s
	u.UNLModifyDisabling = &disabling
	u.LedgerSequence = &seq
	u.UNLModifyValidator = validatorKey
	return u
}

// TestUNLModify_NotFlagLedger tests that UNLModify fails when not on a flag ledger.
// Reference: rippled Change.cpp applyUNLModify() lines 390-396
func TestUNLModify_NotFlagLedger(t *testing.T) {
	env := jtx.NewTestEnv(t)

	// Default ledger sequence is typically 2 or 3 (not a flag ledger = multiple of 256)
	seq := env.Ledger().Sequence()
	require.NotEqual(t, uint32(0), seq%256, "Test setup: ledger should not be on a flag ledger")

	u := newUNLModify(1, seq, validatorKey1)
	result := env.SubmitPseudo(u)
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// TestUNLModify_MissingFields tests that UNLModify fails when required fields are missing.
// Reference: rippled Change.cpp applyUNLModify() lines 397-404
func TestUNLModify_MissingFields(t *testing.T) {
	env := jtx.NewTestEnv(t)

	// Advance to a flag ledger (seq % 256 == 0)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	t.Run("missing UNLModifyDisabling", func(t *testing.T) {
		u := &pseudo.UNLModify{
			BaseTx: *tx.NewBaseTx(tx.TypeUNLModify, "rrrrrrrrrrrrrrrrrrrrrhoLvTp"),
		}
		u.Common.Fee = "0"
		u.Common.SigningPubKey = ""
		s := uint32(0)
		u.Common.Sequence = &s
		// UNLModifyDisabling is nil
		u.LedgerSequence = &seq
		u.UNLModifyValidator = validatorKey1
		result := env.SubmitPseudo(u)
		jtx.RequireTxFail(t, result, "tefFAILURE")
	})

	t.Run("UNLModifyDisabling > 1", func(t *testing.T) {
		badVal := uint8(2)
		u := newUNLModify(badVal, seq, validatorKey1)
		u.UNLModifyDisabling = &badVal
		result := env.SubmitPseudo(u)
		jtx.RequireTxFail(t, result, "tefFAILURE")
	})

	t.Run("wrong LedgerSequence", func(t *testing.T) {
		u := newUNLModify(1, seq+1, validatorKey1)
		result := env.SubmitPseudo(u)
		jtx.RequireTxFail(t, result, "tefFAILURE")
	})

	t.Run("missing UNLModifyValidator", func(t *testing.T) {
		u := newUNLModify(1, seq, "")
		result := env.SubmitPseudo(u)
		jtx.RequireTxFail(t, result, "tefFAILURE")
	})
}

// TestUNLModify_DisableValidator tests successful validator disabling.
// Reference: rippled Change.cpp applyUNLModify() lines 449-478
func TestUNLModify_DisableValidator(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	u := newUNLModify(1, seq, validatorKey1)
	result := env.SubmitPseudo(u)
	jtx.RequireTxSuccess(t, result)

	// Verify NegativeUNL SLE was created with ValidatorToDisable
	data, err := env.Ledger().Read(keylet.NegativeUNL())
	require.NoError(t, err)
	require.NotNil(t, data)

	sle, err := pseudo.ParseNegativeUNLSLE(data)
	require.NoError(t, err)

	expectedKey, _ := hex.DecodeString(validatorKey1)
	require.Equal(t, expectedKey, sle.ValidatorToDisable)
}

// TestUNLModify_ReEnableValidator tests successful validator re-enabling.
// Reference: rippled Change.cpp applyUNLModify() lines 479-508
func TestUNLModify_ReEnableValidator(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// First, we need the validator to be in the disabled list.
	// Seed the NegativeUNL SLE directly with a disabled validator.
	seedDisabledValidator(t, env, validatorKey1)

	u := newUNLModify(0, seq, validatorKey1)
	result := env.SubmitPseudo(u)
	jtx.RequireTxSuccess(t, result)

	// Verify ValidatorToReEnable was set
	data, err := env.Ledger().Read(keylet.NegativeUNL())
	require.NoError(t, err)

	sle, err := pseudo.ParseNegativeUNLSLE(data)
	require.NoError(t, err)

	expectedKey, _ := hex.DecodeString(validatorKey1)
	require.Equal(t, expectedKey, sle.ValidatorToReEnable)
}

// TestUNLModify_AlreadyHasToDisable tests that a second disable request fails.
// Reference: rippled Change.cpp applyUNLModify() lines 452-456
func TestUNLModify_AlreadyHasToDisable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// First disable succeeds
	result := env.SubmitPseudo(newUNLModify(1, seq, validatorKey1))
	jtx.RequireTxSuccess(t, result)

	// Second disable fails (already has ValidatorToDisable)
	result = env.SubmitPseudo(newUNLModify(1, seq, validatorKey2))
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// TestUNLModify_DisableSameAsReEnable tests that trying to disable a validator
// that is scheduled for re-enable fails.
// Reference: rippled Change.cpp applyUNLModify() lines 459-466
func TestUNLModify_DisableSameAsReEnable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// Seed the disabled list and set up re-enable
	seedDisabledValidator(t, env, validatorKey1)
	result := env.SubmitPseudo(newUNLModify(0, seq, validatorKey1))
	jtx.RequireTxSuccess(t, result)

	// Try to disable the same validator → conflict
	result = env.SubmitPseudo(newUNLModify(1, seq, validatorKey1))
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// TestUNLModify_DisableAlreadyInList tests that disabling a validator already
// in the disabled list fails.
// Reference: rippled Change.cpp applyUNLModify() lines 470-475
func TestUNLModify_DisableAlreadyInList(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// Seed the disabled list
	seedDisabledValidator(t, env, validatorKey1)

	// Try to disable again → already in list
	result := env.SubmitPseudo(newUNLModify(1, seq, validatorKey1))
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// TestUNLModify_ReEnableNotInList tests that re-enabling a validator NOT in
// the disabled list fails.
// Reference: rippled Change.cpp applyUNLModify() lines 499-505
func TestUNLModify_ReEnableNotInList(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// Re-enable without being in the disabled list → fail
	result := env.SubmitPseudo(newUNLModify(0, seq, validatorKey1))
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// TestUNLModify_AlreadyHasToReEnable tests that a second re-enable request fails.
// Reference: rippled Change.cpp applyUNLModify() lines 482-486
func TestUNLModify_AlreadyHasToReEnable(t *testing.T) {
	env := jtx.NewTestEnv(t)
	advanceToFlagLedger(t, env)
	seq := env.Ledger().Sequence()

	// Seed two validators in the disabled list
	seedDisabledValidator(t, env, validatorKey1)
	seedDisabledValidator(t, env, validatorKey2)

	// First re-enable succeeds
	result := env.SubmitPseudo(newUNLModify(0, seq, validatorKey1))
	jtx.RequireTxSuccess(t, result)

	// Second re-enable fails (already has ValidatorToReEnable)
	result = env.SubmitPseudo(newUNLModify(0, seq, validatorKey2))
	jtx.RequireTxFail(t, result, "tefFAILURE")
}

// advanceToFlagLedger advances the test environment to a flag ledger (seq % 256 == 0).
func advanceToFlagLedger(t *testing.T, env *jtx.TestEnv) {
	t.Helper()
	for env.Ledger().Sequence()%256 != 0 {
		env.Close()
	}
}

// seedDisabledValidator inserts a validator into the NegativeUNL's DisabledValidators
// list by directly inserting/updating the SLE.
func seedDisabledValidator(t *testing.T, env *jtx.TestEnv, validatorKeyHex string) {
	t.Helper()

	k := keylet.NegativeUNL()
	validatorKey, err := hex.DecodeString(validatorKeyHex)
	require.NoError(t, err)

	var sle *pseudo.NegativeUNLSLE

	data, readErr := env.Ledger().Read(k)
	if readErr != nil || data == nil {
		sle = &pseudo.NegativeUNLSLE{}
	} else {
		sle, err = pseudo.ParseNegativeUNLSLE(data)
		require.NoError(t, err)
	}

	// Add the validator to the disabled list
	sle.DisabledValidators = append(sle.DisabledValidators, validatorKey)

	serialized, serErr := pseudo.SerializeNegativeUNLSLE(sle)
	require.NoError(t, serErr)

	if data == nil || readErr != nil {
		err = env.Ledger().Insert(k, serialized)
	} else {
		err = env.Ledger().Update(k, serialized)
	}
	require.NoError(t, err)
}

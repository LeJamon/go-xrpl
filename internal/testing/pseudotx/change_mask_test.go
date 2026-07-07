package pseudotx_test

// tfChangeMask flag validation on pseudo-transactions. rippled's
// Change::preflight passes tfChangeMask to preflight0 only when
// LendingProtocol is enabled — before activation any flags are accepted, and
// after activation any flag outside {tfUniversal, tfGotMajority, tfLostMajority}
// is rejected with temINVALID_FLAG. Reference: Change.cpp:40-46.

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"

	"github.com/stretchr/testify/require"
)

// an arbitrary flag bit that is not universal and not a majority flag, so it
// lies inside tfChangeMask and must be rejected once LendingProtocol is on.
const tfInvalidChangeFlag uint32 = 0x00080000

// tfGotMajority is the one non-universal flag a Change tx may carry.
const tfGotMajorityFlag uint32 = 0x00010000

func lendingOnRules() *amendment.Rules {
	return amendment.NewRules([][32]byte{amendment.FeatureLendingProtocol})
}

func lendingOffRules() *amendment.Rules {
	return amendment.NewRules(nil)
}

// TestChangeMask_PreActivationAcceptsAnyFlag confirms that with LendingProtocol
// off, a pseudo-tx carrying an otherwise-invalid flag is NOT rejected for its
// flags (mask == 0), matching a rippled 3.0.0 node before activation.
func TestChangeMask_PreActivationAcceptsAnyFlag(t *testing.T) {
	engine, _ := closedEngine(t, lendingOffRules())
	txn := newAmendmentTx()
	txn.Common.Flags = ptrU32(tfInvalidChangeFlag)

	result := engine.ApplyPseudo(txn)
	require.NotEqual(t, "temINVALID_FLAG", result.Result.String(),
		"pre-activation must ignore Change flags, got %s", result.Result.String())
}

// TestChangeMask_ActivatedRejectsInvalidFlag confirms that once LendingProtocol
// is enabled, an out-of-mask flag is rejected with temINVALID_FLAG.
func TestChangeMask_ActivatedRejectsInvalidFlag(t *testing.T) {
	engine, _ := closedEngine(t, lendingOnRules())
	txn := newAmendmentTx()
	txn.Common.Flags = ptrU32(tfInvalidChangeFlag)

	result := engine.ApplyPseudo(txn)
	require.Equal(t, "temINVALID_FLAG", result.Result.String())
}

// TestChangeMask_ActivatedAllowsMajorityFlag confirms tfGotMajority is still
// permitted with LendingProtocol on (it is excluded from tfChangeMask).
func TestChangeMask_ActivatedAllowsMajorityFlag(t *testing.T) {
	engine, _ := closedEngine(t, lendingOnRules())
	txn := newAmendmentTx()
	txn.Common.Flags = ptrU32(tfGotMajorityFlag)

	result := engine.ApplyPseudo(txn)
	require.NotEqual(t, "temINVALID_FLAG", result.Result.String(),
		"tfGotMajority must be allowed under LendingProtocol, got %s", result.Result.String())
}

func ptrU32(v uint32) *uint32 { return &v }

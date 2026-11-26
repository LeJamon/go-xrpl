package tx

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// SetRegularKeyFeeWaived reports whether a SetRegularKey qualifies for the free
// password-change discount: it is signed with the account's master key while
// lsfPasswordSpent is clear. Apply sets lsfPasswordSpent from the resulting
// computed base fee, not from this predicate.
func SetRegularKeyFeeWaived(skipSigVerification bool, common *Common, account *state.AccountRoot) bool {
	if common == nil || account == nil {
		return false
	}
	if common.GetFlags()&TfInnerBatchTxn != 0 {
		return false
	}
	// One-shot discount: once lsfPasswordSpent is set the master key must pay
	// the full fee until a fee-paying transaction clears it.
	if account.Flags&state.LsfPasswordSpent != 0 {
		return false
	}
	return SignedWithMasterKey(skipSigVerification, common)
}

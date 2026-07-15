package mpt

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// mptokenIssuanceSetPermissionMask carves out the universal flags and the
// lock/unlock flags; any other flag bit forbids granular delegation.
const mptokenIssuanceSetPermissionMask = ^(tx.TfUniversal | MPTokenIssuanceSetFlagLock | MPTokenIssuanceSetFlagUnlock)

// CheckDelegatePermission authorizes a delegated MPTokenIssuanceSet carrying a
// granular MPTokenIssuanceLock or MPTokenIssuanceUnlock permission. A
// transaction-level MPTokenIssuanceSet grant is resolved by the engine before
// this runs.
func (m *MPTokenIssuanceSet) CheckDelegatePermission(pc tx.DelegatePermissionContext) ter.Result {
	txFlags := m.GetFlags()

	if txFlags&mptokenIssuanceSetPermissionMask != 0 {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	if txFlags&MPTokenIssuanceSetFlagLock != 0 && !pc.HasGranular(tx.GranularMPTokenIssuanceLock) {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	if txFlags&MPTokenIssuanceSetFlagUnlock != 0 && !pc.HasGranular(tx.GranularMPTokenIssuanceUnlock) {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	return ter.TesSUCCESS
}

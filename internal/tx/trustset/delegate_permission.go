package trustset

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// trustSetPermissionMask carves out the universal flags and the auth/freeze/
// unfreeze flags; any other flag bit forbids granular delegation.
const trustSetPermissionMask = ^(tx.TfUniversal | TrustSetFlagSetfAuth | TrustSetFlagSetFreeze | TrustSetFlagClearFreeze)

// CheckDelegatePermission authorizes a delegated TrustSet carrying a granular
// TrustlineAuthorize, TrustlineFreeze or TrustlineUnfreeze permission. A
// transaction-level TrustSet grant is resolved by the engine before this runs.
//
// Granular permissions may only toggle the auth/freeze flags on an existing
// trust line; they cannot set quality, change the trust limit, or create a
// line.
func (t *TrustSet) CheckDelegatePermission(pc tx.DelegatePermissionContext) ter.Result {
	txFlags := t.GetFlags()
	if txFlags&trustSetPermissionMask != 0 {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if t.QualityIn != nil || t.QualityOut != nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}

	accountID, err := state.DecodeAccountID(t.Account)
	if err != nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	issuerID, err := state.DecodeAccountID(t.LimitAmount.Issuer)
	if err != nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}

	lineData, readErr := pc.View.Read(keylet.Line(accountID, issuerID, t.LimitAmount.Currency))
	if readErr != nil || lineData == nil {
		// Granular permissions cannot create a trust line.
		return ter.TecNO_DELEGATE_PERMISSION
	}
	rs, parseErr := state.ParseRippleState(lineData)
	if parseErr != nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}

	if txFlags&TrustSetFlagSetfAuth != 0 && !pc.HasGranular(tx.GranularTrustlineAuthorize) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if txFlags&TrustSetFlagSetFreeze != 0 && !pc.HasGranular(tx.GranularTrustlineFreeze) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if txFlags&TrustSetFlagClearFreeze != 0 && !pc.HasGranular(tx.GranularTrustlineUnfreeze) {
		return ter.TecNO_DELEGATE_PERMISSION
	}

	// Granular permissions may not change the account's own trust limit.
	curLimit := rs.HighLimit
	if keylet.IsLowAccount(accountID, issuerID) {
		curLimit = rs.LowLimit
	}
	if curLimit.Compare(t.LimitAmount) != 0 {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	return ter.TesSUCCESS
}

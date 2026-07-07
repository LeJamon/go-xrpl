package account

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// CheckDelegatePermission authorizes a delegated AccountSet. AccountSet cannot
// be granted at the transaction level, so only the specific fields covered by a
// granular permission may be modified. Any flag change, or any field without a
// matching granular grant, is rejected.
func (a *AccountSet) CheckDelegatePermission(pc tx.DelegatePermissionContext) ter.Result {
	var setFlag, clearFlag uint32
	if a.SetFlag != nil {
		setFlag = *a.SetFlag
	}
	if a.ClearFlag != nil {
		clearFlag = *a.ClearFlag
	}
	// No flag-based granular permission exists for AccountSet.
	if setFlag != 0 || clearFlag != 0 || a.GetFlags()&tx.TfUniversalMask != 0 {
		return ter.TecNO_DELEGATE_PERMISSION
	}

	if a.present("EmailHash", a.EmailHash != "") && !pc.HasGranular(tx.GranularAccountEmailHashSet) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	// WalletLocator and NFTokenMinter have no granular permission.
	if a.present("WalletLocator", a.WalletLocator != "") || a.present("NFTokenMinter", a.NFTokenMinter != "") {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if a.MessageKey != nil && !pc.HasGranular(tx.GranularAccountMessageKeySet) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if a.Domain != nil && !pc.HasGranular(tx.GranularAccountDomainSet) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if a.TransferRate != nil && !pc.HasGranular(tx.GranularAccountTransferRateSet) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	if a.TickSize != nil && !pc.HasGranular(tx.GranularAccountTickSizeSet) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	return ter.TesSUCCESS
}

// present reports whether a plain-string field was supplied, covering both a
// non-empty value and a present-but-empty value (the clear form recorded during
// parsing).
func (a *AccountSet) present(name string, nonZero bool) bool {
	return nonZero || a.HasField(name)
}

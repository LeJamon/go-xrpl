package definitions

// IsPseudoAccountField reports whether the given SField carries rippled's
// sMD_PseudoAccount metadata bit (SField.h: 0x40), which marks the pseudo-account
// designators on an ltACCOUNT_ROOT entry: sfAMMID, sfVaultID and sfLoanBrokerID.
// It is the codec-layer counterpart to IsBaseTenUInt64FieldName (sMD_BaseTen): a
// 3.0.0 node must recognize the bit even though no field active at 3.0.0
// activation carries it yet (#275). The name set must stay in sync with rippled's
// sMD_PseudoAccount-marked fields; rippled derives the set dynamically from the
// ACCOUNT_ROOT template (isPseudoAccount, src/xrpld/ledger/detail/View.cpp), while
// the bit is declared in include/xrpl/protocol/detail/sfields.macro.
func IsPseudoAccountField(name string) bool {
	switch name {
	case "AMMID", "VaultID", "LoanBrokerID":
		return true
	}
	return false
}

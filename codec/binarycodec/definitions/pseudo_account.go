package definitions

// IsPseudoAccountField reports whether the given SField designates a
// pseudo-account on an ltACCOUNT_ROOT entry. Mirrors rippled's SField metadata
// bit sMD_PseudoAccount = 0x40 (added in rippled 3.0.0), which marks sfAMMID,
// sfVaultID and sfLoanBrokerID. rippled's isPseudoAccount tests an account root
// for the presence of any such field; see rippled
// include/xrpl/protocol/SField.h and src/xrpld/ledger/detail/View.cpp.
func IsPseudoAccountField(name string) bool {
	switch name {
	case "AMMID", "VaultID", "LoanBrokerID":
		return true
	}
	return false
}

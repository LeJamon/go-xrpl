package entry

import "testing"

// TestLedgerSpecificFlags pins every Lsf* constant against rippled. These flags
// are scoped per ledger entry type, so identical numeric values are reused
// across types; the golden table records the expected value for each name.
// Reference: rippled/include/xrpl/protocol/LedgerFormats.h (LedgerSpecificFlags)
func TestLedgerSpecificFlags(t *testing.T) {
	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		// ltACCOUNT_ROOT
		{"LsfPasswordSpent", LsfPasswordSpent, 0x00010000},
		{"LsfRequireDestTag", LsfRequireDestTag, 0x00020000},
		{"LsfRequireAuth", LsfRequireAuth, 0x00040000},
		{"LsfDisallowXRP", LsfDisallowXRP, 0x00080000},
		{"LsfDisableMaster", LsfDisableMaster, 0x00100000},
		{"LsfNoFreeze", LsfNoFreeze, 0x00200000},
		{"LsfGlobalFreeze", LsfGlobalFreeze, 0x00400000},
		{"LsfDefaultRipple", LsfDefaultRipple, 0x00800000},
		{"LsfDepositAuth", LsfDepositAuth, 0x01000000},
		{"LsfDisallowIncomingNFTokenOffer", LsfDisallowIncomingNFTokenOffer, 0x04000000},
		{"LsfDisallowIncomingCheck", LsfDisallowIncomingCheck, 0x08000000},
		{"LsfDisallowIncomingPayChan", LsfDisallowIncomingPayChan, 0x10000000},
		{"LsfDisallowIncomingTrustline", LsfDisallowIncomingTrustline, 0x20000000},
		{"LsfAllowTrustLineLocking", LsfAllowTrustLineLocking, 0x40000000},
		{"LsfAllowTrustLineClawback", LsfAllowTrustLineClawback, 0x80000000},

		// ltOFFER
		{"LsfPassive", LsfPassive, 0x00010000},
		{"LsfSell", LsfSell, 0x00020000},
		{"LsfHybrid", LsfHybrid, 0x00040000},

		// ltRIPPLE_STATE
		{"LsfLowReserve", LsfLowReserve, 0x00010000},
		{"LsfHighReserve", LsfHighReserve, 0x00020000},
		{"LsfLowAuth", LsfLowAuth, 0x00040000},
		{"LsfHighAuth", LsfHighAuth, 0x00080000},
		{"LsfLowNoRipple", LsfLowNoRipple, 0x00100000},
		{"LsfHighNoRipple", LsfHighNoRipple, 0x00200000},
		{"LsfLowFreeze", LsfLowFreeze, 0x00400000},
		{"LsfHighFreeze", LsfHighFreeze, 0x00800000},
		{"LsfAMMNode", LsfAMMNode, 0x01000000},
		{"LsfLowDeepFreeze", LsfLowDeepFreeze, 0x02000000},
		{"LsfHighDeepFreeze", LsfHighDeepFreeze, 0x04000000},

		// ltSIGNER_LIST
		{"LsfOneOwnerCount", LsfOneOwnerCount, 0x00010000},

		// ltDIR_NODE
		{"LsfNFTokenBuyOffers", LsfNFTokenBuyOffers, 0x00000001},
		{"LsfNFTokenSellOffers", LsfNFTokenSellOffers, 0x00000002},

		// ltNFTOKEN_OFFER
		{"LsfSellNFToken", LsfSellNFToken, 0x00000001},

		// ltMPTOKEN_ISSUANCE
		{"LsfMPTLocked", LsfMPTLocked, 0x00000001},
		{"LsfMPTCanLock", LsfMPTCanLock, 0x00000002},
		{"LsfMPTRequireAuth", LsfMPTRequireAuth, 0x00000004},
		{"LsfMPTCanEscrow", LsfMPTCanEscrow, 0x00000008},
		{"LsfMPTCanTrade", LsfMPTCanTrade, 0x00000010},
		{"LsfMPTCanTransfer", LsfMPTCanTransfer, 0x00000020},
		{"LsfMPTCanClawback", LsfMPTCanClawback, 0x00000040},

		// ltMPTOKEN
		{"LsfMPTAuthorized", LsfMPTAuthorized, 0x00000002},

		// ltCREDENTIAL
		{"LsfAccepted", LsfAccepted, 0x00010000},

		// ltVAULT
		{"LsfVaultPrivate", LsfVaultPrivate, 0x00010000},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = 0x%08X, want 0x%08X", tt.name, tt.got, tt.want)
		}
	}
}

// TestMPTokenTxFlags pins the MPToken transaction flag constants. The tfMPT*
// flags map directly onto their lsfMPT* ledger counterparts.
// Reference: rippled/include/xrpl/protocol/TxFlags.h
func TestMPTokenTxFlags(t *testing.T) {
	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"TfMPTCanLock", TfMPTCanLock, 0x00000002},
		{"TfMPTRequireAuth", TfMPTRequireAuth, 0x00000004},
		{"TfMPTCanEscrow", TfMPTCanEscrow, 0x00000008},
		{"TfMPTCanTrade", TfMPTCanTrade, 0x00000010},
		{"TfMPTCanTransfer", TfMPTCanTransfer, 0x00000020},
		{"TfMPTCanClawback", TfMPTCanClawback, 0x00000040},
		{"TfMPTUnauthorize", TfMPTUnauthorize, 0x00000001},
		{"TfMPTLock", TfMPTLock, 0x00000001},
		{"TfMPTUnlock", TfMPTUnlock, 0x00000002},
		{"TfUniversal", TfUniversal, 0x80000000},
	}

	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = 0x%08X, want 0x%08X", tt.name, tt.got, tt.want)
		}
	}
}

// TestMPTokenFlagMasks verifies each transaction flag mask is the complement of
// the universal flag OR'd with the type-specific flags it permits. The golden
// literal pins the absolute value; the recomputed value documents membership
// and cross-checks the literal against the named constants.
func TestMPTokenFlagMasks(t *testing.T) {
	tests := []struct {
		name    string
		got     uint32
		want    uint32
		members []uint32
	}{
		{
			"TfMPTokenIssuanceCreateMask", TfMPTokenIssuanceCreateMask, 0x7FFFFF81,
			[]uint32{TfMPTCanLock, TfMPTRequireAuth, TfMPTCanEscrow, TfMPTCanTrade, TfMPTCanTransfer, TfMPTCanClawback},
		},
		{
			"TfMPTokenAuthorizeMask", TfMPTokenAuthorizeMask, 0x7FFFFFFE,
			[]uint32{TfMPTUnauthorize},
		},
		{
			"TfMPTokenIssuanceSetMask", TfMPTokenIssuanceSetMask, 0x7FFFFFFC,
			[]uint32{TfMPTLock, TfMPTUnlock},
		},
		{
			"TfMPTokenIssuanceDestroyMask", TfMPTokenIssuanceDestroyMask, 0x7FFFFFFF,
			nil,
		},
	}

	for _, tt := range tests {
		allowed := TfUniversal
		for _, m := range tt.members {
			allowed |= m
		}
		recomputed := ^allowed
		if tt.got != tt.want {
			t.Errorf("%s = 0x%08X, want 0x%08X", tt.name, tt.got, tt.want)
		}
		if recomputed != tt.want {
			t.Errorf("%s: ^(universal|members) = 0x%08X, want 0x%08X", tt.name, recomputed, tt.want)
		}
	}
}

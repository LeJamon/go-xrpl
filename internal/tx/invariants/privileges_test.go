package invariants

import (
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// TestPrivilegeTable pins the transcription of transactions.macro's privileges
// column (rippled tag 3.0.0). If a mapping drifts, the invariant checks that
// consult hasPrivilege silently change behaviour, so lock the key entries down.
func TestPrivilegeTable(t *testing.T) {
	cases := []struct {
		txType TxType
		priv   Privilege
	}{
		{protocol.TxTypePayment, createAcct},
		{protocol.TxTypeAccountDelete, mustDeleteAcct},
		{protocol.TxTypeNFTokenMint, changeNFTCounts},
		{protocol.TxTypeNFTokenBurn, changeNFTCounts},
		{protocol.TxTypeAMMClawback, mayDeleteAcct | overrideFreeze},
		{protocol.TxTypeAMMCreate, createPseudoAcct},
		{protocol.TxTypeAMMWithdraw, mayDeleteAcct},
		{protocol.TxTypeAMMDelete, mustDeleteAcct},
		{protocol.TxTypeXChainAddClaimAttestation, createAcct},
		{protocol.TxTypeXChainAddAccountCreateAttest, createAcct},
		{protocol.TxTypeMPTokenIssuanceCreate, createMPTIssuance},
		{protocol.TxTypeMPTokenIssuanceDestroy, destroyMPTIssuance},
		{protocol.TxTypeMPTokenAuthorize, mustAuthorizeMPT},
		{protocol.TxTypeVaultCreate, createPseudoAcct | createMPTIssuance | mustModifyVault},
		{protocol.TxTypeVaultDelete, mustDeleteAcct | destroyMPTIssuance | mustModifyVault},
		{protocol.TxTypeVaultDeposit, mayAuthorizeMPT | mustModifyVault},
		{protocol.TxTypeVaultWithdraw, mayDeleteMPT | mayAuthorizeMPT | mustModifyVault},
		{protocol.TxTypeLoanBrokerSet, createPseudoAcct | mayAuthorizeMPT},
		{protocol.TxTypeLoanBrokerDelete, mustDeleteAcct | mayAuthorizeMPT},
		{protocol.TxTypeLoanBrokerCoverWithdraw, mayAuthorizeMPT},
		{protocol.TxTypeLoanSet, mayAuthorizeMPT | mustModifyVault},
		{protocol.TxTypeLoanManage, mayModifyVault},
		{protocol.TxTypeLoanPay, mayAuthorizeMPT | mustModifyVault},
	}
	for _, c := range cases {
		if got := txPrivileges[c.txType]; got != c.priv {
			t.Errorf("txPrivileges[%d] = 0x%04x, want 0x%04x", c.txType, got, c.priv)
		}
		// Every set bit must be individually reported by hasPrivilege.
		for bit := Privilege(1); bit != 0; bit <<= 1 {
			if c.priv&bit != 0 && !hasPrivilege(c.txType, bit) {
				t.Errorf("hasPrivilege(%d, 0x%04x) = false, want true", c.txType, bit)
			}
		}
	}

	// Transaction types with no declared privileges must report none.
	for _, tt := range []TxType{
		protocol.TxTypeOfferCreate,
		protocol.TxTypeTrustSet,
		protocol.TxTypeBatch,
		protocol.TxTypeEscrowFinish,
	} {
		if txPrivileges[tt] != noPriv {
			t.Errorf("txPrivileges[%d] = 0x%04x, want noPriv", tt, txPrivileges[tt])
		}
		if hasPrivilege(tt, mustDeleteAcct|createAcct|changeNFTCounts) {
			t.Errorf("hasPrivilege(%d, ...) unexpectedly true", tt)
		}
	}
}

package invariants

// privileges.go — per-transaction privilege bitfield.
//
// Each transaction type declares which privileged operations it is permitted to
// perform. Invariant checks consult hasPrivilege instead of hardcoding
// transaction-type lists, mirroring rippled's Privilege enum and the privileges
// column of transactions.macro. A transaction type absent from the
// table carries noPriv, matching rippled's `default: return false` in
// hasPrivilege (InvariantCheck.cpp).

import "github.com/LeJamon/go-xrpl/protocol"

// Privilege is a bitfield of operations a transaction type may perform.
type Privilege uint16

const (
	noPriv             Privilege = 0x0000 // may perform none of the enumerated operations
	createAcct         Privilege = 0x0001 // may create a new AccountRoot
	createPseudoAcct   Privilege = 0x0002 // may create a pseudo-account (implies createAcct)
	mustDeleteAcct     Privilege = 0x0004 // must delete an AccountRoot
	mayDeleteAcct      Privilege = 0x0008 // may delete an AccountRoot
	overrideFreeze     Privilege = 0x0010 // may override some freeze rules
	changeNFTCounts    Privilege = 0x0020 // may mint or burn an NFT
	createMPTIssuance  Privilege = 0x0040 // may create a new MPT issuance
	destroyMPTIssuance Privilege = 0x0080 // may destroy an MPT issuance
	mustAuthorizeMPT   Privilege = 0x0100 // must create or delete an MPToken (except by issuer)
	mayAuthorizeMPT    Privilege = 0x0200 // may create or delete an MPToken (except by issuer)
	mayDeleteMPT       Privilege = 0x0400 // may delete (not create) an MPToken
	mustModifyVault    Privilege = 0x0800 // must modify, delete, or create a vault
	mayModifyVault     Privilege = 0x1000 // may modify, delete, or create a vault
	mayCreateMPT       Privilege = 0x2000 // may create, but not delete, an MPToken
)

// txPrivileges maps each transaction type to its declared privilege bitfield.
// Transaction types
// with noPriv are omitted (the zero value); hasPrivilege treats them, and any
// deprecated/pseudo type, as privilege-less. The Loan* types (74-84) are keyed
// by numeric tt code because go-xrpl does not yet name them (tracked in #1245).
var txPrivileges = map[TxType]Privilege{
	protocol.TxTypePayment:                      createAcct | mayCreateMPT,
	protocol.TxTypeAccountDelete:                mustDeleteAcct,
	protocol.TxTypeNFTokenMint:                  changeNFTCounts,
	protocol.TxTypeNFTokenBurn:                  changeNFTCounts,
	protocol.TxTypeAMMClawback:                  mayDeleteAcct | overrideFreeze,
	protocol.TxTypeAMMCreate:                    createPseudoAcct | mayCreateMPT,
	protocol.TxTypeAMMWithdraw:                  mayDeleteAcct,
	protocol.TxTypeAMMDelete:                    mustDeleteAcct,
	protocol.TxTypeXChainAddClaimAttestation:    createAcct,
	protocol.TxTypeXChainAddAccountCreateAttest: createAcct,
	protocol.TxTypeMPTokenIssuanceCreate:        createMPTIssuance,
	protocol.TxTypeMPTokenIssuanceDestroy:       destroyMPTIssuance,
	protocol.TxTypeMPTokenAuthorize:             mustAuthorizeMPT,
	protocol.TxTypeCheckCash:                    mayCreateMPT,
	protocol.TxTypeOfferCreate:                  mayCreateMPT,
	protocol.TxTypeVaultCreate:                  createPseudoAcct | createMPTIssuance | mustModifyVault,
	protocol.TxTypeVaultSet:                     mustModifyVault,
	protocol.TxTypeVaultDelete:                  mustDeleteAcct | destroyMPTIssuance | mustModifyVault,
	protocol.TxTypeVaultDeposit:                 mayAuthorizeMPT | mustModifyVault,
	protocol.TxTypeVaultWithdraw:                mayDeleteMPT | mayAuthorizeMPT | mustModifyVault,
	protocol.TxTypeVaultClawback:                mayDeleteMPT | mustModifyVault,
	TxType(74):                                  createPseudoAcct | mayAuthorizeMPT, // ttLOAN_BROKER_SET
	TxType(75):                                  mustDeleteAcct | mayAuthorizeMPT,   // ttLOAN_BROKER_DELETE
	TxType(77):                                  mayAuthorizeMPT,                    // ttLOAN_BROKER_COVER_WITHDRAW
	TxType(80):                                  mayAuthorizeMPT | mustModifyVault,  // ttLOAN_SET
	TxType(82):                                  mayModifyVault,                     // ttLOAN_MANAGE
	TxType(84):                                  mayAuthorizeMPT | mustModifyVault,  // ttLOAN_PAY
}

// hasPrivilege reports whether the given transaction type holds priv.
func hasPrivilege(txType TxType, priv Privilege) bool {
	return txPrivileges[txType]&priv != 0
}

// hasPrivilegeName resolves a transaction type by its canonical name (as
// produced by TxType.String()) and reports whether it holds priv. An unknown
// name carries no privileges, matching rippled's hasPrivilege default.
func hasPrivilegeName(name string, priv Privilege) bool {
	t, ok := protocol.TxTypeFromName(name)
	if !ok {
		return false
	}
	return hasPrivilege(t, priv)
}

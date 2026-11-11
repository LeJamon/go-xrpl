package invariants

import (
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Privilege is the per-transaction-type privilege bitfield declared on each
// registered tx.Descriptor. A type without a descriptor carries none.
type Privilege = tx.Privilege

const (
	createAcct         = tx.PrivCreateAcct
	createPseudoAcct   = tx.PrivCreatePseudoAcct
	mustDeleteAcct     = tx.PrivMustDeleteAcct
	mayDeleteAcct      = tx.PrivMayDeleteAcct
	overrideFreeze     = tx.PrivOverrideFreeze
	changeNFTCounts    = tx.PrivChangeNFTCounts
	createMPTIssuance  = tx.PrivCreateMPTIssuance
	destroyMPTIssuance = tx.PrivDestroyMPTIssuance
	mustAuthorizeMPT   = tx.PrivMustAuthorizeMPT
	mayAuthorizeMPT    = tx.PrivMayAuthorizeMPT
	mayDeleteMPT       = tx.PrivMayDeleteMPT
	mustModifyVault    = tx.PrivMustModifyVault
	mayModifyVault     = tx.PrivMayModifyVault
	mayCreateMPT       = tx.PrivMayCreateMPT
)

// hasPrivilege reports whether the given transaction type holds priv.
func hasPrivilege(txType TxType, priv Privilege) bool {
	return tx.PrivilegesOf(txType)&priv != 0
}

// hasPrivilegeName resolves a transaction type by its canonical name (as
// produced by TxType.String()) and reports whether it holds priv. An unknown
// name carries no privileges.
func hasPrivilegeName(name string, priv Privilege) bool {
	t, ok := protocol.TxTypeFromName(name)
	if !ok {
		return false
	}
	return hasPrivilege(t, priv)
}

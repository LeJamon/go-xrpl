package tx

import (
	"github.com/LeJamon/goXRPLd/internal/tx/invariants"
)

// invariantsTxAdapter wraps a tx.Transaction to satisfy the invariants.Transaction
// interface and all optional invariant interfaces. This bridges the gap between
// the tx package types and the invariants package types.
//
// Optional interfaces are implemented by delegating to the underlying transaction
// and converting types where needed (e.g., tx.Asset -> invariants.Asset).
type invariantsTxAdapter struct {
	tx Transaction
}

// --- invariants.Transaction interface ---

func (a *invariantsTxAdapter) TxType() invariants.TxType {
	return invariants.TxType(a.tx.TxType())
}

func (a *invariantsTxAdapter) TxAccount() string {
	return a.tx.GetCommon().Account
}

func (a *invariantsTxAdapter) TxHasField(name string) bool {
	return a.tx.GetCommon().HasField(name)
}

func (a *invariantsTxAdapter) Flatten() (map[string]any, error) {
	return a.tx.Flatten()
}

// --- Optional interfaces ---

// ClawbackAmount implements invariants.ClawbackAmountProvider by delegating to
// the underlying transaction. Only Clawback transactions satisfy the underlying
// interface; for others, the invariant check gates on TxType first.
func (a *invariantsTxAdapter) ClawbackAmount() invariants.Amount {
	type provider interface {
		ClawbackAmount() Amount
	}
	if p, ok := a.tx.(provider); ok {
		return p.ClawbackAmount()
	}
	return invariants.Amount{}
}

// HasHolder implements invariants.HolderFieldProvider.
func (a *invariantsTxAdapter) HasHolder() bool {
	type provider interface {
		HasHolder() bool
	}
	if p, ok := a.tx.(provider); ok {
		return p.HasHolder()
	}
	return false
}

// GetDomainID implements invariants.DomainIDProvider.
func (a *invariantsTxAdapter) GetDomainID() (*[32]byte, bool) {
	type provider interface {
		GetDomainID() (*[32]byte, bool)
	}
	if p, ok := a.tx.(provider); ok {
		return p.GetDomainID()
	}
	return nil, false
}

// GetAMMAsset implements invariants.AMMAssetProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAMMAsset() invariants.Asset {
	type provider interface {
		GetAMMAsset() Asset
	}
	if p, ok := a.tx.(provider); ok {
		asset := p.GetAMMAsset()
		return invariants.Asset{Currency: asset.Currency, Issuer: asset.Issuer}
	}
	return invariants.Asset{}
}

// GetAMMAsset2 implements invariants.AMMAssetProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAMMAsset2() invariants.Asset {
	type provider interface {
		GetAMMAsset2() Asset
	}
	if p, ok := a.tx.(provider); ok {
		asset := p.GetAMMAsset2()
		return invariants.Asset{Currency: asset.Currency, Issuer: asset.Issuer}
	}
	return invariants.Asset{}
}

// GetAmountAsset implements invariants.AMMCreateIssueProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAmountAsset() invariants.Asset {
	type provider interface {
		GetAmountAsset() Asset
	}
	if p, ok := a.tx.(provider); ok {
		asset := p.GetAmountAsset()
		return invariants.Asset{Currency: asset.Currency, Issuer: asset.Issuer}
	}
	return invariants.Asset{}
}

// GetAmount2Asset implements invariants.AMMCreateIssueProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAmount2Asset() invariants.Asset {
	type provider interface {
		GetAmount2Asset() Asset
	}
	if p, ok := a.tx.(provider); ok {
		asset := p.GetAmount2Asset()
		return invariants.Asset{Currency: asset.Currency, Issuer: asset.Issuer}
	}
	return invariants.Asset{}
}

// wrapTxForInvariants wraps a tx.Transaction as an invariants.Transaction.
// The adapter implements all optional invariant interfaces by delegating to
// the underlying transaction and converting types where needed.
func wrapTxForInvariants(tx Transaction) invariants.Transaction {
	return &invariantsTxAdapter{tx: tx}
}

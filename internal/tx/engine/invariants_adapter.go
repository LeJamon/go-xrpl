package engine

import (
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
)

type invariantsTxContext struct {
	feePayerID            [20]byte
	feePayerKnown         bool
	feePayerPreFunded     bool
	currentCloseTime      uint32
	currentCloseTimeKnown bool
}

// invariantsTxAdapter supplies transaction fields and resolved apply context.
type invariantsTxAdapter struct {
	tx      txcore.Transaction
	context invariantsTxContext
}

// --- invariants.Transaction interface ---

func (a *invariantsTxAdapter) TxType() invariants.TxType {
	return a.tx.TxType()
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

func (a *invariantsTxAdapter) FeePayer() ([20]byte, bool, bool) {
	return a.context.feePayerID, a.context.feePayerPreFunded, a.context.feePayerKnown
}

func (a *invariantsTxAdapter) CurrentCloseTime() (uint32, bool) {
	return a.context.currentCloseTime, a.context.currentCloseTimeKnown
}

// --- Optional interfaces ---

// ClawbackAmount implements invariants.ClawbackAmountProvider by delegating to
// the underlying transaction. Only Clawback transactions satisfy the underlying
// interface; for others, the invariant check gates on TxType first.
func (a *invariantsTxAdapter) ClawbackAmount() invariants.Amount {
	type provider interface {
		ClawbackAmount() txcore.Amount
	}
	if p, ok := a.tx.(provider); ok {
		return p.ClawbackAmount()
	}
	return invariants.Amount{}
}

func (a *invariantsTxAdapter) ClawbackHolder() string {
	type provider interface {
		ClawbackHolder() string
	}
	if p, ok := a.tx.(provider); ok {
		return p.ClawbackHolder()
	}
	return ""
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

// toInvariantsAsset converts a tx.Asset to an invariants.Asset, preserving all
// three fields — Currency, Issuer, and MPTIssuanceID. Dropping MPTIssuanceID
// would leave an MPT-asset AMM invariant unable to locate the pool holding.
func toInvariantsAsset(asset txcore.Asset) invariants.Asset {
	return invariants.Asset{
		Currency:      asset.Currency,
		Issuer:        asset.Issuer,
		MPTIssuanceID: asset.MPTIssuanceID,
	}
}

// GetAMMAsset implements invariants.AMMAssetProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAMMAsset() invariants.Asset {
	type provider interface {
		GetAMMAsset() txcore.Asset
	}
	if p, ok := a.tx.(provider); ok {
		return toInvariantsAsset(p.GetAMMAsset())
	}
	return invariants.Asset{}
}

// GetAMMAsset2 implements invariants.AMMAssetProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAMMAsset2() invariants.Asset {
	type provider interface {
		GetAMMAsset2() txcore.Asset
	}
	if p, ok := a.tx.(provider); ok {
		return toInvariantsAsset(p.GetAMMAsset2())
	}
	return invariants.Asset{}
}

// GetAmountAsset implements invariants.AMMCreateIssueProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAmountAsset() invariants.Asset {
	type provider interface {
		GetAmountAsset() txcore.Asset
	}
	if p, ok := a.tx.(provider); ok {
		return toInvariantsAsset(p.GetAmountAsset())
	}
	return invariants.Asset{}
}

// GetAmount2Asset implements invariants.AMMCreateIssueProvider by converting tx.Asset to invariants.Asset.
func (a *invariantsTxAdapter) GetAmount2Asset() invariants.Asset {
	type provider interface {
		GetAmount2Asset() txcore.Asset
	}
	if p, ok := a.tx.(provider); ok {
		return toInvariantsAsset(p.GetAmount2Asset())
	}
	return invariants.Asset{}
}

// wrapTxForInvariants wraps a tx.Transaction as an invariants.Transaction.
// The adapter implements all optional invariant interfaces by delegating to
// the underlying transaction and converting types where needed.
func wrapTxForInvariants(tx txcore.Transaction) invariants.Transaction {
	return wrapTxForInvariantsWithContext(tx, invariantsTxContext{})
}

func wrapTxForInvariantsWithContext(tx txcore.Transaction, context invariantsTxContext) invariants.Transaction {
	return &invariantsTxAdapter{tx: tx, context: context}
}

func invariantsContextForApply(st *applyState, parentCloseTime uint32) invariantsTxContext {
	context := invariantsTxContext{
		currentCloseTime:      parentCloseTime,
		currentCloseTimeKnown: true,
	}
	if st == nil {
		return context
	}
	context.feePayerID = st.feePayer.accountID
	context.feePayerKnown = st.feePayer.known
	context.feePayerPreFunded = st.feePayer.payerTy == feePayerSponsorPreFunded
	if context.feePayerPreFunded {
		context.feePayerID = [20]byte{}
	}
	if !context.feePayerKnown && st.feePayer.payerTy == feePayerAccount {
		context.feePayerID = st.accountID
		context.feePayerKnown = true
	}
	return context
}

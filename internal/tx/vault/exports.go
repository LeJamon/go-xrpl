package vault

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Reuse surface for the lending package. A LoanBroker sits on a Vault and reuses
// its pseudo-account derivation and asset-movement machinery; these thin wrappers
// share the tested vault-internal helpers instead of duplicating them.

// PseudoAccountAddress derives an unoccupied pseudo-account ID from a ledger
// keylet (the shared createPseudoAccount address search).
func PseudoAccountAddress(view tx.LedgerView, parentHash, key [32]byte) [20]byte {
	return pseudoAccountAddress(view, parentHash, key)
}

// ReadAccountRoot reads and parses an AccountRoot, returning (nil, nil) when absent.
func ReadAccountRoot(view tx.LedgerView, id [20]byte) (*state.AccountRoot, error) {
	return readAccountRoot(view, id)
}

// IsPseudoAccountID reports whether id is an existing pseudo-account.
func IsPseudoAccountID(view tx.LedgerView, id [20]byte) bool {
	return isPseudoAccountID(view, id)
}

// CanAddHolding reports whether accountID could hold asset (issuer/DefaultRipple
// for IOU, issuance existence + CanTransfer for MPT).
func CanAddHolding(view tx.LedgerView, asset tx.Asset) ter.Result {
	return canAddHolding(view, asset)
}

// AssetFrozen reports whether asset is frozen/locked for accountID.
func AssetFrozen(view tx.LedgerView, accountID [20]byte, asset tx.Asset) ter.Result {
	return assetFrozen(view, accountID, asset)
}

// AddEmptyHolding gives accountID a zero-balance holding for asset, returning the
// owner-count delta to apply.
func AddEmptyHolding(ctx *tx.ApplyContext, accountID [20]byte, asset tx.Asset) (int32, ter.Result) {
	return addEmptyHolding(ctx, accountID, asset)
}

// RemoveAssetHolding deletes accountID's IOU trust line for asset (XRP/MPT no-op),
// returning the owner-count delta.
func RemoveAssetHolding(ctx *tx.ApplyContext, accountID [20]byte, asset tx.Asset) (int32, ter.Result) {
	return removeVaultAssetHolding(ctx, accountID, asset)
}

// CanWithdraw validates delivery of amount from → to (destination exists,
// dest-tag / deposit-auth, IOU trust-limit).
func CanWithdraw(view tx.LedgerView, from, to [20]byte, amount tx.Amount, hasDestTag bool) ter.Result {
	return canWithdraw(view, from, to, amount, hasDestTag)
}

// AccountHoldsFull returns how much of asset accountID can spend
// (accountHolds with shFULL_BALANCE), issuer treated as effectively unbounded.
func AccountHoldsFull(view tx.LedgerView, config tx.EngineConfig, accountID [20]byte, asset tx.Asset) (state.XRPLNumber, error) {
	return spendableAsset(view, config, accountID, asset)
}

// SendAsset moves amountN of asset from → to (XRP/IOU/MPT), writing ctx.Account
// through for the submitter. amountN is interpreted as drops/units for the
// integral types and as the decimal value for an IOU.
func SendAsset(ctx *tx.ApplyContext, from, to [20]byte, asset tx.Asset, amountN state.XRPLNumber) ter.Result {
	if from == to || amountN.IsZero() {
		return ter.TesSUCCESS
	}
	if isNativeAsset(asset) {
		drops := amountN.ToInt64WithMode(state.RoundTowardsZero)
		if res := adjustXRPBalance(ctx, from, -drops); res != ter.TesSUCCESS {
			return res
		}
		return adjustXRPBalance(ctx, to, drops)
	}
	if asset.IsMPT() {
		id, ok := assetMPTID(asset)
		if !ok {
			return ter.TefINTERNAL
		}
		return sendMPTAsset(ctx, id, from, to, uint64(amountN.ToInt64WithMode(state.RoundTowardsZero)))
	}
	amt := state.NewIssuedAmountFromValue(amountN.Mantissa(), amountN.Exponent(), asset.Currency, asset.Issuer)
	return tx.RippleCredit(ctx.View, from, to, amt)
}

// adjustXRPBalance adds delta drops to an account, modifying ctx.Account for the
// submitter (which the engine writes back) and the SLE otherwise.
func adjustXRPBalance(ctx *tx.ApplyContext, account [20]byte, delta int64) ter.Result {
	if account == ctx.AccountID {
		nb := int64(ctx.Account.Balance) + delta
		if nb < 0 {
			return ter.TecINSUFFICIENT_FUNDS
		}
		ctx.Account.Balance = uint64(nb)
		return ter.TesSUCCESS
	}
	ar, err := readAccountRoot(ctx.View, account)
	if err != nil || ar == nil {
		return ter.TefINTERNAL
	}
	nb := int64(ar.Balance) + delta
	if nb < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	ar.Balance = uint64(nb)
	data, serr := state.SerializeAccountRoot(ar)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(keylet.Account(account), data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// VaultInfo is the subset of a vault entry the lending package reads.
type VaultInfo struct {
	Account    [20]byte // pseudo-account
	Owner      [20]byte
	ShareMPTID [24]byte
	Asset      tx.Asset
	OwnerNode  uint64
}

// ReadVaultInfo reads the vault at vaultKey, returning (nil, nil) when absent.
func ReadVaultInfo(view tx.LedgerView, vaultKey keylet.Keylet) (*VaultInfo, error) {
	vd, err := readVault(view, vaultKey)
	if err != nil || vd == nil {
		return nil, err
	}
	return &VaultInfo{
		Account:    vd.Account,
		Owner:      vd.Owner,
		ShareMPTID: vd.ShareMPTID,
		Asset:      vaultAssetOf(vd),
		OwnerNode:  vd.OwnerNode,
	}, nil
}

// VaultLending exposes the mutable NUMBER totals the lending transactors read and
// write (loan disbursement, impairment loss tracking). The NUMBER fields are the
// codec's decimal-string form ("" = zero).
type VaultLending struct {
	VaultInfo
	AssetsTotal     string
	AssetsAvailable string
	AssetsMaximum   string
	LossUnrealized  string
	Scale           uint8
}

// ReadVaultLending reads the vault's full lending view, (nil, nil) when absent.
func ReadVaultLending(view tx.LedgerView, vaultKey keylet.Keylet) (*VaultLending, error) {
	vd, err := readVault(view, vaultKey)
	if err != nil || vd == nil {
		return nil, err
	}
	return &VaultLending{
		VaultInfo: VaultInfo{
			Account: vd.Account, Owner: vd.Owner, ShareMPTID: vd.ShareMPTID,
			Asset: vaultAssetOf(vd), OwnerNode: vd.OwnerNode,
		},
		AssetsTotal:     vd.AssetsTotal,
		AssetsAvailable: vd.AssetsAvailable,
		AssetsMaximum:   vd.AssetsMaximum,
		LossUnrealized:  vd.LossUnrealized,
		Scale:           vd.Scale,
	}, nil
}

// UpdateVaultTotals reads the vault and rewrites its three NUMBER totals
// (AssetsTotal, AssetsAvailable, LossUnrealized) in place. Values are the codec's
// decimal-string form ("" = zero).
func UpdateVaultTotals(ctx *tx.ApplyContext, vaultKey keylet.Keylet, assetsTotal, assetsAvailable, lossUnrealized string) ter.Result {
	vd, err := readVault(ctx.View, vaultKey)
	if err != nil || vd == nil {
		return ter.TefBAD_LEDGER
	}
	vd.AssetsTotal = assetsTotal
	vd.AssetsAvailable = assetsAvailable
	vd.LossUnrealized = lossUnrealized
	data, serr := serializeVault(vd)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(vaultKey, data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

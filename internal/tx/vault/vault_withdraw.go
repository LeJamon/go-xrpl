package vault

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// VaultWithdraw withdraws assets from a vault.
type VaultWithdraw struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Amount is the amount to withdraw (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the destination account (optional)
	Destination string `json:"Destination,omitempty" xrpl:"Destination,omitempty"`

	// DestinationTag is the destination tag (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
}

// NewVaultWithdraw creates a new VaultWithdraw transaction
func NewVaultWithdraw(account, vaultID string, amount tx.Amount) *VaultWithdraw {
	return &VaultWithdraw{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultWithdraw, account),
		VaultID: vaultID,
		Amount:  amount,
	}
}

func (v *VaultWithdraw) TxType() tx.Type {
	return tx.TypeVaultWithdraw
}

// Reference: rippled VaultWithdraw.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. VaultWithdraw defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (v *VaultWithdraw) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (v *VaultWithdraw) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// VaultID is required and cannot be zero
	if v.VaultID == "" {
		return ErrVaultIDRequired
	}
	if _, err := tx.ParseHash256NonZero(v.VaultID); err != nil {
		if isZeroHash(v.VaultID) {
			return ErrVaultIDZero
		}
		return ter.Errorf(ter.TemMALFORMED, "VaultID must be a valid 256-bit hash")
	}

	// Amount must be positive — rejects default, explicit zero, and negative.
	if v.Amount.Signum() <= 0 {
		return ErrVaultAmountNotPos
	}

	// A present Destination must not be the zero account. When no Destination
	// is given, a DestinationTag is meaningless and rejected.
	if v.Destination != "" {
		if id, err := state.DecodeAccountID(v.Destination); err == nil && id == ([20]byte{}) {
			return ErrVaultDestZero
		}
	} else if v.DestinationTag != nil {
		return ErrVaultDestTagNoAccount
	}

	return nil
}

func (v *VaultWithdraw) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultWithdraw) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

func (v *VaultWithdraw) vaultIDBytes() ([32]byte, bool) {
	var id [32]byte
	b, err := hex.DecodeString(v.VaultID)
	if err != nil || len(b) != 32 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// amountIsShares reports whether the withdrawal Amount denominates the vault's
// share MPT rather than its underlying asset.
func (v *VaultWithdraw) amountIsShares(vd *vaultData) bool {
	return v.Amount.IsMPT() &&
		strings.EqualFold(v.Amount.MPTIssuanceID(), hex.EncodeToString(vd.ShareMPTID[:]))
}

func (v *VaultWithdraw) destination(accountID [20]byte) ([20]byte, error) {
	if v.Destination == "" {
		return accountID, nil
	}
	return state.DecodeAccountID(v.Destination)
}

// Preclaim checks the vault exists, the amount denominates the asset or shares,
// the withdrawal can be delivered to the destination, and nothing is frozen.
// Reference: rippled VaultWithdraw::preclaim.
func (v *VaultWithdraw) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(v.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	vaultID, ok := v.vaultIDBytes()
	if !ok {
		return ter.TemMALFORMED
	}
	vd, verr := readVault(view, keylet.VaultByID(vaultID))
	if verr != nil {
		return ter.TefINTERNAL
	}
	if vd == nil {
		return ter.TecNO_ENTRY
	}

	if !assetMatches(v.Amount, vd) && !v.amountIsShares(vd) {
		return ter.TecWRONG_ASSET
	}

	if vd.WithdrawalPolicy != VaultStrategyFirstComeFirstServe {
		return ter.TefINTERNAL
	}

	dstID, derr := v.destination(accountID)
	if derr != nil {
		return ter.TemMALFORMED
	}
	// canWithdraw's trust-limit branch is exempt for the share MPT, so a
	// share-denominated withdrawal pre-amendment skipped the destination's IOU
	// trust limit entirely. Post-fixCleanup3_1_3 the shares are converted to the
	// equivalent asset amount so the limit is enforced. Integral (XRP/MPT) vault
	// assets stay exempt either way — only an IOU asset needs the conversion.
	limitAmount := v.Amount
	if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_1_3) && v.amountIsShares(vd) &&
		!isNativeAsset(vd.Asset) && !vd.AssetIsMPT {
		assets, res := v.sharesToAssetAmount(view, vd)
		if res != ter.TesSUCCESS {
			return res
		}
		limitAmount = assets
	}
	if res := canWithdraw(view, accountID, dstID, limitAmount, v.DestinationTag != nil); res != ter.TesSUCCESS {
		return res
	}

	asset := vaultAssetOf(vd)
	if res := assetFrozen(view, dstID, asset); res != ter.TesSUCCESS {
		return res
	}

	return ter.TesSUCCESS
}

// sharesToAssetAmount converts the share-denominated withdrawal Amount into the
// equivalent IOU asset amount, so the destination trust-line limit can be
// enforced. Reference: rippled VaultWithdraw::preclaim sharesToAssetsWithdraw.
func (v *VaultWithdraw) sharesToAssetAmount(view tx.LedgerView, vd *vaultData) (tx.Amount, ter.Result) {
	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return tx.Amount{}, ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil || issuance.OutstandingAmount == 0 {
		return tx.Amount{}, ter.TefINTERNAL
	}
	sharesN, aerr := amountToNumber(v.Amount)
	if aerr != nil {
		return tx.Amount{}, ter.TefINTERNAL
	}
	assetsTotalN, _ := vaultNumber(vd.AssetsTotal)
	lossN, _ := vaultNumber(vd.LossUnrealized)
	shareTotalN := state.NewXRPLNumber(int64(issuance.OutstandingAmount), 0)
	assetsN := sharesToAssetsWithdraw(assetsTotalN, lossN, shareTotalN, sharesN)
	return state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), vd.Asset.Currency, vd.Asset.Issuer), ter.TesSUCCESS
}

// Apply redeems the caller's shares for the underlying asset and delivers it to
// the destination. Reference: rippled VaultWithdraw::doApply.
func (v *VaultWithdraw) Apply(ctx *tx.ApplyContext) ter.Result {
	vaultID, ok := v.vaultIDBytes()
	if !ok {
		return ter.TefINTERNAL
	}
	vaultKey := keylet.VaultByID(vaultID)
	vd, err := readVault(ctx.View, vaultKey)
	if err != nil || vd == nil {
		return ter.TefINTERNAL
	}

	shareData, rerr := ctx.View.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil {
		return ter.TefINTERNAL
	}

	assetsTotalN, _ := vaultNumber(vd.AssetsTotal)
	availN, _ := vaultNumber(vd.AssetsAvailable)
	lossN, _ := vaultNumber(vd.LossUnrealized)
	shareTotalN := state.NewXRPLNumber(int64(issuance.OutstandingAmount), 0)

	fix320 := ctx.Rules().FixCleanup3_2_0Enabled()
	// The sole shareholder owns the future value too, so the unrealized-loss
	// subtraction is waived — otherwise they could burn every share yet strand
	// future value in the vault.
	if fix320 && isSoleShareholder(ctx.View, ctx.AccountID, vd.ShareMPTID, issuance.OutstandingAmount) {
		lossN = state.NewXRPLNumber(0, 0)
	}

	var sharesRedeemedN, assetsWithdrawnN state.XRPLNumber
	if v.amountIsShares(vd) {
		sharesRedeemedN, err = amountToNumber(v.Amount)
		if err != nil {
			return ter.TefINTERNAL
		}
		assetsWithdrawnN = sharesToAssetsWithdraw(assetsTotalN, lossN, shareTotalN, sharesRedeemedN)
	} else {
		assetsN, aerr := amountToNumber(v.Amount)
		if aerr != nil {
			return ter.TefINTERNAL
		}
		sharesRedeemedN = assetsToSharesWithdraw(assetsTotalN, lossN, shareTotalN, assetsN, true)
		if sharesRedeemedN.IsZero() {
			return ter.TecPRECISION_LOSS
		}
		assetsWithdrawnN = sharesToAssetsWithdraw(assetsTotalN, lossN, shareTotalN, sharesRedeemedN)
	}

	shares := uint64(sharesRedeemedN.ToInt64WithMode(state.RoundTowardsZero))

	// The caller must hold enough shares.
	token, terr := readMPToken(ctx.View, keylet.MPTokenByID(vd.ShareMPTID, ctx.AccountID))
	if terr != nil {
		return ter.TefINTERNAL
	}
	if token == nil || token.MPTAmount < shares {
		return ter.TecINSUFFICIENT_FUNDS
	}

	// The vault must have enough available assets.
	if availN.Cmp(assetsWithdrawnN) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}

	if fix320 && shares == issuance.OutstandingAmount {
		// Burning every outstanding share drains the vault: pay out all remaining
		// available assets and leave no dust behind. Reaching here with a non-zero
		// unrealized loss is impossible — the available-assets guard above rejects
		// it, since a waived loss makes assetsWithdrawn exceed the available total.
		assetsWithdrawnN = availN
		vd.AssetsTotal = ""
		vd.AssetsAvailable = ""
	} else {
		vd.AssetsTotal = numberToString(assetsTotalN.Sub(assetsWithdrawnN))
		vd.AssetsAvailable = numberToString(availN.Sub(assetsWithdrawnN))
	}
	newVault, serr := serializeVault(vd)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(vaultKey, newVault); uerr != nil {
		return ter.TefINTERNAL
	}

	// Burn the redeemed shares.
	if res := burnShares(ctx, vd.ShareMPTID, ctx.AccountID, shares); res != ter.TesSUCCESS {
		return res
	}
	if ctx.AccountID != vd.Owner {
		if res := removeEmptyShareMPToken(ctx, ctx.AccountID, vd.ShareMPTID); res != ter.TesSUCCESS && res != ter.TecHAS_OBLIGATIONS {
			return res
		}
	}

	// Deliver the asset to the destination, creating a holding when withdrawing
	// to self.
	dstID, derr := v.destination(ctx.AccountID)
	if derr != nil {
		return ter.TefINTERNAL
	}
	asset := vaultAssetOf(vd)
	if dstID == ctx.AccountID {
		if _, res := addEmptyHolding(ctx, ctx.AccountID, asset); res != ter.TesSUCCESS && res != ter.TecDUPLICATE {
			return res
		}
	}
	if res := sendAssetFromVault(ctx, vd.Account, dstID, asset, assetsWithdrawnN); res != ter.TesSUCCESS {
		return res
	}

	return ter.TesSUCCESS
}

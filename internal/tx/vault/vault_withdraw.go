package vault

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
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

	// A present Destination must not be the zero account.
	if v.Destination != "" {
		if id, err := state.DecodeAccountID(v.Destination); err == nil && id == ([20]byte{}) {
			return ErrVaultDestZero
		}
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

func checkVaultShareFrozen(view tx.LedgerView, account [20]byte, vd *vaultData) ter.Result {
	share := tx.Asset{MPTIssuanceID: hex.EncodeToString(vd.ShareMPTID[:])}
	return checkFrozen(view, share, account)
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

	dstID, derr := v.destination(accountID)
	if derr != nil {
		return ter.TemMALFORMED
	}
	asset := vaultAssetOf(vd)
	if res := canTransfer(
		view,
		asset,
		vd.Account,
		dstID,
		config.RequireRules().FixCleanup3_2_0Enabled(),
	); res != ter.TesSUCCESS {
		return res
	}

	if vd.WithdrawalPolicy != VaultStrategyFirstComeFirstServe {
		return ter.TefINTERNAL
	}

	// canWithdraw's trust-limit branch is exempt for the share MPT, so a
	// share-denominated withdrawal pre-amendment skipped the destination's IOU
	// trust limit entirely. Post-fixCleanup3_1_3 the shares are converted to the
	// equivalent asset amount so the limit is enforced. Integral (XRP/MPT) vault
	// assets stay exempt either way — only an IOU asset needs the conversion.
	limitAmount := v.Amount
	if config.RequireRules().Enabled(amendment.FeatureFixCleanup3_1_3) && v.amountIsShares(vd) &&
		!isNativeAsset(vd.Asset) && !vd.AssetIsMPT {
		assets, res := v.sharesToAssetAmount(
			view,
			vd,
			accountID,
			config.RequireRules(),
		)
		if res != ter.TesSUCCESS {
			return res
		}
		limitAmount = assets
	}
	if res := canWithdraw(view, accountID, dstID, limitAmount, v.DestinationTag != nil, config.NumberContext()); res != ter.TesSUCCESS {
		return res
	}

	authType := mptutil.WeakAuth
	if dstID != accountID {
		authType = mptutil.StrongAuth
	}
	if res := requireAuth(view, asset, dstID, authType, config.ParentCloseTime); res != ter.TesSUCCESS {
		return res
	}
	if res := checkFrozen(view, asset, dstID); res != ter.TesSUCCESS {
		return res
	}
	if res := checkVaultShareFrozen(view, accountID, vd); res != ter.TesSUCCESS {
		return res
	}

	return ter.TesSUCCESS
}

// sharesToAssetAmount converts the share-denominated withdrawal Amount into the
// equivalent IOU asset amount, so the destination trust-line limit can be
// enforced. Reference: rippled VaultWithdraw::preclaim sharesToAssetsWithdraw.
func (v *VaultWithdraw) sharesToAssetAmount(
	view tx.LedgerView,
	vd *vaultData,
	accountID [20]byte,
	rules *amendment.Rules,
) (amount tx.Amount, result ter.Result) {
	result = ter.TesSUCCESS
	defer func() {
		if recover() != nil {
			amount = tx.Amount{}
			result = ter.TecPATH_DRY
		}
	}()
	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return tx.Amount{}, ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil || issuance.OutstandingAmount == 0 {
		return tx.Amount{}, ter.TefINTERNAL
	}
	sharesN, aerr := amountToNumberForRules(v.Amount, rules)
	if aerr != nil {
		return tx.Amount{}, ter.TefINTERNAL
	}
	assetsTotalN, _ := vaultNumberForRules(vd.AssetsTotal, rules)
	lossN, _ := vaultNumberForRules(vd.LossUnrealized, rules)
	scale := vaultNumberScale(rules)
	if rules.FixCleanup3_2_0Enabled() && isSoleShareholder(view, accountID, vd.ShareMPTID, issuance.OutstandingAmount) {
		lossN = state.NewXRPLNumberScaled(0, 0, scale, state.RoundToNearest)
	}
	shareTotalN := state.NewXRPLNumberScaled(int64(issuance.OutstandingAmount), 0, scale, state.RoundToNearest)
	asset := vaultAssetOf(vd)
	assetsN := sharesToAssetsWithdraw(
		assetsTotalN,
		lossN,
		shareTotalN,
		sharesN,
		asset.IsNative() || asset.IsMPT(),
	)
	return state.NewIssuedAmountFromValue(assetsN.Mantissa(), assetsN.Exponent(), vd.Asset.Currency, vd.Asset.Issuer), ter.TesSUCCESS
}

func assetWithdrawalAmounts(
	assetsTotal,
	lossUnrealized,
	shareTotal,
	assets state.XRPLNumber,
	integral bool,
) (sharesRedeemed, assetsWithdrawn state.XRPLNumber) {
	sharesRedeemed = assetsToSharesWithdraw(assetsTotal, lossUnrealized, shareTotal, assets, false)
	assetsWithdrawn = sharesToAssetsWithdraw(
		assetsTotal,
		lossUnrealized,
		shareTotal,
		sharesRedeemed,
		integral,
	)
	return sharesRedeemed, assetsWithdrawn
}

func (v *VaultWithdraw) withdrawalAmounts(
	ctx *tx.ApplyContext,
	vd *vaultData,
	issuance *state.MPTokenIssuanceData,
) (
	assetsTotalN, availN, assetsWithdrawnN state.XRPLNumber,
	shares uint64,
	fix320 bool,
	result ter.Result,
) {
	result = ter.TesSUCCESS
	defer func() {
		if recover() != nil {
			result = ter.TecPATH_DRY
		}
	}()

	rules := ctx.Rules()
	numberScale := vaultNumberScale(rules)
	assetsTotalN, _ = vaultNumberForRules(vd.AssetsTotal, rules)
	availN, _ = vaultNumberForRules(vd.AssetsAvailable, rules)
	lossN, _ := vaultNumberForRules(vd.LossUnrealized, rules)
	shareTotalN := state.NewXRPLNumberScaled(
		int64(issuance.OutstandingAmount),
		0,
		numberScale,
		state.RoundToNearest,
	)

	fix320 = rules.FixCleanup3_2_0Enabled()
	asset := vaultAssetOf(vd)
	integral := asset.IsNative() || asset.IsMPT()
	if fix320 && isSoleShareholder(ctx.View, ctx.AccountID, vd.ShareMPTID, issuance.OutstandingAmount) {
		lossN = state.NewXRPLNumberScaled(0, 0, numberScale, state.RoundToNearest)
	}

	var sharesRedeemedN state.XRPLNumber
	if v.amountIsShares(vd) {
		var err error
		sharesRedeemedN, err = amountToNumberForRules(v.Amount, rules)
		if err != nil {
			return assetsTotalN, availN, assetsWithdrawnN, 0, fix320, ter.TefINTERNAL
		}
		assetsWithdrawnN = sharesToAssetsWithdraw(
			assetsTotalN,
			lossN,
			shareTotalN,
			sharesRedeemedN,
			integral,
		)
	} else {
		assetsN, err := amountToNumberForRules(v.Amount, rules)
		if err != nil {
			return assetsTotalN, availN, assetsWithdrawnN, 0, fix320, ter.TefINTERNAL
		}
		sharesRedeemedN, assetsWithdrawnN = assetWithdrawalAmounts(
			assetsTotalN,
			lossN,
			shareTotalN,
			assetsN,
			integral,
		)
		if sharesRedeemedN.IsZero() {
			return assetsTotalN, availN, assetsWithdrawnN, 0, fix320, ter.TecPRECISION_LOSS
		}
	}

	shares = uint64(sharesRedeemedN.ToInt64WithMode(state.RoundTowardsZero))
	return assetsTotalN, availN, assetsWithdrawnN, shares, fix320, ter.TesSUCCESS
}

func addWithdrawDestinationHolding(ctx *tx.ApplyContext, asset tx.Asset) ter.Result {
	if asset.IsMPT() {
		id, ok := assetMPTID(asset)
		if !ok {
			return ter.TefINTERNAL
		}
		exists, err := ctx.View.Exists(keylet.MPTokenByID(id, ctx.AccountID))
		if err != nil {
			return ter.TefINTERNAL
		}
		if !exists && mptIDIssuer(id) != ctx.AccountID && ctx.Account.OwnerCount >= 2 &&
			ctx.PriorBalance() < ctx.AccountReserve(tx.ConfineOwnerCount(ctx.Account.OwnerCount, 1)) {
			return ter.TecINSUFFICIENT_RESERVE
		}
	}
	delta, result := addEmptyHolding(ctx, ctx.AccountID, asset)
	if result == ter.TecDUPLICATE {
		return ter.TesSUCCESS
	}
	if result != ter.TesSUCCESS {
		return result
	}
	if delta > 0 {
		ctx.Account.OwnerCount = tx.ConfineOwnerCount(ctx.Account.OwnerCount, int(delta))
	}
	return ter.TesSUCCESS
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

	rules := ctx.Rules()
	asset := vaultAssetOf(vd)
	assetsTotalN, availN, assetsWithdrawnN, shares, fix320, result := v.withdrawalAmounts(ctx, vd, issuance)
	if result != ter.TesSUCCESS {
		return result
	}

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
	if err := associateVaultAsset(vd, rules); err != nil {
		return ter.TefINTERNAL
	}
	newVault, serr := serializeVaultForRules(vd, rules)
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
	if dstID == ctx.AccountID {
		if res := addWithdrawDestinationHolding(ctx, asset); res != ter.TesSUCCESS {
			return res
		}
	}
	holding, herr := actualAssetHolding(ctx.View, vd.Account, asset, rules)
	if herr != nil || holding.Cmp(assetsWithdrawnN) < 0 {
		return ter.TefINTERNAL
	}
	if res := sendAssetFromVault(ctx, vd.Account, dstID, asset, assetsWithdrawnN); res != ter.TesSUCCESS {
		return res
	}

	return ter.TesSUCCESS
}

package vault

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type VaultClawback struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Holder is the holder to claw back from (required)
	Holder string `json:"Holder" xrpl:"Holder"`

	// Amount is the amount to claw back (optional). It may denominate the vault
	// asset (issuer clawback) or the vault's share MPT (owner share burn).
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`
}

// NewVaultClawback creates a new VaultClawback transaction
func NewVaultClawback(account, vaultID, holder string) *VaultClawback {
	return &VaultClawback{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultClawback, account),
		VaultID: vaultID,
		Holder:  holder,
	}
}

func (v *VaultClawback) TxType() tx.Type {
	return tx.TypeVaultClawback
}

// Validate runs the stateless checks. Reference: rippled VaultClawback::preflight.
// GetFlagsMask adopts the engine FlagsMasker seam. VaultClawback defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (v *VaultClawback) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (v *VaultClawback) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	if v.VaultID == "" {
		return ErrVaultIDRequired
	}
	if _, err := tx.ParseHash256NonZero(v.VaultID); err != nil {
		if isZeroHash(v.VaultID) {
			return ErrVaultIDZero
		}
		return ter.Errorf(ter.TemMALFORMED, "VaultID must be a valid 256-bit hash")
	}

	if v.Holder == "" {
		return ErrVaultHolderRequired
	}

	// A present Amount: zero is valid (means "all"); negative is rejected, and
	// XRP can never be clawed back. The issuer==holder rejection is a preclaim
	// tecNO_PERMISSION, not a preflight check.
	if v.Amount != nil {
		if v.Amount.Signum() < 0 {
			return ErrVaultAmountNotPos
		}
		if v.Amount.IsNative() {
			return ErrVaultAmountXRP
		}
	}

	return nil
}

func (v *VaultClawback) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultClawback) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

func (v *VaultClawback) vaultIDBytes() ([32]byte, bool) {
	var id [32]byte
	b, err := hex.DecodeString(v.VaultID)
	if err != nil || len(b) != 32 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// amountIsShares reports whether a present Amount denominates the vault shares.
func (v *VaultClawback) amountIsShares(vd *vaultData) bool {
	return v.Amount != nil && v.Amount.IsMPT() &&
		strings.EqualFold(v.Amount.MPTIssuanceID(), hex.EncodeToString(vd.ShareMPTID[:]))
}

// clawsBackShares reports whether this clawback targets the vault shares (owner
// share burn) rather than the underlying asset.
func (v *VaultClawback) clawsBackShares(vd *vaultData, accountID [20]byte) bool {
	if v.Amount == nil {
		return accountID == vd.Owner
	}
	return v.amountIsShares(vd)
}

// Preclaim runs the clawback dispatch: owner share burns require an asset-empty
// vault with outstanding shares, and issuer asset clawbacks require the
// appropriate clawback permissions. Reference: rippled VaultClawback::preclaim.
func (v *VaultClawback) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(v.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	holderID, herr := state.DecodeAccountID(v.Holder)
	if herr != nil {
		return ter.TemMALFORMED
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

	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil {
		return ter.TefINTERNAL
	}

	// Ambiguous: when the vault asset's issuer is the vault owner, the clawback
	// must name the asset explicitly.
	if v.Amount == nil && !isNativeAsset(vaultAssetOf(vd)) {
		issuerID, ok := vaultAssetIssuer(vd)
		if !ok {
			return ter.TefINTERNAL
		}
		if issuerID == vd.Owner {
			return ter.TecWRONG_ASSET
		}
	}

	if v.clawsBackShares(vd, accountID) {
		if accountID != vd.Owner {
			return ter.TecNO_PERMISSION
		}
		if issuance.OutstandingAmount == 0 || vd.AssetsTotal != "" || vd.AssetsAvailable != "" {
			return ter.TecNO_PERMISSION
		}
		if v.Amount != nil && v.Amount.Signum() != 0 {
			held := holderMPTBalance(view, vd.ShareMPTID, holderID)
			scale := vaultNumberScale(config.RequireRules())
			want, aerr := amountToNumberForRules(*v.Amount, config.RequireRules())
			if aerr != nil {
				return ter.TefINTERNAL
			}
			if want.Cmp(state.NewXRPLNumberScaled(int64(held), 0, scale, state.RoundToNearest)) != 0 {
				return ter.TecLIMIT_EXCEEDED
			}
		}
		return ter.TesSUCCESS
	}

	// Asset clawback by the issuer.
	if assetMatches(clawbackAssetAmount(v, vd), vd) {
		if isNativeAsset(vaultAssetOf(vd)) {
			return ter.TecNO_PERMISSION
		}
		issuerID, ok := vaultAssetIssuer(vd)
		if !ok {
			return ter.TefINTERNAL
		}
		if issuerID != accountID {
			return ter.TecNO_PERMISSION
		}
		if accountID == holderID {
			return ter.TecNO_PERMISSION
		}
		if vd.AssetIsMPT {
			assetData, rerr := view.Read(keylet.MPTIssuance(vd.AssetMPTID))
			if rerr != nil {
				return ter.TefINTERNAL
			}
			if assetData == nil {
				return ter.TecOBJECT_NOT_FOUND
			}
			assetIssuance, perr := state.ParseMPTokenIssuance(assetData)
			if perr != nil {
				return ter.TefINTERNAL
			}
			if assetIssuance.Flags&entry.LsfMPTCanClawback == 0 {
				return ter.TecNO_PERMISSION
			}
			return ter.TesSUCCESS
		}
		issuerAcct, ierr := tx.ReadAccountRoot(view, accountID)
		if ierr != nil || issuerAcct == nil {
			return ter.TefINTERNAL
		}
		if issuerAcct.Flags&entry.LsfAllowTrustLineClawback == 0 || issuerAcct.Flags&entry.LsfNoFreeze != 0 {
			return ter.TecNO_PERMISSION
		}
		return ter.TesSUCCESS
	}

	return ter.TecWRONG_ASSET
}

// Apply burns the holder's shares and, for an issuer asset clawback, recovers
// the corresponding assets to the issuer. Reference: rippled VaultClawback::doApply.
func (v *VaultClawback) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	holderID, herr := state.DecodeAccountID(v.Holder)
	if herr != nil {
		return ter.TefINTERNAL
	}
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
	assetsTotalN, availN, assetsRecoveredN, sharesDestroyed, result := v.clawbackAmounts(
		ctx,
		vd,
		issuance,
		accountID,
		holderID,
	)
	if result != ter.TesSUCCESS {
		return result
	}

	vd.AssetsTotal = numberToString(assetsTotalN.Sub(assetsRecoveredN))
	vd.AssetsAvailable = numberToString(availN.Sub(assetsRecoveredN))
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

	// Burn the holder's shares.
	if res := burnShares(ctx, vd.ShareMPTID, holderID, sharesDestroyed); res != ter.TesSUCCESS {
		return res
	}
	if holderID != vd.Owner {
		if res := removeEmptyShareMPToken(ctx, holderID, vd.ShareMPTID); res != ter.TesSUCCESS && res != ter.TecHAS_OBLIGATIONS {
			return res
		}
	}

	// Deliver recovered assets to the issuer.
	if assetsRecoveredN.Signum() > 0 {
		if res := sendAssetFromVault(ctx, vd.Account, accountID, vaultAssetOf(vd), assetsRecoveredN); res != ter.TesSUCCESS {
			return res
		}
		holding, err := actualAssetHolding(ctx.View, vd.Account, vaultAssetOf(vd), rules)
		if err != nil || holding.Signum() < 0 {
			return ter.TefINTERNAL
		}
	}

	return ter.TesSUCCESS
}

func (v *VaultClawback) clawbackAmounts(
	ctx *tx.ApplyContext,
	vd *vaultData,
	issuance *state.MPTokenIssuanceData,
	accountID, holderID [20]byte,
) (
	assetsTotalN, availN, assetsRecoveredN state.XRPLNumber,
	sharesDestroyed uint64,
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
	asset := vaultAssetOf(vd)
	integral := asset.IsNative() || asset.IsMPT()
	held := holderMPTBalance(ctx.View, vd.ShareMPTID, holderID)
	assetsRecoveredN = state.NewXRPLNumberScaled(0, 0, numberScale, state.RoundToNearest)
	clampAssets := false

	if v.clawsBackShares(vd, accountID) {
		sharesDestroyed = held
	} else if v.Amount == nil || v.Amount.Signum() == 0 {
		sharesDestroyed = held
		assetsRecoveredN = sharesToAssetsWithdraw(
			assetsTotalN,
			lossN,
			shareTotalN,
			state.NewXRPLNumberScaled(int64(held), 0, numberScale, state.RoundToNearest),
			integral,
		)
		clampAssets = rules.Enabled(amendment.FeatureFixCleanup3_1_3)
	} else {
		amountN, err := amountToNumberForRules(*v.Amount, rules)
		if err != nil {
			return assetsTotalN, availN, assetsRecoveredN, 0, ter.TefINTERNAL
		}
		sharesN := assetsToSharesWithdraw(assetsTotalN, lossN, shareTotalN, amountN, false)
		sharesDestroyed = uint64(sharesN.ToInt64WithMode(state.RoundTowardsZero))
		assetsRecoveredN = sharesToAssetsWithdraw(
			assetsTotalN,
			lossN,
			shareTotalN,
			sharesN,
			integral,
		)
		clampAssets = true
	}

	if clampAssets && assetsRecoveredN.Cmp(availN) > 0 {
		assetsRecoveredN = availN
		sharesN := assetsToSharesWithdraw(assetsTotalN, lossN, shareTotalN, availN, true)
		sharesDestroyed = uint64(sharesN.ToInt64WithMode(state.RoundTowardsZero))
		assetsRecoveredN = sharesToAssetsWithdraw(
			assetsTotalN,
			lossN,
			shareTotalN,
			sharesN,
			integral,
		)
		if assetsRecoveredN.Cmp(availN) > 0 {
			return assetsTotalN, availN, assetsRecoveredN, sharesDestroyed, ter.TecINTERNAL
		}
	}
	if sharesDestroyed == 0 {
		return assetsTotalN, availN, assetsRecoveredN, 0, ter.TecPRECISION_LOSS
	}
	return assetsTotalN, availN, assetsRecoveredN, sharesDestroyed, ter.TesSUCCESS
}

// clawbackAssetAmount returns a tx.Amount denominating the vault asset, used to
// test the asset-clawback branch when Amount is absent.
func clawbackAssetAmount(v *VaultClawback, vd *vaultData) tx.Amount {
	if v.Amount != nil {
		return *v.Amount
	}
	if vd.AssetIsMPT {
		issuer, err := state.EncodeAccountID(mptIDIssuer(vd.AssetMPTID))
		if err != nil {
			return tx.Amount{}
		}
		return state.NewMPTAmountWithIssuanceID(0, issuer, hex.EncodeToString(vd.AssetMPTID[:]))
	}
	if isNativeAsset(vd.Asset) {
		return tx.Amount{Native: true}
	}
	return state.NewIssuedAmountFromValue(0, state.MinExponent, vd.Asset.Currency, vd.Asset.Issuer)
}

func vaultAssetIssuer(vd *vaultData) ([20]byte, bool) {
	if vd.AssetIsMPT {
		return mptIDIssuer(vd.AssetMPTID), true
	}
	issuer, err := state.DecodeAccountID(vd.Asset.Issuer)
	return issuer, err == nil
}

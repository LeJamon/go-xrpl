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
func (v *VaultClawback) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}
	if err := tx.CheckFlags(v.GetFlags(), tx.TfUniversalMask); err != nil {
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

	// Ambiguous: issuer-is-owner must name the asset explicitly.
	if v.Amount == nil && !isNativeAsset(vd.Asset) && !vd.AssetIsMPT && vd.Asset.Issuer == v.Account {
		return ter.TecWRONG_ASSET
	}

	if v.clawsBackShares(vd, accountID) {
		if accountID != vd.Owner {
			return ter.TecNO_PERMISSION
		}
		if issuance.OutstandingAmount == 0 || vd.AssetsTotal != "" || vd.AssetsAvailable != "" {
			return ter.TecNO_PERMISSION
		}
		if v.Amount != nil && v.Amount.Signum() != 0 {
			held := holderShareBalance(view, vd.ShareMPTID, holderID)
			want, aerr := amountToNumber(*v.Amount)
			if aerr != nil {
				return ter.TefINTERNAL
			}
			if want.Cmp(state.NewXRPLNumber(int64(held), 0)) != 0 {
				return ter.TecLIMIT_EXCEEDED
			}
		}
		return ter.TesSUCCESS
	}

	// Asset clawback by the issuer.
	if assetMatches(clawbackAssetAmount(v, vd), vd) {
		if isNativeAsset(vd.Asset) {
			return ter.TecNO_PERMISSION
		}
		if vd.Asset.Issuer != v.Account {
			return ter.TecNO_PERMISSION
		}
		if accountID == holderID {
			return ter.TecNO_PERMISSION
		}
		if vd.AssetIsMPT {
			return ter.TecNO_PERMISSION // MPT-asset clawback deferred
		}
		issuerAcct, ierr := readAccountRoot(view, accountID)
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

	assetsTotalN, _ := vaultNumber(vd.AssetsTotal)
	availN, _ := vaultNumber(vd.AssetsAvailable)
	lossN, _ := vaultNumber(vd.LossUnrealized)
	shareTotalN := state.NewXRPLNumber(int64(issuance.OutstandingAmount), 0)

	held := holderShareBalance(ctx.View, vd.ShareMPTID, holderID)
	var sharesDestroyed uint64
	assetsRecoveredN := state.NewXRPLNumber(0, 0)

	if v.clawsBackShares(vd, accountID) {
		// Owner burns every share held by the holder; the assets are already gone.
		sharesDestroyed = held
	} else if v.Amount == nil || v.Amount.Signum() == 0 {
		sharesDestroyed = held
		assetsRecoveredN = sharesToAssetsWithdraw(assetsTotalN, lossN, shareTotalN, state.NewXRPLNumber(int64(held), 0))
	} else {
		amountN, aerr := amountToNumber(*v.Amount)
		if aerr != nil {
			return ter.TefINTERNAL
		}
		sharesN := assetsToSharesWithdraw(assetsTotalN, lossN, shareTotalN, amountN, true)
		s := uint64(sharesN.ToInt64WithMode(state.RoundTowardsZero))
		if s > held {
			s = held
		}
		sharesDestroyed = s
		assetsRecoveredN = sharesToAssetsWithdraw(assetsTotalN, lossN, shareTotalN, state.NewXRPLNumber(int64(s), 0))
	}

	if sharesDestroyed == 0 {
		return ter.TecPRECISION_LOSS
	}

	vd.AssetsTotal = numberToString(assetsTotalN.Sub(assetsRecoveredN))
	vd.AssetsAvailable = numberToString(availN.Sub(assetsRecoveredN))
	newVault, serr := serializeVault(vd)
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
	}

	return ter.TesSUCCESS
}

// clawbackAssetAmount returns a tx.Amount denominating the vault asset, used to
// test the asset-clawback branch when Amount is absent.
func clawbackAssetAmount(v *VaultClawback, vd *vaultData) tx.Amount {
	if v.Amount != nil {
		return *v.Amount
	}
	if isNativeAsset(vd.Asset) {
		return tx.Amount{Native: true}
	}
	return state.NewIssuedAmountFromValue(0, state.MinExponent, vd.Asset.Currency, vd.Asset.Issuer)
}

package vault

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// VaultDeposit deposits assets into a vault.
type VaultDeposit struct {
	tx.BaseTx

	// VaultID is the ID of the vault (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Amount is the amount to deposit (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`
}

// NewVaultDeposit creates a new VaultDeposit transaction
func NewVaultDeposit(account, vaultID string, amount tx.Amount) *VaultDeposit {
	return &VaultDeposit{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultDeposit, account),
		VaultID: vaultID,
		Amount:  amount,
	}
}

func (v *VaultDeposit) TxType() tx.Type {
	return tx.TypeVaultDeposit
}

// Reference: rippled VaultDeposit.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. VaultDeposit defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (v *VaultDeposit) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (v *VaultDeposit) Validate() error {
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

	return nil
}

func (v *VaultDeposit) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultDeposit) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

func (v *VaultDeposit) vaultIDBytes() ([32]byte, bool) {
	var id [32]byte
	b, err := hex.DecodeString(v.VaultID)
	if err != nil || len(b) != 32 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// Preclaim checks the vault exists, the deposited asset matches, the depositor
// is authorized (private vaults), and holds enough of the asset.
// Reference: rippled VaultDeposit::preclaim.
func (v *VaultDeposit) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
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

	if !assetMatches(v.Amount, vd) {
		return ter.TecWRONG_ASSET
	}

	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	if issuance.Flags&entry.LsfMPTLocked != 0 {
		return ter.TefINTERNAL
	}

	asset := vaultAssetOf(vd)
	if res := tx.AssetFrozen(view, accountID, asset); res != ter.TesSUCCESS {
		return res
	}

	// Private vault: a non-owner needs domain authorization.
	if vd.Flags&VaultFlagPrivate != 0 && accountID != vd.Owner {
		if issuance.DomainID == nil {
			return ter.TecNO_AUTH
		}
		// Domain credential validation is enforced in Apply via the MPToken
		// authorization path; a missing domain is rejected above.
	}

	// The depositor must be authorized to hold the asset (MPT assets only).
	if res := tx.RequireAuth(view, asset, accountID); res != ter.TesSUCCESS {
		return res
	}

	// The depositor must hold at least the deposited amount.
	holds, herr := spendableAsset(view, config, accountID, asset)
	if herr != nil {
		return ter.TefINTERNAL
	}
	assetsN, aerr := amountToNumber(v.Amount)
	if aerr != nil {
		return ter.TefINTERNAL
	}
	integral := asset.IsNative() || asset.IsMPT()
	fix320 := config.RequireRules().FixCleanup3_2_0Enabled()
	if fix320 {
		assetsTotalN, _ := vaultNumber(vd.AssetsTotal)
		assetsN = roundToVaultScale(assetsN, assetsTotalN, integral)
		if assetsN.IsZero() {
			return ter.TecPRECISION_LOSS
		}
	}
	if holds.Cmp(assetsN) < 0 {
		return ter.TecINSUFFICIENT_FUNDS
	}
	// IOU only: reject a deposit that canonicalizes to a no-op at the depositor's
	// own trust-line scale. Issuer-as-depositor uses an unbounded balance, skip.
	if fix320 && !integral {
		if issuerID, ierr := state.DecodeAccountID(asset.Issuer); ierr == nil && accountID != issuerID {
			origN, _ := amountToNumber(v.Amount)
			balScale := holds.AssetExponent(false, state.RoundToNearest)
			if origN.RoundToAssetScale(false, balScale, state.RoundToNearest).IsZero() {
				return ter.TecPRECISION_LOSS
			}
		}
	}

	return ter.TesSUCCESS
}

// Apply mints shares to the depositor in exchange for the deposited asset.
// Reference: rippled VaultDeposit::doApply.
func (v *VaultDeposit) Apply(ctx *tx.ApplyContext) ter.Result {
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

	// Ensure the depositor holds a share MPToken.
	private := vd.Flags&VaultFlagPrivate != 0
	if res := ensureHolderMPToken(ctx, ctx.AccountID, vd.ShareMPTID); res != ter.TesSUCCESS {
		return res
	}
	if private && ctx.AccountID == vd.Owner {
		if res := authorizeHolderMPToken(ctx, ctx.AccountID, vd.ShareMPTID); res != ter.TesSUCCESS {
			return res
		}
	}

	// Compute the share/asset exchange.
	assetsN, aerr := amountToNumber(v.Amount)
	if aerr != nil {
		return ter.TefINTERNAL
	}
	assetsTotalN, _ := vaultNumber(vd.AssetsTotal)
	if ctx.Rules().FixCleanup3_2_0Enabled() {
		asset := vaultAssetOf(vd)
		assetsN = roundToVaultScale(assetsN, assetsTotalN, asset.IsNative() || asset.IsMPT())
		if assetsN.IsZero() {
			return ter.TefINTERNAL
		}
	}
	shareTotalN := newVaultNumber(int64(issuance.OutstandingAmount), 0)
	sharesN := assetsToSharesDeposit(assetsTotalN, shareTotalN, assetsN, vd.Scale)
	if sharesN.IsZero() {
		return ter.TecPRECISION_LOSS
	}
	assetsDepositedN := sharesToAssetsDeposit(assetsTotalN, shareTotalN, sharesN, vd.Scale)
	if assetsDepositedN.Cmp(assetsN) > 0 {
		return ter.TefINTERNAL
	}

	// Update the vault totals.
	newTotal := assetsTotalN.Add(assetsDepositedN)
	availN, _ := vaultNumber(vd.AssetsAvailable)
	vd.AssetsTotal = numberToString(newTotal)
	vd.AssetsAvailable = numberToString(availN.Add(assetsDepositedN))

	// Enforce the maximum after the increment.
	if vd.AssetsMaximum != "" {
		maxN, _ := vaultNumber(vd.AssetsMaximum)
		if maxN.Signum() != 0 && newTotal.Cmp(maxN) > 0 {
			return ter.TecLIMIT_EXCEEDED
		}
	}

	newVault, serr := serializeVault(vd)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(vaultKey, newVault); uerr != nil {
		return ter.TefINTERNAL
	}

	// Transfer the asset from depositor to the vault pseudo-account.
	if res := sendAssetToVault(ctx, vd.Account, v.Amount, assetsDepositedN); res != ter.TesSUCCESS {
		return res
	}

	// Mint the shares to the depositor.
	shares := uint64(sharesN.ToInt64WithMode(state.RoundTowardsZero))
	if res := mintShares(ctx, vd.ShareMPTID, ctx.AccountID, shares); res != ter.TesSUCCESS {
		return res
	}

	return ter.TesSUCCESS
}

package vault

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// VaultDelete deletes a vault.
type VaultDelete struct {
	tx.BaseTx

	// VaultID is the ID of the vault to delete (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`
}

// NewVaultDelete creates a new VaultDelete transaction
func NewVaultDelete(account, vaultID string) *VaultDelete {
	return &VaultDelete{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultDelete, account),
		VaultID: vaultID,
	}
}

func (v *VaultDelete) TxType() tx.Type {
	return tx.TypeVaultDelete
}

// Reference: rippled VaultDelete.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. VaultDelete defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (v *VaultDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (v *VaultDelete) Validate() error {
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

	return nil
}

func (v *VaultDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

func (v *VaultDelete) vaultIDBytes() ([32]byte, bool) {
	var id [32]byte
	b, err := hex.DecodeString(v.VaultID)
	if err != nil || len(b) != 32 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// Preclaim checks the vault exists, the submitter owns it, and the vault holds
// no assets or outstanding shares. Reference: rippled VaultDelete::preclaim.
func (v *VaultDelete) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
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
	if vd.Owner != accountID {
		return ter.TecNO_PERMISSION
	}
	if vd.AssetsAvailable != "" || vd.AssetsTotal != "" {
		return ter.TecHAS_OBLIGATIONS
	}

	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil {
		return ter.TefINTERNAL
	}
	if issuance.OutstandingAmount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}

	return ter.TesSUCCESS
}

// Apply tears the vault down: it removes the pseudo-account's asset holding and
// the owner's share MPToken, destroys the share issuance and pseudo-account,
// then erases the vault. Reference: rippled VaultDelete::doApply.
func (v *VaultDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	vaultID, ok := v.vaultIDBytes()
	if !ok {
		return ter.TefINTERNAL
	}
	vaultKey := keylet.VaultByID(vaultID)
	vd, err := readVault(ctx.View, vaultKey)
	if err != nil || vd == nil {
		return ter.TefINTERNAL
	}

	pseudo, perr := readAccountRoot(ctx.View, vd.Account)
	if perr != nil || pseudo == nil {
		return ter.TefBAD_LEDGER
	}
	asset := vaultAssetOf(vd)

	// Remove the pseudo-account's asset holding.
	assetDelta, res := removeVaultAssetHolding(ctx, vd.Account, asset)
	if res != ter.TesSUCCESS {
		return res
	}
	pseudo.OwnerCount = uint32(int32(pseudo.OwnerCount) + assetDelta)

	// Remove the owner's (kept) share MPToken, if any.
	if res := removeEmptyShareMPToken(ctx, ctx.AccountID, vd.ShareMPTID); res != ter.TesSUCCESS && res != ter.TecHAS_OBLIGATIONS {
		return res
	}

	// Destroy the share issuance.
	shareKey := keylet.MPTIssuance(vd.ShareMPTID)
	shareData, rerr := ctx.View.Read(shareKey)
	if rerr != nil || shareData == nil {
		return ter.TefINTERNAL
	}
	issuance, iperr := state.ParseMPTokenIssuance(shareData)
	if iperr != nil {
		return ter.TefINTERNAL
	}
	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(vd.Account), issuance.OwnerNode, shareKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if pseudo.OwnerCount > 0 {
		pseudo.OwnerCount--
	}
	if e := ctx.View.Erase(shareKey); e != nil {
		return ter.TefINTERNAL
	}

	// The pseudo-account must now be empty; destroy it.
	if pseudo.Balance != 0 || pseudo.OwnerCount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	if e := ctx.View.Erase(keylet.Account(vd.Account)); e != nil {
		return ter.TefINTERNAL
	}

	// Remove the vault from the owner's directory and erase it. The owner is
	// credited back the vault + pseudo-account it was charged for at create
	// (rippled adjustOwnerCount(owner, -2)).
	if r, e := state.DirRemove(ctx.View, keylet.OwnerDir(ctx.AccountID), vd.OwnerNode, vaultKey.Key, false); e != nil || !r.Success {
		return ter.TefBAD_LEDGER
	}
	if ctx.Account.OwnerCount >= 2 {
		ctx.Account.OwnerCount -= 2
	} else {
		ctx.Account.OwnerCount = 0
	}
	if e := ctx.View.Erase(vaultKey); e != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

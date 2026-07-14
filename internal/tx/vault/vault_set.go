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

// VaultSet modifies a vault.
type VaultSet struct {
	tx.BaseTx

	// VaultID is the ID of the vault to modify (required)
	VaultID string `json:"VaultID" xrpl:"VaultID"`

	// Data is arbitrary data (optional)
	Data string `json:"Data,omitempty" xrpl:"Data,omitempty"`

	// DomainID is the permissioned domain ID (optional)
	DomainID string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// AssetsMaximum is the maximum assets (optional). NUMBER field, carried as
	// its decimal/scientific string form.
	AssetsMaximum *string `json:"AssetsMaximum,omitempty" xrpl:"AssetsMaximum,omitempty"`
}

// NewVaultSet creates a new VaultSet transaction
func NewVaultSet(account, vaultID string) *VaultSet {
	return &VaultSet{
		BaseTx:  *tx.NewBaseTx(tx.TypeVaultSet, account),
		VaultID: vaultID,
	}
}

func (v *VaultSet) TxType() tx.Type {
	return tx.TypeVaultSet
}

// Reference: rippled VaultSet.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. VaultSet defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (v *VaultSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (v *VaultSet) Validate() error {
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

	// Data is a Blob: present-but-empty and over-length (in decoded bytes)
	// are both rejected.
	if v.Data != "" {
		dataBytes, err := decodeBlob(v.Data)
		if err != nil {
			return ErrVaultDataTooLong
		}
		if len(dataBytes) == 0 {
			return ErrVaultDataEmpty
		}
		if len(dataBytes) > MaxVaultDataLength {
			return ErrVaultDataTooLong
		}
	}

	// Validate AssetsMaximum if present. It is a NUMBER: negative is rejected.
	if v.AssetsMaximum != nil && isNegativeNumberString(*v.AssetsMaximum) {
		return ErrVaultAssetsMaxNeg
	}

	// Must update at least one field
	if v.DomainID == "" && v.AssetsMaximum == nil && v.Data == "" {
		return ErrVaultNoFieldsToUpdate
	}

	return nil
}

func (v *VaultSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSingleAssetVault}
}

// vaultIDBytes decodes the VaultID hex into a 32-byte value.
func (v *VaultSet) vaultIDBytes() ([32]byte, bool) {
	var id [32]byte
	b, err := hex.DecodeString(v.VaultID)
	if err != nil || len(b) != 32 {
		return id, false
	}
	copy(id[:], b)
	return id, true
}

// Preclaim checks the vault exists, the submitter owns it, and any DomainID
// update targets a private vault whose share issuance exists.
// Reference: rippled VaultSet::preclaim.
func (v *VaultSet) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
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

	shareData, rerr := view.Read(keylet.MPTIssuance(vd.ShareMPTID))
	if rerr != nil || shareData == nil {
		return ter.TefINTERNAL
	}
	issuance, perr := state.ParseMPTokenIssuance(shareData)
	if perr != nil {
		return ter.TefINTERNAL
	}

	if v.DomainID != "" {
		if vd.Flags&VaultFlagPrivate == 0 {
			return ter.TecNO_PERMISSION
		}
		domainID, derr := hex.DecodeString(v.DomainID)
		if derr != nil || len(domainID) != 32 {
			return ter.TemMALFORMED
		}
		if !isZeroBytes(domainID) {
			var id [32]byte
			copy(id[:], domainID)
			if exists, _ := view.Exists(keylet.PermissionedDomainByID(id)); !exists {
				return ter.TecOBJECT_NOT_FOUND
			}
		}
		if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
			return ter.TefINTERNAL
		}
	}

	return ter.TesSUCCESS
}

// Apply mutates the vault's Data, AssetsMaximum, and (for private vaults) the
// share issuance's DomainID.
// Reference: rippled VaultSet::doApply.
func (v *VaultSet) Apply(ctx *tx.ApplyContext) ter.Result {
	vaultID, ok := v.vaultIDBytes()
	if !ok {
		return ter.TefINTERNAL
	}
	vaultKey := keylet.VaultByID(vaultID)
	vd, err := readVault(ctx.View, vaultKey)
	if err != nil || vd == nil {
		return ter.TefINTERNAL
	}

	if v.Data != "" {
		vd.Data = v.Data
	}
	if v.AssetsMaximum != nil {
		newMax, nerr := vaultNumber(*v.AssetsMaximum)
		if nerr != nil {
			return ter.TefINTERNAL
		}
		total, terr := vaultNumber(vd.AssetsTotal)
		if terr != nil {
			return ter.TefINTERNAL
		}
		if newMax.Signum() != 0 && newMax.Cmp(total) < 0 {
			return ter.TecLIMIT_EXCEEDED
		}
		vd.AssetsMaximum = numberToString(newMax)
	}

	if v.DomainID != "" {
		shareKey := keylet.MPTIssuance(vd.ShareMPTID)
		shareData, rerr := ctx.View.Read(shareKey)
		if rerr != nil || shareData == nil {
			return ter.TefINTERNAL
		}
		issuance, perr := state.ParseMPTokenIssuance(shareData)
		if perr != nil {
			return ter.TefINTERNAL
		}
		domainHex, derr := hex.DecodeString(v.DomainID)
		if derr != nil {
			return ter.TefINTERNAL
		}
		if isZeroBytes(domainHex) {
			issuance.DomainID = nil
		} else {
			up := strings.ToUpper(v.DomainID)
			issuance.DomainID = &up
		}
		newShare, serr := state.SerializeMPTokenIssuance(issuance)
		if serr != nil {
			return ter.TefINTERNAL
		}
		if uerr := ctx.View.Update(shareKey, newShare); uerr != nil {
			return ter.TefINTERNAL
		}
	}

	newVault, serr := serializeVault(vd)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(vaultKey, newVault); uerr != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// isZeroBytes reports whether every byte in b is zero.
func isZeroBytes(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

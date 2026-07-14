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

// VaultCreate creates a new vault.
type VaultCreate struct {
	tx.BaseTx

	// Asset is the asset the vault holds (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Data is arbitrary data (optional)
	Data string `json:"Data,omitempty" xrpl:"Data,omitempty"`

	// DomainID is the permissioned domain ID (optional)
	DomainID string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// AssetsMaximum is the maximum assets the vault can hold (optional). It is a
	// NUMBER field, carried as its decimal/scientific string form.
	AssetsMaximum *string `json:"AssetsMaximum,omitempty" xrpl:"AssetsMaximum,omitempty"`

	// MPTokenMetadata is metadata for the vault shares (optional)
	MPTokenMetadata string `json:"MPTokenMetadata,omitempty" xrpl:"MPTokenMetadata,omitempty"`

	// WithdrawalPolicy configures withdrawal rules (optional)
	WithdrawalPolicy *uint8 `json:"WithdrawalPolicy,omitempty" xrpl:"WithdrawalPolicy,omitempty"`

	// Scale is the asset scale for the share issuance (optional, IOU only, 0..18).
	Scale *uint8 `json:"Scale,omitempty" xrpl:"Scale,omitempty"`
}

// NewVaultCreate creates a new VaultCreate transaction
func NewVaultCreate(account string, asset tx.Asset) *VaultCreate {
	return &VaultCreate{
		BaseTx: *tx.NewBaseTx(tx.TypeVaultCreate, account),
		Asset:  asset,
	}
}

func (v *VaultCreate) TxType() tx.Type {
	return tx.TypeVaultCreate
}

// Reference: rippled VaultCreate.cpp preflight()
// GetFlagsMask adopts the engine FlagsMasker seam with the VaultCreate-specific
// invalid-flags mask (rippled VaultCreate::getFlagsMask = tfVaultCreateMask),
// checked at preflight0.
func (v *VaultCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfVaultCreateMask
}

func (v *VaultCreate) Validate() error {
	if err := v.BaseTx.Validate(); err != nil {
		return err
	}

	// Asset is required (an issued currency, XRP, or an MPT issuance).
	if v.Asset.Currency == "" && !v.Asset.IsMPT() {
		return ErrVaultAssetRequired
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

	// Validate WithdrawalPolicy if present
	if v.WithdrawalPolicy != nil {
		if *v.WithdrawalPolicy != VaultStrategyFirstComeFirstServe {
			return ErrVaultWithdrawalPolicy
		}
	}

	// Validate DomainID if present
	if v.DomainID != "" {
		if _, err := tx.ParseHash256NonZero(v.DomainID); err != nil {
			if isZeroHash(v.DomainID) {
				return ErrVaultDomainIDZero
			}
			return ter.Errorf(ter.TemMALFORMED, "DomainID must be a valid 256-bit hash")
		}
		// DomainID only allowed on private vaults
		if v.Common.Flags == nil || (*v.Common.Flags&VaultFlagPrivate) == 0 {
			return ErrVaultDomainNotPrivate
		}
	}

	// Validate AssetsMaximum if present. It is a NUMBER: negative is rejected.
	if v.AssetsMaximum != nil && isNegativeNumberString(*v.AssetsMaximum) {
		return ErrVaultAssetsMaxNeg
	}

	// MPTokenMetadata is a Blob: present-but-empty and over-length (in decoded
	// bytes) are both rejected.
	if v.MPTokenMetadata != "" {
		metaBytes, err := decodeBlob(v.MPTokenMetadata)
		if err != nil {
			return ErrVaultMetadataTooLong
		}
		if len(metaBytes) == 0 {
			return ErrVaultMetadataEmpty
		}
		if len(metaBytes) > MaxMPTokenMetadataLength {
			return ErrVaultMetadataTooLong
		}
	}

	// Scale is only valid for an IOU asset and must not exceed the max IOU scale.
	if v.Scale != nil {
		if isNativeAsset(v.Asset) || v.Asset.IsMPT() {
			return ErrVaultScaleForbidden
		}
		if *v.Scale > vaultMaximumIOUScale {
			return ErrVaultScaleTooLarge
		}
	}

	return nil
}

// isNativeAsset reports whether a is the native XRP asset.
func isNativeAsset(a tx.Asset) bool {
	return a.IsNative()
}

// isNegativeNumberString reports whether a NUMBER field's string form is
// strictly negative. The binary codec renders negatives with a leading '-'
// and normalises zero to "0", so a leading '-' is a reliable sign test.
func isNegativeNumberString(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "-")
}

func (v *VaultCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(v)
}

func (v *VaultCreate) RequiredAmendments() [][32]byte {
	amendments := [][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureMPTokensV1}
	if v.DomainID != "" {
		amendments = append(amendments, amendment.FeaturePermissionedDomains)
	}
	return amendments
}

// CalculateBaseFee returns one owner reserve increment: creating a vault also
// creates a pseudo-account, so rippled charges the increment as the base fee.
// Reference: rippled VaultCreate::calculateBaseFee.
func (v *VaultCreate) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return config.ReserveIncrement
}

// Preclaim runs the stateful checks: the vault asset must be addable, must not
// be issued by a pseudo-account, must not be frozen for the owner, a private
// vault's DomainID must exist, and the derived pseudo-account must not collide.
// Reference: rippled VaultCreate::preclaim.
func (v *VaultCreate) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(v.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	asset := v.Asset

	if res := canAddHolding(view, asset); res != ter.TesSUCCESS {
		return res
	}

	// A vault must not hold an asset issued by a pseudo-account (e.g. AMM LP
	// tokens or other vault shares) — such an asset could never be clawed back.
	if asset.IsMPT() {
		if id, ok := assetMPTID(asset); ok && tx.IsPseudoAccountID(view, mptIDIssuer(id)) {
			return ter.TecWRONG_ASSET
		}
	} else if !isNativeAsset(asset) && asset.Issuer != "" {
		if issuerID, derr := state.DecodeAccountID(asset.Issuer); derr == nil {
			if tx.IsPseudoAccountID(view, issuerID) {
				return ter.TecWRONG_ASSET
			}
		}
	}

	if res := tx.AssetFrozen(view, accountID, asset); res != ter.TesSUCCESS {
		return res
	}

	if v.DomainID != "" {
		domainID, derr := hex.DecodeString(v.DomainID)
		if derr != nil || len(domainID) != 32 {
			return ter.TemMALFORMED
		}
		var id [32]byte
		copy(id[:], domainID)
		if exists, _ := view.Exists(keylet.PermissionedDomainByID(id)); !exists {
			return ter.TecOBJECT_NOT_FOUND
		}
	}

	vaultKey := keylet.Vault(accountID, v.GetCommon().SeqProxy())
	if tx.PseudoAccountAddress(view, config.ParentHash, vaultKey.Key) == ([20]byte{}) {
		return ter.TerADDRESS_COLLISION
	}

	return ter.TesSUCCESS
}

// Apply creates the vault ledger entry, its pseudo-account, and the share
// MPTokenIssuance held by that pseudo-account.
// Reference: rippled VaultCreate::doApply.
func (v *VaultCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	accountID := ctx.AccountID
	owner := ctx.Account
	asset := v.Asset
	sequence := v.GetCommon().SeqProxy()

	vaultKey := keylet.Vault(accountID, sequence)
	if exists, _ := ctx.View.Exists(vaultKey); exists {
		return ter.TecDUPLICATE
	}

	// Creating a vault also creates its pseudo-account, so the owner is charged
	// for two objects up front (rippled adjustOwnerCount(+2)).
	newOwnerCount := owner.OwnerCount + 2
	if ctx.PriorBalance() < ctx.AccountReserve(newOwnerCount) {
		return ter.TecINSUFFICIENT_RESERVE
	}

	// Link the vault into the owner's directory.
	ownerDirKey := keylet.OwnerDir(accountID)
	vaultDir, err := state.DirInsert(ctx.View, ownerDirKey, vaultKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = accountID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}

	// Compute the share issuance scale and flags. Scale applies to IOU assets
	// only; XRP and MPT vaults use scale 0.
	scale := uint8(0)
	if !isNativeAsset(asset) && !asset.IsMPT() {
		if v.Scale != nil {
			scale = *v.Scale
		} else {
			scale = vaultDefaultIOUScale
		}
	}
	txFlags := v.GetFlags()
	mptFlags := uint32(0)
	if txFlags&VaultFlagShareNonTransferable == 0 {
		mptFlags |= entry.LsfMPTCanEscrow | entry.LsfMPTCanTrade | entry.LsfMPTCanTransfer
	}
	if txFlags&VaultFlagPrivate != 0 {
		mptFlags |= entry.LsfMPTRequireAuth
	}

	// Create the pseudo-account, inserted before holdings so trust-line creation
	// can read it back.
	pseudoID, pseudo, res := tx.CreatePseudoAccount(ctx, vaultKey.Key, tx.PseudoVaultID)
	if res != ter.TesSUCCESS {
		return res
	}

	// Create the share MPTokenIssuance held by the pseudo-account.
	shareMPTID := keylet.MakeMPTID(1, pseudoID)
	shareKey := keylet.MPTIssuance(shareMPTID)
	issuance := &state.MPTokenIssuanceData{
		Issuer:            pseudoID,
		Sequence:          1,
		OutstandingAmount: 0,
		Flags:             mptFlags,
		AssetScale:        scale,
	}
	if v.MPTokenMetadata != "" {
		issuance.MPTokenMetadata = v.MPTokenMetadata
	}
	if v.DomainID != "" {
		domainID := strings.ToUpper(v.DomainID)
		issuance.DomainID = &domainID
	}
	pseudoDirKey := keylet.OwnerDir(pseudoID)
	shareDir, err := state.DirInsert(ctx.View, pseudoDirKey, shareKey.Key, false, func(d *state.DirectoryNode) {
		d.Owner = pseudoID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	issuance.OwnerNode = shareDir.Page
	pseudo.OwnerCount++

	// Give the pseudo-account an empty holding for the vault asset (a trust line
	// for an IOU asset; nothing for XRP).
	lineDelta, res := addEmptyHolding(ctx, pseudoID, asset)
	if res != ter.TesSUCCESS {
		return res
	}
	pseudo.OwnerCount = uint32(int32(pseudo.OwnerCount) + lineDelta)

	// Post-fixCleanup3_2_0: surface the pseudo-account's holding of the underlying
	// (its MPToken for an MPT asset, its trust line for an IOU) on the share
	// issuance, so a share's transferability/tradability/freeze inherit from the
	// underlying. XRP underlyings leave it unset.
	if ctx.Rules().FixCleanup3_2_0Enabled() && !asset.IsNative() {
		var holdingKey [32]byte
		if asset.IsMPT() {
			id, mok := assetMPTID(asset)
			if !mok {
				return ter.TefINTERNAL
			}
			holdingKey = keylet.MPTokenByID(id, pseudoID).Key
		} else {
			issuerID, derr := state.DecodeAccountID(asset.Issuer)
			if derr != nil {
				return ter.TefINTERNAL
			}
			holdingKey = keylet.Line(pseudoID, issuerID, asset.Currency).Key
		}
		refHex := strings.ToUpper(hex.EncodeToString(holdingKey[:]))
		issuance.ReferenceHolding = &refHex
	}

	// Build and insert the vault entry.
	policy := VaultStrategyFirstComeFirstServe
	if v.WithdrawalPolicy != nil {
		policy = *v.WithdrawalPolicy
	}
	vd := &vaultData{
		Owner:            accountID,
		Account:          pseudoID,
		Sequence:         sequence,
		OwnerNode:        vaultDir.Page,
		ShareMPTID:       shareMPTID,
		Asset:            asset,
		WithdrawalPolicy: policy,
		Scale:            scale,
		Flags:            txFlags & VaultFlagPrivate,
		Data:             v.Data,
	}
	if asset.IsMPT() {
		vd.AssetIsMPT = true
		if id, ok := assetMPTID(asset); ok {
			vd.AssetMPTID = id
		}
	}
	if v.AssetsMaximum != nil {
		maximum, nerr := vaultNumber(*v.AssetsMaximum)
		if nerr != nil {
			return ter.TefINTERNAL
		}
		vd.AssetsMaximum = numberToString(maximum)
	}
	vaultBytes, err := serializeVault(vd)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(vaultKey, vaultBytes); err != nil {
		return ter.TefINTERNAL
	}

	// Persist the pseudo-account's final owner count.
	pseudoBytes, err := state.SerializeAccountRoot(pseudo)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(keylet.Account(pseudoID), pseudoBytes); err != nil {
		return ter.TefINTERNAL
	}

	shareBytes, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(shareKey, shareBytes); err != nil {
		return ter.TefINTERNAL
	}

	// Charge the owner for the vault + pseudo-account before creating the owner's
	// own share MPToken (which bumps the owner count once more).
	owner.OwnerCount = newOwnerCount

	// Explicitly create the vault owner's share MPToken.
	if res := ensureHolderMPToken(ctx, accountID, shareMPTID); res != ter.TesSUCCESS {
		return res
	}
	// A private vault authorizes its owner's shares up front.
	if txFlags&VaultFlagPrivate != 0 {
		if res := authorizeHolderMPToken(ctx, accountID, shareMPTID); res != ter.TesSUCCESS {
			return res
		}
	}

	return ter.TesSUCCESS
}

// decodeBlob decodes a hex-encoded Blob field to its raw bytes.
func decodeBlob(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// isZeroHash reports whether s is a valid 64-char hex string decoding to the
// all-zero 256-bit hash.
func isZeroHash(s string) bool {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

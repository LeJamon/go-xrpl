package mpt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

func init() {
	tx.Register(tx.TypeMPTokenIssuanceCreate, func() tx.Transaction {
		return &MPTokenIssuanceCreate{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "")}
	})
}

// MPTokenIssuanceCreate creates a new multi-purpose token issuance.
type MPTokenIssuanceCreate struct {
	tx.BaseTx

	// AssetScale is the scale for the token (0-10, decimal places)
	AssetScale *uint8 `json:"AssetScale,omitempty" xrpl:"AssetScale,omitempty"`

	// MaximumAmount is the maximum amount that can be issued (optional)
	// Must be within unsigned 63-bit range (0x7FFFFFFFFFFFFFFF)
	MaximumAmount *uint64 `json:"MaximumAmount,omitempty" xrpl:"MaximumAmount,omitempty"`

	// TransferFee is the fee for transfers (0-50000, where 50000 = 50%)
	TransferFee *uint16 `json:"TransferFee,omitempty" xrpl:"TransferFee,omitempty"`

	// MPTokenMetadata is metadata for the token (optional, 1-1024 bytes as hex)
	// Pointer type distinguishes nil (absent) from &"" (present but empty).
	MPTokenMetadata *string `json:"MPTokenMetadata,omitempty" xrpl:"MPTokenMetadata,omitempty"`

	// DomainID is the permissioned domain for this issuance (optional).
	// When set, the issuance is restricted to the specified domain.
	// Requires featurePermissionedDomains AND featureSingleAssetVault.
	// Reference: rippled MPTokenIssuanceCreate.cpp sfDomainID
	DomainID *string `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`

	// hasDomainID tracks whether the DomainID field was present in the parsed JSON.
	hasDomainID bool
}

// UnmarshalJSON handles MaximumAmount as a hex string from the binary codec
// and tracks DomainID field presence.
// UInt64 fields are encoded as hex strings by the binary codec.
func (m *MPTokenIssuanceCreate) UnmarshalJSON(data []byte) error {
	type Alias MPTokenIssuanceCreate
	aux := &struct {
		MaximumAmount *json.RawMessage `json:"MaximumAmount,omitempty"`
		DomainID      *string          `json:"DomainID,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.MaximumAmount != nil {
		val, err := parseUInt64Field(*aux.MaximumAmount)
		if err != nil {
			return fmt.Errorf("invalid MaximumAmount: %w", err)
		}
		m.MaximumAmount = &val
	}
	if aux.DomainID != nil {
		m.DomainID = aux.DomainID
		m.hasDomainID = true
	}
	return nil
}

// parseUInt64Field parses a JSON value that may be a hex string (from binary codec)
// or a numeric value (from JSON). UInt64 fields in XRPL are encoded as hex strings.
func parseUInt64Field(raw json.RawMessage) (uint64, error) {
	// Try as string first (hex from binary codec)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseUint(s, 16, 64)
	}
	// Try as number
	var n uint64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	return 0, fmt.Errorf("cannot parse UInt64 field: %s", string(raw))
}

// NewMPTokenIssuanceCreate creates a new MPTokenIssuanceCreate transaction
func NewMPTokenIssuanceCreate(account string) *MPTokenIssuanceCreate {
	return &MPTokenIssuanceCreate{
		BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, account),
	}
}

// TxType returns the transaction type
func (m *MPTokenIssuanceCreate) TxType() tx.Type {
	return tx.TypeMPTokenIssuanceCreate
}

// Validate validates the MPTokenIssuanceCreate transaction
// Reference: rippled MPTokenIssuanceCreate.cpp preflight
func (m *MPTokenIssuanceCreate) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Any flags other than the valid ones are not allowed
	flags := m.GetFlags()
	if flags&^tfMPTokenIssuanceCreateValidMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for MPTokenIssuanceCreate")
	}

	// Validate TransferFee
	if m.TransferFee != nil {
		if *m.TransferFee > entry.MaxTransferFee {
			return tx.Errorf(tx.TemBAD_TRANSFER_FEE, "TransferFee cannot exceed 50000")
		}
		// If a non-zero TransferFee is set, tfMPTCanTransfer must also be set
		if *m.TransferFee > 0 && (flags&MPTokenIssuanceCreateFlagCanTransfer) == 0 {
			return tx.Errorf(tx.TemMALFORMED, "TransferFee requires tfMPTCanTransfer flag")
		}
	}

	// Validate DomainID
	// Reference: rippled MPTokenIssuanceCreate.cpp:56-64
	if m.hasDomainID && m.DomainID != nil {
		if *m.DomainID == zeroHash256 {
			return tx.Errorf(tx.TemMALFORMED, "DomainID cannot be zero")
		}
		// Domain present implies that MPTokenIssuance is not public
		if flags&MPTokenIssuanceCreateFlagRequireAuth == 0 {
			return tx.Errorf(tx.TemMALFORMED, "DomainID requires tfMPTRequireAuth flag")
		}
	}

	// Validate MPTokenMetadata
	if m.MPTokenMetadata != nil {
		metadataBytes, err := hex.DecodeString(*m.MPTokenMetadata)
		if err != nil {
			return tx.Errorf(tx.TemMALFORMED, "MPTokenMetadata must be valid hex")
		}
		if len(metadataBytes) == 0 || len(metadataBytes) > entry.MaxMPTokenMetadataLength {
			return tx.Errorf(tx.TemMALFORMED, "MPTokenMetadata length must be 1-1024 bytes")
		}
	}

	// Validate MaximumAmount
	if m.MaximumAmount != nil {
		if *m.MaximumAmount == 0 {
			return tx.Errorf(tx.TemMALFORMED, "MaximumAmount cannot be zero")
		}
		if *m.MaximumAmount > entry.MaxMPTokenAmount {
			return tx.Errorf(tx.TemMALFORMED, "MaximumAmount exceeds maximum allowed")
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (m *MPTokenIssuanceCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

// RequiredAmendments returns the amendments required for this transaction type
func (m *MPTokenIssuanceCreate) RequiredAmendments() [][32]byte {
	amendments := [][32]byte{amendment.FeatureMPTokensV1}
	// DomainID requires both PermissionedDomains and SingleAssetVault
	// Reference: rippled MPTokenIssuanceCreate.cpp:34-37
	if m.hasDomainID {
		amendments = append(amendments, amendment.FeaturePermissionedDomains, amendment.FeatureSingleAssetVault)
	}
	return amendments
}

// Apply applies the MPTokenIssuanceCreate transaction to ledger state.
// Reference: rippled MPTokenIssuanceCreate.cpp doApply() / create()
func (m *MPTokenIssuanceCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	// Reserve check
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Compute MPTokenIssuanceID from sequence + account
	sequence := m.GetCommon().SeqProxy()
	mptID := keylet.MakeMPTID(sequence, ctx.AccountID)
	issuanceKey := keylet.MPTIssuance(mptID)

	// Build the issuance entry
	issuanceData := &state.MPTokenIssuanceData{
		Issuer:            ctx.AccountID,
		Sequence:          sequence,
		OutstandingAmount: 0,
		Flags:             m.GetFlags() & ^tx.TfUniversal, // Strip universal flag
	}

	if m.TransferFee != nil {
		issuanceData.TransferFee = *m.TransferFee
	}
	if m.AssetScale != nil {
		issuanceData.AssetScale = *m.AssetScale
	}
	if m.MaximumAmount != nil {
		issuanceData.MaximumAmount = m.MaximumAmount
	}
	if m.MPTokenMetadata != nil && *m.MPTokenMetadata != "" {
		issuanceData.MPTokenMetadata = *m.MPTokenMetadata
	}
	if m.hasDomainID && m.DomainID != nil {
		issuanceData.DomainID = m.DomainID
	}

	// Serialize and insert into ledger
	data, err := state.SerializeMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(issuanceKey, data); err != nil {
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	_, err = state.DirInsert(ctx.View, ownerDirKey, issuanceKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

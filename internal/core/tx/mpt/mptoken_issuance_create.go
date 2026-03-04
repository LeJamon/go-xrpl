package mpt

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/entry"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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
		return errors.New("temINVALID_FLAG: invalid flags for MPTokenIssuanceCreate")
	}

	// Validate TransferFee
	if m.TransferFee != nil {
		if *m.TransferFee > entry.MaxTransferFee {
			return errors.New("temBAD_TRANSFER_FEE: TransferFee cannot exceed 50000")
		}
		// If a non-zero TransferFee is set, tfMPTCanTransfer must also be set
		if *m.TransferFee > 0 && (flags&MPTokenIssuanceCreateFlagCanTransfer) == 0 {
			return errors.New("temMALFORMED: TransferFee requires tfMPTCanTransfer flag")
		}
	}

	// Validate MPTokenMetadata
	if m.MPTokenMetadata != nil {
		metadataBytes, err := hex.DecodeString(*m.MPTokenMetadata)
		if err != nil {
			return errors.New("temMALFORMED: MPTokenMetadata must be valid hex")
		}
		if len(metadataBytes) == 0 || len(metadataBytes) > entry.MaxMPTokenMetadataLength {
			return errors.New("temMALFORMED: MPTokenMetadata length must be 1-1024 bytes")
		}
	}

	// Validate MaximumAmount
	if m.MaximumAmount != nil {
		if *m.MaximumAmount == 0 {
			return errors.New("temMALFORMED: MaximumAmount cannot be zero")
		}
		if *m.MaximumAmount > entry.MaxMPTokenAmount {
			return errors.New("temMALFORMED: MaximumAmount exceeds maximum allowed")
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
	return [][32]byte{amendment.FeatureMPTokensV1}
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
	issuanceData := &sle.MPTokenIssuanceData{
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

	// Serialize and insert into ledger
	data, err := sle.SerializeMPTokenIssuance(issuanceData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(issuanceKey, data); err != nil {
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	_, err = sle.DirInsert(ctx.View, ownerDirKey, issuanceKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

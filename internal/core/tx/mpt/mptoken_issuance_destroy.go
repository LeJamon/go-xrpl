package mpt

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeMPTokenIssuanceDestroy, func() tx.Transaction {
		return &MPTokenIssuanceDestroy{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "")}
	})
}

// MPTokenIssuanceDestroy destroys a multi-purpose token issuance.
type MPTokenIssuanceDestroy struct {
	tx.BaseTx

	// MPTokenIssuanceID is the ID of the issuance to destroy (required)
	// 48-character hex string (24 bytes / Hash192)
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
}

// MPTokenIssuanceDestroy flag mask (only universal flags allowed)
const (
	tfMPTokenIssuanceDestroyValidMask uint32 = tx.TfUniversal
)

// NewMPTokenIssuanceDestroy creates a new MPTokenIssuanceDestroy transaction
func NewMPTokenIssuanceDestroy(account, issuanceID string) *MPTokenIssuanceDestroy {
	return &MPTokenIssuanceDestroy{
		BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, account),
		MPTokenIssuanceID: issuanceID,
	}
}

// TxType returns the transaction type
func (m *MPTokenIssuanceDestroy) TxType() tx.Type {
	return tx.TypeMPTokenIssuanceDestroy
}

// Validate validates the MPTokenIssuanceDestroy transaction
// Reference: rippled MPTokenIssuanceDestroy.cpp preflight
func (m *MPTokenIssuanceDestroy) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	flags := m.GetFlags()
	if flags&^tfMPTokenIssuanceDestroyValidMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for MPTokenIssuanceDestroy")
	}

	// MPTokenIssuanceID is required and must be valid hex
	if m.MPTokenIssuanceID == "" {
		return errors.New("temMALFORMED: MPTokenIssuanceID is required")
	}

	// MPTokenIssuanceID should be 48 hex characters (24 bytes / Hash192)
	if len(m.MPTokenIssuanceID) != 48 {
		return errors.New("temMALFORMED: MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return errors.New("temMALFORMED: MPTokenIssuanceID must be valid hex")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (m *MPTokenIssuanceDestroy) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

// RequiredAmendments returns the amendments required for this transaction type
func (m *MPTokenIssuanceDestroy) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Apply applies the MPTokenIssuanceDestroy transaction to ledger state.
// Reference: rippled MPTokenIssuanceDestroy.cpp preclaim() + doApply()
func (m *MPTokenIssuanceDestroy) Apply(ctx *tx.ApplyContext) tx.Result {
	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return tx.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	// Preclaim: issuance must exist
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	// Parse issuance entry
	issuance, err := sle.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// Cannot destroy with outstanding balances
	if issuance.OutstandingAmount != 0 {
		return tx.TecHAS_OBLIGATIONS
	}
	if issuance.LockedAmount != nil && *issuance.LockedAmount != 0 {
		return tx.TecHAS_OBLIGATIONS
	}

	// doApply: remove from owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	sle.DirRemove(ctx.View, ownerDirKey, issuance.OwnerNode, issuanceKey.Key, false)

	// Erase the issuance
	if err := ctx.View.Erase(issuanceKey); err != nil {
		return tx.TefINTERNAL
	}

	// Decrement owner count
	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

package mpt

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	if err := tx.Register(tx.TypeMPTokenIssuanceDestroy, func() tx.Transaction {
		return &MPTokenIssuanceDestroy{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "")}
	}); err != nil {
		panic(err)
	}
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

func NewMPTokenIssuanceDestroy(account, issuanceID string) *MPTokenIssuanceDestroy {
	return &MPTokenIssuanceDestroy{
		BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, account),
		MPTokenIssuanceID: issuanceID,
	}
}

func (m *MPTokenIssuanceDestroy) TxType() tx.Type {
	return tx.TypeMPTokenIssuanceDestroy
}

// Reference: rippled MPTokenIssuanceDestroy.cpp preflight
func (m *MPTokenIssuanceDestroy) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	flags := m.GetFlags()
	if flags&^tfMPTokenIssuanceDestroyValidMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for MPTokenIssuanceDestroy")
	}

	// MPTokenIssuanceID is required and must be valid hex
	if m.MPTokenIssuanceID == "" {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID is required")
	}

	// MPTokenIssuanceID should be 48 hex characters (24 bytes / Hash192)
	if len(m.MPTokenIssuanceID) != 48 {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	return nil
}

func (m *MPTokenIssuanceDestroy) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

func (m *MPTokenIssuanceDestroy) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Reference: rippled MPTokenIssuanceDestroy.cpp preclaim() + doApply()
func (m *MPTokenIssuanceDestroy) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("mptoken issuance destroy apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
	)

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
		ctx.Log.Warn("mptoken issuance destroy: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	// Parse issuance entry
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance destroy: failed to parse issuance", "error", err)
		return tx.TefINTERNAL
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		ctx.Log.Warn("mptoken issuance destroy: caller is not issuer")
		return tx.TecNO_PERMISSION
	}

	// Cannot destroy with outstanding balances
	if issuance.OutstandingAmount != 0 {
		ctx.Log.Warn("mptoken issuance destroy: has outstanding obligations",
			"outstandingAmount", issuance.OutstandingAmount,
		)
		return tx.TecHAS_OBLIGATIONS
	}
	if issuance.LockedAmount != nil && *issuance.LockedAmount != 0 {
		ctx.Log.Warn("mptoken issuance destroy: has locked obligations",
			"lockedAmount", *issuance.LockedAmount,
		)
		return tx.TecHAS_OBLIGATIONS
	}

	// doApply: remove from owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	state.DirRemove(ctx.View, ownerDirKey, issuance.OwnerNode, issuanceKey.Key, false)

	// Erase the issuance
	if err := ctx.View.Erase(issuanceKey); err != nil {
		ctx.Log.Error("mptoken issuance destroy: failed to erase issuance", "error", err)
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

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
	tx.Register(tx.TypeMPTokenIssuanceSet, func() tx.Transaction {
		return &MPTokenIssuanceSet{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "")}
	})
}

// MPTokenIssuanceSet modifies a multi-purpose token issuance.
type MPTokenIssuanceSet struct {
	tx.BaseTx

	// MPTokenIssuanceID is the ID of the issuance (required)
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`

	// Holder is the holder account (optional)
	// When set, the issuer is modifying a specific holder's MPToken
	Holder string `json:"Holder,omitempty" xrpl:"Holder,omitempty"`
}

// NewMPTokenIssuanceSet creates a new MPTokenIssuanceSet transaction
func NewMPTokenIssuanceSet(account, issuanceID string) *MPTokenIssuanceSet {
	return &MPTokenIssuanceSet{
		BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, account),
		MPTokenIssuanceID: issuanceID,
	}
}

// TxType returns the transaction type
func (m *MPTokenIssuanceSet) TxType() tx.Type {
	return tx.TypeMPTokenIssuanceSet
}

// Validate validates the MPTokenIssuanceSet transaction
// Reference: rippled MPTokenIssuanceSet.cpp preflight
func (m *MPTokenIssuanceSet) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	flags := m.GetFlags()

	// Check for invalid flags
	if flags&^tfMPTokenIssuanceSetValidMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for MPTokenIssuanceSet")
	}

	// Cannot set both tfMPTLock and tfMPTUnlock
	if (flags&MPTokenIssuanceSetFlagLock) != 0 && (flags&MPTokenIssuanceSetFlagUnlock) != 0 {
		return errors.New("temINVALID_FLAG: cannot set both tfMPTLock and tfMPTUnlock")
	}

	// MPTokenIssuanceID is required
	if m.MPTokenIssuanceID == "" {
		return errors.New("temMALFORMED: MPTokenIssuanceID is required")
	}

	if len(m.MPTokenIssuanceID) != 48 {
		return errors.New("temMALFORMED: MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return errors.New("temMALFORMED: MPTokenIssuanceID must be valid hex")
	}

	// Holder cannot be the same as Account
	if m.Holder != "" && m.Holder == m.Account {
		return errors.New("temMALFORMED: Holder cannot be the same as Account")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (m *MPTokenIssuanceSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

// RequiredAmendments returns the amendments required for this transaction type
func (m *MPTokenIssuanceSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Apply applies the MPTokenIssuanceSet transaction to ledger state.
// Reference: rippled MPTokenIssuanceSet.cpp preclaim() + doApply()
func (m *MPTokenIssuanceSet) Apply(ctx *tx.ApplyContext) tx.Result {
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

	issuance, err := sle.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	txFlags := m.GetFlags()

	// Issuance must have CanLock capability for Set to work at all
	// Reference: rippled MPTokenIssuanceSet.cpp preclaim() - without featureSingleAssetVault,
	// if issuance doesn't have lsfMPTCanLock, any Set returns tecNO_PERMISSION
	if issuance.Flags&entry.LsfMPTCanLock == 0 {
		return tx.TecNO_PERMISSION
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	if m.Holder != "" {
		// Targeting a specific holder's MPToken
		return m.setHolderToken(ctx, issuanceKey, issuance, txFlags)
	}
	// Targeting the issuance itself
	return m.setIssuance(ctx, issuanceKey, issuance, txFlags)
}

// setHolderToken modifies a specific holder's MPToken (lock/unlock).
func (m *MPTokenIssuanceSet) setHolderToken(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *sle.MPTokenIssuanceData, txFlags uint32) tx.Result {
	holderID, err := sle.DecodeAccountID(m.Holder)
	if err != nil {
		return tx.TemINVALID
	}

	// Holder account must exist
	// Reference: rippled MPTokenIssuanceSet.cpp:132 — ctx.view.exists(keylet::account(...))
	holderAcctKey := keylet.Account(holderID)
	holderExists, err := ctx.View.Exists(holderAcctKey)
	if err != nil || !holderExists {
		return tx.TecNO_DST
	}

	// MPToken must exist
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := sle.ParseMPToken(tokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Toggle lock/unlock on the token
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		token.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		token.Flags &= ^entry.LsfMPTLocked
	}

	// Serialize and update
	updatedData, err := sle.SerializeMPToken(token)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// setIssuance modifies the issuance itself (lock/unlock).
func (m *MPTokenIssuanceSet) setIssuance(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, issuance *sle.MPTokenIssuanceData, txFlags uint32) tx.Result {
	// Toggle lock/unlock on the issuance
	if txFlags&MPTokenIssuanceSetFlagLock != 0 {
		issuance.Flags |= entry.LsfMPTLocked
	} else if txFlags&MPTokenIssuanceSetFlagUnlock != 0 {
		issuance.Flags &= ^entry.LsfMPTLocked
	}

	// Serialize and update
	updatedData, err := sle.SerializeMPTokenIssuance(issuance)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(issuanceKey, updatedData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

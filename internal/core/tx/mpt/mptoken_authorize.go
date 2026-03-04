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
	tx.Register(tx.TypeMPTokenAuthorize, func() tx.Transaction {
		return &MPTokenAuthorize{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "")}
	})
}

// MPTokenAuthorize authorizes or unauthorizes MPToken operations.
type MPTokenAuthorize struct {
	tx.BaseTx

	// MPTokenIssuanceID is the ID of the issuance (required)
	MPTokenIssuanceID string `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`

	// Holder is the holder account (optional)
	// When the issuer submits: Holder specifies which account to authorize/unauthorize
	// When a holder submits: Holder should not be set (or set to own account to delete)
	Holder string `json:"Holder,omitempty" xrpl:"Holder,omitempty"`
}

// NewMPTokenAuthorize creates a new MPTokenAuthorize transaction
func NewMPTokenAuthorize(account, issuanceID string) *MPTokenAuthorize {
	return &MPTokenAuthorize{
		BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, account),
		MPTokenIssuanceID: issuanceID,
	}
}

// TxType returns the transaction type
func (m *MPTokenAuthorize) TxType() tx.Type {
	return tx.TypeMPTokenAuthorize
}

// Validate validates the MPTokenAuthorize transaction
// Reference: rippled MPTokenAuthorize.cpp preflight
func (m *MPTokenAuthorize) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	flags := m.GetFlags()

	// Check for invalid flags
	if flags&^tfMPTokenAuthorizeValidMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for MPTokenAuthorize")
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
func (m *MPTokenAuthorize) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

// RequiredAmendments returns the amendments required for this transaction type
func (m *MPTokenAuthorize) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Apply applies the MPTokenAuthorize transaction to ledger state.
// Reference: rippled MPTokenAuthorize.cpp preclaim() + doApply() + View.cpp::authorizeMPToken()
func (m *MPTokenAuthorize) Apply(ctx *tx.ApplyContext) tx.Result {
	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return tx.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	issuanceKey := keylet.MPTIssuance(mptID)
	txFlags := m.GetFlags()

	if m.Holder == "" {
		// Holder path: submitter is a holder (not issuer)
		return m.applyHolderPath(ctx, issuanceKey, txFlags)
	}
	// Issuer path: submitter is the issuer, authorizing/unauthorizing a holder
	return m.applyIssuerPath(ctx, issuanceKey, txFlags)
}

// applyHolderPath handles when a holder submits MPTokenAuthorize (no Holder field).
func (m *MPTokenAuthorize) applyHolderPath(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, txFlags uint32) tx.Result {
	tokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)

	if txFlags&MPTokenAuthorizeFlagUnauthorize != 0 {
		// Holder wants to delete their MPToken
		return m.holderUnauthorize(ctx, issuanceKey, tokenKey)
	}
	// Holder wants to create/hold an MPToken
	return m.holderAuthorize(ctx, issuanceKey, tokenKey)
}

// holderUnauthorize handles a holder deleting their MPToken.
func (m *MPTokenAuthorize) holderUnauthorize(ctx *tx.ApplyContext, issuanceKey, tokenKey keylet.Keylet) tx.Result {
	// MPToken must exist
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := sle.ParseMPToken(tokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Cannot delete with non-zero balance
	if token.MPTAmount != 0 {
		return tx.TecHAS_OBLIGATIONS
	}
	if token.LockedAmount != nil && *token.LockedAmount != 0 {
		return tx.TecHAS_OBLIGATIONS
	}

	// Remove from owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	sle.DirRemove(ctx.View, ownerDirKey, token.OwnerNode, tokenKey.Key, false)

	// Erase the MPToken
	if err := ctx.View.Erase(tokenKey); err != nil {
		return tx.TefINTERNAL
	}

	if ctx.Account.OwnerCount > 0 {
		ctx.Account.OwnerCount--
	}

	return tx.TesSUCCESS
}

// holderAuthorize handles a holder creating a new MPToken (opting in to hold).
func (m *MPTokenAuthorize) holderAuthorize(ctx *tx.ApplyContext, issuanceKey, tokenKey keylet.Keylet) tx.Result {
	// Issuance must exist
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := sle.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Issuer cannot hold own token
	if issuance.Issuer == ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// MPToken must not already exist
	exists, _ := ctx.View.Exists(tokenKey)
	if exists {
		return tx.TecDUPLICATE
	}

	// Reserve check - first 2 MPT objects are free (no reserve required)
	// Reference: rippled View.cpp authorizeMPToken() reserve logic
	reserveNeeded := ctx.ReserveForNewObject(ctx.Account.OwnerCount)
	if reserveNeeded > 0 && ctx.Account.Balance < reserveNeeded {
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Build MPToken entry
	tokenData := &sle.MPTokenData{
		Account:           ctx.AccountID,
		MPTokenIssuanceID: decodeMPTIDToHash192(m.MPTokenIssuanceID),
		Flags:             0,
		MPTAmount:         0,
	}

	// Serialize and insert
	data, err := sle.SerializeMPToken(tokenData)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(tokenKey, data); err != nil {
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	_, err = sle.DirInsert(ctx.View, ownerDirKey, tokenKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// applyIssuerPath handles when the issuer submits MPTokenAuthorize with Holder field.
func (m *MPTokenAuthorize) applyIssuerPath(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, txFlags uint32) tx.Result {
	// Decode holder account
	holderID, err := sle.DecodeAccountID(m.Holder)
	if err != nil {
		return tx.TemINVALID
	}

	// Holder account must exist
	// Reference: rippled MPTokenAuthorize.cpp:119 — ctx.view.exists(keylet::account(...))
	holderAcctKey := keylet.Account(holderID)
	holderExists, err := ctx.View.Exists(holderAcctKey)
	if err != nil || !holderExists {
		return tx.TecNO_DST
	}

	// Issuance must exist
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := sle.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		return tx.TecNO_PERMISSION
	}

	// Issuance must have RequireAuth flag
	if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
		return tx.TecNO_AUTH
	}

	// Holder's MPToken must exist
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := sle.ParseMPToken(tokenRaw)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Toggle authorization flag
	if txFlags&MPTokenAuthorizeFlagUnauthorize != 0 {
		token.Flags &= ^entry.LsfMPTAuthorized
	} else {
		token.Flags |= entry.LsfMPTAuthorized
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

// decodeMPTIDToHash192 converts a 48-char hex string to [24]byte.
func decodeMPTIDToHash192(hexID string) [24]byte {
	var id [24]byte
	data, _ := hex.DecodeString(hexID)
	if len(data) >= 24 {
		copy(id[:], data[:24])
	}
	return id
}

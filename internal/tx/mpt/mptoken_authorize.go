package mpt

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

func init() {
	if err := tx.Register(tx.TypeMPTokenAuthorize, func() tx.Transaction {
		return &MPTokenAuthorize{BaseTx: *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "")}
	}); err != nil {
		panic(err)
	}
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

func (m *MPTokenAuthorize) TxType() tx.Type {
	return tx.TypeMPTokenAuthorize
}

// Reference: rippled MPTokenAuthorize.cpp preflight
func (m *MPTokenAuthorize) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	flags := m.GetFlags()

	if flags&^tfMPTokenAuthorizeValidMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid flags for MPTokenAuthorize")
	}

	// MPTokenIssuanceID is required
	if m.MPTokenIssuanceID == "" {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID is required")
	}

	if len(m.MPTokenIssuanceID) != 48 {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return tx.Errorf(tx.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	// Holder cannot be the same as Account
	if m.Holder != "" && m.Holder == m.Account {
		return tx.Errorf(tx.TemMALFORMED, "Holder cannot be the same as Account")
	}

	return nil
}

// HasHolder returns true if the Holder field is present (non-empty).
// Implements tx.holderFieldProvider for the ValidMPTIssuance invariant checker.
func (m *MPTokenAuthorize) HasHolder() bool {
	return m.Holder != ""
}

func (m *MPTokenAuthorize) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

func (m *MPTokenAuthorize) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Reference: rippled MPTokenAuthorize.cpp preclaim() + doApply() + View.cpp::authorizeMPToken()
func (m *MPTokenAuthorize) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("mptoken authorize apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
		"holder", m.Holder,
	)

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
		ctx.Log.Warn("mptoken authorize: token not found for holder unauthorize")
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse token", "error", err)
		return tx.TefINTERNAL
	}

	// Cannot delete with non-zero balance
	if token.MPTAmount != 0 {
		ctx.Log.Warn("mptoken authorize: cannot delete token with balance",
			"amount", token.MPTAmount,
		)
		return tx.TecHAS_OBLIGATIONS
	}
	if token.LockedAmount != nil && *token.LockedAmount != 0 {
		ctx.Log.Warn("mptoken authorize: cannot delete token with locked amount",
			"lockedAmount", *token.LockedAmount,
		)
		return tx.TecHAS_OBLIGATIONS
	}

	// With featureSingleAssetVault, a locked MPToken cannot be deleted.
	// Reference: rippled MPTokenAuthorize.cpp:95-97
	if ctx.Rules().Enabled(amendment.FeatureSingleAssetVault) &&
		token.Flags&entry.LsfMPTLocked != 0 {
		return tx.TecNO_PERMISSION
	}

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	state.DirRemove(ctx.View, ownerDirKey, token.OwnerNode, tokenKey.Key, false)

	// Erase the MPToken
	if err := ctx.View.Erase(tokenKey); err != nil {
		ctx.Log.Error("mptoken authorize: failed to erase token", "error", err)
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
		ctx.Log.Warn("mptoken authorize: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse issuance", "error", err)
		return tx.TefINTERNAL
	}

	// Issuer cannot hold own token
	if issuance.Issuer == ctx.AccountID {
		ctx.Log.Warn("mptoken authorize: issuer cannot hold own token")
		return tx.TecNO_PERMISSION
	}

	// MPToken must not already exist
	exists, _ := ctx.View.Exists(tokenKey)
	if exists {
		ctx.Log.Warn("mptoken authorize: token already exists")
		return tx.TecDUPLICATE
	}

	// Reserve check - first 2 MPT objects are free (no reserve required)
	// Reference: rippled View.cpp authorizeMPToken() reserve logic
	reserveNeeded := ctx.ReserveForNewObject(ctx.Account.OwnerCount)
	if reserveNeeded > 0 && ctx.Account.Balance < reserveNeeded {
		ctx.Log.Warn("mptoken authorize: insufficient reserve",
			"balance", ctx.Account.Balance,
			"reserve", reserveNeeded,
		)
		return tx.TecINSUFFICIENT_RESERVE
	}

	// Build MPToken entry
	tokenData := &state.MPTokenData{
		Account:           ctx.AccountID,
		MPTokenIssuanceID: decodeMPTIDToHash192(m.MPTokenIssuanceID),
		Flags:             0,
		MPTAmount:         0,
	}

	// Serialize and insert
	data, err := state.SerializeMPToken(tokenData)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to serialize token", "error", err)
		return tx.TefINTERNAL
	}
	if err := ctx.View.Insert(tokenKey, data); err != nil {
		ctx.Log.Error("mptoken authorize: failed to insert token", "error", err)
		return tx.TefINTERNAL
	}

	// Insert into owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	_, err = state.DirInsert(ctx.View, ownerDirKey, tokenKey.Key, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		ctx.Log.Error("mptoken authorize: directory full", "error", err)
		return tx.TecDIR_FULL
	}

	ctx.Account.OwnerCount++
	return tx.TesSUCCESS
}

// applyIssuerPath handles when the issuer submits MPTokenAuthorize with Holder field.
func (m *MPTokenAuthorize) applyIssuerPath(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, txFlags uint32) tx.Result {
	// Decode holder account
	holderID, err := state.DecodeAccountID(m.Holder)
	if err != nil {
		return tx.TemINVALID
	}

	// Holder account must exist
	// Reference: rippled MPTokenAuthorize.cpp:119 — ctx.view.exists(keylet::account(...))
	holderAcctKey := keylet.Account(holderID)
	holderExists, err := ctx.View.Exists(holderAcctKey)
	if err != nil || !holderExists {
		ctx.Log.Warn("mptoken authorize: holder account does not exist",
			"holder", m.Holder,
		)
		return tx.TecNO_DST
	}

	// Issuance must exist
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		ctx.Log.Warn("mptoken authorize: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse issuance", "error", err)
		return tx.TefINTERNAL
	}

	// Caller must be the issuer
	if issuance.Issuer != ctx.AccountID {
		ctx.Log.Warn("mptoken authorize: caller is not issuer")
		return tx.TecNO_PERMISSION
	}

	// Issuance must have RequireAuth flag
	if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
		ctx.Log.Warn("mptoken authorize: issuance does not require auth")
		return tx.TecNO_AUTH
	}

	// Holder's MPToken must exist
	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		ctx.Log.Warn("mptoken authorize: holder token not found",
			"holder", m.Holder,
		)
		return tx.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse holder token", "error", err)
		return tx.TefINTERNAL
	}

	// Toggle authorization flag
	if txFlags&MPTokenAuthorizeFlagUnauthorize != 0 {
		token.Flags &= ^entry.LsfMPTAuthorized
	} else {
		token.Flags |= entry.LsfMPTAuthorized
	}

	// Serialize and update
	updatedData, err := state.SerializeMPToken(token)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to serialize token", "error", err)
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedData); err != nil {
		ctx.Log.Error("mptoken authorize: failed to update token", "error", err)
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

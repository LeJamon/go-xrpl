package mpt

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

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

// GetFlagsMask adopts the engine FlagsMasker seam with the MPTokenAuthorize
// invalid-flags mask (rippled MPTokenAuthorize::getFlagsMask =
// tfMPTokenAuthorizeMask), checked at preflight0.
func (m *MPTokenAuthorize) GetFlagsMask(rules *amendment.Rules) uint32 {
	return ^tfMPTokenAuthorizeValidMask
}

// Reference: rippled MPTokenAuthorize.cpp preflight
func (m *MPTokenAuthorize) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// MPTokenIssuanceID is required
	if m.MPTokenIssuanceID == "" {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID is required")
	}

	if len(m.MPTokenIssuanceID) != 48 {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	// Holder cannot be the same as Account
	if m.Holder != "" && m.Holder == m.Account {
		return ter.Errorf(ter.TemMALFORMED, "Holder cannot be the same as Account")
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

// Preclaim runs MPTokenAuthorize's ledger-aware checks in rippled
// MPTokenAuthorize::preclaim order. Holder path (no Holder field): a delete
// (tfMPTUnauthorize) requires the MPToken (tecOBJECT_NOT_FOUND) with a zero
// balance and zero locked amount (tecHAS_OBLIGATIONS) and, under SingleAssetVault,
// an unlocked token (tecNO_PERMISSION); an authorize requires the issuance
// (tecOBJECT_NOT_FOUND), a non-issuer submitter (tecNO_PERMISSION), and no existing
// MPToken (tecDUPLICATE). Issuer path (Holder present): holder account
// (tecNO_DST), issuance (tecOBJECT_NOT_FOUND), issuer match (tecNO_PERMISSION),
// RequireAuth (tecNO_AUTH), and the holder MPToken (tecOBJECT_NOT_FOUND). The
// reserve check and the create/delete/toggle mutations stay in Apply. (The
// pre-existing gap where rippled additionally rejects a pseudo-account holder on
// the issuer path is left unchanged — that is a separate behaviour fix.)
func (m *MPTokenAuthorize) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	var mptID [24]byte
	b, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(b) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], b)
	issuanceKey := keylet.MPTIssuance(mptID)
	txFlags := m.GetFlags()
	accountID, aerr := state.DecodeAccountID(m.Account)
	if aerr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}

	if m.Holder == "" {
		tokenKey := keylet.MPToken(issuanceKey.Key, accountID)
		if txFlags&MPTokenAuthorizeFlagUnauthorize != 0 {
			tokenRaw, rerr := view.Read(tokenKey)
			if rerr != nil || tokenRaw == nil {
				return ter.TecOBJECT_NOT_FOUND
			}
			token, perr := state.ParseMPToken(tokenRaw)
			if perr != nil {
				return ter.TefINTERNAL
			}
			if token.MPTAmount != 0 {
				return ter.TecHAS_OBLIGATIONS
			}
			if token.LockedAmount != nil && *token.LockedAmount != 0 {
				return ter.TecHAS_OBLIGATIONS
			}
			if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureSingleAssetVault) &&
				token.Flags&entry.LsfMPTLocked != 0 {
				return ter.TecNO_PERMISSION
			}
			if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureConfidentialTransfer) &&
				(len(token.ConfidentialBalanceInbox) != 0 || len(token.ConfidentialBalanceSpending) != 0) {
				issuanceRaw, rerr := view.Read(issuanceKey)
				if rerr != nil {
					return ter.TefINTERNAL
				}
				if issuanceRaw != nil {
					issuance, perr := state.ParseMPTokenIssuance(issuanceRaw)
					if perr != nil {
						return ter.TefINTERNAL
					}
					if issuance.ConfidentialOutstandingAmount != 0 {
						return ter.TecHAS_OBLIGATIONS
					}
				}
			}
			return ter.TesSUCCESS
		}
		issuanceRaw, rerr := view.Read(issuanceKey)
		if rerr != nil || issuanceRaw == nil {
			return ter.TecOBJECT_NOT_FOUND
		}
		issuance, perr := state.ParseMPTokenIssuance(issuanceRaw)
		if perr != nil {
			return ter.TefINTERNAL
		}
		if issuance.Issuer == accountID {
			return ter.TecNO_PERMISSION
		}
		if exists, _ := view.Exists(tokenKey); exists {
			return ter.TecDUPLICATE
		}
		return ter.TesSUCCESS
	}

	holderID, herr := state.DecodeAccountID(m.Holder)
	if herr != nil {
		return ter.TemINVALID
	}
	if exists, _ := view.Exists(keylet.Account(holderID)); !exists {
		return ter.TecNO_DST
	}
	issuanceRaw, rerr := view.Read(issuanceKey)
	if rerr != nil || issuanceRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuance, perr := state.ParseMPTokenIssuance(issuanceRaw)
	if perr != nil {
		return ter.TefINTERNAL
	}
	if issuance.Issuer != accountID {
		return ter.TecNO_PERMISSION
	}
	if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
		return ter.TecNO_AUTH
	}
	if exists, _ := view.Exists(keylet.MPToken(issuanceKey.Key, holderID)); !exists {
		return ter.TecOBJECT_NOT_FOUND
	}
	return ter.TesSUCCESS
}

// Reference: rippled MPTokenAuthorize.cpp doApply() + View.cpp::authorizeMPToken();
// the ledger-aware gates live in Preclaim.
func (m *MPTokenAuthorize) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("mptoken authorize apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
		"holder", m.Holder,
	)

	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	issuanceKey := keylet.MPTIssuance(mptID)
	txFlags := m.GetFlags()

	if m.Holder == "" {
		// Holder path: submitter is a holder (not issuer)
		return m.applyHolderPath(ctx, mptID, issuanceKey, txFlags)
	}
	// Issuer path: submitter is the issuer, authorizing/unauthorizing a holder
	return m.applyIssuerPath(ctx, issuanceKey, txFlags)
}

// applyHolderPath handles when a holder submits MPTokenAuthorize (no Holder field).
func (m *MPTokenAuthorize) applyHolderPath(ctx *tx.ApplyContext, mptID [24]byte, issuanceKey keylet.Keylet, txFlags uint32) ter.Result {
	tokenKey := keylet.MPToken(issuanceKey.Key, ctx.AccountID)

	if txFlags&MPTokenAuthorizeFlagUnauthorize != 0 {
		// Holder wants to delete their MPToken
		return m.holderUnauthorize(ctx, tokenKey)
	}
	// Holder wants to create/hold an MPToken
	return m.holderAuthorize(ctx, mptID, issuanceKey, tokenKey)
}

// holderUnauthorize handles a holder deleting their MPToken. The existence,
// obligations, and lock gates run in Preclaim; the token is read here for its
// OwnerNode.
func (m *MPTokenAuthorize) holderUnauthorize(ctx *tx.ApplyContext, tokenKey keylet.Keylet) ter.Result {
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse token", "error", err)
		return ter.TefINTERNAL
	}

	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	if res, err := state.DirRemove(ctx.View, ownerDirKey, token.OwnerNode, tokenKey.Key, false); err != nil || !res.Success {
		ctx.Log.Error("mptoken authorize: failed to remove from owner directory", "error", err)
		return ter.TecINTERNAL
	}

	// Erase the MPToken
	if err := ctx.View.Erase(tokenKey); err != nil {
		ctx.Log.Error("mptoken authorize: failed to erase token", "error", err)
		return ter.TefINTERNAL
	}

	if result := tx.DecreaseOwnerCountForObject(ctx, ctx.AccountID, ctx.Account, tokenRaw, "Sponsor", 1); result != ter.TesSUCCESS {
		return result
	}

	return ter.TesSUCCESS
}

// holderAuthorize handles a holder creating a new MPToken (opting in to hold).
// The issuance existence, non-issuer, and no-duplicate gates run in Preclaim.
func (m *MPTokenAuthorize) holderAuthorize(ctx *tx.ApplyContext, mptID [24]byte, issuanceKey, tokenKey keylet.Keylet) ter.Result {
	// Reserve check against the prior balance (before fee deduction).
	// The first 2 MPT objects are free, like trust lines, so
	// ReserveForNewObject returns 0 when fewer than 2 objects are owned.
	ownerCount := tx.OwnerCountForReserve(ctx.Account, ctx.Rules())
	freeHolding := !tx.TransactionHasReserveSponsor(m.GetCommon()) && ownerCount < 2
	if result := tx.CheckReserve(ctx, m.GetCommon(), ctx.AccountID, ctx.Account, ctx.PriorBalance(), tx.ReserveAdjustment{OwnerCountDelta: 1}, ter.TecINSUFFICIENT_RESERVE); !freeHolding && result != ter.TesSUCCESS {
		ctx.Log.Warn("mptoken authorize: insufficient reserve",
			"priorBalance", ctx.PriorBalance(),
		)
		return result
	}

	// Build MPToken entry
	tokenData := &state.MPTokenData{
		Account:           ctx.AccountID,
		MPTokenIssuanceID: mptID,
		Flags:             0,
		MPTAmount:         0,
	}

	// Insert into owner directory first so sfOwnerNode records the actual page.
	// Reference: rippled MPTokenAuthorize.cpp:161-171.
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	dirResult, err := state.DirInsert(ctx.View, ownerDirKey, tokenKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = ctx.AccountID
	})
	if err != nil {
		ctx.Log.Error("mptoken authorize: directory full", "error", err)
		return ter.TecDIR_FULL
	}
	tokenData.OwnerNode = dirResult.Page
	sponsor, result := tx.IncreaseOwnerCount(ctx, m.GetCommon(), ctx.AccountID, ctx.Account, 1)
	if result != ter.TesSUCCESS {
		return result
	}
	tokenData.Sponsor = sponsor

	// Serialize and insert
	data, err := state.SerializeMPToken(tokenData)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to serialize token", "error", err)
		return ter.TefINTERNAL
	}
	if err := ctx.View.Insert(tokenKey, data); err != nil {
		ctx.Log.Error("mptoken authorize: failed to insert token", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// applyIssuerPath handles when the issuer submits MPTokenAuthorize with Holder field.
func (m *MPTokenAuthorize) applyIssuerPath(ctx *tx.ApplyContext, issuanceKey keylet.Keylet, txFlags uint32) ter.Result {
	// Decode holder account. Existence, issuance, issuer, RequireAuth, and holder
	// MPToken gates run in Preclaim; the token is read here for the flag toggle.
	holderID, err := state.DecodeAccountID(m.Holder)
	if err != nil {
		return ter.TemINVALID
	}

	tokenKey := keylet.MPToken(issuanceKey.Key, holderID)
	tokenRaw, err := ctx.View.Read(tokenKey)
	if err != nil || tokenRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}

	token, err := state.ParseMPToken(tokenRaw)
	if err != nil {
		ctx.Log.Error("mptoken authorize: failed to parse holder token", "error", err)
		return ter.TefINTERNAL
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
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(tokenKey, updatedData); err != nil {
		ctx.Log.Error("mptoken authorize: failed to update token", "error", err)
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

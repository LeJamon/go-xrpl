package mpt

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

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

// GetFlagsMask adopts the engine FlagsMasker seam. MPTokenIssuanceDestroy
// defines no type-specific flags, so it uses the base universal mask, checked at
// preflight0.
func (m *MPTokenIssuanceDestroy) GetFlagsMask(rules *amendment.Rules) uint32 {
	return ^tfMPTokenIssuanceDestroyValidMask
}

// Reference: rippled MPTokenIssuanceDestroy.cpp preflight
func (m *MPTokenIssuanceDestroy) Validate() error {
	if err := m.BaseTx.Validate(); err != nil {
		return err
	}

	// MPTokenIssuanceID is required and must be valid hex
	if m.MPTokenIssuanceID == "" {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID is required")
	}

	// MPTokenIssuanceID should be 48 hex characters (24 bytes / Hash192)
	if len(m.MPTokenIssuanceID) != 48 {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be 48 hex characters")
	}

	if _, err := hex.DecodeString(m.MPTokenIssuanceID); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "MPTokenIssuanceID must be valid hex")
	}

	return nil
}

func (m *MPTokenIssuanceDestroy) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(m)
}

func (m *MPTokenIssuanceDestroy) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureMPTokensV1}
}

// Preclaim runs MPTokenIssuanceDestroy's ledger-aware checks: the issuance must
// exist (tecOBJECT_NOT_FOUND), the caller must be its issuer (tecNO_PERMISSION),
// and it must carry no outstanding or locked balances (tecHAS_OBLIGATIONS).
// Extracting these from Apply makes them visible to the preclaim-only paths (TxQ
// admission, simulate), matching rippled where they live in
// MPTokenIssuanceDestroy::preclaim.
// Reference: rippled MPTokenIssuanceDestroy.cpp preclaim().
func (m *MPTokenIssuanceDestroy) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	var mptID [24]byte
	issuanceIDBytes, decErr := hex.DecodeString(m.MPTokenIssuanceID)
	if decErr != nil || len(issuanceIDBytes) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	issuanceRaw, readErr := view.Read(keylet.MPTIssuance(mptID))
	if readErr != nil || issuanceRaw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	issuance, parseErr := state.ParseMPTokenIssuance(issuanceRaw)
	if parseErr != nil {
		return ter.TefINTERNAL
	}
	accountID, acctErr := state.DecodeAccountID(m.Account)
	if acctErr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if issuance.Issuer != accountID {
		return ter.TecNO_PERMISSION
	}
	if issuance.OutstandingAmount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	if issuance.LockedAmount != nil && *issuance.LockedAmount != 0 {
		return ter.TecHAS_OBLIGATIONS
	}
	return ter.TesSUCCESS
}

// Reference: rippled MPTokenIssuanceDestroy.cpp doApply()
func (m *MPTokenIssuanceDestroy) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("mptoken issuance destroy apply",
		"account", m.Account,
		"issuanceID", m.MPTokenIssuanceID,
	)

	// Parse MPTokenIssuanceID
	var mptID [24]byte
	issuanceIDBytes, err := hex.DecodeString(m.MPTokenIssuanceID)
	if err != nil || len(issuanceIDBytes) != 24 {
		return ter.TemINVALID
	}
	copy(mptID[:], issuanceIDBytes)

	// Preclaim: issuance must exist
	issuanceKey := keylet.MPTIssuance(mptID)
	issuanceRaw, err := ctx.View.Read(issuanceKey)
	if err != nil || issuanceRaw == nil {
		ctx.Log.Warn("mptoken issuance destroy: issuance not found",
			"issuanceID", m.MPTokenIssuanceID,
		)
		return ter.TecOBJECT_NOT_FOUND
	}

	// Parse issuance entry
	issuance, err := state.ParseMPTokenIssuance(issuanceRaw)
	if err != nil {
		ctx.Log.Error("mptoken issuance destroy: failed to parse issuance", "error", err)
		return ter.TefINTERNAL
	}

	// doApply: remove from owner directory
	ownerDirKey := keylet.OwnerDir(ctx.AccountID)
	if res, err := state.DirRemove(ctx.View, ownerDirKey, issuance.OwnerNode, issuanceKey.Key, false); err != nil || !res.Success {
		ctx.Log.Error("mptoken issuance destroy: failed to remove from owner directory", "error", err)
		return ter.TefBAD_LEDGER
	}

	// Erase the issuance
	if err := ctx.View.Erase(issuanceKey); err != nil {
		ctx.Log.Error("mptoken issuance destroy: failed to erase issuance", "error", err)
		return ter.TefINTERNAL
	}

	if result := tx.DecreaseOwnerCountForObject(ctx, ctx.AccountID, ctx.Account, issuanceRaw, "Sponsor", 1); result != ter.TesSUCCESS {
		return result
	}

	return ter.TesSUCCESS
}

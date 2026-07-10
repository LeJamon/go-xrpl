package oracle

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// OracleDelete deletes a price oracle.
type OracleDelete struct {
	tx.BaseTx

	// OracleDocumentID identifies the oracle to delete (required)
	OracleDocumentID uint32 `json:"OracleDocumentID" xrpl:"OracleDocumentID"`
}

// NewOracleDelete creates a new OracleDelete transaction
func NewOracleDelete(account string, oracleDocID uint32) *OracleDelete {
	return &OracleDelete{
		BaseTx:           *tx.NewBaseTx(tx.TypeOracleDelete, account),
		OracleDocumentID: oracleDocID,
	}
}

func (o *OracleDelete) TxType() tx.Type {
	return tx.TypeOracleDelete
}

// Validate matches rippled's DeleteOracle::preflight()
// GetFlagsMask adopts the engine FlagsMasker seam. OracleDelete defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (o *OracleDelete) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (o *OracleDelete) Validate() error {
	return o.BaseTx.Validate()
}

func (o *OracleDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

func (o *OracleDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePriceOracle}
}

// Preclaim runs OracleDelete's ledger-aware check: the oracle must exist
// (tecNO_ENTRY). Extracting it from Apply makes it visible to the preclaim-only
// paths (TxQ admission, simulate), matching rippled where it lives in
// DeleteOracle::preclaim. Ownership is implicit in the oracle keylet (owner ==
// Account), so no separate owner check is needed.
// Reference: rippled DeleteOracle.cpp preclaim().
func (o *OracleDelete) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	accountID, err := state.DecodeAccountID(o.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	exists, existsErr := view.Exists(keylet.Oracle(accountID, o.OracleDocumentID))
	if existsErr != nil {
		return ter.TefINTERNAL
	}
	if !exists {
		return ter.TecNO_ENTRY
	}
	return ter.TesSUCCESS
}

// Apply applies an OracleDelete transaction to the ledger state.
// Reference: rippled DeleteOracle.cpp doApply().
func (o *OracleDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("oracle delete apply",
		"account", o.Account,
		"oracleDocumentID", o.OracleDocumentID,
	)

	// --- Preclaim ---
	// Reference: rippled DeleteOracle.cpp preclaim lines 47-69
	oracleKey := keylet.Oracle(ctx.AccountID, o.OracleDocumentID)
	oracleData, err := ctx.View.Read(oracleKey)
	if err != nil || oracleData == nil {
		ctx.Log.Warn("oracle delete: oracle not found",
			"oracleDocumentID", o.OracleDocumentID,
		)
		return ter.TecNO_ENTRY
	}

	oracle, err := state.ParseOracle(oracleData)
	if err != nil {
		ctx.Log.Error("oracle delete: failed to parse oracle", "error", err)
		return ter.TefINTERNAL
	}

	// --- doApply ---
	// Reference: rippled DeleteOracle.cpp deleteOracle lines 71-102
	return DeleteOracleFromView(ctx.View, oracleKey, oracle, ctx.AccountID, &ctx.Account.OwnerCount)
}

// DeleteOracleFromView deletes an oracle from the ledger view.
// This is a shared helper used by both OracleDelete.Apply() and AccountDelete cascade.
// If ownerCount is nil, the OwnerCount adjustment is skipped (account deletion case).
// Reference: rippled DeleteOracle.cpp deleteOracle()
func DeleteOracleFromView(view tx.LedgerView, oracleKey keylet.Keylet, oracle *state.OracleData, accountID [20]byte, ownerCount *uint32) ter.Result {
	// DirRemove from owner directory
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err := state.DirRemove(view, ownerDirKey, oracle.OwnerNode, oracleKey.Key, true)
	if err != nil {
		return ter.TefBAD_LEDGER
	}

	// Adjust OwnerCount
	if ownerCount != nil {
		count := uint32(1)
		if len(oracle.PriceDataSeries) > 5 {
			count = 2
		}
		if *ownerCount >= count {
			*ownerCount -= count
		}
	}

	// Erase oracle SLE
	if err := view.Erase(oracleKey); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

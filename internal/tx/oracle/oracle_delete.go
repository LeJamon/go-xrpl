package oracle

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
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
func (o *OracleDelete) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	if err := tx.CheckFlags(o.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	return nil
}

func (o *OracleDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

func (o *OracleDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePriceOracle}
}

// Apply applies an OracleDelete transaction to the ledger state.
// Combines rippled's DeleteOracle::preclaim() and DeleteOracle::doApply().
// Reference: rippled DeleteOracle.cpp
func (o *OracleDelete) Apply(ctx *tx.ApplyContext) tx.Result {
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
		return tx.TecNO_ENTRY
	}

	oracle, err := state.ParseOracle(oracleData)
	if err != nil {
		ctx.Log.Error("oracle delete: failed to parse oracle", "error", err)
		return tx.TefINTERNAL
	}

	// --- doApply ---
	// Reference: rippled DeleteOracle.cpp deleteOracle lines 71-102
	return DeleteOracleFromView(ctx.View, oracleKey, oracle, ctx.AccountID, &ctx.Account.OwnerCount)
}

// DeleteOracleFromView deletes an oracle from the ledger view.
// This is a shared helper used by both OracleDelete.Apply() and AccountDelete cascade.
// If ownerCount is nil, the OwnerCount adjustment is skipped (account deletion case).
// Reference: rippled DeleteOracle.cpp deleteOracle()
func DeleteOracleFromView(view state.LedgerView, oracleKey keylet.Keylet, oracle *state.OracleData, accountID [20]byte, ownerCount *uint32) tx.Result {
	// DirRemove from owner directory
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err := state.DirRemove(view, ownerDirKey, oracle.OwnerNode, oracleKey.Key, true)
	if err != nil {
		return tx.TefBAD_LEDGER
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
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

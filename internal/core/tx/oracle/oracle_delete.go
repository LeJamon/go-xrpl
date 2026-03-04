package oracle

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeOracleDelete, func() tx.Transaction {
		return &OracleDelete{BaseTx: *tx.NewBaseTx(tx.TypeOracleDelete, "")}
	})
}

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

// TxType returns the transaction type
func (o *OracleDelete) TxType() tx.Type {
	return tx.TypeOracleDelete
}

// Validate validates the OracleDelete transaction (preflight validation)
// This matches rippled's DeleteOracle::preflight()
func (o *OracleDelete) Validate() error {
	if err := o.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - only universal flags allowed
	if o.Flags != nil && *o.Flags&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags set")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (o *OracleDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// RequiredAmendments returns the amendments required for this transaction type
func (o *OracleDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePriceOracle}
}

// Apply applies an OracleDelete transaction to the ledger state.
// Combines rippled's DeleteOracle::preclaim() and DeleteOracle::doApply().
// Reference: rippled DeleteOracle.cpp
func (o *OracleDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	// --- Preclaim ---
	// Reference: rippled DeleteOracle.cpp preclaim lines 47-69
	oracleKey := keylet.Oracle(ctx.AccountID, o.OracleDocumentID)
	oracleData, err := ctx.View.Read(oracleKey)
	if err != nil || oracleData == nil {
		return tx.TecNO_ENTRY
	}

	oracle, err := sle.ParseOracle(oracleData)
	if err != nil {
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
func DeleteOracleFromView(view sle.LedgerView, oracleKey keylet.Keylet, oracle *sle.OracleData, accountID [20]byte, ownerCount *uint32) tx.Result {
	// DirRemove from owner directory
	ownerDirKey := keylet.OwnerDir(accountID)
	_, err := sle.DirRemove(view, ownerDirKey, oracle.OwnerNode, oracleKey.Key, true)
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

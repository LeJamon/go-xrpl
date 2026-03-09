package amm

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
)

func init() {
	tx.Register(tx.TypeAMMDelete, func() tx.Transaction {
		return &AMMDelete{BaseTx: *tx.NewBaseTx(tx.TypeAMMDelete, "")}
	})
}

// AMMDelete deletes an empty AMM.
type AMMDelete struct {
	tx.BaseTx

	// Asset identifies the first asset of the AMM (required)
	Asset tx.Asset `json:"Asset" xrpl:"Asset,asset"`

	// Asset2 identifies the second asset of the AMM (required)
	Asset2 tx.Asset `json:"Asset2" xrpl:"Asset2,asset"`
}

// NewAMMDelete creates a new AMMDelete transaction
func NewAMMDelete(account string, asset, asset2 tx.Asset) *AMMDelete {
	return &AMMDelete{
		BaseTx: *tx.NewBaseTx(tx.TypeAMMDelete, account),
		Asset:  asset,
		Asset2: asset2,
	}
}

// TxType returns the transaction type
func (a *AMMDelete) TxType() tx.Type {
	return tx.TypeAMMDelete
}

// Validate validates the AMMDelete transaction
// Reference: rippled AMMDelete.cpp preflight
func (a *AMMDelete) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Check flags - no flags are valid for AMMDelete
	if a.GetFlags()&tfAMMDeleteMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags for AMMDelete")
	}

	// Validate asset pair
	if a.Asset.Currency == "" {
		return errors.New("temMALFORMED: Asset is required")
	}

	if a.Asset2.Currency == "" {
		return errors.New("temMALFORMED: Asset2 is required")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (a *AMMDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

// RequiredAmendments returns the amendments required for this transaction type
func (a *AMMDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Apply applies the AMMDelete transaction to ledger state.
// Reference: rippled AMMDelete.cpp preclaim + doApply
func (a *AMMDelete) Apply(ctx *tx.ApplyContext) tx.Result {
	// Preclaim: AMM must exist and be empty
	// Reference: rippled AMMDelete.cpp preclaim (line 49-63)
	ammKey := computeAMMKeylet(a.Asset, a.Asset2)
	ammRawData, err := ctx.View.Read(ammKey)
	if err != nil || ammRawData == nil {
		return TerNO_AMM
	}

	amm, err := parseAMMData(ammRawData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// AMM must be empty (LPTokenBalance == 0)
	if !amm.LPTokenBalance.IsZero() {
		return tx.TecAMM_NOT_EMPTY
	}

	// doApply: delete the AMM account
	// Reference: rippled AMMDelete.cpp doApply (line 67-79)
	return DeleteAMMAccount(ctx.View, a.Asset, a.Asset2)
}

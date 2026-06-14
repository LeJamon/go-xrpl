package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

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

func (a *AMMDelete) TxType() tx.Type {
	return tx.TypeAMMDelete
}

// Reference: rippled AMMDelete.cpp preflight
func (a *AMMDelete) Validate() error {
	if err := a.BaseTx.Validate(); err != nil {
		return err
	}

	// Reference: rippled AMMDelete.cpp preflight lines 39-43. rippled validates
	// nothing else here; a missing/invalid asset pair surfaces as terNO_AMM when
	// the AMM lookup fails in preclaim.
	if a.GetFlags()&tfAMMDeleteMask != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "invalid flags for AMMDelete")
	}

	return nil
}

func (a *AMMDelete) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(a)
}

func (a *AMMDelete) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureAMM, amendment.FeatureFixUniversalNumber}
}

// Preclaim requires the AMM to exist and be empty.
// Reference: rippled AMMDelete.cpp preclaim
func (a *AMMDelete) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	amm, _, result := readAMM(view, a.Asset, a.Asset2)
	if result != ter.TesSUCCESS {
		return result
	}
	if !amm.LPTokenBalance.IsZero() {
		return ter.TecAMM_NOT_EMPTY
	}
	return ter.TesSUCCESS
}

// Reference: rippled AMMDelete.cpp doApply
func (a *AMMDelete) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("amm delete apply",
		"account", a.Account,
		"asset", a.Asset,
		"asset2", a.Asset2,
	)

	return DeleteAMMAccount(ctx.View, a.Asset, a.Asset2)
}

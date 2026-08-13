package depositpreauth

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDepositPreauthPseudoTargetCleanup330(t *testing.T) {
	ownerID := [20]byte{1}
	pseudoID := [20]byte{2}
	owner := state.EncodeAccountIDSafe(ownerID)
	pseudo := state.EncodeAccountIDSafe(pseudoID)
	view := newFaultView()
	pseudoRaw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: pseudo,
		VaultID: [32]byte{1},
	})
	require.NoError(t, err)
	view.data[keylet.Account(pseudoID).Key] = pseudoRaw

	newTx := func() *DepositPreauth {
		txn := NewDepositPreauth(owner)
		txn.Authorize = pseudo
		return txn
	}
	off := amendment.NewRules(nil)
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	require.Equal(t, ter.TesSUCCESS, newTx().Preclaim(view, tx.EngineConfig{Rules: off}))
	require.Equal(t, ter.TecPSEUDO_ACCOUNT, newTx().Preclaim(view, tx.EngineConfig{Rules: on}))

	preauthKey := keylet.DepositPreauth(ownerID, pseudoID)
	view.data[preauthKey.Key] = []byte{1}
	require.Equal(t, ter.TecPSEUDO_ACCOUNT, newTx().Preclaim(view, tx.EngineConfig{Rules: on}))

	unauthorize := NewDepositPreauth(owner)
	unauthorize.Unauthorize = pseudo
	require.Equal(t, ter.TesSUCCESS, unauthorize.Preclaim(view, tx.EngineConfig{Rules: on}))
}

func TestDepositPreauthMissingTargetRemainsNoTarget(t *testing.T) {
	ownerID := [20]byte{1}
	owner := state.EncodeAccountIDSafe(ownerID)
	missing := state.EncodeAccountIDSafe([20]byte{2})
	view := newFaultView()
	txn := NewDepositPreauth(owner)
	txn.Authorize = missing
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	require.Equal(t, ter.TecNO_TARGET, txn.Preclaim(view, tx.EngineConfig{Rules: on}))
}

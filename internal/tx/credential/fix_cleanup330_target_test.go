package credential

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestCredentialCreatePseudoSubjectCleanup330(t *testing.T) {
	issuerID := [20]byte{1}
	pseudoID := [20]byte{2}
	issuer := state.EncodeAccountIDSafe(issuerID)
	pseudo := state.EncodeAccountIDSafe(pseudoID)
	view := newMapView()
	pseudoRaw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: pseudo,
		VaultID: [32]byte{1},
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(pseudoID), pseudoRaw))

	newTx := func() *CredentialCreate {
		return NewCredentialCreate(issuer, pseudo, "4B5943")
	}
	off := amendment.NewRules(nil)
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	require.Equal(t, ter.TesSUCCESS, newTx().Preclaim(view, tx.EngineConfig{Rules: off}))
	require.Equal(t, ter.TecPSEUDO_ACCOUNT, newTx().Preclaim(view, tx.EngineConfig{Rules: on}))

	credKey := keylet.Credential(pseudoID, issuerID, []byte("KYC"))
	require.NoError(t, view.Insert(credKey, []byte{1}))
	require.Equal(t, ter.TecDUPLICATE, newTx().Preclaim(view, tx.EngineConfig{Rules: on}))
}

func TestCredentialCreateMissingSubjectRemainsNoTarget(t *testing.T) {
	issuerID := [20]byte{1}
	issuer := state.EncodeAccountIDSafe(issuerID)
	missing := state.EncodeAccountIDSafe([20]byte{2})
	view := newMapView()
	create := NewCredentialCreate(issuer, missing, "4B5943")
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	require.Equal(t, ter.TecNO_TARGET, create.Preclaim(view, tx.EngineConfig{Rules: on}))
}

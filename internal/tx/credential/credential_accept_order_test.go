package credential

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func TestCredentialAcceptReservePrecedesExpiration(t *testing.T) {
	view := newMapView()
	var subject, issuer [20]byte
	subject[19] = 1
	issuer[19] = 2
	credentialType := []byte("KYC")
	credentialKey := keylet.Credential(subject, issuer, credentialType)
	expiration := uint32(100)
	credentialRaw, err := serializeCredentialEntry(&CredentialEntry{
		Subject:        subject,
		Issuer:         issuer,
		CredentialType: credentialType,
		Expiration:     &expiration,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(credentialKey, credentialRaw))

	subjectAccount := &state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(subject),
		Balance:  20,
		Sequence: 1,
	}
	issuerAccount := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(issuer),
		Balance:    1_000,
		Sequence:   1,
		OwnerCount: 1,
	}
	for id, account := range map[[20]byte]*state.AccountRoot{subject: subjectAccount, issuer: issuerAccount} {
		raw, err := state.SerializeAccountRoot(account)
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.Account(id), raw))
	}

	accept := NewCredentialAccept(
		state.EncodeAccountIDSafe(subject),
		state.EncodeAccountIDSafe(issuer),
		hex.EncodeToString(credentialType),
	)
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   subjectAccount,
		AccountID: subject,
		Config: tx.EngineConfig{
			Rules:            amendment.AllSupportedRules(),
			ReserveBase:      20,
			ReserveIncrement: 10,
			ParentCloseTime:  101,
		},
		Metadata: &tx.Metadata{},
		Log:      xrpllog.Discard(),
		Ctx:      context.Background(),
	}

	require.Equal(t, ter.TecINSUFFICIENT_RESERVE, accept.Apply(ctx))
	after, err := view.Read(credentialKey)
	require.NoError(t, err)
	require.Equal(t, credentialRaw, after)
}

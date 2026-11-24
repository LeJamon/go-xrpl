package adapter

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/replayfault"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	Backend
	validated uint32
	status    replayfault.Status
}

func (f *fakeBackend) GetValidatedLedgerIndex() uint32       { return f.validated }
func (f *fakeBackend) ReplayFaultStatus() replayfault.Status { return f.status }
func (f *fakeBackend) ReplayBlocked() bool                   { return f.status.Blocked }

func TestLedgerServiceAdapterDelegatesToBackend(t *testing.T) {
	status := replayfault.Status{FollowerMode: true}
	a := NewLedgerServiceAdapter(&fakeBackend{validated: 42, status: status})
	require.Equal(t, uint32(42), a.GetValidatedLedgerIndex())
	require.False(t, a.ReplayBlocked())
	require.Equal(t, status, a.ReplayFaultStatus())
}

func TestLedgerServiceAdapterWithoutService(t *testing.T) {
	var svc *service.Service
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, a := range map[string]*LedgerServiceAdapter{
		"nil":        NewLedgerServiceAdapter(nil),
		"typed nil":  NewLedgerServiceAdapter(svc),
		"zero value": {},
	} {
		t.Run(name, func(t *testing.T) {
			address, err := a.GetGenesisAccount()
			require.NoError(t, err)
			require.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", address)

			_, err = a.GetAccountInfo(ctx, "", "current")
			require.ErrorIs(t, err, context.Canceled)
			_, err = a.GetLedgerByHashContext(ctx, [32]byte{})
			require.ErrorIs(t, err, context.Canceled)
			_, err = a.GetAutofillFee(nil, false, 1, 0)
			require.ErrorContains(t, err, "invalid fee limit factors")

			require.True(t, a.ReplayBlocked())
			status := a.ReplayFaultStatus()
			require.True(t, status.Blocked)
			require.Error(t, status.PersistenceError)
			require.Error(t, a.StartReplayFaultRecovery("id"))
			require.Error(t, a.RevalidateReplayFault(t.Context(), "id"))
		})
	}
}

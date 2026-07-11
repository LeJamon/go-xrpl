package rpc

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/stretchr/testify/require"
)

func TestRPCBookAmountMPT(t *testing.T) {
	const idString = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	amount, err := rpcBookAmount(types.Amount{MPTIssuanceID: idString})
	require.NoError(t, err)
	require.True(t, amount.IsMPT())
	require.Equal(t, idString, amount.MPTIssuanceID())
	id, err := mptutil.DecodeID(idString)
	require.NoError(t, err)
	issuer, err := state.DecodeAccountID(amount.Issuer)
	require.NoError(t, err)
	require.Equal(t, mptutil.Issuer(id), issuer)
}

package openledger_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func TestBuildClosedLedger(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.Close()
	parent := env.LastClosedLedger()
	closeTime := parent.CloseTime().Add(10 * time.Second)

	result, err := openledger.BuildClosedLedger(parent, nil, openledger.BuildConfig{CloseTime: closeTime})
	require.NoError(t, err)
	require.NotNil(t, result.Ledger)
	require.False(t, result.Ledger.IsOpen())
	require.Equal(t, parent.Sequence()+1, result.Ledger.Sequence())
	require.Equal(t, parent.Hash(), result.Ledger.ParentHash())
	require.Equal(t, closeTime, result.Ledger.CloseTime())
	require.Empty(t, result.Retries)
}

func TestBuildClosedLedgerRejectsInvalidParent(t *testing.T) {
	_, err := openledger.BuildClosedLedger(nil, nil, openledger.BuildConfig{})
	require.Error(t, err)

	env := jtx.NewTestEnv(t)
	_, err = openledger.BuildClosedLedger(env.Ledger(), nil, openledger.BuildConfig{})
	require.Error(t, err)
}

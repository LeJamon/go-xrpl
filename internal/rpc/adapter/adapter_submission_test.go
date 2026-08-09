package adapter

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAdaptSubmitResultCopiesLedgerState(t *testing.T) {
	source := &service.SubmitResult{
		Result: ter.TesSUCCESS,
		CurrentLedgerState: &service.SubmitLedgerState{
			ValidatedLedgerIndex:     17,
			OpenLedgerCost:           123,
			AccountSequenceNext:      9,
			AccountSequenceAvailable: 11,
		},
	}
	adapted := adaptSubmitResult(source, true, false)
	require.NotNil(t, adapted.CurrentLedgerState)
	require.NotSame(t, source.CurrentLedgerState, adapted.CurrentLedgerState)
	require.Equal(t, uint32(17), adapted.CurrentLedgerState.ValidatedLedgerIndex)
	require.Equal(t, uint64(123), adapted.CurrentLedgerState.OpenLedgerCost)
	require.Equal(t, uint32(9), adapted.CurrentLedgerState.AccountSequenceNext)
	require.Equal(t, uint32(11), adapted.CurrentLedgerState.AccountSequenceAvailable)

	source.CurrentLedgerState.OpenLedgerCost = 999
	adapted.CurrentLedgerState.AccountSequenceNext = 42
	require.Equal(t, uint64(123), adapted.CurrentLedgerState.OpenLedgerCost)
	require.Equal(t, uint32(9), source.CurrentLedgerState.AccountSequenceNext)
}

func TestAdapterSubmissionSmoke(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	var relayed []byte
	adapter := NewLedgerServiceAdapter(svc)
	adapter.SetTxBroadcaster(func(blob []byte) { relayed = append([]byte(nil), blob...) })
	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	txn := accountset.AccountSet(master).Fee(10).Sequence(1).Build()
	env.SignWith(txn, master)
	txJSON, err := tx.ToJSON(txn)
	require.NoError(t, err)
	blob, err := tx.SerializeTransaction(txn)
	require.NoError(t, err)
	result, err := adapter.SubmitTransaction(txJSON, hex.EncodeToString(blob))
	require.NoError(t, err)
	require.Equal(t, "tesSUCCESS", result.EngineResult)
	require.True(t, result.Applied)
	require.True(t, result.Broadcast)
	require.True(t, result.Kept)
	require.Equal(t, blob, relayed)
}

func TestAdapterSubmissionQueueAndFailHardRelayDecisions(t *testing.T) {
	build := func(t *testing.T) (*service.Service, []byte, string) {
		t.Helper()
		svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
		require.NoError(t, err)
		require.NoError(t, svc.Start())
		t.Cleanup(svc.Stop)
		env := jtx.NewTestEnv(t)
		master := jtx.MasterAccount()
		destination := jtx.NewAccount("destination")
		txn := payment.Pay(master, destination, 100_000_000).Fee(1).Sequence(1).Build()
		env.SignWith(txn, master)
		txJSON, err := tx.ToJSON(txn)
		require.NoError(t, err)
		blob, err := tx.SerializeTransaction(txn)
		require.NoError(t, err)
		return svc, txJSON, hex.EncodeToString(blob)
	}

	t.Run("queued transaction is relayed and kept", func(t *testing.T) {
		svc, txJSON, blobHex := build(t)
		var relayed []byte
		adapter := NewLedgerServiceAdapter(svc)
		adapter.SetTxBroadcaster(func(blob []byte) { relayed = append([]byte(nil), blob...) })
		result, err := adapter.SubmitTransaction(txJSON, blobHex)
		require.NoError(t, err)
		require.Equal(t, "terQUEUED", result.EngineResult)
		require.False(t, result.Applied)
		require.True(t, result.Broadcast)
		require.True(t, result.Queued)
		require.True(t, result.Kept)
		require.NotEmpty(t, relayed)
	})

	t.Run("fail-hard rejection is neither relayed nor kept", func(t *testing.T) {
		svc, txJSON, blobHex := build(t)
		var relayed []byte
		adapter := NewLedgerServiceAdapter(svc)
		adapter.SetTxBroadcaster(func(blob []byte) { relayed = append([]byte(nil), blob...) })
		result, err := adapter.SubmitTransactionFailHard(txJSON, blobHex)
		require.NoError(t, err)
		require.False(t, result.Applied)
		require.False(t, result.Broadcast)
		require.False(t, result.Queued)
		require.False(t, result.Kept)
		require.Empty(t, relayed)
	})
}

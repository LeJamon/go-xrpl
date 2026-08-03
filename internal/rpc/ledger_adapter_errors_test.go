package rpc

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLedgerReaderAdapterPreservesHeaderRootsAndRules(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	reader, err := NewLedgerServiceAdapter(svc).GetLedgerBySequence(closed.Sequence())
	require.NoError(t, err)

	rootSource, ok := reader.(types.LedgerMapHashSource)
	require.True(t, ok)
	header := closed.Header()
	txHash, err := rootSource.TxMapHashWithError()
	require.NoError(t, err)
	stateHash, err := rootSource.StateMapHashWithError()
	require.NoError(t, err)
	require.Equal(t, header.TxHash, txHash)
	require.Equal(t, header.AccountHash, stateHash)
	require.Equal(t, header.TxHash, reader.TxMapHash())
	require.Equal(t, header.AccountHash, reader.StateMapHash())

	rulesSource, ok := reader.(types.LedgerAmendmentRulesErrorSource)
	require.True(t, ok)
	rules, err := rulesSource.LedgerAmendmentRulesWithError()
	require.NoError(t, err)
	require.NotNil(t, rules)
	require.Same(t, rules, reader.(types.LedgerAmendmentRulesSource).LedgerAmendmentRules())
}

func TestLedgerReaderAdapterValidatesOpenMapsButReturnsHeaderRoots(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	open := svc.GetOpenLedger()
	require.NotNil(t, open)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	header := open.Header()
	parentHeader := parent.Header()
	require.NotEqual(t, parentHeader.Hash, header.Hash)
	require.Equal(t, [32]byte{}, header.TxHash)
	require.Equal(t, [32]byte{}, header.AccountHash)
	require.Zero(t, header.CloseFlags)

	// Mutating an open view changes its live SHAMap roots, but does not change
	// the reset provisional header roots exposed by ledger RPC responses.
	require.NoError(t, open.AddTransaction([32]byte{0x01}, make([]byte, 12)))
	require.NoError(t, open.Insert(keylet.Keylet{Key: [32]byte{0x02}}, make([]byte, 12)))
	liveTxHash, err := open.TxMapHash()
	require.NoError(t, err)
	liveStateHash, err := open.StateMapHash()
	require.NoError(t, err)
	require.NotEqual(t, header.TxHash, liveTxHash)
	require.NotEqual(t, header.AccountHash, liveStateHash)

	reader := &ledgerReaderAdapter{l: open}
	txHash, err := reader.TxMapHashWithError()
	require.NoError(t, err)
	stateHash, err := reader.StateMapHashWithError()
	require.NoError(t, err)
	require.Equal(t, header.TxHash, txHash)
	require.Equal(t, header.AccountHash, stateHash)
	require.Equal(t, header.TxHash, reader.TxMapHash())
	require.Equal(t, header.AccountHash, reader.StateMapHash())
}

func TestLedgerServiceAdapterRejectsMalformedTransactionBlob(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	txJSON := []byte(`{"TransactionType":"AccountSet","Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","Fee":"10","Sequence":1,"SigningPubKey":""}`)
	_, err = NewLedgerServiceAdapter(svc).SubmitTransaction(txJSON, "not-hex")
	require.Error(t, err)
	require.ErrorContains(t, err, "decode tx_blob")
}

func TestLedgerMapHashSourceErrorsAreNotSuppressed(t *testing.T) {
	wantErr := errors.New("SHAMap unavailable")
	reader := failingLedgerReader{err: wantErr}
	_, err := reader.TxMapHashWithError()
	require.ErrorIs(t, err, wantErr)
}

type failingLedgerReader struct {
	types.LedgerReader
	err error
}

func (r failingLedgerReader) TxMapHashWithError() ([32]byte, error) {
	return [32]byte{}, r.err
}

func (r failingLedgerReader) StateMapHashWithError() ([32]byte, error) {
	return [32]byte{}, r.err
}

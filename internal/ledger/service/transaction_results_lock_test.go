package service

import (
	"errors"
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

type transactionResultOrderProbe struct {
	validatedCalls         int
	validationDuringWalk   bool
	walkingTransactions    bool
	transactionIdentifiers [][32]byte
}

type failingTransactionResultSource struct{}

func (failingTransactionResultSource) IsValidated() bool { return true }

func (failingTransactionResultSource) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	fn([32]byte{1}, []byte("partial"))
	return errors.New("traversal failed")
}

func (p *transactionResultOrderProbe) IsValidated() bool {
	p.validatedCalls++
	if p.walkingTransactions {
		p.validationDuringWalk = true
	}
	return true
}

func TestCollectTransactionResultsDoesNotIndexPartialTraversal(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	results, err := svc.collectTransactionResults(failingTransactionResultSource{}, 2, [32]byte{3})
	require.Error(t, err)
	require.Nil(t, results)
	require.Empty(t, svc.txIndex)
	require.Empty(t, svc.txPositionIndex)
}

func (p *transactionResultOrderProbe) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	p.walkingTransactions = true
	defer func() { p.walkingTransactions = false }()
	for _, txHash := range p.transactionIdentifiers {
		if !fn(txHash, []byte("transaction-data")) {
			break
		}
	}
	return nil
}

func TestCollectTransactionResultsSnapshotsValidationBeforeTraversal(t *testing.T) {
	source := &transactionResultOrderProbe{
		transactionIdentifiers: [][32]byte{{1}, {2}},
	}
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	results, err := svc.collectTransactionResults(source, 2, [32]byte{3})
	require.NoError(t, err)

	require.Len(t, results, 2)
	require.Equal(t, 1, source.validatedCalls)
	require.False(t, source.validationDuringWalk)
	require.True(t, results[0].Validated)
	require.True(t, results[1].Validated)
}

type indexedTransactionResultSource struct {
	entries []struct {
		hash [32]byte
		data []byte
	}
}

func (s indexedTransactionResultSource) IsValidated() bool { return true }

func (s indexedTransactionResultSource) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	for _, entry := range s.entries {
		if !fn(entry.hash, entry.data) {
			break
		}
	}
	return nil
}

func TestStageTransactionResultsSortsByTransactionIndex(t *testing.T) {
	makeData := func(index uint32) []byte {
		data, err := txcore.CreateTxWithMetaBlob([]byte{0x12, 0x00}, &txcore.Metadata{
			AffectedNodes:     []txcore.AffectedNode{},
			TransactionIndex:  index,
			TransactionResult: ter.TesSUCCESS,
		})
		require.NoError(t, err)
		return data
	}
	source := indexedTransactionResultSource{entries: []struct {
		hash [32]byte
		data []byte
	}{
		{hash: [32]byte{9}, data: []byte("missing-index")},
		{hash: [32]byte{3}, data: makeData(2)},
		{hash: [32]byte{1}, data: makeData(0)},
		{hash: [32]byte{2}, data: makeData(1)},
	}}

	staged, err := stageTransactionResults(source, 7, [32]byte{8})
	require.NoError(t, err)
	require.Equal(t, [][32]byte{{1}, {2}, {3}, {9}}, [][32]byte{
		staged.results[0].TxHash,
		staged.results[1].TxHash,
		staged.results[2].TxHash,
		staged.results[3].TxHash,
	})
	for _, result := range staged.results[:3] {
		require.Error(t, result.Accepted.ParseError())
		_, hasIndex := result.Accepted.TransactionIndex()
		require.True(t, hasIndex)
	}
}

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type transactionResultOrderProbe struct {
	validatedCalls         int
	validationDuringWalk   bool
	walkingTransactions    bool
	transactionIdentifiers [][32]byte
}

func (p *transactionResultOrderProbe) IsValidated() bool {
	p.validatedCalls++
	if p.walkingTransactions {
		p.validationDuringWalk = true
	}
	return true
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

	results := svc.collectTransactionResults(source, 2, [32]byte{3})

	require.Len(t, results, 2)
	require.Equal(t, 1, source.validatedCalls)
	require.False(t, source.validationDuringWalk)
	require.True(t, results[0].Validated)
	require.True(t, results[1].Validated)
}

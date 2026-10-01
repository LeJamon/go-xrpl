package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type cancelingTransactionResultSource struct {
	entries        [][32]byte
	beforeEntry    func(int)
	visitedEntries int
}

func (s *cancelingTransactionResultSource) IsValidated() bool { return true }

func (s *cancelingTransactionResultSource) ForEachTransactionContext(ctx context.Context, fn func([32]byte, []byte) bool) error {
	for i, hash := range s.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.beforeEntry != nil {
			s.beforeEntry(i)
		}
		if !fn(hash, []byte("transaction-data")) {
			return nil
		}
		s.visitedEntries++
	}
	return ctx.Err()
}

func TestStageTransactionResultsContextPreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &cancelingTransactionResultSource{entries: [][32]byte{{1}}}

	staged, err := stageTransactionResultsContext(ctx, source, 7, [32]byte{8})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, staged)
	require.Zero(t, source.visitedEntries)
}

func TestStageTransactionResultsContextCancelsBeforeNextEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &cancelingTransactionResultSource{
		entries: [][32]byte{{1}, {2}},
		beforeEntry: func(index int) {
			if index == 1 {
				cancel()
			}
		},
	}

	staged, err := stageTransactionResultsContext(ctx, source, 7, [32]byte{8})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, staged)
	require.Equal(t, 1, source.visitedEntries)
}

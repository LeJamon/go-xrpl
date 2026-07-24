package peermanagement

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestReadBudgetAdmitsFramesUpToByteLimit(t *testing.T) {
	budget := newReadBudget(100)
	closeCh := make(chan struct{})
	require.NoError(t, budget.acquire(t.Context(), closeCh, 60))
	require.NoError(t, budget.acquire(t.Context(), closeCh, 40))

	acquired := make(chan error, 1)
	go func() { acquired <- budget.acquire(t.Context(), closeCh, 1) }()
	select {
	case err := <-acquired:
		t.Fatalf("over-budget reservation completed early: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	budget.release(40)
	require.NoError(t, <-acquired)
	budget.release(61)
}

func TestReadBudgetRejectsImpossibleReservation(t *testing.T) {
	budget := newReadBudget(100)
	err := budget.acquire(context.Background(), make(chan struct{}), 101)
	require.ErrorIs(t, err, message.ErrMessageTooLarge)
}

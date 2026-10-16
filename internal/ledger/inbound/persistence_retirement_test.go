package inbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestInboundPersistenceRetirementClassification(t *testing.T) {
	ioErr := errors.New("backing write failed")
	for _, tc := range []struct {
		name        string
		retire      bool
		cancel      bool
		storeErr    error
		persistence bool
	}{
		{name: "retired scope", retire: true, storeErr: fmt.Errorf("wrapped: %w", ErrAcquisitionRetired)},
		{name: "write finished after retirement", retire: true},
		{name: "active scope lost ownership", storeErr: ErrAcquisitionRetired, persistence: true},
		{name: "active write failed", storeErr: ioErr, persistence: true},
		{name: "write failed during retirement", retire: true, storeErr: ioErr, persistence: true},
		{name: "unexpected backing cancellation", storeErr: context.Canceled, persistence: true},
		{name: "caller canceled", cancel: true, storeErr: context.Canceled},
		{name: "write failed during cancellation", cancel: true, storeErr: ioErr, persistence: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rootHash, rootData := buildBackedTestState(t, 0)
			headerData, ledgerHash := encodeHeader(header.LedgerHeader{LedgerIndex: 100, AccountHash: rootHash})
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			t.Cleanup(unblock)
			var logs bytes.Buffer
			family := blockingPersistenceFamily{
				Family: backend.NewMemory(),
				store: func([]shamap.FlushEntry) error {
					close(entered)
					<-release
					return tc.storeErr
				},
			}
			acquired := New(ledgerHash, 100, 7, slog.New(slog.NewTextHandler(&logs, nil)), WithFamily(family))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- acquired.GotBaseContext(ctx, []message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback did not reach persistence")
			}
			if tc.retire {
				require.NoError(t, acquired.RetirePersistence(t.Context()))
			}
			if tc.cancel {
				cancel()
			}
			unblock()
			var err error
			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("callback did not finish")
			}
			if tc.persistence {
				require.ErrorIs(t, err, ErrPersistence)
				require.ErrorIs(t, err, tc.storeErr)
				require.Contains(t, logs.String(), "failed to persist verified roots")
			} else {
				require.ErrorIs(t, err, context.Canceled)
				require.NotErrorIs(t, err, ErrPersistence)
				require.NotContains(t, logs.String(), "level=WARN")
			}
			require.False(t, acquired.IsComplete())
			if tc.retire {
				require.ErrorIs(t, acquired.GotBase(nil), context.Canceled)
				require.ErrorIs(t, acquired.GotStateNodes([]message.LedgerNode{{NodeData: rootData}}), context.Canceled)
				require.ErrorIs(t, acquired.GotTransactionNodes([]message.LedgerNode{{NodeData: rootData}}), context.Canceled)
				_, err = acquired.ApplyMissingReplyContext(t.Context(), 7, true, nil)
				require.ErrorIs(t, err, context.Canceled)
				require.ErrorIs(t, acquired.CheckpointMemory(t.Context()), context.Canceled)
				require.ErrorIs(t, acquired.PromotePersistence(t.Context()), context.Canceled)
			}
		})
	}
}

type retirementBarrierFamily struct {
	shamap.Family
	entered chan struct{}
	release chan struct{}
}

func (f retirementBarrierFamily) Flush(context.Context) error {
	close(f.entered)
	<-f.release
	return nil
}

func (f retirementBarrierFamily) Promote(ctx context.Context) (shamap.Family, error) {
	return f.Family, f.Flush(ctx)
}

func TestInboundRetirementDuringDurabilityBarrier(t *testing.T) {
	for _, operation := range []string{"flush", "checkpoint", "promote"} {
		t.Run(operation, func(t *testing.T) {
			family := retirementBarrierFamily{
				Family: backend.NewMemory(), entered: make(chan struct{}), release: make(chan struct{}),
			}
			unblock := sync.OnceFunc(func() { close(family.release) })
			t.Cleanup(unblock)
			acquired := New([32]byte{1}, 100, 7, discardLogger(), WithFamily(family))
			done := make(chan error, 1)
			go func() {
				switch operation {
				case "flush":
					done <- acquired.FlushPersistence(t.Context())
				case "checkpoint":
					done <- acquired.CheckpointMemory(t.Context())
				case "promote":
					done <- acquired.PromotePersistence(t.Context())
				}
			}()
			select {
			case <-family.entered:
			case <-time.After(time.Second):
				t.Fatal("durability barrier did not start")
			}
			require.NoError(t, acquired.RetirePersistence(t.Context()))
			unblock()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
				require.NotErrorIs(t, err, ErrPersistence)
			case <-time.After(time.Second):
				t.Fatal("durability barrier did not finish")
			}
			require.Zero(t, acquired.Snapshot().MemoryCheckpoints)
			require.False(t, acquired.IsComplete())
		})
	}
}

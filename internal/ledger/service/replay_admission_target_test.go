package service

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type foreignTargetProofFault uint8

const (
	foreignTargetProofMissing foreignTargetProofFault = iota
	foreignTargetProofCorrupt
)

type foreignTargetProofFamily struct {
	mu sync.Mutex

	nodes   map[[32]byte][]byte
	txRoot  [32]byte
	fault   foreignTargetProofFault
	bad     [32]byte
	batches int
}

func newForeignTargetProofFamily(txRoot [32]byte, fault foreignTargetProofFault) *foreignTargetProofFamily {
	return &foreignTargetProofFamily{
		nodes:  make(map[[32]byte][]byte),
		txRoot: txRoot,
		fault:  fault,
	}
}

func (f *foreignTargetProofFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	data := bytes.Clone(f.nodes[hash])
	f.mu.Unlock()
	return data, nil
}

func (f *foreignTargetProofFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches++
	for _, entry := range entries {
		data := bytes.Clone(entry.Data)
		if entry.MapType == shamap.TypeTransaction && entry.Hash != f.txRoot && f.bad == ([32]byte{}) {
			f.bad = entry.Hash
			switch f.fault {
			case foreignTargetProofMissing:
				continue
			case foreignTargetProofCorrupt:
				data[len(data)-1] ^= 0xff
			}
		}
		f.nodes[entry.Hash] = data
	}
	return nil
}

func (f *foreignTargetProofFamily) badHash() [32]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bad
}

func (f *foreignTargetProofFamily) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batches
}

func TestPrepareReplayLedgerRejectsForeignTargetClosureFailures(t *testing.T) {
	for name, fault := range map[string]foreignTargetProofFault{
		"missing child": foreignTargetProofMissing,
		"corrupt child": foreignTargetProofCorrupt,
	} {
		t.Run(name, func(t *testing.T) {
			state := shamap.New(shamap.TypeState)
			require.NoError(t, state.PutWithNodeType(
				[32]byte{1}, []byte("foreign state fixture"), shamap.NodeTypeAccountState,
			))
			transactions := shamap.New(shamap.TypeTransaction)
			require.NoError(t, transactions.PutWithNodeType(
				[32]byte{2}, []byte("foreign transaction fixture"), shamap.NodeTypeTransactionWithMeta,
			))
			txRoot, err := transactions.Hash()
			require.NoError(t, err)

			targetFamily := newForeignTargetProofFamily(txRoot, fault)
			targetExecution := newTestExecutionStore(t, targetFamily)
			svc, err := New(Config{Execution: targetExecution, SHAMapFamily: targetFamily})
			require.NoError(t, err)
			t.Cleanup(svc.Stop)

			derived := replayAdmissionLedger(t, 2059, [32]byte{3}, [32]byte{4}, state, transactions)
			t.Cleanup(derived.Discard)

			prepared, err := svc.PrepareReplayLedger(t.Context(), derived, nil)
			if prepared != nil {
				prepared.Release()
			}
			require.Error(t, err)
			require.Nil(t, prepared)
			require.NotZero(t, targetFamily.badHash())
			require.Positive(t, targetFamily.batchCount())
		})
	}
}

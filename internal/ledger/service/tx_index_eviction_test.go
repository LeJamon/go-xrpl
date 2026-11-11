package service

import (
	"maps"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// txIndexReference is the full-scan transaction-index maintenance that the
// per-sequence index replaces.
type txIndexReference struct {
	txIndex         map[[32]byte]uint32
	txPositionIndex map[[32]byte]uint32
}

func (r *txIndexReference) commit(staged *stagedTransactionResults) {
	for _, result := range staged.results {
		r.txIndex[result.TxHash] = staged.ledgerSeq
	}
	for txHash, position := range staged.positions {
		r.txPositionIndex[txHash] = position
	}
	for _, txHash := range staged.missingPositions {
		delete(r.txPositionIndex, txHash)
	}
}

func (r *txIndexReference) dropWhere(match func(seq uint32) bool) {
	for txHash, txSeq := range r.txIndex {
		if match(txSeq) {
			delete(r.txIndex, txHash)
			delete(r.txPositionIndex, txHash)
		}
	}
}

func TestTxIndexEvictionMatchesFullScan(t *testing.T) {
	const (
		window   = 8
		hashPool = 48
		maxSeq   = 96
	)
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, 2192))
		cfg := DefaultConfig()
		cfg.LedgerCacheSize = window
		svc, err := New(cfg)
		require.NoError(t, err)
		ref := &txIndexReference{
			txIndex:         make(map[[32]byte]uint32),
			txPositionIndex: make(map[[32]byte]uint32),
		}
		randomHash := func() [32]byte {
			return [32]byte{0xD1, byte(rng.IntN(hashPool))}
		}

		for step := range 300 {
			seq := uint32(1 + rng.IntN(maxSeq))
			switch op := rng.IntN(10); {
			case op < 5:
				// Hashes come from a small pool so the same transaction is
				// re-indexed at other sequences, as across forks.
				staged := &stagedTransactionResults{
					positions: make(map[[32]byte]uint32),
					ledgerSeq: seq,
				}
				for i := range rng.IntN(6) {
					txHash := randomHash()
					staged.results = append(staged.results, TransactionResultEvent{TxHash: txHash})
					if rng.IntN(4) == 0 {
						staged.missingPositions = append(staged.missingPositions, txHash)
					} else {
						staged.positions[txHash] = uint32(i)
					}
				}
				svc.commitTransactionResultsLocked(staged)
				ref.commit(staged)
			case op < 7:
				svc.evictOldHistoryLocked(seq)
				if seq > window {
					cutoff := seq - window
					ref.dropWhere(func(txSeq uint32) bool { return txSeq <= cutoff })
				}
			case op < 9:
				svc.unindexLedgerTxsLocked(seq)
				ref.dropWhere(func(txSeq uint32) bool { return txSeq == seq })
			default:
				// Replaced historical ledgers drop their hashes regardless of
				// the sequence they resolve to.
				txHash := randomHash()
				delete(svc.txIndex, txHash)
				delete(svc.txPositionIndex, txHash)
				delete(ref.txIndex, txHash)
				delete(ref.txPositionIndex, txHash)
			}

			require.Truef(t, maps.Equal(ref.txIndex, svc.txIndex), "seed %d step %d: txIndex diverged", seed, step)
			require.Truef(t, maps.Equal(ref.txPositionIndex, svc.txPositionIndex), "seed %d step %d: txPositionIndex diverged", seed, step)
			for txHash, txSeq := range svc.txIndex {
				require.Containsf(t, svc.txHashesBySeq[txSeq], txHash, "seed %d step %d: indexed hash missing from its sequence list", seed, step)
			}
		}
	}
}

func TestEvictOldHistoryLockedFreesSequenceLists(t *testing.T) {
	const window = 4
	cfg := DefaultConfig()
	cfg.LedgerCacheSize = window
	svc, err := New(cfg)
	require.NoError(t, err)

	moved := [32]byte{0xE1}
	for seq := uint32(1); seq <= 3*window; seq++ {
		svc.indexTxLocked([32]byte{0xE0, byte(seq)}, seq)
		svc.indexTxLocked(moved, seq)
		svc.evictOldHistoryLocked(seq)
	}

	require.Len(t, svc.txHashesBySeq, window)
	for seq := range svc.txHashesBySeq {
		require.Greater(t, seq, uint32(2*window))
	}
	require.Equal(t, uint32(3*window), svc.txIndex[moved])
}

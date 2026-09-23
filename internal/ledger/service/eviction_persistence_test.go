package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestHistoryEvictionDrainsInFlightTipInvalidation(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "flush"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			base := newTestNodeStore(t, 100)
			t.Cleanup(func() { require.NoError(t, base.Close()) })
			db := &gatedTipDatabase{Database: base, entered: make(chan struct{}), release: make(chan struct{})}
			cfg := DefaultConfig()
			cfg.NodeStore, cfg.SHAMapFamily = db, backend.New(db)
			svc, err := New(cfg)
			require.NoError(t, err)
			svc.persistenceWorker.start()
			t.Cleanup(svc.persistenceWorker.stop)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(db.release) }) }
			defer unblock()

			l := makeStubLedger(t, 27, [32]byte{0x27}, [32]byte{0x26})
			svc.historyComponent.mu.Lock()
			svc.putHistoryLocked(l)
			svc.historyComponent.mu.Unlock()
			svc.enqueuePersist(l)
			select {
			case <-db.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("persistence did not reach validated-tip storage")
			}
			svc.persistMu.Lock()
			job := svc.validatedPersistJobs[l.Sequence()]
			svc.persistMu.Unlock()
			require.NotNil(t, job)
			svc.validatedStateBaseMu.Lock()
			svc.validatedStateBaseProof = &validatedStateBaseProof{}
			svc.fastLoadCheckpointState.Store(fastLoadCheckpointEligible)
			epoch := svc.stateBaseMutationEpoch
			svc.validatedStateBaseMu.Unlock()

			evicted := make(chan struct{})
			go func() {
				svc.openLedgerMu.LockRole(openLedgerValidation)
				svc.mu.Lock()
				svc.historyComponent.mu.Lock()
				svc.evictOldHistoryLocked(l.Sequence() + svc.ledgerCacheSize())
				svc.historyComponent.mu.Unlock()
				svc.mu.Unlock()
				svc.openLedgerMu.Unlock()
				close(evicted)
			}()
			select {
			case <-evicted:
			case <-time.After(time.Second):
				t.Fatal("history eviction waited for an in-flight tip write")
			}
			require.True(t, job.canceled.Load())
			require.False(t, svc.HasCompleteLedger(l.Sequence()))
			require.False(t, svc.openLedgerMu.Snapshot().Held)

			drained := make(chan struct{})
			go func() {
				if shutdown {
					svc.persistenceWorker.stop()
				} else {
					svc.FlushPersists()
				}
				close(drained)
			}()
			select {
			case <-drained:
				t.Fatal("persistence drain finished before the blocked tip write")
			default:
			}
			unblock()
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				t.Fatal("persistence drain did not finish")
			}
			tip, err := db.Fetch(t.Context(), validatedTipKey)
			require.NoError(t, err)
			require.NotNil(t, tip)
			require.Zero(t, tip.LedgerSeq)
			require.False(t, svc.HasCompleteLedger(l.Sequence()))
			svc.validatedStateBaseMu.RLock()
			proof, finalEpoch := svc.validatedStateBaseProof, svc.stateBaseMutationEpoch
			svc.validatedStateBaseMu.RUnlock()
			require.Nil(t, proof)
			require.Greater(t, finalEpoch, epoch)
			require.EqualValues(t, fastLoadCheckpointInvalidated, svc.fastLoadCheckpointState.Load())
		})
	}
}

func TestHistoryEvictionPreservesLaterTipPersistence(t *testing.T) {
	for _, mode := range []string{"queued_same_hash", "queued_replacement", "direct_same_hash", "direct_newer", "history_only_replacement"} {
		t.Run(mode, func(t *testing.T) {
			db := newTestNodeStore(t, 100)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			cfg := DefaultConfig()
			cfg.NodeStore, cfg.SHAMapFamily = db, backend.New(db)
			svc, err := New(cfg)
			require.NoError(t, err)
			svc.persistStarted = true
			old := makeStubLedger(t, 27, [32]byte{0x27}, [32]byte{0x26})
			require.NoError(t, svc.persistValidatedTip(t.Context(), old))
			svc.beginValidatedPersistence(old.Sequence(), old.Hash())
			svc.putHistoryLocked(old)
			svc.evictOldHistoryLocked(old.Sequence() + svc.ledgerCacheSize())
			require.Len(t, svc.persistQueue, 1)

			replacement := old
			switch mode {
			case "queued_same_hash":
				svc.enqueuePersist(replacement)
			case "queued_replacement":
				replacement = makeStubLedger(t, old.Sequence(), [32]byte{0x28}, old.ParentHash())
				svc.enqueuePersist(replacement)
			case "direct_same_hash":
				require.NoError(t, svc.persistValidatedLedger(t.Context(), replacement, true))
			case "direct_newer":
				replacement = makeStubLedger(t, old.Sequence()+1, [32]byte{0x28}, old.Hash())
				require.NoError(t, svc.persistValidatedLedger(t.Context(), replacement, true))
			case "history_only_replacement":
				replacement = makeStubLedger(t, old.Sequence(), [32]byte{0x28}, old.ParentHash())
				require.NoError(t, svc.persistValidatedLedger(t.Context(), replacement, false))
			}
			jobs := svc.persistQueue
			svc.persistQueue = nil
			for _, job := range jobs {
				svc.runPersistJob(job)
			}
			tip, err := db.Fetch(t.Context(), validatedTipKey)
			require.NoError(t, err)
			require.NotNil(t, tip)
			if mode == "history_only_replacement" {
				require.Zero(t, tip.LedgerSeq)
			} else {
				hash := replacement.Hash()
				require.Equal(t, replacement.Sequence(), tip.LedgerSeq)
				require.Equal(t, hash[:], tip.Data)
			}
			require.True(t, svc.hasDurableCompleteLedger(replacement))
		})
	}
}

func TestHistoryEvictionDrainsBeforeWorkerStart(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "flush"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) {
			db := newTestNodeStore(t, 100)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			cfg := DefaultConfig()
			cfg.NodeStore, cfg.SHAMapFamily = db, backend.New(db)
			svc, err := New(cfg)
			require.NoError(t, err)
			t.Cleanup(svc.Stop)
			old := makeStubLedger(t, 27, [32]byte{0x27}, [32]byte{0x26})
			require.NoError(t, svc.persistValidatedTip(t.Context(), old))
			svc.beginValidatedPersistence(old.Sequence(), old.Hash())
			svc.putHistoryLocked(old)
			svc.evictOldHistoryLocked(old.Sequence() + svc.ledgerCacheSize())
			require.False(t, svc.persistStarted)
			if shutdown {
				svc.Stop()
			} else {
				svc.FlushPersists()
			}
			tip, err := db.Fetch(t.Context(), validatedTipKey)
			require.NoError(t, err)
			require.NotNil(t, tip)
			require.Zero(t, tip.LedgerSeq)
		})
	}
}

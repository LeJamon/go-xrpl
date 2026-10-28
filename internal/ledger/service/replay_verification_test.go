package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func (s *Service) replayVerificationFlightStats() (flights, waiters int) {
	s.replayVerificationMu.Lock()
	defer s.replayVerificationMu.Unlock()
	flights = len(s.replayVerificationFlights)
	for _, flight := range s.replayVerificationFlights {
		waiters += flight.waiters
	}
	return flights, waiters
}

type blockingReplayVerificationFamily struct {
	*replayAdmissionFamily
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	pins    atomic.Int32
}

type generationChangingReplayVerificationFamily struct {
	*blockingReplayVerificationFamily
	changeHash [32]byte
	changed    atomic.Bool
}

type drainingReplayVerificationFamily struct {
	*replayAdmissionFamily
	entered      chan struct{}
	release      chan struct{}
	drainStarted chan struct{}
	drainRelease chan struct{}
	once         sync.Once
}

func (f *blockingReplayVerificationFamily) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	fingerprint, release, err := f.replayAdmissionFamily.AcquireDurableSnapshot(ctx)
	if err != nil {
		return fingerprint, release, err
	}
	f.pins.Add(1)
	return fingerprint, sync.OnceFunc(func() {
		release()
		f.pins.Add(-1)
	}), nil
}

func (f *blockingReplayVerificationFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	first := false
	f.once.Do(func() {
		first = true
		close(f.entered)
	})
	if first {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.replayAdmissionFamily.FetchDurable(ctx, hash)
}

func (f *generationChangingReplayVerificationFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.blockingReplayVerificationFamily.FetchDurable(ctx, hash)
	if hash == f.changeHash && f.changed.CompareAndSwap(false, true) {
		f.bumpFingerprint()
	}
	return data, err
}

func (f *drainingReplayVerificationFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	first := false
	f.once.Do(func() {
		first = true
		close(f.entered)
	})
	if first {
		select {
		case <-f.release:
		case <-ctx.Done():
			close(f.drainStarted)
			<-f.drainRelease
			return nil, ctx.Err()
		}
	}
	return f.replayAdmissionFamily.FetchDurable(ctx, hash)
}

func newBlockingReplayVerificationFamily(t *testing.T) *blockingReplayVerificationFamily {
	t.Helper()
	return &blockingReplayVerificationFamily{
		replayAdmissionFamily: newReplayAdmissionFamily(t),
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
}

func persistedReplayAdmissionTransactions(t *testing.T, family *replayAdmissionFamily) (*shamap.SHAMap, [32]byte) {
	t.Helper()
	source := shamap.New(shamap.TypeTransaction)
	require.NoError(t, source.PutWithNodeType([32]byte{0x43}, []byte("replay-admission-transaction"), shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	root, err := source.Hash()
	require.NoError(t, err)
	loaded, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeTransaction, root, family)
	require.NoError(t, err)
	return loaded, root
}

func TestVerifyDetachedLedgerCoalescesColdWalkAndPublishesStateProof(t *testing.T) {
	family := newBlockingReplayVerificationFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 512)
	ledger := replayAdmissionLedger(t, 1200, [32]byte{0xc0}, [32]byte{0x12}, state, shamap.New(shamap.TypeTransaction))
	family.resetReads()

	type verificationResult struct {
		stats ReplayAdmissionStats
		err   error
	}
	firstDone := make(chan verificationResult, 1)
	go func() {
		stats, err := svc.verifyDetachedLedgerStats(t.Context(), ledger)
		firstDone <- verificationResult{stats: stats, err: err}
	}()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("first detached verification did not start")
	}

	secondDone := make(chan verificationResult, 1)
	go func() {
		stats, err := svc.verifyDetachedLedgerStats(t.Context(), ledger)
		secondDone <- verificationResult{stats: stats, err: err}
	}()
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 1 && waiters == 2
	}, time.Second, time.Millisecond)

	close(family.release)
	first := <-firstDone
	second := <-secondDone
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.False(t, first.stats.State.Coalesced)
	require.True(t, second.stats.State.Coalesced)
	require.Zero(t, second.stats.State.NodesLoaded)

	family.mu.Lock()
	fingerprint := family.fingerprint
	family.mu.Unlock()
	header := ledger.Header()
	require.True(t, svc.lookupReplayProof(header, fingerprint, false), "successful fallback must publish a state proof")
	require.False(t, svc.lookupReplayProof(header, fingerprint, true), "an in-memory transaction tree must not publish a durable transaction proof")

	// A fresh service verifies the same tree once. Coalesced callers must have
	// issued no more durable reads than that single walk.
	singleFamily := newReplayAdmissionFamily(t)
	singleSvc, err := New(Config{SHAMapFamily: singleFamily})
	require.NoError(t, err)
	t.Cleanup(singleSvc.Stop)
	singleState := persistedReplayAdmissionState(t, singleFamily, 512)
	singleLedger := replayAdmissionLedger(t, 1200, [32]byte{0xc0}, [32]byte{0x12}, singleState, shamap.New(shamap.TypeTransaction))
	singleFamily.resetReads()
	require.NoError(t, singleSvc.VerifyDetachedLedger(t.Context(), singleLedger))
	require.Equal(t, singleFamily.readCount(), family.readCount())
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 0 && waiters == 0
	}, time.Second, time.Millisecond)
	root, err := state.Hash()
	require.NoError(t, err)
	family.hide(root)
	require.ErrorIs(t, svc.VerifyDetachedLedger(t.Context(), ledger), shamap.ErrNodeNotInStore)
}

func TestVerifyDetachedLedgerCoalescesStateProofTransactionUpgrade(t *testing.T) {
	family := newBlockingReplayVerificationFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 256)
	txMap, txRoot := persistedReplayAdmissionTransactions(t, family.replayAdmissionFamily)
	ledger := replayAdmissionLedger(t, 1207, [32]byte{0xc7}, [32]byte{0x19}, state, txMap)
	family.mu.Lock()
	fingerprint := family.fingerprint
	family.mu.Unlock()
	svc.rememberReplayProof(ledger.Header(), fingerprint, false)
	require.True(t, svc.lookupReplayProof(ledger.Header(), fingerprint, false))
	require.False(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true))

	firstDone := make(chan error, 1)
	go func() { firstDone <- svc.VerifyDetachedLedger(t.Context(), ledger) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("transaction upgrade did not start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- svc.VerifyDetachedLedger(t.Context(), ledger) }()
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 1 && waiters == 2
	}, time.Second, time.Millisecond)
	close(family.release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)

	family.mu.Lock()
	rootReads := family.readHashes[txRoot]
	family.mu.Unlock()
	require.Equal(t, 1, rootReads, "state-only upgrade must share the durable transaction walk")
	require.True(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true))
}

func TestVerifyDetachedLedgerRejectsGenerationChangeDuringSharedWalk(t *testing.T) {
	for _, phase := range []string{"state", "transactions"} {
		t.Run(phase, func(t *testing.T) {
			base := newBlockingReplayVerificationFamily(t)
			family := &generationChangingReplayVerificationFamily{blockingReplayVerificationFamily: base}
			svc, err := New(Config{SHAMapFamily: family})
			require.NoError(t, err)
			t.Cleanup(svc.Stop)
			state := persistedReplayAdmissionState(t, base.replayAdmissionFamily, 256)
			txMap, txRoot := persistedReplayAdmissionTransactions(t, base.replayAdmissionFamily)
			ledger := replayAdmissionLedger(t, 1208, [32]byte{0xc8}, [32]byte{0x1a}, state, txMap)
			family.mu.Lock()
			fingerprint := family.fingerprint
			family.mu.Unlock()
			family.changeHash = ledger.Header().AccountHash
			if phase == "transactions" {
				family.changeHash = txRoot
				svc.rememberReplayProof(ledger.Header(), fingerprint, false)
			}

			done := make(chan error, 2)
			go func() { done <- svc.VerifyDetachedLedger(t.Context(), ledger) }()
			select {
			case <-family.entered:
			case <-time.After(time.Second):
				t.Fatal("verification did not start")
			}
			go func() { done <- svc.VerifyDetachedLedger(t.Context(), ledger) }()
			require.Eventually(t, func() bool {
				flights, waiters := svc.replayVerificationFlightStats()
				return flights == 1 && waiters == 2
			}, time.Second, time.Millisecond)
			close(family.release)
			for range 2 {
				require.ErrorContains(t, <-done, "replay generation changed")
			}
			require.True(t, family.changed.Load())
			require.Equal(t, phase == "transactions", svc.lookupReplayProof(ledger.Header(), fingerprint, false))
			require.False(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true))
			family.mu.Lock()
			current := family.fingerprint
			family.mu.Unlock()
			require.False(t, svc.lookupReplayProof(ledger.Header(), current, false), "a changed generation must not inherit the in-flight proof")
		})
	}
}

func TestVerifyDetachedLedgerCancellationKeepsOtherWaiter(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		name := "joining caller"
		if cancelFirst {
			name = "first caller"
		}
		t.Run(name, func(t *testing.T) {
			family := newBlockingReplayVerificationFamily(t)
			svc, err := New(Config{SHAMapFamily: family})
			require.NoError(t, err)
			t.Cleanup(svc.Stop)
			state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 256)
			l := replayAdmissionLedger(t, 1201, [32]byte{0xc1}, [32]byte{0x13}, state, shamap.New(shamap.TypeTransaction))
			ownerCtx, cancelOwner := context.WithCancel(t.Context())
			defer cancelOwner()
			ownerDone := make(chan error, 1)
			go func() { ownerDone <- svc.VerifyDetachedLedger(ownerCtx, l) }()
			awaitPreferredPreparation(t, family.entered)
			waiterCtx, cancelWaiter := context.WithCancel(t.Context())
			defer cancelWaiter()
			waiterDone := make(chan error, 1)
			go func() { waiterDone <- svc.VerifyDetachedLedger(waiterCtx, l) }()
			require.Eventually(t, func() bool {
				flights, waiters := svc.replayVerificationFlightStats()
				return flights == 1 && waiters == 2
			}, time.Second, time.Millisecond)
			canceled, surviving := waiterDone, ownerDone
			cancel := cancelWaiter
			if cancelFirst {
				canceled, surviving, cancel = ownerDone, waiterDone, cancelOwner
			}
			cancel()
			require.ErrorIs(t, <-canceled, context.Canceled)
			flights, waiters := svc.replayVerificationFlightStats()
			require.Equal(t, 1, flights)
			require.Equal(t, 1, waiters)
			require.Eventually(t, func() bool { return family.pins.Load() == 1 }, time.Second, time.Millisecond)
			select {
			case err := <-surviving:
				t.Fatalf("shared verification stopped while another caller still needed it: %v", err)
			default:
			}
			close(family.release)
			require.NoError(t, <-surviving)
		})
	}
}

func TestVerifyDetachedLedgerLastCanceledWaiterDoesNotPoisonNextFlight(t *testing.T) {
	family := newBlockingReplayVerificationFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 256)
	ledger := replayAdmissionLedger(t, 1203, [32]byte{0xc3}, [32]byte{0x15}, state, shamap.New(shamap.TypeTransaction))

	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- svc.VerifyDetachedLedger(ownerCtx, ledger) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("owner verification did not start")
	}
	waiterCtx, cancelWaiter := context.WithCancel(t.Context())
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- svc.VerifyDetachedLedger(waiterCtx, ledger) }()
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 1 && waiters == 2
	}, time.Second, time.Millisecond)
	cancelOwner()
	cancelWaiter()
	require.ErrorIs(t, <-ownerDone, context.Canceled)
	require.ErrorIs(t, <-waiterDone, context.Canceled)
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 0 && waiters == 0
	}, time.Second, time.Millisecond)
	close(family.release)
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), ledger))
}

func TestVerifyDetachedLedgerFreshCallerWaitsForCanceledFlightToDrain(t *testing.T) {
	family := &drainingReplayVerificationFamily{
		replayAdmissionFamily: newReplayAdmissionFamily(t),
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
		drainStarted:          make(chan struct{}),
		drainRelease:          make(chan struct{}),
	}
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	releaseDrain := sync.OnceFunc(func() { close(family.drainRelease) })
	t.Cleanup(releaseDrain)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 256)
	ledger := replayAdmissionLedger(t, 1209, [32]byte{0xc9}, [32]byte{0x1b}, state, shamap.New(shamap.TypeTransaction))
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	waiterCtx, cancelWaiter := context.WithCancel(t.Context())
	defer cancelWaiter()
	ownerDone := make(chan error, 1)
	waiterDone := make(chan error, 1)
	go func() { ownerDone <- svc.VerifyDetachedLedger(ownerCtx, ledger) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("owner verification did not start")
	}
	go func() { waiterDone <- svc.VerifyDetachedLedger(waiterCtx, ledger) }()
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 1 && waiters == 2
	}, time.Second, time.Millisecond)
	cancelOwner()
	cancelWaiter()
	require.ErrorIs(t, <-ownerDone, context.Canceled)
	require.ErrorIs(t, <-waiterDone, context.Canceled)
	select {
	case <-family.drainStarted:
	case <-time.After(time.Second):
		t.Fatal("canceled owner did not enter its draining read")
	}

	freshDone := make(chan error, 1)
	go func() { freshDone <- svc.VerifyDetachedLedger(t.Context(), ledger) }()
	select {
	case err := <-freshDone:
		t.Fatalf("fresh caller inherited canceled flight: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	flights, waiters := svc.replayVerificationFlightStats()
	require.Equal(t, 1, flights)
	require.Zero(t, waiters, "fresh caller must wait for the canceled flight instead of joining it")
	releaseDrain()
	require.NoError(t, <-freshDone)
}

func TestVerifyDetachedLedgerFailureRemovesFlight(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family, 128)
	root, err := state.Hash()
	require.NoError(t, err)
	family.hide(root)
	ledger := replayAdmissionLedger(t, 1202, [32]byte{0xc2}, [32]byte{0x14}, state, shamap.New(shamap.TypeTransaction))

	require.ErrorIs(t, svc.VerifyDetachedLedger(t.Context(), ledger), shamap.ErrNodeNotInStore)
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 0 && waiters == 0
	}, time.Second, time.Millisecond)

	family.mu.Lock()
	delete(family.hidden, root)
	family.fingerprint[0]++
	family.mu.Unlock()
	require.NoError(t, svc.VerifyDetachedLedger(t.Context(), ledger))
}

func TestVerifyDetachedLedgerStopDrainsSharedWalkAndPins(t *testing.T) {
	family := newBlockingReplayVerificationFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 128)
	l := replayAdmissionLedger(t, 1206, [32]byte{0xc6}, [32]byte{0x18}, state, shamap.New(shamap.TypeTransaction))
	done := make(chan error, 2)
	go func() { done <- svc.VerifyDetachedLedger(t.Context(), l) }()
	awaitPreferredPreparation(t, family.entered)
	go func() { done <- svc.VerifyDetachedLedger(t.Context(), l) }()
	require.Eventually(t, func() bool {
		flights, waiters := svc.replayVerificationFlightStats()
		return flights == 1 && waiters == 2
	}, time.Second, time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain shared verification")
	}
	for range 2 {
		require.ErrorIs(t, <-done, context.Canceled)
	}
	require.Zero(t, family.pins.Load())
	flights, waiters := svc.replayVerificationFlightStats()
	require.Zero(t, flights)
	require.Zero(t, waiters)
	require.False(t, svc.lookupReplayProof(l.Header(), family.fingerprint, false))
}

func TestReplayVerificationWaitsForBoundedFlightSlot(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	release := make(chan struct{})
	started := make(chan struct{}, replayVerificationFlightLimit+1)
	results := make(chan error, replayVerificationFlightLimit+1)
	for sequence := uint32(0); sequence < replayVerificationFlightLimit; sequence++ {
		key := replayVerificationKey{sequence: sequence, ledgerHash: [32]byte{byte(sequence + 1)}}
		go func() {
			_, runErr := svc.coalesceReplayVerification(t.Context(), key, nil, func(context.Context) (replayVerificationResult, error) {
				started <- struct{}{}
				<-release
				return replayVerificationResult{}, nil
			})
			results <- runErr
		}()
	}
	require.Eventually(t, func() bool {
		flights, _ := svc.replayVerificationFlightStats()
		return flights == replayVerificationFlightLimit
	}, time.Second, time.Millisecond)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bounded verification owners did not start")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	var startedFifth atomic.Bool
	key := replayVerificationKey{sequence: replayVerificationFlightLimit, ledgerHash: [32]byte{0xff}}
	_, err = svc.coalesceReplayVerification(ctx, key, nil, func(context.Context) (replayVerificationResult, error) {
		startedFifth.Store(true)
		return replayVerificationResult{}, nil
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, startedFifth.Load(), "a fifth flight must wait for a bounded slot")

	close(release)
	for range replayVerificationFlightLimit {
		require.NoError(t, <-results)
	}
}

func TestVerifyDetachedLedgerRetainsOnlyStateProofWhenTransactionsFail(t *testing.T) {
	family := newReplayAdmissionFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family, 128)
	txSource := shamap.New(shamap.TypeTransaction)
	require.NoError(t, txSource.PutWithNodeType([32]byte{0x41}, []byte("transaction-proof"), shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txSource.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	txRoot, err := txSource.Hash()
	require.NoError(t, err)
	txMap, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeTransaction, txRoot, family)
	require.NoError(t, err)
	ledger := replayAdmissionLedger(t, 1204, [32]byte{0xc4}, [32]byte{0x16}, state, txMap)
	family.hide(txRoot)
	require.ErrorIs(t, svc.VerifyDetachedLedger(t.Context(), ledger), shamap.ErrNodeNotInStore)
	family.mu.Lock()
	fingerprint := family.fingerprint
	family.mu.Unlock()
	require.True(t, svc.lookupReplayProof(ledger.Header(), fingerprint, false), "successful state verification remains reusable")
	require.False(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true), "failed transaction verification must not publish a complete proof")

	family.mu.Lock()
	delete(family.hidden, txRoot)
	family.mu.Unlock()
	stats, err := svc.verifyDetachedLedgerStats(t.Context(), ledger)
	require.NoError(t, err)
	require.True(t, stats.ProofCacheHit)
	require.Zero(t, stats.State.NodesLoaded)
	require.True(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true))
	svc.rememberReplayProof(ledger.Header(), fingerprint, false)
	require.True(t, svc.lookupReplayProof(ledger.Header(), fingerprint, true), "a state proof must not downgrade completed transaction verification")
}

func TestVerifyDetachedLedgerSeparatesInMemoryAndBackedTransactionCompleteness(t *testing.T) {
	family := newBlockingReplayVerificationFamily(t)
	svc, err := New(Config{SHAMapFamily: family})
	require.NoError(t, err)
	t.Cleanup(svc.Stop)
	state := persistedReplayAdmissionState(t, family.replayAdmissionFamily, 128)
	txSource := shamap.New(shamap.TypeTransaction)
	txKey := [32]byte{0x42}
	txValue := []byte("shared-transaction-root")
	require.NoError(t, txSource.PutWithNodeType(txKey, txValue, shamap.NodeTypeTransactionWithMeta))
	require.NoError(t, txSource.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	txRoot, err := txSource.Hash()
	require.NoError(t, err)
	backedTx, err := shamap.NewFromRootHashContext(t.Context(), shamap.TypeTransaction, txRoot, family.replayAdmissionFamily)
	require.NoError(t, err)
	inMemoryTx := shamap.New(shamap.TypeTransaction)
	require.NoError(t, inMemoryTx.PutWithNodeType(txKey, txValue, shamap.NodeTypeTransactionWithMeta))
	family.hide(txRoot)

	ownerLedger := replayAdmissionLedger(t, 1205, [32]byte{0xc5}, [32]byte{0x17}, state, inMemoryTx)
	backedLedger := replayAdmissionLedger(t, 1205, [32]byte{0xc5}, [32]byte{0x17}, state, backedTx)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- svc.VerifyDetachedLedger(t.Context(), ownerLedger) }()
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("owner verification did not start")
	}
	waiterDone := make(chan error, 1)
	go func() { waiterDone <- svc.VerifyDetachedLedger(t.Context(), backedLedger) }()
	require.Eventually(t, func() bool {
		flights, _ := svc.replayVerificationFlightStats()
		return flights == 2
	}, time.Second, time.Millisecond)
	close(family.release)
	require.NoError(t, <-ownerDone)
	require.ErrorIs(t, <-waiterDone, shamap.ErrNodeNotInStore)
}

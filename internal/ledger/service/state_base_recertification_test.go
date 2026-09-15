package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type stateBaseRecertificationFixture struct {
	svc       *Service
	db        *nodestore.KVDatabase
	validated *ledger.Ledger
	stateRoot [32]byte
	childHash [32]byte
}

func newStateBaseRecertificationFixture(t *testing.T) *stateBaseRecertificationFixture {
	t.Helper()
	ctx := context.Background()
	db := newTestNodeStore(t, 100_000)
	repositories := newTestRepositories(t, ctx)
	svc := newFastLoadCheckpointService(t, db, repositories, true)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		require.NoError(t, db.Close())
	})

	for branch := range shamap.BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	rawTx, _ := validRelationalTestTransaction(t, 1)
	txBlob, txHash := makeTxMetaBlobForTest(t, rawTx, 0)
	require.NoError(t, svc.openLedger.AddTransactionWithMeta(txHash, txBlob))
	_, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	validated := svc.GetValidatedLedger()
	require.NotNil(t, validated)
	h := validated.Header()

	require.NoError(t, db.WithDurableSnapshot(ctx, func(fingerprint [32]byte) error {
		if err := svc.verifyStoredSHAMap(ctx, h.AccountHash, shamap.TypeState); err != nil {
			return err
		}
		if h.TxHash != ([32]byte{}) {
			if err := svc.verifyStoredSHAMap(ctx, h.TxHash, shamap.TypeTransaction); err != nil {
				return err
			}
		}
		svc.rememberValidatedStateBase(h, fingerprint)
		return nil
	}))

	rootNode, err := db.Fetch(ctx, nodestore.Hash256(h.AccountHash))
	require.NoError(t, err)
	require.NotNil(t, rootNode)
	rootReader, err := shamap.DeserializeFromPrefix(rootNode.Data)
	require.NoError(t, err)
	rootInner, ok := rootReader.(shamap.InnerNodeReader)
	require.True(t, ok)
	var childHash [32]byte
	for branch := range shamap.BranchFactor {
		if rootInner.IsEmptyBranch(branch) {
			continue
		}
		childHash, err = rootInner.ChildHash(branch)
		require.NoError(t, err)
		break
	}
	require.NotEqual(t, [32]byte{}, childHash)
	childNode, err := db.Fetch(ctx, nodestore.Hash256(childHash))
	require.NoError(t, err)
	require.NotNil(t, childNode)

	return &stateBaseRecertificationFixture{
		svc: svc, db: db, validated: validated,
		stateRoot: h.AccountHash, childHash: childHash,
	}
}

func (f *stateBaseRecertificationFixture) invalidate(t *testing.T) {
	t.Helper()
	f.svc.InvalidateFastLoadCheckpointEligibility()
	proof, found := f.svc.currentValidatedStateBaseProof()
	require.False(t, found, "invalidation must clear the old proof: %+v", proof)
	require.EqualValues(t, fastLoadCheckpointInvalidated, f.svc.fastLoadCheckpointState.Load())
}

func requireStateBaseRecertificationUnavailable(t *testing.T, f *stateBaseRecertificationFixture) {
	t.Helper()
	proof, found := f.svc.currentValidatedStateBaseProof()
	require.False(t, found, "failed re-certification published a proof: %+v", proof)
	require.EqualValues(t, fastLoadCheckpointInvalidated, f.svc.fastLoadCheckpointState.Load())
	_, release, available, err := f.svc.AcquireValidatedStateBase(context.Background())
	if err != nil {
		t.Logf("validated state base remained unavailable: %v", err)
	}
	require.False(t, available)
	require.Nil(t, release)
}

func TestStateBaseRecertificationRejectsMissingDurableDescendantWithWarmCache(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	ctx := context.Background()

	provider, ok := f.svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	cache := provider.FullBelowCache()
	generation := cache.Generation()
	require.True(t, cache.Has(generation, f.stateRoot))
	require.True(t, cache.Has(generation, f.childHash))

	child, err := f.db.Fetch(ctx, nodestore.Hash256(f.childHash))
	require.NoError(t, err)
	require.NotNil(t, child)
	child.LedgerSeq = f.validated.Sequence() - 1
	require.NoError(t, f.db.Store(ctx, child))
	require.NoError(t, f.db.Sync(ctx))
	deleted, err := f.db.DeleteBefore(ctx, f.validated.Sequence(), 1)
	require.NoError(t, err)
	require.Positive(t, deleted)
	raw := interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	}(f.db)
	missing, err := raw.FetchDataUncached(ctx, nodestore.Hash256(f.childHash))
	require.NoError(t, err)
	require.Nil(t, missing)
	require.True(t, cache.Has(generation, f.childHash))

	f.invalidate(t)
	err = f.svc.recertifyValidatedStateBase(ctx)
	require.Error(t, err)
	requireStateBaseRecertificationUnavailable(t, f)
}

type blockingStateBaseRecertificationDatabase struct {
	*checkpointTrackingDatabase
	target  nodestore.Hash256
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type missingRecertificationNodeDatabase struct {
	*checkpointTrackingDatabase
	missing nodestore.Hash256
}

func (d *missingRecertificationNodeDatabase) FetchDataUncached(ctx context.Context, hash nodestore.Hash256) ([]byte, error) {
	if hash == d.missing {
		return nil, nil
	}
	return d.checkpointTrackingDatabase.FetchDataUncached(ctx, hash)
}

func TestStateBaseRecertificationRejectsMissingTransactionDescendant(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	root := f.validated.Header().TxHash
	require.NotEqual(t, [32]byte{}, root)
	stored, err := f.db.Fetch(t.Context(), nodestore.Hash256(root))
	require.NoError(t, err)
	reader, err := shamap.DeserializeFromPrefix(stored.Data)
	require.NoError(t, err)
	inner := reader.(shamap.InnerNodeReader)
	var child [32]byte
	for branch := range shamap.BranchFactor {
		if !inner.IsEmptyBranch(branch) {
			child, err = inner.ChildHash(branch)
			require.NoError(t, err)
			break
		}
	}
	require.NotEqual(t, [32]byte{}, child)
	cached, err := f.db.Fetch(t.Context(), nodestore.Hash256(child))
	require.NoError(t, err)
	require.NotNil(t, cached)
	f.svc.nodeStore = &missingRecertificationNodeDatabase{
		checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
		missing:                    nodestore.Hash256(child),
	}
	f.invalidate(t)
	err = f.svc.recertifyValidatedStateBase(t.Context())
	require.ErrorContains(t, err, "transaction tree")
	requireStateBaseRecertificationUnavailable(t, f)
}

func (d *blockingStateBaseRecertificationDatabase) FetchDataUncached(
	ctx context.Context,
	hash nodestore.Hash256,
) ([]byte, error) {
	if hash == d.target {
		d.once.Do(func() { close(d.started) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return d.checkpointTrackingDatabase.FetchDataUncached(ctx, hash)
}

func (d *blockingStateBaseRecertificationDatabase) unblock() {
	close(d.release)
}

func TestStateBaseRecertificationWorkerStopsWhileDurableWalkIsCanceled(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	f.invalidate(t)
	blocked := &blockingStateBaseRecertificationDatabase{
		checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
		target:                     nodestore.Hash256(f.childHash),
		started:                    make(chan struct{}),
		release:                    make(chan struct{}),
	}
	f.svc.nodeStore = blocked
	f.svc.RequestStateBaseRecertification()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("re-certification did not start the durable walk")
	}

	stopped := make(chan struct{})
	go func() {
		f.svc.StopStateBaseRecertification()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stopping re-certification did not join the canceled walk")
	}
	requireStateBaseRecertificationUnavailable(t, f)
}

func TestStateBaseRecertificationMutationDuringWalkPreventsPublication(t *testing.T) {
	for _, mutation := range []string{"invalidation epoch", "retention floor", "cache generation"} {
		t.Run(mutation, func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.invalidate(t)
			blocked := &blockingStateBaseRecertificationDatabase{
				checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
				target:                     nodestore.Hash256(f.childHash), started: make(chan struct{}), release: make(chan struct{}),
			}
			f.svc.nodeStore = blocked
			done := make(chan error, 1)
			go func() { done <- f.svc.recertifyValidatedStateBase(t.Context()) }()
			select {
			case <-blocked.started:
			case <-time.After(time.Second):
				t.Fatal("re-certification did not start the durable walk")
			}
			family := f.svc.shamapFamily.(*backend.NodeStore)
			switch mutation {
			case "invalidation epoch":
				f.svc.InvalidateFastLoadCheckpointEligibility()
			case "retention floor":
				family.SetMinimumLedgerSeq(f.validated.Sequence() + 1)
			case "cache generation":
				family.FullBelowCache().Bump()
			}
			blocked.unblock()
			require.Error(t, <-done)
			requireStateBaseRecertificationUnavailable(t, f)
		})
	}
}

type staleFingerprintStateBaseRecertificationDatabase struct {
	*checkpointTrackingDatabase
	mu          sync.Mutex
	stale       bool
	fingerprint [32]byte
}

func (d *staleFingerprintStateBaseRecertificationDatabase) AcquireDurableSnapshot(
	ctx context.Context,
) ([32]byte, func(), error) {
	fingerprint, release, err := d.checkpointTrackingDatabase.AcquireDurableSnapshot(ctx)
	if err == nil {
		d.mu.Lock()
		d.fingerprint = fingerprint
		d.fingerprint[0] ^= 1
		d.stale = true
		d.mu.Unlock()
	}
	return fingerprint, release, err
}

func (d *staleFingerprintStateBaseRecertificationDatabase) DurableFingerprint(
	ctx context.Context,
) ([32]byte, error) {
	fingerprint, err := d.checkpointTrackingDatabase.DurableFingerprint(ctx)
	d.mu.Lock()
	if d.stale {
		fingerprint = d.fingerprint
	}
	d.mu.Unlock()
	return fingerprint, err
}

func TestStateBaseRecertificationFailsClosedOnStaleFingerprintIdentityOrRetention(t *testing.T) {
	tests := []struct {
		name   string
		before func(*testing.T, *stateBaseRecertificationFixture)
	}{
		{
			name: "fingerprint",
			before: func(t *testing.T, f *stateBaseRecertificationFixture) {
				db := &staleFingerprintStateBaseRecertificationDatabase{
					checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
				}
				f.svc.nodeStore = db
			},
		},
		{
			name: "retention floor",
			before: func(t *testing.T, f *stateBaseRecertificationFixture) {
				family, ok := f.svc.shamapFamily.(*backend.NodeStore)
				require.True(t, ok)
				family.SetMinimumLedgerSeq(f.validated.Sequence() + 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.invalidate(t)
			test.before(t, f)
			err := f.svc.recertifyValidatedStateBase(context.Background())
			require.Error(t, err)
			requireStateBaseRecertificationUnavailable(t, f)
		})
	}

	t.Run("validated identity", func(t *testing.T) {
		f := newStateBaseRecertificationFixture(t)
		f.invalidate(t)
		blocked := &blockingStateBaseRecertificationDatabase{
			checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
			target:                     nodestore.Hash256(f.childHash),
			started:                    make(chan struct{}),
			release:                    make(chan struct{}),
		}
		f.svc.nodeStore = blocked
		alternateHeader := f.validated.Header()
		alternateHeader.CloseFlags ^= header.LCFNoConsensusTime
		alternateHeader.Validated = false
		alternateHeader.Hash = header.CalculateHash(alternateHeader)
		stateMap, err := f.validated.StateMapSnapshot()
		require.NoError(t, err)
		txMap, err := f.validated.TxMapSnapshot()
		require.NoError(t, err)
		alternate, err := ledger.NewFromHeader(alternateHeader, stateMap, txMap, drops.Fees{})
		require.NoError(t, err)
		require.NoError(t, alternate.SetValidated())

		done := make(chan error, 1)
		go func() { done <- f.svc.recertifyValidatedStateBase(context.Background()) }()
		select {
		case <-blocked.started:
		case <-time.After(time.Second):
			t.Fatal("re-certification did not start the durable walk")
		}
		f.svc.mu.Lock()
		f.svc.validatedLedger = alternate
		f.svc.mu.Unlock()
		blocked.unblock()
		err = <-done
		require.Error(t, err)
		requireStateBaseRecertificationUnavailable(t, f)
	})
}

func TestStateBaseRecertificationCarriesProofAcrossValidationDuringWalk(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	initial := f.validated
	f.invalidate(t)
	blocked := &blockingStateBaseRecertificationDatabase{
		checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
		target:                     nodestore.Hash256(f.childHash),
		started:                    make(chan struct{}),
		release:                    make(chan struct{}),
	}
	f.svc.nodeStore = blocked
	f.svc.RequestStateBaseRecertification()
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("re-certification did not start the durable walk")
	}
	_, release, available, err := f.svc.AcquireValidatedStateBase(context.Background())
	require.ErrorContains(t, err, "no matching completeness proof")
	require.False(t, available)
	require.Nil(t, release)

	for i := range 3 {
		var key [32]byte
		key[0], key[31] = 0xfe, 0xff
		data := make([]byte, 12)
		data[11] = byte(i + 50)
		if i == 0 {
			require.NoError(t, f.svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
		} else {
			require.NoError(t, f.svc.openLedger.Update(keylet.Keylet{Key: key}, data))
		}
		_, err = f.svc.AcceptLedger(context.Background())
		require.NoError(t, err)
	}
	f.svc.FlushPersists()
	latest := f.svc.GetValidatedLedger()
	require.NotNil(t, latest)
	require.Greater(t, latest.Sequence(), initial.Sequence())
	require.NotEqual(t, initial.Header().AccountHash, latest.Header().AccountHash)
	blocked.unblock()

	require.Eventually(t, func() bool {
		proof, found := f.svc.currentValidatedStateBaseProof()
		return found && proof.sequence == latest.Sequence() &&
			f.svc.fastLoadCheckpointState.Load() == fastLoadCheckpointEligible
	}, 2*time.Second, 5*time.Millisecond)
	root, release, available, err := f.svc.AcquireValidatedStateBase(context.Background())
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, latest.Header().AccountHash, root)
	require.NotNil(t, release)
	release()
	f.svc.StopStateBaseRecertification()
}

type publicationStateBaseDatabase struct {
	*checkpointTrackingDatabase
	checked chan struct{}
	once    sync.Once
}

func (d *publicationStateBaseDatabase) DurableFingerprint(ctx context.Context) ([32]byte, error) {
	fingerprint, err := d.checkpointTrackingDatabase.DurableFingerprint(ctx)
	d.once.Do(func() { close(d.checked) })
	return fingerprint, err
}

func TestStateBaseRecertificationRejectsMutationAtPublication(t *testing.T) {
	for _, mutation := range []string{"durable tip", "family retention", "online retention"} {
		t.Run(mutation, func(t *testing.T) {
			f := newStateBaseRecertificationFixture(t)
			f.svc.StopStateBaseRecertification()
			f.invalidate(t)
			db := &publicationStateBaseDatabase{
				checkpointTrackingDatabase: &checkpointTrackingDatabase{Database: f.db, uncached: f.db},
				checked:                    make(chan struct{}),
			}
			f.svc.nodeStore = db
			var floor atomic.Uint32
			f.svc.SetMinimumOnlineFunc(floor.Load)
			release := f.svc.BeginStateBaseRetentionChange()
			var once sync.Once
			finish := func() { once.Do(release) }
			defer finish()
			done := make(chan error, 1)
			go func() { done <- f.svc.recertifyValidatedStateBase(t.Context()) }()
			select {
			case <-db.checked:
			case <-time.After(time.Second):
				t.Fatal("verification did not reach the final generation check")
			}
			switch mutation {
			case "durable tip":
				f.svc.invalidatePersistedValidatedTipHash(f.validated.Sequence(), f.validated.Hash())
			case "family retention":
				f.svc.shamapFamily.(*backend.NodeStore).SetMinimumLedgerSeq(f.validated.Sequence() + 1)
			case "online retention":
				floor.Store(f.validated.Sequence() + 1)
			}
			finish()
			require.Error(t, <-done)
			requireStateBaseRecertificationUnavailable(t, f)
		})
	}
}

type publicationRetentionFamily struct {
	*backend.NodeStore
	acquired chan struct{}
}

func (f *publicationRetentionFamily) AcquireMinimumLedgerSeq() (uint32, func()) {
	floor, release := f.NodeStore.AcquireMinimumLedgerSeq()
	close(f.acquired)
	return floor, release
}

func TestStateBasePublicationWaitingForServiceDoesNotBlockRetention(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	f.svc.StopStateBaseRecertification()
	f.invalidate(t)
	family := &publicationRetentionFamily{
		NodeStore: f.svc.shamapFamily.(*backend.NodeStore),
		acquired:  make(chan struct{}),
	}
	f.svc.shamapFamily = family
	pending := &stateBaseRecertification{
		tip: f.validated.Header(), epoch: f.svc.stateBaseMutationEpoch,
	}
	f.svc.stateBaseRecertification = pending
	fingerprint, err := f.db.DurableFingerprint(t.Context())
	require.NoError(t, err)

	f.svc.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(f.svc.mu.Unlock) }
	defer unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.publishStateBaseRecertification(t.Context(), pending, fingerprint)
		done <- err
	}()
	require.Eventually(t, func() bool {
		if !f.svc.stateBaseRetentionMu.TryLock() {
			return true
		}
		f.svc.stateBaseRetentionMu.Unlock()
		return false
	}, time.Second, time.Millisecond)
	select {
	case <-family.acquired:
	case <-time.After(20 * time.Millisecond):
	}

	advanced := make(chan struct{})
	go func() {
		family.SetMinimumLedgerSeq(f.validated.Sequence() + 1)
		close(advanced)
	}()
	var progressed bool
	select {
	case <-advanced:
		progressed = true
	case <-time.After(time.Second):
	}
	unlock()
	publicationErr := <-done
	<-advanced
	require.True(t, progressed, "proof publication blocked retention while waiting for the service")
	require.ErrorContains(t, publicationErr, "below retention")
	requireStateBaseRecertificationUnavailable(t, f)
}

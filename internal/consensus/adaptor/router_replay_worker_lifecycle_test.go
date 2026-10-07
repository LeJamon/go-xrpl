package adaptor

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func newBlockedReplayWorker(t *testing.T) (*Router, *service.Service, standardReplayTestLink, func()) {
	t.Helper()
	base := backend.NewMemory()
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	family := &replayPublicationBarrierFamily{Family: base, entered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(family.resume) })
	svc, err := service.New(service.Config{
		Standalone: true, GenesisConfig: genesis.DefaultConfig(), SHAMapFamily: family,
		ReplayFaultPath: filepath.Join(t.TempDir(), "replay-fault.json"),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	adaptor, sender := newRecordingAdaptor(t, svc)
	router := newTestRouter(nil, adaptor, nil)
	router.SetAcquisitionMemoryLimit(2 << 30)
	c := router.catchupReplay
	links := buildStandardReplayTestChain(t, router, svc.GetClosedLedger(), 2)
	armStandardReplayTestPipeline(t, router, adaptor, sender, links)
	c.standardReplay.pivotReady = false
	completeStandardReplayTestLink(t, router, links[0])
	family.root = links[0].ledger.Header().AccountHash
	family.blocked.Store(true)
	c.standardReplay.pivotReady = true
	c.startTasks(t.Context())
	t.Cleanup(func() {
		resume()
		c.stopTasks()
		router.StopAcquisitions()
		require.Zero(t, c.acquisitionMemory.Snapshot().Used)
	})
	c.startStandardReplayDrain()
	select {
	case <-family.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reach durable verification")
	}
	return router, svc, links[0], resume
}

func TestReplayWorkerCancellationRetainsSingleOwner(t *testing.T) {
	router, svc, link, _ := newBlockedReplayWorker(t)
	c := router.catchupReplay
	c.acquisitionMu.Lock()
	owner := c.standardReplayDrainOwner
	identity := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	require.NotNil(t, owner)
	for range 20 {
		c.startStandardReplayDrain()
	}
	c.acquisitionMu.Lock()
	require.Same(t, owner, c.standardReplayDrainOwner)
	c.acquisitionMu.Unlock()
	_, canceled := c.cancelStandardReplayPipelineIdentity(identity, "test_superseded")
	require.True(t, canceled)
	require.Eventually(t, func() bool {
		c.acquisitionMu.Lock()
		defer c.acquisitionMu.Unlock()
		return c.standardReplayDrainOwner == nil
	}, time.Second, time.Millisecond)
	_, err := svc.GetLedgerByHash(link.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.Zero(t, c.replayPipelineApplied.Load())
	require.False(t, svc.ReplayBlocked())
}

func TestReplayWorkerQueuedResultCannotAdvanceRetiredGeneration(t *testing.T) {
	router, svc, link, resume := newBlockedReplayWorker(t)
	c := router.catchupReplay
	resume()
	var result *standardReplayDrainResult
	select {
	case result = <-c.standardReplayResults:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not queue its prepared result")
	}
	require.NoError(t, result.err)
	c.acquisitionMu.Lock()
	identity := c.standardReplayIdentityLocked()
	c.acquisitionMu.Unlock()
	_, canceled := c.cancelStandardReplayPipelineIdentity(identity, "test_queued_superseded")
	require.True(t, canceled)
	c.handleStandardReplayDrainResult(result)
	c.acquisitionMu.Lock()
	require.Greater(t, c.standardReplay.generation, identity.generation)
	require.Zero(t, c.standardReplay.anchorSeq)
	require.Nil(t, c.standardReplayDrainOwner)
	c.acquisitionMu.Unlock()
	require.Nil(t, result.prepared)
	require.Nil(t, result.entry.txMap)
	require.Nil(t, result.entry.acquisition)
	require.Zero(t, c.replayPipelineApplied.Load())
	stored, err := svc.GetLedgerByHash(link.hash)
	require.NoError(t, err, "publication completed before cancellation acquired the commit lock")
	require.NotNil(t, stored)
	require.False(t, svc.ReplayBlocked())
}

func TestReplayWorkerShutdownCancelsCommitLockWait(t *testing.T) {
	router, svc, link, resume := newBlockedReplayWorker(t)
	c := router.catchupReplay
	c.replayCommitMu.Lock()
	unlock := sync.OnceFunc(c.replayCommitMu.Unlock)
	defer unlock()
	resume()
	done := make(chan struct{})
	go func() { c.stopTasks(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker shutdown waited for the publication lock")
	}
	unlock()
	_, err := svc.GetLedgerByHash(link.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.False(t, svc.ReplayBlocked())
}

func TestReplayWorkerRechecksFaultGateBeforePublication(t *testing.T) {
	router, svc, link, resume := newBlockedReplayWorker(t)
	c := router.catchupReplay
	svc.RecordReplayPreparationFailure(context.Background(), link.ledger.Header(), nil,
		svc.GetClosedLedger(), true, errors.New("concurrent replay fault"))
	require.True(t, svc.ReplayBlocked())
	resume()
	select {
	case result := <-c.standardReplayResults:
		require.Error(t, result.err)
		c.handleStandardReplayDrainResult(result)
	case <-time.After(3 * time.Second):
		t.Fatal("fault-gated worker did not finish")
	}
	_, err := svc.GetLedgerByHash(link.hash)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.Zero(t, c.replayPipelineApplied.Load())
}

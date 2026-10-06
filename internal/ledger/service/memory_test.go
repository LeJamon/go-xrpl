package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type relayTestMemory struct {
	mu          sync.Mutex
	limit, used uint64
}

func (b *relayTestMemory) Limit() uint64            { return b.limit }
func (b *relayTestMemory) Changed() <-chan struct{} { return nil }
func (b *relayTestMemory) TryReserve(n uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return false
	}
	b.used += n
	return true
}
func (b *relayTestMemory) Release(n uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.used {
		panic("relay test memory underflow")
	}
	b.used -= n
}

func TestServiceRelayCacheSharedMemoryEvictsAndReleases(t *testing.T) {
	budget := &relayTestMemory{limit: relayCacheStructureBytes + 4}
	s := &Service{config: Config{RelayMemory: budget}}
	first, second := [32]byte{1}, [32]byte{2}
	s.rememberRelayTransaction(first, []byte{1, 2, 3}, false)
	s.rememberRelayTransaction(second, []byte{4, 5, 6}, false)
	_, _, _, ok := s.relayCacheGet(first)
	require.False(t, ok)
	blob, _, _, ok := s.relayCacheGet(second)
	require.True(t, ok)
	require.Equal(t, []byte{4, 5, 6}, blob)
	require.Equal(t, uint64(relayCacheStructureBytes+3), budget.used)
	s.rememberRelayTransaction(second, []byte{7, 8}, true)
	require.Equal(t, uint64(relayCacheStructureBytes+2), budget.used)
	record := s.relayTxCache[second]
	record.seenAt = time.Now().Add(-relayTxCacheTTL - time.Second)
	s.relayTxCache[second] = record
	_, _, _, ok = s.relayCacheGet(second)
	require.False(t, ok)
	require.Zero(t, budget.used)
	s.rememberRelayTransaction(first, []byte{1}, false)
	s.clearRelayCache()
	require.Zero(t, budget.used)
	s.clearRelayCache()
}

func TestServiceRelayCacheSkipsUnavailableSharedCapacity(t *testing.T) {
	budget := &relayTestMemory{limit: relayCacheStructureBytes - 1}
	s := &Service{config: Config{RelayMemory: budget}}
	s.rememberRelayTransaction([32]byte{1}, []byte{1}, false)
	require.Empty(t, s.relayTxCache)
	require.Zero(t, budget.used)
}

func TestPersistenceAdmissionResumesAfterWorkReturns(t *testing.T) {
	s := &Service{config: Config{PersistenceMemory: &relayTestMemory{limit: 1 << 20}}}
	s.persistStarted = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releases := make([]func(), 0, persistenceAdmissionLimit)
	for range persistenceAdmissionLimit {
		release, err := s.acquirePersistenceAdmission(ctx)
		require.NoError(t, err)
		releases = append(releases, release)
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
	})
	type result struct {
		release func()
		err     error
	}
	ready := make(chan result, 1)
	go func() {
		release, err := s.acquirePersistenceAdmission(ctx)
		ready <- result{release, err}
	}()
	require.Eventually(t, func() bool {
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		return s.persistCapacityWake != nil
	}, time.Second, time.Millisecond)
	releases[0]()
	got := <-ready
	require.NoError(t, got.err)
	got.release()
	got.release()
}

func TestPersistenceAdmissionShutdownUnblocksProducer(t *testing.T) {
	s := &Service{config: Config{PersistenceMemory: &relayTestMemory{limit: 1 << 20}}}
	s.persistStarted = true
	s.persistQueue = make([]*persistJob, persistenceAdmissionLimit)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := s.acquirePersistenceAdmission(ctx)
		result <- err
	}()
	require.Eventually(t, func() bool {
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		return s.persistCapacityWake != nil
	}, time.Second, time.Millisecond)
	s.persistenceWorker.stopAdmissions()
	require.ErrorIs(t, <-result, errServiceNotRunning)
	require.Zero(t, s.persistAdmissions)
}

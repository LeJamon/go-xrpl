package shamap

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanResidencyReleaseAcrossPendingForks(t *testing.T) {
	family := newMemoryFamily()
	base, err := NewBacked(TypeState, family)
	require.NoError(t, err)

	values := []map[[32]byte][]byte{{
		{0xF0}: bytes.Repeat([]byte{0xA0}, 12),
	}}
	require.NoError(t, base.Put([32]byte{0xF0}, values[0][[32]byte{0xF0}]))
	require.NoError(t, base.StoreDirty(func(entries []FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))

	roots := []*SHAMap{base}
	latest := base
	for i := range 8 {
		fork, forkErr := latest.MutableFork()
		require.NoError(t, forkErr)
		key := [32]byte{byte((i + 1) << 4)}
		value := bytes.Repeat([]byte{byte(0xB0 + i)}, 12+i)
		require.NoError(t, fork.Put(key, value))

		nextValues := cloneResidencyValues(values[len(values)-1])
		nextValues[key] = bytes.Clone(value)
		values = append(values, nextValues)
		roots = append(roots, fork)
		latest = fork
	}

	for i := 1; i < len(roots); i++ {
		if roots[i].tree.root == roots[i-1].tree.root {
			t.Fatalf("fork %d shares its root with fork %d", i, i-1)
		}
		require.True(t, roots[i].tree.root.IsDirty(), "fork %d should remain pending", i)
	}

	stop := make(chan struct{})
	var stopOnce sync.Once
	var readers sync.WaitGroup
	readErrors := make(chan error, 1)
	stopReaders := func() {
		stopOnce.Do(func() {
			close(stop)
			readers.Wait()
		})
	}
	t.Cleanup(stopReaders)

	latestReady := startResidencyReader(latest, values[len(values)-1], stop, &readers, readErrors)
	<-latestReady

	rootHashes := make([][32]byte, len(roots)-1)
	for i, root := range roots[:len(roots)-1] {
		token := root.PrepareCleanResidencyRelease()
		require.NotNil(t, token, "fork %d release token", i)
		require.NoError(t, root.StoreDirty(func(entries []FlushEntry) error {
			return family.StoreBatch(t.Context(), entries)
		}))
		rootHash, hashErr := root.Hash()
		require.NoError(t, hashErr)
		rootHashes[i] = rootHash

		immutable, snapshotErr := root.SnapshotImmutable()
		require.NoError(t, snapshotErr)
		snapshotHash, hashErr := immutable.Hash()
		require.NoError(t, hashErr)
		require.Equal(t, rootHash, snapshotHash, "snapshot %d hash", i)
		ready := startResidencyReader(immutable, values[i], stop, &readers, readErrors)
		<-ready
		runtime.Gosched()
		require.True(t, root.AcknowledgeCleanResidencyRelease(token), "fork %d release", i)
	}

	stopReaders()
	select {
	case err := <-readErrors:
		t.Fatal(err)
	default:
	}

	for i, root := range roots[:len(roots)-1] {
		gotHash, hashErr := root.Hash()
		require.NoError(t, hashErr)
		require.Equal(t, rootHashes[i], gotHash, "released root %d hash changed", i)
		assertResidencyValues(t, root, values[i])
	}
	assertResidencyValues(t, latest, values[len(values)-1])

	finalToken := latest.PrepareCleanResidencyRelease()
	require.NotNil(t, finalToken)
	require.NoError(t, latest.StoreDirty(func(entries []FlushEntry) error {
		return family.StoreBatch(t.Context(), entries)
	}))
	finalHash, err := latest.Hash()
	require.NoError(t, err)
	require.True(t, latest.AcknowledgeCleanResidencyRelease(finalToken))

	reloaded, err := NewFromRootHash(TypeState, finalHash, family)
	require.NoError(t, err)
	assertResidencyValues(t, reloaded, values[len(values)-1])
}

func cloneResidencyValues(values map[[32]byte][]byte) map[[32]byte][]byte {
	clone := make(map[[32]byte][]byte, len(values)+1)
	for key, value := range values {
		clone[key] = bytes.Clone(value)
	}
	return clone
}

func assertResidencyValues(t *testing.T, sm *SHAMap, values map[[32]byte][]byte) {
	t.Helper()
	for key, want := range values {
		item, found, err := sm.Get(key)
		require.NoError(t, err)
		require.True(t, found, "key %x missing", key)
		require.Equal(t, want, item.Data(), "key %x", key)
	}
}

func startResidencyReader(sm *SHAMap, values map[[32]byte][]byte, stop <-chan struct{}, readers *sync.WaitGroup, readErrors chan<- error) <-chan struct{} {
	ready := make(chan struct{})
	readers.Add(1)
	go func() {
		defer readers.Done()
		readySignaled := false
		signalReady := func() {
			if !readySignaled {
				close(ready)
				readySignaled = true
			}
		}
		defer signalReady()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for key, want := range values {
				item, found, err := sm.Get(key)
				if err != nil {
					reportResidencyReadError(readErrors, err)
					return
				}
				if !found || !bytes.Equal(item.Data(), want) {
					reportResidencyReadError(readErrors, fmt.Errorf("reader found wrong value for %x", key))
					return
				}
			}
			signalReady()
			runtime.Gosched()
		}
	}()
	return ready
}

func reportResidencyReadError(readErrors chan<- error, err error) {
	select {
	case readErrors <- err:
	default:
	}
}

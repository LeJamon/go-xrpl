package service

import (
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	shamapbackend "github.com/LeJamon/go-xrpl/shamap/backend"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type residencyLedgerRecord struct {
	seq      uint32
	hash     [32]byte
	root     [32]byte
	entries  []residencyEntry
	snapshot *ledgerpkg.Ledger
	state    *shamap.SHAMap
	cold     *shamap.SHAMap
}

type residencyEntry struct {
	key   [32]byte
	value []byte
}

func TestService_RotatingRetentionKeepsHistoricalStateReadable(t *testing.T) {
	ctx := t.Context()
	const (
		initialEntries    = 128
		workingSetEntries = 32
		workingRounds     = 64
		rotateEvery       = 16
		cleanCacheLimit   = 16 << 10
	)
	path := filepath.Join(t.TempDir(), "nodes")
	options := kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}
	backend, err := kvpebble.NewRotating(path, options)
	require.NoError(t, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	cleanBudget := &preferredMemoryBudget{limit: cleanCacheLimit}
	family := shamapbackend.New(db, shamapbackend.NodeStoreOptions{
		CleanCacheBytes:   cleanCacheLimit,
		CleanMemoryBudget: cleanBudget,
	})
	require.True(t, family.UsesDatabase(db))
	familyClosed := false

	svc, err := New(Config{
		PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes},
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         db,
		SHAMapFamily:      family,
		LedgerCacheSize:   8,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		if !familyClosed {
			require.NoError(t, family.Close())
			familyClosed = true
		}
		require.Zero(t, family.SHAMapCleanResidency().Stats().UsedBytes)
		require.Zero(t, cleanBudget.Used())
	})

	// Keep background recertification out of this deterministic lifecycle test;
	// each rotation below explicitly exercises the retention admission protocol.
	svc.recertificationMu.Lock()
	svc.recertificationStopped = true
	svc.recertificationMu.Unlock()

	initialSet := residencyEntries(0, 1, initialEntries)
	for _, entry := range initialSet {
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: entry.key}, entry.value))
	}
	initial := closeStoredLedgerFixture(t, svc)
	svc.FlushPersists()
	require.NoError(t, svc.flushPersists(ctx))
	committed, err := db.RotateGeneration(ctx, initial.Sequence(), 1)
	require.NoError(t, err)
	require.True(t, committed)
	lastRotated, minimumOnline := db.GenerationState()
	require.Equal(t, initial.Sequence(), lastRotated)
	require.Equal(t, uint32(1), minimumOnline)

	initialRecord := captureResidencyRecord(t, ctx, initial, initialSet, family)
	records := []residencyLedgerRecord{initialRecord}

	for round := uint32(1); round <= workingRounds; round++ {
		workingSet := residencyEntries(round, 0, workingSetEntries)
		svc.mu.Lock()
		var insertErr error
		for _, entry := range workingSet {
			if err := svc.openLedger.Insert(keylet.Keylet{Key: entry.key}, entry.value); err != nil {
				insertErr = err
				break
			}
		}
		svc.mu.Unlock()
		require.NoError(t, insertErr)
		closed := closeStoredLedgerFixture(t, svc)
		require.NoError(t, svc.flushPersists(ctx))
		record := captureResidencyRecord(t, ctx, closed, workingSet, family)
		records = append(records, record)

		if round%rotateEvery != 0 {
			continue
		}

		// Install a real pending persistence write before retention preparation.
		// The deferred release also unblocks the worker if an assertion fails.
		svc.mu.Lock()
		pending := svc.openLedger
		pendingErr := pending.Insert(keylet.Keylet{Key: residencyKey(round, workingSetEntries+1)}, residencyValue(round, workingSetEntries+1))
		svc.mu.Unlock()
		require.NoError(t, pendingErr)
		storeBatchStart := make(chan struct{})
		storeBatchResume := make(chan struct{})
		db.storeBatchOnce = sync.Once{}
		db.storeBatchStart = storeBatchStart
		db.storeBatchResume = storeBatchResume
		var resumeOnce sync.Once
		resumeStoreBatch := func() {
			resumeOnce.Do(func() { close(storeBatchResume) })
		}
		defer resumeStoreBatch()
		svc.enqueueNodePersist(pending)
		select {
		case <-storeBatchStart:
		case <-time.After(5 * time.Second):
			t.Fatal("successor persistence did not reach the real node-store write")
		}

		prepareDone := make(chan retentionPreparationResult, 1)
		go func() {
			seq, guard, prepareErr := svc.PrepareStateRetention(ctx, closed.Sequence(), nil)
			prepareDone <- retentionPreparationResult{seq: seq, guard: guard, err: prepareErr}
		}()
		wait := time.NewTimer(250 * time.Millisecond)
		select {
		case result := <-prepareDone:
			if !wait.Stop() {
				<-wait.C
			}
			t.Fatalf("retention preparation bypassed blocked persistence: seq=%d err=%v", result.seq, result.err)
		case <-wait.C:
		}
		resumeStoreBatch()

		var prepared retentionPreparationResult
		select {
		case prepared = <-prepareDone:
		case <-time.After(10 * time.Second):
			t.Fatal("retention preparation did not complete after persistence resumed")
		}
		require.NoError(t, prepared.err)
		require.Equal(t, closed.Sequence(), prepared.seq)
		require.NotNil(t, prepared.guard)

		rotating, ok := db.generation.(*nodestore.RotatingKVDatabase)
		require.True(t, ok)
		committed, err = rotating.RotateGenerationWithRetention(
			ctx,
			prepared.seq,
			1,
			prepared.guard,
			family.BeginPrune,
		)
		require.NoError(t, err)
		require.True(t, committed)
		lastRotated, minimumOnline = db.GenerationState()
		require.Equal(t, closed.Sequence(), lastRotated)
		require.Equal(t, uint32(1), minimumOnline)

		for _, historical := range records {
			require.NoError(t, svc.verifyStoredSHAMap(ctx, historical.root, shamap.TypeState))
		}
	}

	require.NoError(t, svc.flushPersists(ctx))
	require.Equal(t, 1+workingRounds, len(records))

	retainedBefore := retainedColdResidencyBytes(t, records)
	warmBefore := retainedSnapshotResidencyBytes(t, records)
	for pass := 0; pass < 3; pass++ {
		for i := range records {
			readResidencyRecord(t, ctx, records[i])
			readActiveColdResidencyRecord(t, ctx, records[i])
		}
		stats := family.SHAMapCleanResidency().Stats()
		require.Equal(t, uint64(cleanCacheLimit), stats.Limit)
		require.LessOrEqual(t, stats.UsedBytes, stats.Limit)
	}
	retainedAfter := retainedColdResidencyBytes(t, records)
	warmAfter := retainedSnapshotResidencyBytes(t, records)
	require.LessOrEqual(t, retainedAfter, retainedBefore+2*cleanCacheLimit,
		"historical reads grew retained decoded trees beyond the bounded cache")
	require.LessOrEqual(t, warmAfter, warmBefore+2*cleanCacheLimit,
		"historical snapshot reads grew retained decoded trees beyond the bounded cache")
	stats := family.SHAMapCleanResidency().Stats()
	t.Logf("clean residency: limit=%d used=%d nodes=%d evictions=%d; cold retained=%d->%d warm retained=%d->%d",
		stats.Limit, stats.UsedBytes, stats.Nodes, stats.Evictions, retainedBefore, retainedAfter, warmBefore, warmAfter)
	require.Positive(t, stats.Evictions,
		"disjoint working sets did not exercise clean-cache eviction")

	latest := records[len(records)-1]
	svc.Stop()
	require.NoError(t, family.Close())
	familyClosed = true
	require.Zero(t, family.SHAMapCleanResidency().Stats().UsedBytes)
	require.Zero(t, cleanBudget.Used())

	// Reopen the actual rotating Pebble store and recover the claimed-complete
	// tip from its persisted header. The state root and an older root are read
	// through the reopened family, so this cannot succeed from warm ledger maps.
	reopenedBackend, err := kvpebble.NewRotating(path, options)
	require.NoError(t, err)
	reopenedBase, err := nodestore.NewRotatingKVDatabase(reopenedBackend, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	reopenedDB := &countingGenerationDatabase{Database: reopenedBase, generation: reopenedBase}
	reopenedCleanBudget := &preferredMemoryBudget{limit: cleanCacheLimit}
	reopenedFamily := shamapbackend.New(reopenedDB, shamapbackend.NodeStoreOptions{
		CleanCacheBytes:   cleanCacheLimit,
		CleanMemoryBudget: reopenedCleanBudget,
	})
	require.True(t, reopenedFamily.UsesDatabase(reopenedDB))
	restarted, err := New(Config{
		PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes},
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         reopenedDB,
		SHAMapFamily:      reopenedFamily,
	})
	require.NoError(t, err)
	require.NoError(t, restarted.Start())
	t.Cleanup(func() {
		restarted.Stop()
		require.NoError(t, reopenedFamily.Close())
		require.Zero(t, reopenedFamily.SHAMapCleanResidency().Stats().UsedBytes)
		require.Zero(t, reopenedCleanBudget.Used())
	})

	recovered, err := restarted.loadStoredLedgerByHash(ctx, latest.hash)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, latest.seq, recovered.Sequence())
	recoveredRoot, err := recovered.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, latest.root, recoveredRoot)
	restarted.PromoteStoredValidatedLedgerAt(latest.seq, latest.hash, time.Time{})
	require.Equal(t, latest.hash, restarted.GetValidatedLedger().Hash())
	recoveredState, err := recovered.StateMapSnapshot()
	require.NoError(t, err)
	readResidencyMap(t, ctx, recoveredState, latest)
	for _, historical := range records[:len(records)-1] {
		readColdResidencyRecord(t, ctx, reopenedFamily, historical)
	}
	lastRotated, minimumOnline = reopenedDB.GenerationState()
	require.Equal(t, latest.seq, lastRotated)
	require.Equal(t, uint32(1), minimumOnline)
}

type retentionPreparationResult struct {
	seq   uint32
	guard func(context.Context) (func(), error)
	err   error
}

func residencyKey(round, entry uint32) [32]byte {
	var key [32]byte
	binary.BigEndian.PutUint32(key[0:4], round)
	binary.BigEndian.PutUint32(key[28:32], entry)
	return key
}

func residencyValue(round, entry uint32) []byte {
	value := make([]byte, 24)
	binary.BigEndian.PutUint32(value[0:4], round)
	binary.BigEndian.PutUint32(value[20:24], entry)
	return value
}

func residencyEntries(round, first, count uint32) []residencyEntry {
	entries := make([]residencyEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		entry := first + i
		entries = append(entries, residencyEntry{
			key:   residencyKey(round, entry),
			value: residencyValue(round, entry),
		})
	}
	return entries
}

func captureResidencyRecord(
	t *testing.T,
	ctx context.Context,
	l *ledgerpkg.Ledger,
	entries []residencyEntry,
	family shamap.Family,
) residencyLedgerRecord {
	t.Helper()
	snapshot, err := l.Snapshot()
	require.NoError(t, err)
	stateRoot, err := snapshot.StateMapHash()
	require.NoError(t, err)
	state, err := snapshot.StateMapSnapshot()
	require.NoError(t, err)
	cold, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, stateRoot, family)
	require.NoError(t, err)
	ownedEntries := make([]residencyEntry, len(entries))
	for i, entry := range entries {
		ownedEntries[i] = residencyEntry{key: entry.key, value: append([]byte(nil), entry.value...)}
	}
	return residencyLedgerRecord{
		seq:      snapshot.Sequence(),
		hash:     snapshot.Hash(),
		root:     stateRoot,
		entries:  ownedEntries,
		snapshot: snapshot,
		state:    state,
		cold:     cold,
	}
}

func readResidencyRecord(t *testing.T, ctx context.Context, record residencyLedgerRecord) {
	t.Helper()
	require.NotNil(t, record.state)
	readResidencyMap(t, ctx, record.state, record)
}

func readActiveColdResidencyRecord(t *testing.T, ctx context.Context, record residencyLedgerRecord) {
	t.Helper()
	require.NotNil(t, record.cold)
	readResidencyMap(t, ctx, record.cold, record)
}

func readColdResidencyRecord(t *testing.T, ctx context.Context, family shamap.Family, record residencyLedgerRecord) {
	t.Helper()
	cold, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, record.root, family)
	require.NoError(t, err)
	readResidencyMap(t, ctx, cold, record)
}

func readResidencyMap(t *testing.T, ctx context.Context, state *shamap.SHAMap, record residencyLedgerRecord) {
	t.Helper()
	for _, entry := range record.entries {
		item, found, err := state.GetContext(ctx, entry.key)
		require.NoError(t, err)
		require.True(t, found, "missing historical key seq=%d key=%x", record.seq, entry.key[:4])
		require.Equal(t, entry.value, item.Data(), "historical value seq=%d", record.seq)
	}
}

func retainedColdResidencyBytes(t *testing.T, records []residencyLedgerRecord) uint64 {
	t.Helper()
	var total uint64
	for _, record := range records {
		retained, err := record.cold.RetainedMemoryBytes()
		require.NoError(t, err)
		total += retained
	}
	return total
}

func retainedSnapshotResidencyBytes(t *testing.T, records []residencyLedgerRecord) uint64 {
	t.Helper()
	var total uint64
	for _, record := range records {
		require.NotNil(t, record.state)
		retained, err := record.state.RetainedMemoryBytes()
		require.NoError(t, err)
		total += retained
	}
	return total
}

func (d *countingGenerationDatabase) DurableFingerprint(ctx context.Context) ([32]byte, error) {
	durable, ok := d.Database.(interface {
		DurableFingerprint(context.Context) ([32]byte, error)
	})
	if !ok {
		return [32]byte{}, errors.New("test database does not support durable fingerprints")
	}
	return durable.DurableFingerprint(ctx)
}

func (d *countingGenerationDatabase) StoreDurable(ctx context.Context, node *nodestore.Node) error {
	durable, ok := d.Database.(interface {
		StoreDurable(context.Context, *nodestore.Node) error
	})
	if !ok {
		return errors.New("test database does not support durable stores")
	}
	return durable.StoreDurable(ctx, node)
}

func (d *countingGenerationDatabase) WithDurableSnapshot(ctx context.Context, fn func([32]byte) error) error {
	durable, ok := d.Database.(interface {
		WithDurableSnapshot(context.Context, func([32]byte) error) error
	})
	if !ok {
		return errors.New("test database does not support durable snapshots")
	}
	return durable.WithDurableSnapshot(ctx, fn)
}

func (d *countingGenerationDatabase) AcquireDurableSnapshot(ctx context.Context) ([32]byte, func(), error) {
	durable, ok := d.Database.(interface {
		AcquireDurableSnapshot(context.Context) ([32]byte, func(), error)
	})
	if !ok {
		return [32]byte{}, nil, errors.New("test database does not support durable snapshots")
	}
	return durable.AcquireDurableSnapshot(ctx)
}

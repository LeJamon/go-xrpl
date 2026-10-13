package service

import (
	"context"
	"encoding/binary"
	"path/filepath"
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
	key      [32]byte
	value    []byte
	snapshot *ledgerpkg.Ledger
	retained uint64
}

func TestService_RotatingRetentionKeepsHistoricalStateReadable(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "nodes")
	options := kvpebble.Options{BlockCacheBytes: 16 << 20, MaxOpenFiles: 200}
	backend, err := kvpebble.NewRotating(path, options)
	require.NoError(t, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	family := shamapbackend.New(db)

	svc, err := New(Config{
		PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes},
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         base,
		SHAMapFamily:      family,
		LedgerCacheSize:   256,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(func() {
		svc.Stop()
		_ = db.Close()
	})

	// Keep background recertification out of this deterministic lifecycle test;
	// each rotation below explicitly exercises the retention admission protocol.
	svc.recertificationMu.Lock()
	svc.recertificationStopped = true
	svc.recertificationMu.Unlock()

	const initialEntries = 32
	for i := 1; i <= initialEntries; i++ {
		key := residencyKey(0, uint32(i))
		value := residencyValue(0, uint32(i))
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, value))
	}
	initial := closeStoredLedgerFixture(t, svc)
	svc.FlushPersists()
	committed, err := db.RotateGeneration(ctx, initial.Sequence(), 1)
	require.NoError(t, err)
	require.True(t, committed)
	lastRotated, minimumOnline := db.GenerationState()
	require.Equal(t, initial.Sequence(), lastRotated)
	require.Equal(t, uint32(1), minimumOnline)

	initialRecord := captureResidencyRecord(t, initial, residencyKey(0, 1), residencyValue(0, 1))
	records := []residencyLedgerRecord{initialRecord}
	var storeBatchResume chan struct{}

	for round := uint32(1); round <= 3; round++ {
		key := residencyKey(round, 0)
		value := residencyValue(round, 0)
		svc.mu.Lock()
		insertErr := svc.openLedger.Insert(keylet.Keylet{Key: key}, value)
		svc.mu.Unlock()
		require.NoError(t, insertErr)
		closed := closeStoredLedgerFixture(t, svc)
		if round == 1 {
			// closeStoredLedgerFixture verifies the successor synchronously, so
			// install the gate only after publication. Requeueing the published
			// ledger's dirty successor through the real persistence worker gives
			// retention a genuine pending write to wait for without blocking
			// successor construction.
			require.NoError(t, svc.flushPersists(ctx))
			svc.mu.Lock()
			pending := svc.openLedger
			pendingErr := pending.Insert(keylet.Keylet{Key: residencyKey(round, 1)}, residencyValue(round, 1))
			svc.mu.Unlock()
			require.NoError(t, pendingErr)
			storeBatchStart := make(chan struct{})
			storeBatchResume = make(chan struct{})
			db.storeBatchStart = storeBatchStart
			db.storeBatchResume = storeBatchResume
			svc.enqueueNodePersist(pending)
			select {
			case <-storeBatchStart:
			case <-time.After(5 * time.Second):
				t.Fatal("successor persistence did not reach the real node-store write")
			}
		}

		prepareDone := make(chan retentionPreparationResult, 1)
		prepareStarted := make(chan struct{})
		go func() {
			close(prepareStarted)
			seq, guard, prepareErr := svc.PrepareStateRetention(ctx, closed.Sequence(), nil)
			prepareDone <- retentionPreparationResult{seq: seq, guard: guard, err: prepareErr}
		}()
		if round == 1 {
			<-prepareStarted
			select {
			case result := <-prepareDone:
				t.Fatalf("retention preparation bypassed blocked persistence: seq=%d err=%v", result.seq, result.err)
			default:
			}
			close(storeBatchResume)
		}

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

		record := captureResidencyRecord(t, closed, key, value)
		records = append(records, record)
		for _, historical := range records {
			readResidencyRecord(t, historical)
			readColdResidencyRecord(t, ctx, family, historical)
			require.NoError(t, svc.verifyStoredSHAMap(ctx, historical.root, shamap.TypeState))
		}
	}

	require.NoError(t, svc.flushPersists(ctx))
	require.GreaterOrEqual(t, len(records), 4)

	latest := records[len(records)-1]
	svc.Stop()
	require.NoError(t, db.Close())

	// Reopen the actual rotating Pebble store and recover the claimed-complete
	// tip from its persisted header. The state root and an older root are read
	// through the reopened family, so this cannot succeed from warm ledger maps.
	reopenedBackend, err := kvpebble.NewRotating(path, options)
	require.NoError(t, err)
	reopenedBase, err := nodestore.NewRotatingKVDatabase(reopenedBackend, nodestore.DatabaseConfig{})
	require.NoError(t, err)
	reopenedDB := &countingGenerationDatabase{Database: reopenedBase, generation: reopenedBase}
	reopenedFamily := shamapbackend.New(reopenedDB)
	restarted, err := New(Config{
		PersistenceMemory: &relayTestMemory{limit: shamap.DirtyWorkspaceBytes},
		Standalone:        true,
		GenesisConfig:     genesis.DefaultConfig(),
		NodeStore:         reopenedBase,
		SHAMapFamily:      reopenedFamily,
	})
	require.NoError(t, err)
	require.NoError(t, restarted.Start())
	t.Cleanup(func() {
		restarted.Stop()
		_ = reopenedDB.Close()
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
	item, found, err := recoveredState.Get(latest.key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, latest.value, item.Data())
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

func captureResidencyRecord(t *testing.T, l *ledgerpkg.Ledger, key [32]byte, value []byte) residencyLedgerRecord {
	t.Helper()
	snapshot, err := l.Snapshot()
	require.NoError(t, err)
	stateRoot, err := snapshot.StateMapHash()
	require.NoError(t, err)
	state, err := snapshot.StateMapSnapshot()
	require.NoError(t, err)
	retained, err := state.RetainedMemoryBytes()
	require.NoError(t, err)
	return residencyLedgerRecord{
		seq:      snapshot.Sequence(),
		hash:     snapshot.Hash(),
		root:     stateRoot,
		key:      key,
		value:    append([]byte(nil), value...),
		snapshot: snapshot,
		retained: retained,
	}
}

func readResidencyRecord(t *testing.T, record residencyLedgerRecord) {
	t.Helper()
	state, err := record.snapshot.StateMapSnapshot()
	require.NoError(t, err)
	item, found, err := state.Get(record.key)
	require.NoError(t, err)
	require.True(t, found, "missing historical key seq=%d key=%x", record.seq, record.key[:4])
	require.Equal(t, record.value, item.Data(), "historical value seq=%d", record.seq)
	retained, err := state.RetainedMemoryBytes()
	require.NoError(t, err)
	require.LessOrEqual(t, retained, record.retained+1<<20,
		"historical read expanded retained state unexpectedly at seq %d", record.seq)
}

func readColdResidencyRecord(t *testing.T, ctx context.Context, family shamap.Family, record residencyLedgerRecord) {
	t.Helper()
	cold, err := shamap.NewFromRootHashContext(ctx, shamap.TypeState, record.root, family)
	require.NoError(t, err)
	item, found, err := cold.GetContext(ctx, record.key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, record.value, item.Data())
}

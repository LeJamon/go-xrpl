package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/kvstore/memorydb"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	sqlitedb "github.com/LeJamon/go-xrpl/storage/relationaldb/sqlite"
	"github.com/stretchr/testify/require"
)

type corruptDescendantFamily struct {
	inner shamap.Family
	roots map[[32]byte]struct{}
}

type parallelFetchDatabase struct {
	nodestore.Database
	root    nodestore.Hash256
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	active  int
	peak    int
}

type blockingVerificationDatabase struct {
	nodestore.Database
	root    nodestore.Hash256
	started chan struct{}
	release chan struct{}
	err     error
	once    sync.Once
}

type synchronizedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan struct{}
}

type verificationLogRecord struct {
	Level             string `json:"level"`
	Message           string `json:"msg"`
	Topic             string `json:"t"`
	MapType           string `json:"map_type"`
	Root              string `json:"root"`
	Elapsed           string `json:"elapsed"`
	NodesChecked      uint64 `json:"nodes_checked"`
	NodesPerSecond    uint64 `json:"nodes_per_second"`
	ActiveBranches    uint32 `json:"active_branches"`
	BranchesComplete  uint32 `json:"branches_complete"`
	BranchesTotal     uint32 `json:"branches_total"`
	VerificationError string `json:"err"`
}

type verificationTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (d *parallelFetchDatabase) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	if hash == d.root {
		return d.Database.Fetch(ctx, hash)
	}
	d.mu.Lock()
	d.active++
	if d.active > d.peak {
		d.peak = d.active
	}
	if d.active >= 2 {
		d.once.Do(func() { close(d.started) })
	}
	d.mu.Unlock()

	select {
	case <-d.started:
	case <-ctx.Done():
		d.mu.Lock()
		d.active--
		d.mu.Unlock()
		return nil, ctx.Err()
	}
	node, err := d.Database.Fetch(ctx, hash)
	d.mu.Lock()
	d.active--
	d.mu.Unlock()
	return node, err
}

func (d *blockingVerificationDatabase) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	if hash == d.root {
		return d.Database.Fetch(ctx, hash)
	}
	d.once.Do(func() { close(d.started) })
	select {
	case <-d.release:
		if d.err != nil {
			return nil, d.err
		}
		return d.Database.Fetch(ctx, hash)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *synchronizedLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buffer.Write(data)
	b.mu.Unlock()
	select {
	case b.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (b *synchronizedLogBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func (c *verificationTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *verificationTestClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	c.mu.Unlock()
}

func newVerificationLogCapture() (*synchronizedLogBuffer, xrpllog.Logger) {
	capture := &synchronizedLogBuffer{writes: make(chan struct{}, 16)}
	cfg := &xrpllog.Config{
		Level:  xrpllog.LevelInfo,
		Format: "json",
		Output: capture,
	}
	return capture, xrpllog.New(xrpllog.NewHandler(cfg), cfg)
}

func decodeVerificationLogs(t *testing.T, capture *synchronizedLogBuffer) []verificationLogRecord {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(capture.bytes()))
	var records []verificationLogRecord
	for {
		var record verificationLogRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		require.NoError(t, err)
		records = append(records, record)
	}
}

func waitForVerificationSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stored SHAMap verification")
	}
}

func newStoredVerificationFixture(
	t *testing.T,
	branches int,
) (*Service, nodestore.Database, [32]byte, uint64, uint32) {
	t.Helper()
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "verification-progress", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for branch := range branches {
		var key [32]byte
		key[0] = byte(branch << 4)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)

	var nodes uint64
	require.NoError(t, svc.walkStoredSHAMap(ctx, root, shamap.TypeState,
		func([32]byte, *nodestore.Node) error {
			nodes++
			return nil
		},
	))
	rootNode, _, err := svc.loadStoredSHAMapNode(ctx, storedSHAMapNode{hash: root}, shamap.TypeState)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	var activeBranches uint32
	for branch := range shamap.BranchFactor {
		if !inner.IsEmptyBranch(branch) {
			activeBranches++
		}
	}
	return svc, db, root, nodes, activeBranches
}

func (d *parallelFetchDatabase) peakFetches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.peak
}

func (f *corruptDescendantFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.inner.Fetch(ctx, hash)
	if err != nil || data == nil {
		return data, err
	}
	if _, ok := f.roots[hash]; ok {
		return data, nil
	}
	return []byte("corrupt"), nil
}

func (f *corruptDescendantFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.inner.StoreBatch(ctx, entries)
}

func TestStoredLedgerFeesPreservesDefaultsForAbsentFields(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	feeData, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
		"BaseFeeDrops":    "17",
	})
	require.NoError(t, err)
	require.NoError(t, stateMap.Put(keylet.Fees().Key, feeData))

	configured := drops.Fees{Base: 23, Reserve: 34_000_000, Increment: 5_000_000}
	fees, err := storedLedgerFees(context.Background(), stateMap, true, configured)
	require.NoError(t, err)
	require.EqualValues(t, 17, fees.Base)
	require.Equal(t, configured.Reserve, fees.Reserve)
	require.Equal(t, configured.Increment, fees.Increment)

	_, err = storedLedgerFees(context.Background(), stateMap, false, configured)
	require.ErrorContains(t, err, "before the amendment is enabled")
}

func TestService_FastLoadRestoresPersistedValidatedLedger(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	first, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, first.Start())
	txBlob := []byte("synthetic-tx")
	txHash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBlob)
	require.NoError(t, first.openLedger.AddTransaction(txHash, txBlob))
	seq, err := first.AcceptLedger(ctx)
	require.NoError(t, err)
	first.FlushPersists()
	want := first.GetValidatedLedger()
	require.NotNil(t, want)
	wantHash := want.Hash()
	wantCloseTime := want.CloseTime()
	first.Stop()

	second, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, second.Start())
	t.Cleanup(second.Stop)

	require.False(t, second.NeedsInitialSync())
	require.True(t, second.IsFastLoadProvisional())
	require.False(t, second.GetServerInfo().NeedsNetworkLedger)
	require.Equal(t, seq, second.GetValidatedLedgerIndex())
	require.Equal(t, wantHash, second.GetValidatedLedger().Hash())
	second.SetValidatedLedgerAgeClock(func() time.Time {
		return wantCloseTime.Add(37 * time.Second)
	})
	require.Equal(t, 37*time.Second, second.GetValidatedLedgerAge())
	require.Equal(t, seq+1, second.GetCurrentLedgerIndex())
	gotTx, ok, err := second.GetValidatedLedger().GetTransaction(txHash)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, txBlob, gotTx)
	txResult, err := second.GetTransaction(txHash)
	require.NoError(t, err)
	require.Equal(t, txBlob, txResult.TxData)
	firstSeq, lastSeq, ok := second.AdvertisableLedgerRange()
	require.True(t, ok)
	require.Equal(t, seq, firstSeq)
	require.Equal(t, seq, lastSeq)

	loaded := second.GetValidatedLedger()
	second.SetValidatedLedgerAt(seq, wantHash, wantCloseTime.Add(time.Second))
	require.False(t, second.IsFastLoadProvisional())
	require.Same(t, loaded, second.GetValidatedLedger())
	require.Equal(t, wantHash, second.GetValidatedLedger().Hash())
}

func TestService_FastLoadReplacesSameHeightOnlyAfterTrustedQuorum(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-replacement", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	writer, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	writer.Stop()

	svc, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	loaded := svc.GetValidatedLedger()
	require.NotNil(t, loaded)
	loadedHash := loaded.Hash()
	require.True(t, svc.IsFastLoadProvisional())
	require.False(t, svc.NeedsInitialSync())

	events := make(chan *LedgerAcceptedEvent, 4)
	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		events <- event
	})

	stateMap, err := loaded.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := loaded.TxMapSnapshot()
	require.NoError(t, err)
	replacementHeader := loaded.Header()
	replacementHeader.Validated = false
	replacementHeader.Hash[0] ^= 0xFF
	replacementHash := replacementHeader.Hash

	initialCandidate, err := svc.BootstrapLedgerWithState(
		ctx,
		&replacementHeader,
		stateMap,
		txMap,
	)
	require.NoError(t, err)
	require.True(t, initialCandidate)
	require.Equal(t, loadedHash, svc.GetClosedLedger().Hash())
	require.Equal(t, loadedHash, svc.GetValidatedLedger().Hash())

	replacement, err := svc.GetLedgerByHash(replacementHash)
	require.NoError(t, err)
	closedBefore := svc.GetClosedLedger()
	openBefore := svc.GetOpenLedger()
	err = svc.SwitchToPreferredLedger(replacement)
	require.ErrorIs(t, err, ErrPreferredChainSwitch)
	require.Same(t, closedBefore, svc.GetClosedLedger())
	require.Same(t, openBefore, svc.GetOpenLedger())
	require.Same(t, loaded, svc.GetValidatedLedger())
	require.True(t, svc.IsFastLoadProvisional())

	stashed := make(chan struct{}, 1)
	svc.SetOnPendingValidationStashed(func(seq uint32, hash [32]byte) {
		if seq == replacement.Sequence() && hash == replacementHash {
			stashed <- struct{}{}
		}
	})
	signTime := loaded.CloseTime().Add(2 * time.Second)
	svc.SetPendingValidationResolver(func(seq uint32, hash [32]byte) (time.Time, bool) {
		return signTime, seq == replacement.Sequence() && hash == replacementHash
	})
	svc.SetValidatedLedgerAt(replacement.Sequence(), replacementHash, signTime)
	select {
	case <-stashed:
	case <-time.After(time.Second):
		t.Fatal("same-height provisional validation did not arm acquisition")
	}
	require.True(t, svc.HasPendingLedgerValidation(replacement.Sequence(), replacementHash))

	require.NoError(t, svc.SwitchToPreferredLedger(replacement))
	require.Equal(t, replacementHash, svc.GetClosedLedger().Hash())
	require.Equal(t, replacementHash, svc.GetValidatedLedger().Hash())
	require.True(t, svc.GetValidatedLedger().IsValidated())
	require.False(t, svc.IsFastLoadProvisional())
	require.False(t, svc.NeedsInitialSync())
	require.False(t, svc.HasPendingLedgerValidation(replacement.Sequence(), replacementHash))

	svc.mu.RLock()
	gotSignTime := svc.validatedSignTime
	svc.mu.RUnlock()
	require.Equal(t, signTime, gotSignTime)
	svc.ledgerEventMu.Lock()
	frontierHash := svc.ledgerEventFrontierHash
	svc.ledgerEventMu.Unlock()
	require.Equal(t, replacementHash, frontierHash)

	select {
	case event := <-events:
		require.NotNil(t, event.Ledger)
		require.Equal(t, replacementHash, event.Ledger.Hash())
	case <-time.After(time.Second):
		t.Fatal("same-height replacement was not published")
	}

	svc.SetValidatedLedgerAt(replacement.Sequence(), replacementHash, signTime)
	select {
	case event := <-events:
		t.Fatalf("same-height replacement published twice: %x", event.Ledger.Hash())
	case <-time.After(50 * time.Millisecond):
	}

	childSeq, err := svc.AcceptConsensusResult(
		ctx,
		replacement,
		nil,
		nil,
		replacement.CloseTime().Add(time.Second),
		true,
	)
	require.NoError(t, err)
	child := svc.GetClosedLedger()
	childHash := child.Hash()
	svc.SetValidatedLedgerAt(childSeq, childHash, child.CloseTime())
	select {
	case event := <-events:
		require.NotNil(t, event.Ledger)
		require.Equal(t, childHash, event.Ledger.Hash())
		require.Equal(t, replacementHash, event.Ledger.ParentHash())
	case <-time.After(time.Second):
		t.Fatal("replacement child was not published")
	}
}

func TestService_VerifyStoredSHAMapWalksRootBranchesInParallel(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "parallel-fast-load", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	for branch := range shamap.BranchFactor {
		var key [32]byte
		key[0] = byte(branch << 4)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)

	tracked := &parallelFetchDatabase{
		Database: db,
		root:     nodestore.Hash256(root),
		started:  make(chan struct{}),
	}
	svc.nodeStore = tracked
	walkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.NoError(t, svc.verifyStoredSHAMap(walkCtx, root, shamap.TypeState))
	require.Greater(t, tracked.peakFetches(), 1)
	require.LessOrEqual(t, tracked.peakFetches(), shamap.BranchFactor)
}

func TestService_VerifyStoredSHAMapReportsConcurrentSuccess(t *testing.T) {
	svc, _, root, expectedNodes, expectedBranches := newStoredVerificationFixture(t, shamap.BranchFactor)
	capture, logger := newVerificationLogCapture()
	svc.logger = logger.Named(xrpllog.PartitionLedger)
	startedAt := time.Date(2026, time.July, 27, 20, 0, 0, 0, time.UTC)
	now := func() time.Time {
		return startedAt.Add(2 * time.Second)
	}

	require.NoError(t, svc.verifyStoredSHAMapWithTicks(
		context.Background(),
		root,
		shamap.TypeState,
		startedAt,
		now,
		nil,
	))

	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "stored SHAMap verification started", records[0].Message)
	require.Equal(t, "INFO", records[0].Level)
	require.Equal(t, "Ledger", records[0].Topic)
	require.Equal(t, "state", records[0].MapType)
	require.Equal(t, fmt.Sprintf("%x", root[:8]), records[0].Root)
	require.Equal(t, expectedBranches, records[0].ActiveBranches)
	require.Equal(t, "stored SHAMap verification complete", records[1].Message)
	require.Equal(t, "2s", records[1].Elapsed)
	require.Equal(t, expectedNodes, records[1].NodesChecked)
	require.Equal(t, expectedNodes/2, records[1].NodesPerSecond)
	require.Equal(t, expectedBranches, records[1].BranchesComplete)
	require.Equal(t, expectedBranches, records[1].BranchesTotal)
}

func TestService_VerifyStoredSHAMapReportsProgressAtCompletionBoundary(t *testing.T) {
	svc, _, root, expectedNodes, expectedBranches := newStoredVerificationFixture(t, 1)
	capture, logger := newVerificationLogCapture()
	svc.logger = logger.Named(xrpllog.PartitionLedger)
	startedAt := time.Date(2026, time.July, 27, 20, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(storedSHAMapVerificationLogInterval)

	require.NoError(t, svc.verifyStoredSHAMapWithTicks(
		context.Background(),
		root,
		shamap.TypeState,
		startedAt,
		func() time.Time { return finishedAt },
		nil,
	))

	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 3)
	require.Equal(t, "stored SHAMap verification started", records[0].Message)
	require.Equal(t, "stored SHAMap verification progress", records[1].Message)
	require.Equal(t, storedSHAMapVerificationLogInterval.String(), records[1].Elapsed)
	require.Equal(t, expectedNodes, records[1].NodesChecked)
	require.Equal(t, expectedBranches, records[1].BranchesComplete)
	require.Equal(t, "stored SHAMap verification complete", records[2].Message)
}

func TestService_VerifyStoredSHAMapRateLimitsProgressAndReportsCancellation(t *testing.T) {
	svc, db, root, _, expectedBranches := newStoredVerificationFixture(t, 1)
	blocked := &blockingVerificationDatabase{
		Database: db,
		root:     nodestore.Hash256(root),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	svc.nodeStore = blocked
	capture, logger := newVerificationLogCapture()
	svc.logger = logger.Named(xrpllog.PartitionLedger)
	startedAt := time.Date(2026, time.July, 27, 20, 0, 0, 0, time.UTC)
	clock := &verificationTestClock{now: startedAt}
	ticks := make(chan time.Time, 4)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- svc.verifyStoredSHAMapWithTicks(ctx, root, shamap.TypeState, startedAt, clock.Now, ticks)
	}()

	waitForVerificationSignal(t, blocked.started)
	waitForVerificationSignal(t, capture.writes)
	ticks <- startedAt.Add(5 * time.Second)
	ticks <- startedAt.Add(15 * time.Second)
	ticks <- startedAt.Add(16 * time.Second)
	ticks <- startedAt.Add(30 * time.Second)
	waitForVerificationSignal(t, capture.writes)
	waitForVerificationSignal(t, capture.writes)
	clock.Set(startedAt.Add(31 * time.Second))
	cancel()
	select {
	case err := <-errs:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled stored SHAMap verification")
	}

	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 4)
	require.Equal(t, "stored SHAMap verification started", records[0].Message)
	require.Equal(t, "stored SHAMap verification progress", records[1].Message)
	require.Equal(t, "15s", records[1].Elapsed)
	require.EqualValues(t, 1, records[1].NodesChecked)
	require.Equal(t, "stored SHAMap verification progress", records[2].Message)
	require.Equal(t, "30s", records[2].Elapsed)
	require.GreaterOrEqual(t, records[2].NodesChecked, records[1].NodesChecked)
	require.Equal(t, "stored SHAMap verification failed", records[3].Message)
	require.Equal(t, "WARN", records[3].Level)
	require.Equal(t, "31s", records[3].Elapsed)
	require.GreaterOrEqual(t, records[3].NodesChecked, records[2].NodesChecked)
	require.Zero(t, records[3].BranchesComplete)
	require.Equal(t, expectedBranches, records[3].BranchesTotal)
	require.Contains(t, records[3].VerificationError, context.Canceled.Error())
}

func TestService_VerifyStoredSHAMapReportsTraversalFailure(t *testing.T) {
	svc, db, root, _, expectedBranches := newStoredVerificationFixture(t, 1)
	fetchErr := errors.New("read stored node")
	release := make(chan struct{})
	close(release)
	svc.nodeStore = &blockingVerificationDatabase{
		Database: db,
		root:     nodestore.Hash256(root),
		started:  make(chan struct{}),
		release:  release,
		err:      fetchErr,
	}
	capture, logger := newVerificationLogCapture()
	svc.logger = logger.Named(xrpllog.PartitionLedger)
	startedAt := time.Date(2026, time.July, 27, 20, 0, 0, 0, time.UTC)
	now := func() time.Time {
		return startedAt.Add(3 * time.Second)
	}

	err := svc.verifyStoredSHAMapWithTicks(
		context.Background(),
		root,
		shamap.TypeState,
		startedAt,
		now,
		nil,
	)
	require.ErrorIs(t, err, fetchErr)

	records := decodeVerificationLogs(t, capture)
	require.Len(t, records, 2)
	require.Equal(t, "stored SHAMap verification started", records[0].Message)
	require.Equal(t, "stored SHAMap verification failed", records[1].Message)
	require.Equal(t, "WARN", records[1].Level)
	require.Equal(t, "3s", records[1].Elapsed)
	require.EqualValues(t, 1, records[1].NodesChecked)
	require.Zero(t, records[1].BranchesComplete)
	require.Equal(t, expectedBranches, records[1].BranchesTotal)
	require.Contains(t, records[1].VerificationError, fetchErr.Error())
}

func TestService_FastLoadFallsBackWhenStorageIsEmpty(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-empty", 100, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	svc, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.False(t, svc.NeedsInitialSync())
	require.True(t, svc.IsFastLoadProvisional())
	require.False(t, svc.GetServerInfo().NeedsNetworkLedger)
	require.Nil(t, svc.GetValidatedLedger())
	require.Zero(t, svc.GetValidatedLedgerIndex())
}

func TestService_FastLoadRejectsRelationalLedgerWithoutValidatedTip(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-unvalidated", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	writer, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	untrusted := buildLedgerWithState(t, 99)
	require.NoError(t, writer.persistValidatedLedger(ctx, untrusted, false))

	reader, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, reader.Start())
	t.Cleanup(reader.Stop)
	require.False(t, reader.NeedsInitialSync())
	require.True(t, reader.IsFastLoadProvisional())
	require.False(t, reader.GetServerInfo().NeedsNetworkLedger)
	require.Nil(t, reader.GetValidatedLedger())
	require.Zero(t, reader.GetValidatedLedgerIndex())
}

func TestService_FastLoadFallsBackWhenTreeIsCorrupt(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-corrupt", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	first, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, first.Start())
	_, err = first.AcceptLedger(ctx)
	require.NoError(t, err)
	first.FlushPersists()
	stateRoot := first.GetValidatedLedger().Header().AccountHash
	first.Stop()

	stored, err := db.Fetch(ctx, nodestore.Hash256(stateRoot))
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.NoError(t, db.Store(ctx, &nodestore.Node{
		Type:      stored.Type,
		Hash:      stored.Hash,
		Data:      []byte("corrupt"),
		LedgerSeq: stored.LedgerSeq,
	}))

	second, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
		FastLoad:      true,
	})
	require.NoError(t, err)
	require.NoError(t, second.Start())
	t.Cleanup(second.Stop)
	require.False(t, second.NeedsInitialSync())
	require.True(t, second.IsFastLoadProvisional())
	require.False(t, second.GetServerInfo().NeedsNetworkLedger)
	require.Nil(t, second.GetValidatedLedger())
	require.Zero(t, second.GetValidatedLedgerIndex())
}

func TestService_GetLedgerByHashTreatsCorruptDescendantAsNotFound(t *testing.T) {
	ctx := context.Background()
	db := nodestore.NewKVDatabase(memorydb.New(), "fast-load-corrupt-descendant", 10_000, time.Hour)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm, err := sqlitedb.NewRepositoryManager(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, rm.Open(ctx))
	t.Cleanup(func() { require.NoError(t, rm.Close(ctx)) })

	family := backend.New(db)
	writer, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  family,
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, writer.Start())
	_, err = writer.AcceptLedger(ctx)
	require.NoError(t, err)
	writer.FlushPersists()
	persisted := writer.GetValidatedLedger()
	wantHash := persisted.Hash()
	hdr := persisted.Header()
	writer.Stop()

	roots := map[[32]byte]struct{}{hdr.AccountHash: {}}
	if hdr.TxHash != ([32]byte{}) {
		roots[hdr.TxHash] = struct{}{}
	}
	reader, err := New(Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily: &corruptDescendantFamily{
			inner: family,
			roots: roots,
		},
		RelationalDB: rm,
	})
	require.NoError(t, err)

	_, err = reader.GetLedgerByHash(wantHash)
	require.ErrorIs(t, err, ErrLedgerNotFound)
	require.False(t, errors.Is(err, shamap.ErrInvalidNodeData))
}

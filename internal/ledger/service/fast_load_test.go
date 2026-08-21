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
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

type corruptDescendantFamily struct {
	inner shamap.Family
	roots map[[32]byte]struct{}
}

type parallelFetchDatabase struct {
	nodestore.Database
	unblocked map[nodestore.Hash256]struct{}
	started   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	active    int
	peak      int
}

type blockingVerificationDatabase struct {
	nodestore.Database
	root      nodestore.Hash256
	unblocked map[nodestore.Hash256]struct{}
	started   chan struct{}
	release   chan struct{}
	err       error
	once      sync.Once
}

type uncachedTrackingDatabase struct {
	nodestore.Database
	mu              sync.Mutex
	fetches         int
	uncachedFetches int
	rewrite         func(nodestore.Hash256, []byte) ([]byte, error)
}

type fallbackTrackingDatabase struct {
	nodestore.Database
	mu      sync.Mutex
	fetches int
}

type cancelingVerificationDatabase struct {
	nodestore.Database
	root      nodestore.Hash256
	unblocked map[nodestore.Hash256]struct{}
	fail      nodestore.Hash256
	err       error
	expected  int
	ready     chan struct{}
	once      sync.Once
	mu        sync.Mutex
	active    int
	peak      int
}

type synchronizedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	writes chan struct{}
}

type verificationLogRecord struct {
	Level                    string `json:"level"`
	Message                  string `json:"msg"`
	Topic                    string `json:"t"`
	MapType                  string `json:"map_type"`
	Root                     string `json:"root"`
	Elapsed                  string `json:"elapsed"`
	NodesChecked             uint64 `json:"nodes_checked"`
	NodesPerSecond           uint64 `json:"nodes_per_second"`
	IntervalNodesRate        uint64 `json:"interval_nodes_per_second"`
	ActiveBranches           uint32 `json:"active_branches"`
	BranchesComplete         uint32 `json:"branches_complete"`
	BranchesTotal            uint32 `json:"branches_total"`
	Workers                  uint32 `json:"workers"`
	WorkerPoolSize           uint32 `json:"worker_pool_size"`
	ActiveWorkers            int32  `json:"active_workers"`
	IdleWorkers              int64  `json:"idle_workers"`
	FrontierSize             int64  `json:"frontier_size"`
	NodeStoreReadsBefore     uint64 `json:"node_store_reads_before"`
	NodeStoreReadsAfter      uint64 `json:"node_store_reads_after"`
	NodeStoreReadBytesBefore uint64 `json:"node_store_read_bytes_before"`
	NodeStoreReadBytesAfter  uint64 `json:"node_store_read_bytes_after"`
	NodeCacheHitsBefore      uint64 `json:"node_cache_hits_before"`
	NodeCacheHitsAfter       uint64 `json:"node_cache_hits_after"`
	NodeCacheMissesBefore    uint64 `json:"node_cache_misses_before"`
	NodeCacheMissesAfter     uint64 `json:"node_cache_misses_after"`
	VerificationError        string `json:"err"`
}

type verificationTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (d *parallelFetchDatabase) Fetch(ctx context.Context, hash nodestore.Hash256) (*nodestore.Node, error) {
	if _, ok := d.unblocked[hash]; ok {
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
	if _, ok := d.unblocked[hash]; ok {
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

func (d *uncachedTrackingDatabase) Fetch(
	ctx context.Context,
	hash nodestore.Hash256,
) (*nodestore.Node, error) {
	d.mu.Lock()
	d.fetches++
	d.mu.Unlock()
	return d.Database.Fetch(ctx, hash)
}

func (d *uncachedTrackingDatabase) FetchDataUncached(
	ctx context.Context,
	hash nodestore.Hash256,
) ([]byte, error) {
	d.mu.Lock()
	d.uncachedFetches++
	d.mu.Unlock()
	raw, ok := d.Database.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	})
	if !ok {
		return nil, errors.New("uncached reads are unavailable")
	}
	data, err := raw.FetchDataUncached(ctx, hash)
	if err != nil || data == nil || d.rewrite == nil {
		return data, err
	}
	return d.rewrite(hash, data)
}

func (d *uncachedTrackingDatabase) counts() (fetches, uncached int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fetches, d.uncachedFetches
}

func (d *fallbackTrackingDatabase) Fetch(
	ctx context.Context,
	hash nodestore.Hash256,
) (*nodestore.Node, error) {
	d.mu.Lock()
	d.fetches++
	d.mu.Unlock()
	return d.Database.Fetch(ctx, hash)
}

func (d *fallbackTrackingDatabase) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fetches
}

func (d *cancelingVerificationDatabase) Fetch(
	ctx context.Context,
	hash nodestore.Hash256,
) (*nodestore.Node, error) {
	if hash == d.root {
		return d.Database.Fetch(ctx, hash)
	}
	if _, ok := d.unblocked[hash]; ok {
		return d.Database.Fetch(ctx, hash)
	}
	d.mu.Lock()
	d.active++
	if d.active > d.peak {
		d.peak = d.active
	}
	if d.active == d.expected {
		d.once.Do(func() { close(d.ready) })
	}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.active--
		d.mu.Unlock()
	}()

	select {
	case <-d.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if hash == d.fail {
		return nil, d.err
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (d *cancelingVerificationDatabase) fetchState() (active, peak int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active, d.peak
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
	db := newTestNodeStore(t, 10_000)
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

func newParallelStoredVerificationFixture(
	t *testing.T,
) (*Service, nodestore.Database, [32]byte, []nodestore.Hash256) {
	t.Helper()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{NodeStore: db})
	require.NoError(t, err)
	sm := shamap.New(shamap.TypeState)
	for rootBranch := range shamap.BranchFactor {
		for childBranch := range shamap.BranchFactor {
			var key [32]byte
			key[0] = byte(rootBranch<<4 | childBranch)
			key[31] = byte(childBranch + 1)
			data := make([]byte, 12)
			data[10] = byte(rootBranch)
			data[11] = byte(childBranch + 1)
			require.NoError(t, sm.Put(key, data))
		}
	}
	root := persistVerificationSHAMap(t, db, sm)
	rootNode, _, err := svc.loadStoredSHAMapNode(
		t.Context(),
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	rootChildren := make([]nodestore.Hash256, 0, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		child, childErr := inner.ChildHash(branch)
		require.NoError(t, childErr)
		rootChildren = append(rootChildren, nodestore.Hash256(child))
	}
	return svc, db, root, rootChildren
}

func storePrefixedVerificationNode(
	t *testing.T,
	db nodestore.Database,
	data []byte,
) [32]byte {
	t.Helper()
	node, err := shamap.DeserializeFromPrefix(data)
	require.NoError(t, err)
	hash := node.Hash()
	require.NoError(t, db.Store(t.Context(), &nodestore.Node{
		Type: nodestore.NodeAccount,
		Hash: nodestore.Hash256(hash),
		Data: data,
	}))
	return hash
}

func prefixedInnerVerificationNode(branch int, child [32]byte) []byte {
	data := make([]byte, 4+shamap.BranchFactor*32)
	copy(data, protocol.HashPrefixInnerNode().Bytes())
	copy(data[4+branch*32:4+(branch+1)*32], child[:])
	return data
}

func persistVerificationSHAMap(
	t *testing.T,
	db nodestore.Database,
	sm *shamap.SHAMap,
) [32]byte {
	t.Helper()
	var entries []shamap.FlushEntry
	require.NoError(t, sm.StoreDirty(func(dirty []shamap.FlushEntry) error {
		entries = dirty
		return nil
	}))
	nodes := make([]*nodestore.Node, 0, len(entries))
	for _, entry := range entries {
		nodes = append(nodes, &nodestore.Node{
			Type:      nodestore.NodeAccount,
			Hash:      nodestore.Hash256(entry.Hash),
			Data:      entry.Data,
			LedgerSeq: entry.LedgerSeq,
		})
	}
	require.NoError(t, db.StoreBatch(t.Context(), nodes))
	root, err := sm.Hash()
	require.NoError(t, err)
	return root
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
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

	first, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
		RelationalDB:  rm,
	})
	require.NoError(t, err)
	require.NoError(t, first.Start())
	rawTx, _ := validRelationalTestTransaction(t, 1)
	txBlob, txHash := makeTxMetaBlobForTest(t, rawTx, 0)
	require.NoError(t, first.openLedger.AddTransactionWithMeta(txHash, txBlob))
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

func TestService_FastLoadSameHeightSwitchRemainsProvisionalUntilValidated(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

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
	setEventSinkFunc(svc, func(event *LedgerAcceptedEvent) {
		events <- event
	})

	stateMap, err := loaded.StateMapSnapshot()
	require.NoError(t, err)
	txMap, err := loaded.TxMapSnapshot()
	require.NoError(t, err)
	replacementHeader := loaded.Header()
	replacementHeader.Validated = false
	replacementHeader.CloseFlags ^= header.LCFNoConsensusTime
	replacementHeader.Hash = header.CalculateHash(replacementHeader)
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
	require.NoError(t, svc.SwitchToPreferredLedger(replacement))
	require.Equal(t, replacementHash, svc.GetClosedLedger().Hash())
	require.Same(t, loaded, svc.GetValidatedLedger())
	require.True(t, svc.IsFastLoadProvisional())

	validated := make(chan [32]byte, 1)
	svc.SetOnValidatedLedger(func(seq uint32, hash, _ [32]byte) {
		if seq == replacement.Sequence() {
			validated <- hash
		}
	})
	signTime := loaded.CloseTime().Add(2 * time.Second)
	svc.SetValidatedLedgerAt(replacement.Sequence(), replacementHash, signTime)
	require.Equal(t, replacementHash, svc.GetValidatedLedger().Hash())
	select {
	case hash := <-validated:
		require.Equal(t, replacementHash, hash)
	case <-time.After(time.Second):
		t.Fatal("same-height validated-ledger replacement was not notified")
	}
	require.True(t, svc.GetValidatedLedger().IsValidated())
	require.False(t, svc.IsFastLoadProvisional())
	require.False(t, svc.NeedsInitialSync())

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

func TestService_VerifyStoredSHAMapRebalancesBelowRoot(t *testing.T) {
	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{
		NodeStore:       db,
		FastLoadWorkers: 4,
	})
	require.NoError(t, err)

	sm := shamap.New(shamap.TypeState)
	for branch := range shamap.BranchFactor {
		var key [32]byte
		key[0] = byte(branch)
		key[31] = byte(branch + 1)
		data := make([]byte, 12)
		data[11] = byte(branch + 1)
		require.NoError(t, sm.Put(key, data))
	}
	root := persistVerificationSHAMap(t, db, sm)
	rootNode, _, err := svc.loadStoredSHAMapNode(
		ctx,
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	require.False(t, inner.IsEmptyBranch(0))
	rootChild, err := inner.ChildHash(0)
	require.NoError(t, err)
	frontier, _, err := svc.buildStoredSHAMapFrontier(
		ctx,
		[][32]byte{rootChild},
		4*storedSHAMapFrontierTasksPerWorker,
		shamap.TypeState,
		svc.nodeStore.Fetch,
		nil,
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(frontier), 4*storedSHAMapFrontierTasksPerWorker)

	tracked := &parallelFetchDatabase{
		Database: db,
		unblocked: map[nodestore.Hash256]struct{}{
			nodestore.Hash256(root):      {},
			nodestore.Hash256(rootChild): {},
		},
		started: make(chan struct{}),
	}
	svc.nodeStore = tracked
	walkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err = svc.verifyStoredSHAMap(walkCtx, root, shamap.TypeState)
	require.NoError(t, err, "peak concurrent fetches: %d", tracked.peakFetches())
	require.Greater(t, tracked.peakFetches(), 1)
	require.LessOrEqual(t, tracked.peakFetches(), 4)
}

func TestService_VerifyStoredSHAMapUsesUncachedReads(t *testing.T) {
	svc, db, root, expectedNodes, _ := newStoredVerificationFixture(t, shamap.BranchFactor)
	tracked := &uncachedTrackingDatabase{Database: db}
	svc.nodeStore = tracked
	svc.config.FastLoadWorkers = 4
	before := db.Stats()

	require.NoError(t, svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState))

	fetches, uncached := tracked.counts()
	require.Zero(t, fetches)
	require.EqualValues(t, expectedNodes, uncached)
	after := db.Stats()
	require.Equal(t, before.CacheSize, after.CacheSize)
	require.Equal(t, before.CacheHits, after.CacheHits)
	require.Equal(t, before.CacheMisses, after.CacheMisses)
	require.Equal(t, before.Reads+expectedNodes, after.Reads)
}

func TestService_VerifyStoredSHAMapSeedsFullBelowCache(t *testing.T) {
	svc, _, root, _, _ := newStoredVerificationFixture(t, shamap.BranchFactor)
	provider, ok := svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	cache := provider.FullBelowCache()
	cache.Bump()
	generation := cache.Generation()

	rootNode, _, err := svc.loadStoredSHAMapNode(
		t.Context(),
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	child, err := inner.ChildHash(0)
	require.NoError(t, err)
	require.False(t, cache.Has(generation, root))
	require.False(t, cache.Has(generation, child))

	require.NoError(t, svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState))

	require.True(t, cache.Has(generation, root))
	require.True(t, cache.Has(generation, child))
}

func TestService_VerifyStoredSHAMapDoesNotSeedFailedProofs(t *testing.T) {
	svc, db, root, _, _ := newStoredVerificationFixture(t, 1)
	provider, ok := svc.shamapFamily.(interface {
		FullBelowCache() *shamap.FullBelowCache
	})
	require.True(t, ok)
	cache := provider.FullBelowCache()
	cache.Bump()
	generation := cache.Generation()

	raw := db.(interface {
		FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
	})
	tracked := &uncachedTrackingDatabase{Database: db}
	tracked.rewrite = func(hash nodestore.Hash256, data []byte) ([]byte, error) {
		if hash == nodestore.Hash256(root) {
			return data, nil
		}
		return raw.FetchDataUncached(t.Context(), nodestore.Hash256(root))
	}
	svc.nodeStore = tracked

	err := svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
	require.ErrorContains(t, err, "invalid content hash")
	require.False(t, cache.Has(generation, root))
	require.Zero(t, cache.Size())
}

func TestService_VerifyStoredSHAMapFallsBackToFetch(t *testing.T) {
	svc, db, root, expectedNodes, _ := newStoredVerificationFixture(t, shamap.BranchFactor)
	tracked := &fallbackTrackingDatabase{Database: db}
	svc.nodeStore = tracked
	svc.config.FastLoadWorkers = 4

	require.NoError(t, svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState))
	require.EqualValues(t, expectedNodes, tracked.count())
}

func TestService_VerifyStoredSHAMapUncachedReadsPreserveIntegrityChecks(t *testing.T) {
	tests := []struct {
		name    string
		rewrite func(rootData []byte) func(nodestore.Hash256, []byte) ([]byte, error)
		want    string
	}{
		{
			name: "content hash",
			rewrite: func(rootData []byte) func(nodestore.Hash256, []byte) ([]byte, error) {
				return func(nodestore.Hash256, []byte) ([]byte, error) {
					return rootData, nil
				}
			},
			want: "invalid content hash",
		},
		{
			name: "missing node",
			rewrite: func([]byte) func(nodestore.Hash256, []byte) ([]byte, error) {
				return func(nodestore.Hash256, []byte) ([]byte, error) {
					return nil, nil
				}
			},
			want: "is missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, db, root, _, _ := newStoredVerificationFixture(t, 1)
			raw := db.(interface {
				FetchDataUncached(context.Context, nodestore.Hash256) ([]byte, error)
			})
			rootData, err := raw.FetchDataUncached(t.Context(), nodestore.Hash256(root))
			require.NoError(t, err)
			tracked := &uncachedTrackingDatabase{Database: db}
			tracked.rewrite = func(hash nodestore.Hash256, data []byte) ([]byte, error) {
				if hash == nodestore.Hash256(root) {
					return data, nil
				}
				return test.rewrite(rootData)(hash, data)
			}
			svc.nodeStore = tracked
			svc.config.FastLoadWorkers = 4

			err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
			require.ErrorContains(t, err, test.want)
			fetches, uncached := tracked.counts()
			require.Zero(t, fetches)
			require.Greater(t, uncached, 1)
		})
	}
}

func TestService_VerifyStoredSHAMapPreservesLeafAndDepthChecks(t *testing.T) {
	t.Run("wrong leaf type", func(t *testing.T) {
		db := newTestNodeStore(t, 32)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		txData := append(protocol.HashPrefixTransactionID().Bytes(), []byte("transaction!")...)
		txHash := storePrefixedVerificationNode(t, db, txData)
		root := storePrefixedVerificationNode(t, db, prefixedInnerVerificationNode(0, txHash))
		svc, err := New(Config{
			NodeStore:       db,
			FastLoadWorkers: 1,
		})
		require.NoError(t, err)

		err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
		require.ErrorContains(t, err, "state tree contains")
		require.ErrorContains(t, err, "transaction")
	})

	t.Run("excessive depth", func(t *testing.T) {
		db := newTestNodeStore(t, 128)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		leafData := make([]byte, 4+12+32)
		copy(leafData, protocol.HashPrefixLeafNode().Bytes())
		leafData[4] = 1
		leafData[len(leafData)-1] = 1
		root := storePrefixedVerificationNode(t, db, leafData)
		for range 65 {
			root = storePrefixedVerificationNode(t, db, prefixedInnerVerificationNode(0, root))
		}
		svc, err := New(Config{
			NodeStore:       db,
			FastLoadWorkers: 1,
		})
		require.NoError(t, err)

		err = svc.verifyStoredSHAMap(t.Context(), root, shamap.TypeState)
		require.ErrorContains(t, err, "exceeds maximum depth")
	})
}

func TestService_VerifyStoredSHAMapWorkerCountsProduceIdenticalTotals(t *testing.T) {
	svc, _, root, expectedNodes, expectedBranches := newStoredVerificationFixture(
		t,
		shamap.BranchFactor,
	)
	startedAt := time.Date(2026, time.July, 28, 20, 0, 0, 0, time.UTC)
	for _, workers := range []int{1, 8} {
		t.Run(fmt.Sprintf("%d workers", workers), func(t *testing.T) {
			capture, logger := newVerificationLogCapture()
			svc.logger = logger.Named(xrpllog.PartitionLedger)
			svc.config.FastLoadWorkers = workers
			require.NoError(t, svc.verifyStoredSHAMapWithTicks(
				t.Context(),
				root,
				shamap.TypeState,
				startedAt,
				func() time.Time { return startedAt.Add(time.Second) },
				nil,
			))

			records := decodeVerificationLogs(t, capture)
			terminal := records[len(records)-1]
			require.Equal(t, "stored SHAMap verification complete", terminal.Message)
			require.Equal(t, expectedNodes, terminal.NodesChecked)
			require.Equal(t, expectedBranches, terminal.BranchesComplete)
			require.Equal(t, expectedBranches, terminal.BranchesTotal)
			require.EqualValues(t, workers, terminal.Workers)
		})
	}
}

func TestService_VerifyStoredSHAMapCancelsSaturatedWorkers(t *testing.T) {
	svc, db, root, rootChildren := newParallelStoredVerificationFixture(t)
	branchNode, _, err := svc.loadStoredSHAMapNode(
		t.Context(),
		storedSHAMapNode{hash: [32]byte(rootChildren[0]), depth: 1},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := branchNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	failingChild, err := inner.ChildHash(0)
	require.NoError(t, err)
	unblocked := make(map[nodestore.Hash256]struct{}, len(rootChildren))
	for _, child := range rootChildren {
		unblocked[child] = struct{}{}
	}
	fetchErr := errors.New("corrupt descendant")
	tracked := &cancelingVerificationDatabase{
		Database:  db,
		root:      nodestore.Hash256(root),
		unblocked: unblocked,
		fail:      nodestore.Hash256(failingChild),
		err:       fetchErr,
		expected:  4,
		ready:     make(chan struct{}),
	}
	svc.nodeStore = tracked
	svc.config.FastLoadWorkers = 4

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = svc.verifyStoredSHAMap(ctx, root, shamap.TypeState)
	require.ErrorIs(t, err, fetchErr)
	active, peak := tracked.fetchState()
	require.Zero(t, active)
	require.Equal(t, 4, peak)
}

func TestService_StoredSHAMapFrontierIsBounded(t *testing.T) {
	ctx := t.Context()
	db := newTestNodeStore(t, 10_000)
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

	for i := range 256 {
		var key [32]byte
		key[0] = byte(i / shamap.BranchFactor)
		key[1] = byte((i % shamap.BranchFactor) << 4)
		key[31] = byte(i)
		data := make([]byte, 12)
		data[10] = byte(i >> 8)
		data[11] = byte(i)
		require.NoError(t, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(t, err)
	rootNode, _, err := svc.loadStoredSHAMapNode(
		ctx,
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	require.False(t, inner.IsEmptyBranch(0))
	rootChild, err := inner.ChildHash(0)
	require.NoError(t, err)

	const target = 32
	frontier, outstanding, err := svc.buildStoredSHAMapFrontier(
		ctx,
		[][32]byte{rootChild},
		target,
		shamap.TypeState,
		svc.nodeStore.Fetch,
		nil,
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(frontier), target)
	require.LessOrEqual(t, len(frontier), target+shamap.BranchFactor-1)
	require.EqualValues(t, len(frontier), outstanding[0])
}

func TestService_StoredSHAMapFrontierSplitsEveryRootBranch(t *testing.T) {
	db := newTestNodeStore(t, 256)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{NodeStore: db})
	require.NoError(t, err)
	sm := shamap.New(shamap.TypeState)
	for rootBranch := range shamap.BranchFactor {
		for childBranch := range 2 {
			var key [32]byte
			key[0] = byte(rootBranch<<4 | childBranch)
			key[31] = byte(rootBranch*2 + childBranch + 1)
			data := make([]byte, 12)
			data[11] = key[31]
			require.NoError(t, sm.Put(key, data))
		}
	}
	root := persistVerificationSHAMap(t, db, sm)
	rootNode, _, err := svc.loadStoredSHAMapNode(
		t.Context(),
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	branches := make([][32]byte, 0, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		child, childErr := inner.ChildHash(branch)
		require.NoError(t, childErr)
		branches = append(branches, child)
	}

	frontier, outstanding, err := svc.buildStoredSHAMapFrontier(
		t.Context(),
		branches,
		32,
		shamap.TypeState,
		svc.nodeStore.Fetch,
		nil,
	)
	require.NoError(t, err)
	require.Len(t, frontier, 32)
	for branch, count := range outstanding {
		require.EqualValues(t, 2, count, "root branch %d was not split", branch)
	}
}

func TestService_StoredSHAMapFrontierRedistributesUnusedCapacity(t *testing.T) {
	db := newTestNodeStore(t, 2_048)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	svc, err := New(Config{NodeStore: db})
	require.NoError(t, err)
	sm := shamap.New(shamap.TypeState)
	for rootBranch := range shamap.BranchFactor - 1 {
		var key [32]byte
		key[0] = byte(rootBranch << 4)
		key[31] = byte(rootBranch + 1)
		data := make([]byte, 12)
		data[11] = key[31]
		require.NoError(t, sm.Put(key, data))
	}
	for i := range 1_024 {
		var seed [8]byte
		seed[0] = byte(i >> 8)
		seed[1] = byte(i)
		key := sha512half.Sum(seed[:])
		key[0] = 0xf0 | key[0]&0x0f
		data := make([]byte, 12)
		data[10] = seed[0]
		data[11] = seed[1]
		require.NoError(t, sm.Put(key, data))
	}
	root := persistVerificationSHAMap(t, db, sm)
	rootNode, _, err := svc.loadStoredSHAMapNode(
		t.Context(),
		storedSHAMapNode{hash: root},
		shamap.TypeState,
	)
	require.NoError(t, err)
	inner, ok := rootNode.(shamap.InnerNodeReader)
	require.True(t, ok)
	branches := make([][32]byte, 0, shamap.BranchFactor)
	for branch := range shamap.BranchFactor {
		child, childErr := inner.ChildHash(branch)
		require.NoError(t, childErr)
		branches = append(branches, child)
	}

	const target = 4 * storedSHAMapFrontierTasksPerWorker
	frontier, outstanding, err := svc.buildStoredSHAMapFrontier(
		t.Context(),
		branches,
		target,
		shamap.TypeState,
		svc.nodeStore.Fetch,
		nil,
	)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(frontier), target)
	for branch := range shamap.BranchFactor - 1 {
		require.Zero(t, outstanding[branch])
	}
	require.EqualValues(t, len(frontier), outstanding[shamap.BranchFactor-1])
}

func BenchmarkService_VerifyStoredSHAMapWorkers(b *testing.B) {
	ctx := b.Context()
	db := newTestNodeStore(b, 32_768)
	b.Cleanup(func() { require.NoError(b, db.Close()) })
	svc, err := New(Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
		SHAMapFamily:  backend.New(db),
	})
	require.NoError(b, err)
	require.NoError(b, svc.Start())
	b.Cleanup(svc.Stop)

	for i := range 16_384 {
		var seed [8]byte
		seed[0] = byte(i >> 24)
		seed[1] = byte(i >> 16)
		seed[2] = byte(i >> 8)
		seed[3] = byte(i)
		key := sha512half.Sum(seed[:])
		data := make([]byte, 12)
		data[8] = seed[0]
		data[9] = seed[1]
		data[10] = seed[2]
		data[11] = seed[3]
		require.NoError(b, svc.openLedger.Insert(keylet.Keylet{Key: key}, data))
	}
	_, err = svc.AcceptLedger(ctx)
	require.NoError(b, err)
	svc.FlushPersists()
	root, err := svc.GetValidatedLedger().StateMapHash()
	require.NoError(b, err)
	var nodes uint64
	require.NoError(b, svc.walkStoredSHAMap(
		ctx,
		root,
		shamap.TypeState,
		func([32]byte, *nodestore.Node) error {
			nodes++
			return nil
		},
	))

	for _, workers := range []int{8, 16, 32, 64} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			svc.config.FastLoadWorkers = workers
			b.ResetTimer()
			startedAt := time.Now()
			for range b.N {
				require.NoError(b, svc.verifyStoredSHAMap(ctx, root, shamap.TypeState))
			}
			b.StopTimer()
			b.ReportMetric(
				float64(uint64(b.N)*nodes)/time.Since(startedAt).Seconds(),
				"nodes/s",
			)
		})
	}
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
	require.EqualValues(t, resolveStoredSHAMapWorkers(0), records[0].Workers)
	require.Equal(t, "stored SHAMap verification complete", records[1].Message)
	require.Equal(t, "2s", records[1].Elapsed)
	require.Equal(t, expectedNodes, records[1].NodesChecked)
	require.Equal(t, expectedNodes/2, records[1].NodesPerSecond)
	require.Equal(t, expectedNodes/2, records[1].IntervalNodesRate)
	require.Equal(t, expectedBranches, records[1].BranchesComplete)
	require.Equal(t, expectedBranches, records[1].BranchesTotal)
	require.Zero(t, records[1].WorkerPoolSize)
	require.Zero(t, records[1].ActiveWorkers)
	require.Zero(t, records[1].IdleWorkers)
	require.Zero(t, records[1].FrontierSize)
	require.Equal(
		t,
		records[1].NodeStoreReadsBefore+expectedNodes,
		records[1].NodeStoreReadsAfter,
	)
	require.Greater(t, records[1].NodeStoreReadBytesAfter, records[1].NodeStoreReadBytesBefore)
	require.Equal(t, records[1].NodeCacheHitsBefore, records[1].NodeCacheHitsAfter)
	require.Equal(t, records[1].NodeCacheMissesBefore, records[1].NodeCacheMissesAfter)
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
	require.Len(t, records, 2)
	require.Equal(t, "stored SHAMap verification started", records[0].Message)
	require.Equal(t, "stored SHAMap verification complete", records[1].Message)
	require.Equal(t, storedSHAMapVerificationLogInterval.String(), records[1].Elapsed)
	require.Equal(t, expectedNodes, records[1].NodesChecked)
	require.Equal(t, expectedBranches, records[1].BranchesComplete)
	require.Equal(
		t,
		expectedNodes/uint64(storedSHAMapVerificationLogInterval/time.Second),
		records[1].IntervalNodesRate,
	)
}

func TestService_VerifyStoredSHAMapRateLimitsProgressAndReportsCancellation(t *testing.T) {
	svc, db, root, rootChildren := newParallelStoredVerificationFixture(t)
	svc.config.FastLoadWorkers = 4
	unblocked := make(map[nodestore.Hash256]struct{}, len(rootChildren))
	for _, child := range rootChildren {
		unblocked[child] = struct{}{}
	}
	blocked := &blockingVerificationDatabase{
		Database:  db,
		root:      nodestore.Hash256(root),
		unblocked: unblocked,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
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
	require.EqualValues(t, 1+len(rootChildren), records[1].NodesChecked)
	require.Equal(t, "stored SHAMap verification progress", records[2].Message)
	require.Equal(t, "30s", records[2].Elapsed)
	require.GreaterOrEqual(t, records[2].NodesChecked, records[1].NodesChecked)
	require.Equal(t, "stored SHAMap verification failed", records[3].Message)
	require.Equal(t, "WARN", records[3].Level)
	require.Equal(t, "31s", records[3].Elapsed)
	require.GreaterOrEqual(t, records[3].NodesChecked, records[2].NodesChecked)
	require.Zero(t, records[3].BranchesComplete)
	require.EqualValues(t, len(rootChildren), records[3].BranchesTotal)
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
	db := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

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
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

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
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

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
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	rm := newTestRepositories(t, ctx)

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
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
	require.False(t, errors.Is(err, shamap.ErrInvalidNodeData))
}

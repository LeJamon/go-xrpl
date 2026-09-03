package inbound

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

type trackerBlockingFamily struct {
	base    shamap.Family
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	block   bool
}

type trackerBlockingClock struct {
	now     time.Time
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (c *trackerBlockingClock) Now() time.Time {
	if c.block.Load() {
		close(c.entered)
		<-c.release
	}
	return c.now
}

func (f *trackerBlockingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if !f.block {
		return f.base.Fetch(ctx, hash)
	}
	f.once.Do(func() { close(f.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return f.base.Fetch(ctx, hash)
	}
}

func (f *trackerBlockingFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// driveToFailure runs the acquisition's OnTimer retry loop past its budget so it
// reaches StateFailed, the white-box stand-in for the router's reaper.
func driveToFailure(il *Ledger) {
	base := time.Unix(1_700_000_000, 0)
	il.lastTimer = base
	for i := 1; i <= ledgerTimeoutRetriesMax+2; i++ {
		il.OnTimer(base.Add(time.Duration(i) * acquireTimerInterval))
	}
}

func trackTerminal(tr *Tracker, hash [32]byte, seq uint32, complete bool) {
	il := New(hash, seq, 1, discardLogger())
	tr.Track(il)
	tr.RemoveExpectedWithSnapshot(il, Snapshot{Hash: hash, Seq: seq}, complete)
}

func trackerHistoryCounts(tr *Tracker) (terminal, failures int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.terminal), len(tr.failures)
}

// encodeHeader serializes a header for the wire and returns the hash a peer
// answering GetLedger must produce. GotBase recomputes the hash from these exact
// bytes and rejects a mismatch (mirroring rippled's takeHeader), so tests
// driving a real acquisition must request the header's true byte-level hash.
func encodeHeader(h header.LedgerHeader) (data []byte, hash [32]byte) {
	data = header.AddRaw(h, false)
	return data, sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), data)
}

// buildSourceState returns a multi-level state SHAMap plus its root hash,
// serialized root, and wire nodes — enough to drive a real acquisition.
func buildSourceState(t *testing.T) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	return buildSourceMap(t, shamap.TypeState)
}

// buildSourceMap builds a multi-level SHAMap of the given type and returns its
// root hash, serialized root, and wire nodes — enough to drive a real
// state-tree or transaction-tree acquisition.
func buildSourceMap(t *testing.T, mapType shamap.Type) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	source := shamap.New(mapType)
	for branch := range byte(4) {
		for sub := range byte(4) {
			for i := range byte(4) {
				data := []byte{branch, sub, i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				key[31] = 0xA5
				if mapType == shamap.TypeTransaction {
					key = sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), data)
				}
				if err := source.Put(key, data); err != nil {
					t.Fatalf("put: %v", err)
				}
			}
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("source hash: %v", err)
	}
	rootData, err = source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize root: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk wire nodes: %v", err)
	}
	wire = make([]message.LedgerNode, 0, len(wireNodes))
	for _, w := range wireNodes {
		wire = append(wire, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	return rootHash, rootData, wire
}

// newAcquiring returns a Ledger that has received its header + state root and
// is mid-acquisition (StateWantState), with missing state nodes outstanding.
func newAcquiring(t *testing.T, seq uint32) *Ledger {
	t.Helper()
	rootHash, rootData, _ := buildSourceState(t)
	hdrBytes, hash := encodeHeader(header.LedgerHeader{LedgerIndex: seq, AccountHash: rootHash})
	il := New(hash, seq, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() != StateWantState {
		t.Fatalf("state = %d, want StateWantState", il.State())
	}
	il.CollectMissingRequest(false)
	return il
}

func TestTracker_ActiveAcquisitionSnapshot(t *testing.T) {
	t.Parallel()
	il := newAcquiring(t, 200)

	tr := NewTracker()
	tr.Track(il)

	info := tr.Info()
	entry, ok := info["200"].(map[string]any)
	if !ok {
		t.Fatalf("expected entry keyed by seq %q, got %#v", "200", info)
	}
	if entry["have_header"] != true {
		t.Errorf("have_header = %v, want true", entry["have_header"])
	}
	if entry["have_state"] != false {
		t.Errorf("have_state = %v, want false", entry["have_state"])
	}
	if entry["peers"] != 1 {
		t.Errorf("peers = %v, want 1", entry["peers"])
	}
	needed, ok := entry["needed_state_hashes"].([]any)
	if !ok || len(needed) == 0 {
		t.Errorf("needed_state_hashes = %#v, want non-empty array", entry["needed_state_hashes"])
	}
	if entry["timeouts"] != 0 {
		t.Errorf("timeouts = %v, want 0", entry["timeouts"])
	}
	for _, field := range []string{
		"state_equal_subtrees_skipped",
		"state_nodes_descended",
		"state_durable_reads",
		"state_missing_discovered",
		"state_verified_base_fallbacks",
	} {
		if _, ok := entry[field]; !ok {
			t.Errorf("missing discovery diagnostic %q", field)
		}
	}
}

func TestTracker_CompletedReportedThenSwept(t *testing.T) {
	t.Parallel()
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	rootHash, rootData, wire := buildSourceState(t)
	hdrBytes, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 300, AccountHash: rootHash})
	il := New(hash, 300, 9, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdrBytes}, {NodeData: rootData}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}

	tr := NewTrackerWithClock(clock)
	tr.Track(il)

	if err := il.GotStateNodes(wire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	il.CollectMissingRequest(false)
	if !il.IsComplete() {
		t.Fatalf("acquisition not complete")
	}
	// rippled keeps a completed acquisition in mLedgers until sweep, so
	// fetch_info reports complete:true for a short window.
	entry, ok := tr.Info()["300"].(map[string]any)
	if !ok {
		t.Fatalf("completed acquisition should be reported, got %#v", tr.Info())
	}
	if entry["complete"] != true {
		t.Errorf("complete = %v, want true", entry["complete"])
	}
	if entry["have_state"] != true {
		t.Errorf("have_state = %v, want true", entry["have_state"])
	}
	if _, hasPeers := entry["peers"]; hasPeers {
		t.Errorf("completed entry must not report peers, got %#v", entry)
	}
	if got := tr.Find(hash); got != il {
		t.Fatal("fetch_info retired a completed acquisition before its owner")
	}
	tr.RemoveExpectedWithSnapshot(il, il.Snapshot(), true)

	clock.Advance(terminalRetention + time.Nanosecond)
	if info := tr.Info(); len(info) != 1 {
		t.Fatalf("Info mutated expired history before maintenance, got %#v", info)
	}
	if completed, _ := trackerHistoryCounts(tr); completed != 1 {
		t.Fatalf("Info pruned completed history: got %d records, want 1", completed)
	}

	tr.Sweep()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("completed acquisition should be swept after retention, got %#v", info)
	}
}

func TestTracker_SweepExpiresHistoryWithoutInfo(t *testing.T) {
	t.Parallel()
	const sweepInterval = 10 * time.Second
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	tr := NewTrackerWithClockAndSweepInterval(clock, sweepInterval)
	trackTerminal(tr, [32]byte{0xC1}, 301, true)
	failureHash := [32]byte{0xF1}
	failure := New(failureHash, 302, 1, discardLogger())
	tr.Track(failure)
	tr.RemoveExpectedWithSnapshot(failure, Snapshot{
		Hash:       failureHash,
		Seq:        302,
		HaveHeader: true,
		Timeouts:   3,
	}, false)

	clock.Advance(terminalRetention)
	tr.Sweep()
	if terminal, failures := trackerHistoryCounts(tr); terminal != 2 || failures != 1 {
		t.Fatalf("history at terminal retention = (%d terminal, %d failures), want (2, 1)", terminal, failures)
	}
	richFailure := tr.Info()["302"].(map[string]any)
	if richFailure["failed"] != true || richFailure["have_header"] != true || richFailure["timeouts"] != 3 {
		t.Fatalf("retained failure = %#v, want rich terminal snapshot", richFailure)
	}

	clock.Advance(sweepInterval)
	tr.Sweep()
	if terminal, failures := trackerHistoryCounts(tr); terminal != 0 || failures != 1 {
		t.Fatalf("history after terminal retention = (%d terminal, %d failures), want (0, 1)", terminal, failures)
	}
	entry := tr.Info()["302"].(map[string]any)
	if len(entry) != 1 || entry["failed"] != true {
		t.Fatalf("expired rich failure = %#v, want bare failed marker", entry)
	}

	clock.Advance(reacquireInterval - terminalRetention - sweepInterval)
	tr.Sweep()
	if terminal, failures := trackerHistoryCounts(tr); terminal != 0 || failures != 0 {
		t.Fatalf("history at failure retention = (%d terminal, %d failures), want (0, 0)", terminal, failures)
	}
}

func TestTracker_TerminalInsertionBoundsSustainedChurn(t *testing.T) {
	t.Parallel()
	const (
		acquisitions  = 10_009
		sweepInterval = 10 * time.Second
	)
	start := time.Unix(1_700_000_000, 0)

	completedClock := inboundtest.NewFakeClock(start)
	completedTracker := NewTrackerWithClockAndSweepInterval(completedClock, sweepInterval)
	for i := range acquisitions {
		completedClock.Advance(time.Second)
		hash := [32]byte{byte(i), byte(i >> 8), byte(i >> 16), 0xC0}
		trackTerminal(completedTracker, hash, uint32(i+2), true)
	}
	terminal, _ := trackerHistoryCounts(completedTracker)
	if max := int((terminalRetention + sweepInterval) / time.Second); terminal != max {
		t.Fatalf("terminal history retained %d records just before sweep, want %d", terminal, max)
	}

	failureClock := inboundtest.NewFakeClock(start)
	failureTracker := NewTrackerWithClockAndSweepInterval(failureClock, sweepInterval)
	for i := range acquisitions {
		failureClock.Advance(time.Second)
		hash := [32]byte{byte(i), byte(i >> 8), byte(i >> 16), 0xF0}
		trackTerminal(failureTracker, hash, uint32(i+2), false)
	}
	_, failures := trackerHistoryCounts(failureTracker)
	if min, max := int(reacquireInterval/time.Second), int((reacquireInterval+sweepInterval)/time.Second); failures <= min || failures > max {
		t.Fatalf("failure history retained %d records just before sweep, want (%d, %d]", failures, min, max)
	}
}

func TestTracker_LiveAcquisitionOverwritesSameSeqFailure(t *testing.T) {
	t.Parallel()
	var failHash [32]byte
	failHash[0] = 0x33

	// A prior attempt at seq 600 (failHash) failed and is remembered.
	failed := New(failHash, 600, 3, discardLogger())
	if err := failed.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail")
	}
	// A fresh attempt at the same seq is now in flight.
	live := newAcquiring(t, 600)

	tr := NewTracker()
	tr.Track(failed)
	tr.RemoveExpectedWithSnapshot(failed, failed.Snapshot(), false)
	tr.Track(live)

	entry, ok := tr.Info()["600"].(map[string]any)
	if !ok {
		t.Fatalf("seq 600 should be present, got %#v", tr.Info())
	}
	if entry["failed"] == true {
		t.Errorf("live re-acquisition must win over a stale same-seq failure, got %#v", entry)
	}
	if entry["have_header"] != true {
		t.Errorf("expected the live acquisition entry, got %#v", entry)
	}
}

func TestTracker_FailedReportedThenCleared(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0xEF
	il := New(hash, 400, 3, discardLogger())
	// Peer-originated base errors are recoverable; model the owner's terminal
	// timeout verdict directly for this tracker-lifecycle test.
	if err := il.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail with a single node")
	}
	il.mu.Lock()
	il.state = StateFailed
	il.err = errors.New("retry budget exhausted")
	il.mu.Unlock()

	tr := NewTracker()
	tr.Track(il)
	tr.RemoveExpectedWithSnapshot(il, il.Snapshot(), false)

	entry, ok := tr.Info()["400"].(map[string]any)
	if !ok || entry["failed"] != true {
		t.Fatalf("expected {failed:true} for failed acquisition, got %#v", tr.Info())
	}

	tr.Clear()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("Clear should empty the tracker, got %#v", info)
	}
}

func TestTracker_StopDrainsAndRejectsLaterAdmissions(t *testing.T) {
	t.Parallel()
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	tr := NewTrackerWithClock(clock)
	first := New([32]byte{0xA1}, 401, 3, discardLogger())
	tr.Track(first)
	trackTerminal(tr, [32]byte{0xB1}, 402, true)
	trackTerminal(tr, [32]byte{0xB2}, 403, false)

	drained := tr.Stop()
	if len(drained) != 1 || drained[0] != first {
		t.Fatalf("Stop drained %#v, want the active acquisition", drained)
	}
	if got := tr.Find(first.Hash()); got != nil {
		t.Fatalf("stopped tracker retained active acquisition %p", got)
	}
	if terminal, failures := trackerHistoryCounts(tr); terminal != 0 || failures != 0 {
		t.Fatalf("stopped tracker retained history (%d terminal, %d failures)", terminal, failures)
	}
	clock.Advance(reacquireInterval)
	tr.Sweep()

	second := New([32]byte{0xA2}, 402, 4, discardLogger())
	tr.Track(second)
	if got := tr.Find(second.Hash()); got != nil {
		t.Fatalf("Track repopulated stopped tracker with %p", got)
	}
	created, ok := tr.GetOrCreate([32]byte{0xA3}, func() *Ledger { return second })
	if ok || created != nil {
		t.Fatalf("GetOrCreate on stopped tracker = (%p,%v), want (nil,false)", created, ok)
	}
	if again := tr.Stop(); len(again) != 0 {
		t.Fatalf("second Stop drained %#v, want empty", again)
	}
}

func TestTracker_ConcurrentAccess(t *testing.T) {
	clock := inboundtest.NewFakeClock(time.Unix(1_700_000_000, 0))
	tr := NewTrackerWithClock(clock)
	start := make(chan struct{})
	var workers sync.WaitGroup

	for worker := range 4 {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			for i := range 250 {
				hash := [32]byte{byte(i), byte(i >> 8), byte(worker), 0xAC}
				il, created := tr.GetOrCreate(hash, func() *Ledger {
					return New(hash, uint32(worker*250+i+2), uint64(worker+1), discardLogger())
				})
				if created {
					tr.RemoveExpectedWithSnapshot(il, Snapshot{Hash: hash, Seq: il.Seq()}, i%2 == 0)
				}
			}
		}(worker)
	}

	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for range 250 {
			clock.Advance(time.Second)
			tr.Sweep()
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range 250 {
			_ = tr.Info()
		}
	}()

	close(start)
	workers.Wait()
	if active := tr.Active(); len(active) != 0 {
		t.Fatalf("tracker retained %d active acquisitions", len(active))
	}
	tr.Stop()
	if terminal, failures := trackerHistoryCounts(tr); terminal != 0 || failures != 0 {
		t.Fatalf("stopped tracker retained history (%d terminal, %d failures)", terminal, failures)
	}
}

func TestTracker_StopSerializesWithInFlightFinalization(t *testing.T) {
	clock := &trackerBlockingClock{
		now:     time.Unix(1_700_000_000, 0),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	tr := NewTrackerWithClock(clock)
	hash := [32]byte{0xA3}
	ledger := New(hash, 1828, 1, discardLogger())
	tr.Track(ledger)
	clock.block.Store(true)
	finalizationDone := make(chan bool, 1)
	go func() {
		finalizationDone <- tr.RemoveExpectedWithSnapshot(
			ledger,
			Snapshot{Hash: hash, Seq: 1828},
			true,
		)
	}()

	<-clock.entered
	stopStarted := make(chan struct{})
	stopDone := make(chan []*Ledger, 1)
	go func() {
		close(stopStarted)
		stopDone <- tr.Stop()
	}()
	<-stopStarted
	close(clock.release)

	if !<-finalizationDone {
		t.Fatal("in-flight finalization did not retire the tracked ledger")
	}
	drained := <-stopDone
	if len(drained) != 0 {
		t.Fatalf("Stop drained %#v after finalization, want no active ledgers", drained)
	}
	if got := tr.Find(hash); got != nil {
		t.Fatalf("stopped tracker retained finalized ledger %p", got)
	}
	if terminal, failures := trackerHistoryCounts(tr); terminal != 0 || failures != 0 {
		t.Fatalf("stopped tracker retained history (%d terminal, %d failures)", terminal, failures)
	}
}

func TestTracker_TimedOutDemotedToFailure(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0x11
	il := New(hash, 500, 4, discardLogger())
	driveToFailure(il) // white-box: exhaust the retry budget

	tr := NewTracker()
	tr.Track(il)
	tr.RemoveExpectedWithSnapshot(il, il.Snapshot(), false)

	entry, ok := tr.Info()["500"].(map[string]any)
	if !ok || entry["failed"] != true {
		t.Fatalf("timed-out acquisition should report {failed:true}, got %#v", tr.Info())
	}
	// The owner moved it out of the active set into the failure history.
	tr.mu.Lock()
	_, stillActive := tr.active[hash]
	tr.mu.Unlock()
	if stillActive {
		t.Error("timed-out acquisition should be removed from the active set")
	}
}

func TestTracker_GenesisKeyedByHash(t *testing.T) {
	t.Parallel()
	var hash [32]byte
	hash[0] = 0x22
	il := New(hash, 1, 5, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: []byte{0x00}}}); err == nil {
		t.Fatal("expected GotBase to fail")
	}

	tr := NewTracker()
	tr.Track(il)

	wantKey := acquisitionKey(1, hash)
	if _, ok := tr.Info()[wantKey].(map[string]any); !ok {
		t.Fatalf("seq<=1 should be keyed by hash %q, got %#v", wantKey, tr.Info())
	}
}

func TestTracker_NilSafe(t *testing.T) {
	t.Parallel()
	var tr *Tracker
	tr.Track(New([32]byte{}, 1, 0, discardLogger())) // must not panic
	tr.Clear()
	if info := tr.Info(); len(info) != 0 {
		t.Errorf("nil tracker Info should be empty, got %#v", info)
	}
}

func TestTracker_DiscardExpectedLeavesNoTerminalRecord(t *testing.T) {
	tracker := NewTracker()
	ledger := New([32]byte{0xD1}, 700, 9, discardLogger())
	tracker.Track(ledger)
	if !tracker.DiscardExpected(ledger) {
		t.Fatal("DiscardExpected did not remove the active acquisition")
	}
	if tracker.Find(ledger.Hash()) != nil {
		t.Fatal("discarded acquisition remained active")
	}
	if info := tracker.Info(); len(info) != 0 {
		t.Fatalf("discard recorded a terminal network result: %v", info)
	}
	if tracker.DiscardExpected(ledger) {
		t.Fatal("discard was not idempotent")
	}
}

func TestTracker_InfoUsesCachedFrontierWithoutStoreReads(t *testing.T) {
	_, rootHash, rootData := buildBackedTestState(t, 32)
	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 501, AccountHash: rootHash})
	family := &trackerBlockingFamily{
		base:    backend.NewMemory(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	il := New(hash, 501, 7, discardLogger(), WithFamily(family))
	requireNoError(t, il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: rootData}}))
	stateIDs, _ := il.CollectMissingRequest(false)
	if len(stateIDs) == 0 {
		t.Fatal("missing-node worker did not cache a state frontier")
	}
	family.block = true
	defer close(family.release)
	tr := NewTracker()
	tr.Track(il)

	infoDone := make(chan map[string]any, 1)
	go func() {
		infoDone <- tr.Info()
	}()
	select {
	case info := <-infoDone:
		entry, ok := info["501"].(map[string]any)
		if !ok {
			t.Fatalf("missing active acquisition: %#v", info)
		}
		needed, ok := entry["needed_state_hashes"].([]any)
		if !ok || len(needed) == 0 {
			t.Fatalf("cached state frontier missing: %#v", entry)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("fetch_info blocked on a backing-store traversal")
	}
	select {
	case <-family.entered:
		t.Fatal("fetch_info read the backing store")
	default:
	}
}

func TestTracker_InfoDistinguishesUnscannedAndEmptyFrontiers(t *testing.T) {
	il := newAcquiring(t, 503)
	il.mu.Lock()
	il.cacheMissingLocked(false, []shamap.MissingNode{})
	il.mu.Unlock()

	snap := il.Snapshot()
	if snap.NeededState == nil || len(snap.NeededState) != 0 {
		t.Fatalf("scanned empty frontier = %#v, want non-nil empty", snap.NeededState)
	}
	entry := AcquisitionJSON(snap)
	needed, ok := entry["needed_state_hashes"].([]any)
	if !ok || len(needed) != 0 {
		t.Fatalf("needed_state_hashes = %#v, want present empty array", entry["needed_state_hashes"])
	}

	unscanned := New([32]byte{0x50}, 504, 7, discardLogger()).Snapshot()
	if _, ok := AcquisitionJSON(unscanned)["needed_state_hashes"]; ok {
		t.Fatal("unscanned frontier was emitted")
	}
}

func TestTracker_InfoDoesNotWaitForWorkerStoreRead(t *testing.T) {
	rootHash, rootData, wire := buildSourceMap(t, shamap.TypeState)
	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 502, AccountHash: rootHash})
	family := &trackerBlockingFamily{
		base:    backend.NewMemory(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	il := New(hash, 502, 7, discardLogger(), WithFamily(family))
	requireNoError(t, il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: rootData}}))
	il.CollectMissingRequest(false)
	tr := NewTracker()
	tr.Track(il)

	var depthOne message.LedgerNode
	for _, node := range wire {
		id, err := shamap.ParseNodeID(node.NodeID)
		if err == nil && id.Depth() == 1 {
			depthOne = node
			break
		}
	}
	if len(depthOne.NodeData) == 0 {
		t.Fatal("source map had no depth-one node")
	}

	family.block = true
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(family.release) }) }
	defer release()
	workerDone := make(chan error, 1)
	applied := make(chan struct{})
	go func() {
		_, err := il.GotStateNodesUseful([]message.LedgerNode{depthOne})
		close(applied)
		if err == nil {
			// Force fresh frontier discovery. A timeout collect intentionally
			// reuses the cached frontier and no longer touches durable storage.
			_, _, _, err = il.CollectMissingRequestContext(context.Background(), true)
		}
		workerDone <- err
	}()
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("state reply application performed a backing-store completeness walk")
	}
	select {
	case <-family.entered:
	case <-time.After(time.Second):
		t.Fatal("state worker did not block in the backing store")
	}

	infoDone := make(chan map[string]any, 1)
	go func() { infoDone <- tr.Info() }()
	select {
	case info := <-infoDone:
		entry, ok := info["502"].(map[string]any)
		if !ok || entry["have_header"] != true {
			t.Fatalf("cached acquisition snapshot missing: %#v", info)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("fetch_info waited for the worker's backing-store read")
	}

	release()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("state worker did not resume")
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestInbound_FullAcquisitionWithTransactions drives a ledger with both a
// non-empty state tree and a non-empty transaction tree through the full
// acquisition. fetch_info reports have_transactions + needed_transaction_hashes
// while the tx tree is outstanding, and the acquisition is complete only once
// both trees are in hand.
func TestInbound_FullAcquisitionWithTransactions(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, stateWire := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, txWire := buildSourceMap(t, shamap.TypeTransaction)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 700, AccountHash: stateRootHash, TxHash: txRootHash})
	il := New(hash, 700, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}, {NodeData: txRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.State() != StateWantState {
		t.Fatalf("state = %d, want StateWantState", il.State())
	}
	requests, complete, err := il.CollectMissingAddedRequestsContext(context.Background(), []uint64{7})
	if err != nil || complete || len(requests) == 0 || requests[0].Transaction {
		t.Fatalf("initial state request = %#v, complete=%t, err=%v", requests, complete, err)
	}

	tr := NewTracker()
	tr.Track(il)

	entry := tr.Info()["700"].(map[string]any)
	if entry["have_transactions"] != false {
		t.Errorf("have_transactions = %v, want false", entry["have_transactions"])
	}
	if _, ok := entry["needed_transaction_hashes"]; ok {
		t.Errorf("unscanned transaction frontier should be omitted, got %#v", entry["needed_transaction_hashes"])
	}

	// State completes first; the acquisition must still wait for the tx tree.
	if err := il.GotStateNodes(stateWire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	if il.IsComplete() {
		t.Fatal("acquisition complete before tx tree fetched")
	}
	il.ReleaseMissingPeer(7)
	requests, complete, err = il.CollectMissingReplyRequestsContext(context.Background(), []uint64{7})
	if err != nil || complete || len(requests) == 0 || !requests[0].Transaction {
		t.Fatalf("transaction request = %#v, complete=%t, err=%v", requests, complete, err)
	}
	entry = tr.Info()["700"].(map[string]any)
	if needed, ok := entry["needed_transaction_hashes"].([]any); !ok || len(needed) == 0 {
		t.Errorf("worker-cached transaction frontier = %#v, want non-empty", entry["needed_transaction_hashes"])
	}

	// Tx completes; the acquisition is now complete.
	if err := il.GotTransactionNodes(txWire); err != nil {
		t.Fatalf("GotTransactionNodes: %v", err)
	}
	il.CollectMissingRequest(false)
	if !il.IsComplete() {
		t.Fatal("acquisition not complete after both trees fetched")
	}
	if _, _, gotTx, err := il.Result(); err != nil || gotTx == nil {
		t.Fatalf("Result tx map = %v (err %v), want the acquired tree", gotTx, err)
	}

	entry = tr.Info()["700"].(map[string]any)
	if entry["complete"] != true {
		t.Errorf("complete = %v, want true", entry["complete"])
	}
	if entry["have_transactions"] != true {
		t.Errorf("have_transactions = %v, want true", entry["have_transactions"])
	}
	if _, has := entry["needed_transaction_hashes"]; has {
		t.Errorf("needed_transaction_hashes must be absent once tx acquired, got %#v", entry["needed_transaction_hashes"])
	}
}

func TestInbound_TransactionOnlyAcquisition(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, _ := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, txWire := buildSourceMap(t, shamap.TypeTransaction)

	hdr, hash := encodeHeader(header.LedgerHeader{
		LedgerIndex: 701,
		AccountHash: stateRootHash,
		TxHash:      txRootHash,
	})
	il := New(hash, 701, 7, discardLogger(), WithTransactionOnly())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: hdr},
		{NodeData: stateRoot},
		{NodeData: txRoot},
	}))
	require.True(t, il.TransactionOnly())
	require.Nil(t, il.stateMap, "transaction-only acquisition must not construct the target state tree")
	require.True(t, il.Snapshot().HaveState, "local replay supplies state after acquisition")
	require.False(t, il.IsComplete(), "non-empty transaction tree is still outstanding")

	requests, complete, err := il.CollectMissingAddedRequestsContext(t.Context(), []uint64{7})
	require.NoError(t, err)
	require.False(t, complete)
	require.NotEmpty(t, requests)
	for _, request := range requests {
		require.True(t, request.Transaction, "transaction-only mode must never request state nodes")
	}

	require.NoError(t, il.GotTransactionNodes(txWire))
	il.CollectMissingRequest(false)
	require.True(t, il.IsComplete())
	_, gotState, gotTx, err := il.Result()
	require.NoError(t, err)
	require.Nil(t, gotState)
	require.NotNil(t, gotTx)
}

// TestInbound_EmptyTxTreeImmediatelyComplete confirms a ledger with no
// transactions (zero TxHash) reports have_transactions:true on arrival and
// completes on the state tree alone, with no tx round-trip and a nil tx map.
func TestInbound_EmptyTxTreeImmediatelyComplete(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, stateWire := buildSourceMap(t, shamap.TypeState)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 800, AccountHash: stateRootHash}) // TxHash zero
	il := New(hash, 800, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	if il.NeedsMissingTxNodeIDs() != nil {
		t.Error("empty tx tree must not request tx nodes")
	}

	tr := NewTracker()
	tr.Track(il)
	entry := tr.Info()["800"].(map[string]any)
	if entry["have_transactions"] != true {
		t.Errorf("have_transactions = %v, want true (empty tx tree)", entry["have_transactions"])
	}
	if _, has := entry["needed_transaction_hashes"]; has {
		t.Error("needed_transaction_hashes must be absent for an empty tx tree")
	}

	if err := il.GotStateNodes(stateWire); err != nil {
		t.Fatalf("GotStateNodes: %v", err)
	}
	il.CollectMissingRequest(false)
	if !il.IsComplete() {
		t.Fatal("acquisition with empty tx tree should complete on state")
	}
	if _, _, gotTx, err := il.Result(); err != nil || gotTx != nil {
		t.Fatalf("Result tx map = %v (err %v), want nil for empty tx tree", gotTx, err)
	}
}

// TestTracker_FailedEntryCarriesRichShape confirms a failed/timed-out
// acquisition reports the full per-tree getJson shape (failed:true plus
// have_header, no peers), mirroring rippled's still-in-mLedgers failed ledger
// rather than a bare {failed:true}.
func TestTracker_FailedEntryCarriesRichShape(t *testing.T) {
	t.Parallel()
	stateRootHash, stateRoot, _ := buildSourceMap(t, shamap.TypeState)
	txRootHash, txRoot, _ := buildSourceMap(t, shamap.TypeTransaction)

	hdr, hash := encodeHeader(header.LedgerHeader{LedgerIndex: 950, AccountHash: stateRootHash, TxHash: txRootHash})
	il := New(hash, 950, 7, discardLogger())
	if err := il.GotBase([]message.LedgerNode{{NodeData: hdr}, {NodeData: stateRoot}, {NodeData: txRoot}}); err != nil {
		t.Fatalf("GotBase: %v", err)
	}
	driveToFailure(il) // white-box: exhaust the retry budget

	tr := NewTracker()
	tr.Track(il)
	entry := tr.Info()["950"].(map[string]any)
	if entry["failed"] != true {
		t.Errorf("failed = %v, want true", entry["failed"])
	}
	if entry["have_header"] != true {
		t.Errorf("failed entry should carry have_header, got %#v", entry)
	}
	if _, hasPeers := entry["peers"]; hasPeers {
		t.Errorf("failed entry must not report peers, got %#v", entry)
	}
}

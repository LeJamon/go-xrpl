package adaptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedLatencySender struct {
	*acqRecordingSender
	latencies map[uint64]time.Duration
}

type failingAcquisitionCheckpointFamily struct {
	shamap.Family
	err error
}

func (f failingAcquisitionCheckpointFamily) Flush(context.Context) error {
	return f.err
}

type orderedAcquisitionEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *orderedAcquisitionEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *orderedAcquisitionEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

type orderedAcquisitionSender struct {
	acqRecordingSender
	events *orderedAcquisitionEvents
}

func (s *orderedAcquisitionSender) RequestStateNodes(peerID uint64, hash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error {
	s.events.add("state request")
	return s.acqRecordingSender.RequestStateNodes(peerID, hash, nodeIDs, queryDepth, indirect)
}

type orderedAcquisitionManifestSender struct {
	*fakeManifestSender
	events *orderedAcquisitionEvents
}

func (s *orderedAcquisitionManifestSender) Send(peerID peermanagement.PeerID, frame []byte) error {
	s.events.add("manifest")
	return s.fakeManifestSender.Send(peerID, frame)
}

func (s *orderedAcquisitionManifestSender) SendManifestFrames(peerID peermanagement.PeerID, frames [][]byte) error {
	s.events.add("manifest")
	return s.fakeManifestSender.SendManifestFrames(peerID, frames)
}

type blockingProposalEngine struct {
	*mockEngine
	entered chan struct{}
	release chan struct{}
}

func (e *blockingProposalEngine) OnProposal(proposal *consensus.Proposal, peerID uint64) error {
	close(e.entered)
	<-e.release
	return e.mockEngine.OnProposal(proposal, peerID)
}

func (s *fixedLatencySender) PeerLatency(peerID uint64) (time.Duration, bool) {
	latency, ok := s.latencies[peerID]
	return latency, ok
}

func TestAcquisitionWork_DoesNotBlockRouter(t *testing.T) {
	engine := &mockEngine{}
	inbox := make(chan *peermanagement.InboundMessage, 2)
	router := newTestRouter(engine, newTestAdaptor(t), inbox)
	router.adaptor.LedgerService().SetValidatedLedgerAgeClock(func() time.Time {
		return time.Now().Add(time.Minute)
	})
	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
		case <-release:
			return acquisitionWorkResult{ledger: ledger}
		}
	}
	router.acquisitionWork = lane

	hash := [32]byte{0xA1}
	ledger := inbound.New(hash, 42, 7, serveTestLogger())
	router.fetchTracker.Track(ledger)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.Run(ctx)
	}()

	data := &message.LedgerData{
		LedgerHash: hash[:], LedgerSeq: 42, InfoType: message.LedgerInfoAsNode,
		Nodes: []message.LedgerNode{{NodeID: make([]byte, 33), NodeData: []byte{1}}},
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  7,
		Type:    message.TypeLedgerData,
		Payload: encodePayload(t, data),
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("acquisition worker did not start")
	}
	time.Sleep(150 * time.Millisecond)

	proposal := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Now()),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	proposal.NodePubKey[0] = 0x02
	inbox <- &peermanagement.InboundMessage{
		PeerID:  8,
		Type:    message.TypeProposeLedger,
		Payload: encodePayload(t, proposal),
	}
	require.Eventually(t, func() bool { return len(engine.getProposals()) == 1 }, time.Second, time.Millisecond)

	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("router did not join acquisition worker")
	}
}

func TestAcquisitionWork_DropsRetiredResult(t *testing.T) {
	sender := &acqRecordingSender{}
	adaptor := newTestAdaptor(t)
	adaptor.sender = sender
	router := newTestRouter(nil, adaptor, nil)
	hash := [32]byte{0xB2}
	old := inbound.New(hash, 50, 7, serveTestLogger())
	router.fetchTracker.Track(old)
	router.fetchTracker.RemoveWithSnapshot(hash, old.Snapshot(), false)
	replacement, created := router.fetchTracker.GetOrCreate(hash, func() *inbound.Ledger {
		return inbound.New(hash, 50, 8, serveTestLogger())
	})
	require.True(t, created)
	require.NotSame(t, old, replacement)

	router.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger:   old,
		targets:  []uint64{7},
		stateIDs: [][]byte{make([]byte, 33)},
	})
	assert.Empty(t, sender.stateIndirects())
}

func TestSelectUsefulAcquisitionPeers(t *testing.T) {
	counts := map[uint64]int{1: 20, 2: 10, 3: 9, 4: 1, 5: 20, 6: 19, 7: 18, 8: 17, 9: 16}
	for range 100 {
		peers := selectUsefulAcquisitionPeers(counts)
		require.Len(t, peers, acquisitionMaxUsefulPeers)
		seen := make(map[uint64]struct{}, len(peers))
		for _, peerID := range peers {
			assert.GreaterOrEqual(t, counts[peerID], 10)
			_, duplicate := seen[peerID]
			assert.False(t, duplicate)
			seen[peerID] = struct{}{}
		}
	}
	assert.Nil(t, selectUsefulAcquisitionPeers(map[uint64]int{1: 0, 2: -1}))
}

func TestProcessAcquisitionWork_PlansSixDisjointPeerRequests(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	events := make([]acquisitionWorkEvent, 0, len(replies))
	for i, node := range replies {
		events = append(events, acquisitionWorkEvent{
			kind:   acquisitionWorkData,
			peerID: uint64(100 + i),
			data: &message.LedgerData{
				InfoType: message.LedgerInfoAsNode,
				Nodes:    []message.LedgerNode{node},
			},
		})
	}
	result := processAcquisitionWork(t.Context(), ledger, events)
	require.NoError(t, result.err)
	require.Len(t, result.requests, acquisitionMaxUsefulPeers)
	seen := make(map[string]uint64, 256)
	for _, request := range result.requests {
		assert.GreaterOrEqual(t, request.PeerID, uint64(100))
		assert.Less(t, request.PeerID, uint64(100+acquisitionMaxUsefulPeers))
		require.NotEmpty(t, request.NodeIDs)
		for _, nodeID := range request.NodeIDs {
			key := string(nodeID)
			if owner, duplicate := seen[key]; duplicate {
				t.Fatalf("node assigned to peers %d and %d", owner, request.PeerID)
			}
			seen[key] = request.PeerID
		}
	}
	require.Len(t, seen, 256)
}

func TestProcessAcquisitionWork_AddedPeerGetsBlindFrontier(t *testing.T) {
	ledger, _ := newWideWorkLedger(t)
	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkAdded, peerID: 22,
	}})
	require.NoError(t, result.err)
	require.Len(t, result.requests, 1)
	assert.Equal(t, uint64(22), result.requests[0].PeerID)
	assert.True(t, result.requests[0].Blind)
	assert.Len(t, result.requests[0].NodeIDs, 12)
}

func TestProcessAcquisitionWork_TimeoutSeparatesExistingAndAddedPeerRequests(t *testing.T) {
	ledger, _ := newWideWorkLedger(t)
	require.True(t, ledger.AddPeer(22))

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind:  acquisitionWorkTimer,
		peers: []uint64{1},
		added: []uint64{22},
	}})
	require.NoError(t, result.err)
	assert.Equal(t, []uint64{1}, result.targets)
	require.NotEmpty(t, result.stateIDs)
	require.Empty(t, result.txIDs)
	require.Len(t, result.requests, 1)
	assert.Equal(t, uint64(22), result.requests[0].PeerID)
	assert.True(t, result.requests[0].Blind)

	existing := make(map[string]struct{}, len(result.stateIDs))
	for _, nodeID := range result.stateIDs {
		existing[string(nodeID)] = struct{}{}
	}
	for _, request := range result.requests {
		for _, nodeID := range request.NodeIDs {
			if _, duplicate := existing[string(nodeID)]; duplicate {
				t.Fatalf("node assigned to both existing peer 1 and added peer %d", request.PeerID)
			}
		}
	}
}

func TestProcessAcquisitionWork_TimeoutSchedulesCachedFrontierBeforeLocalScan(t *testing.T) {
	ledger, _ := newWideWorkLedger(t)
	require.True(t, ledger.AddPeer(22))
	seeded, _ := ledger.CollectMissingRequest(false)
	require.NotEmpty(t, seeded)

	localFetches := 0
	result := processAcquisitionWorkWithBudget(t.Context(), ledger, []acquisitionWorkEvent{{
		kind:  acquisitionWorkTimer,
		peers: []uint64{1},
		added: []uint64{22},
		fetch: func([32]byte) ([]byte, bool) {
			localFetches++
			return nil, false
		},
	}}, 1)

	require.NoError(t, result.err)
	assert.Equal(t, []uint64{1}, result.targets)
	assert.NotEmpty(t, result.stateIDs)
	require.Len(t, result.requests, 1)
	assert.Equal(t, uint64(22), result.requests[0].PeerID)
	require.NotNil(t, result.localFetch)
	assert.Zero(t, localFetches, "timeout must defer the local traversal until requests are sent")
}

func TestHandleAcquisitionWorkResult_TimeoutRequestsPrecedeLocalRefresh(t *testing.T) {
	events := &orderedAcquisitionEvents{}
	sender := &orderedAcquisitionSender{events: events}
	adaptor := newTestAdaptor(t)
	adaptor.sender = sender
	router := newTestRouter(nil, adaptor, nil)
	router.acquisition = sender
	ledger, _ := newWideWorkLedger(t)
	router.fetchTracker.Track(ledger)

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind:  acquisitionWorkTimer,
		peers: []uint64{1},
		fetch: func([32]byte) ([]byte, bool) {
			events.add("local fetch")
			return nil, false
		},
	}})
	require.NoError(t, result.err)
	require.NotEmpty(t, result.stateIDs)

	router.handleAcquisitionWorkResult(result)
	got := events.snapshot()
	require.NotEmpty(t, got)
	assert.Equal(t, "state request", got[0])
	assert.Contains(t, got, "local fetch")
}

func TestProcessAcquisitionWork_BaseSplitsBlindFrontierAcrossSeededPeers(t *testing.T) {
	peers := []uint64{101, 102, 103, 104, 105}
	ledger, base := newWantBaseWorkLedger(t, newWideWorkSource(t, 16), peers)

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind:   acquisitionWorkData,
		peerID: peers[0],
		data: &message.LedgerData{
			InfoType: message.LedgerInfoBase,
			Nodes:    base,
		},
	}})

	require.NoError(t, result.err)
	require.Len(t, result.requests, len(peers))
	seen := make(map[string]uint64, 16)
	for i, request := range result.requests {
		assert.Equal(t, peers[i], request.PeerID)
		assert.True(t, request.Blind)
		assert.NotEmpty(t, request.NodeIDs)
		for _, nodeID := range request.NodeIDs {
			key := string(nodeID)
			if owner, duplicate := seen[key]; duplicate {
				t.Fatalf("node assigned to peers %d and %d", owner, request.PeerID)
			}
			seen[key] = request.PeerID
		}
	}
	assert.Len(t, seen, 16)
}

func TestProcessAcquisitionWork_RefillsReleasedPeerSlots(t *testing.T) {
	ledger, _ := newWideWorkLedger(t)
	peers := []uint64{1, 101, 102, 103, 104}
	for _, peerID := range peers[1:] {
		require.True(t, ledger.AddPeer(peerID))
	}
	initial, _, err := ledger.CollectMissingAddedRequestsContext(t.Context(), peers)
	require.NoError(t, err)
	require.Len(t, initial, len(peers))

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{
		{
			kind: acquisitionWorkData, peerID: peers[0], resume: true, useful: 10,
			data: &message.LedgerData{InfoType: message.LedgerInfoAsNode},
		},
		{
			kind: acquisitionWorkData, peerID: peers[1], resume: true,
			data: &message.LedgerData{InfoType: message.LedgerInfoAsNode},
		},
	})
	require.NoError(t, result.err)
	require.Len(t, result.requests, 2)
	assert.ElementsMatch(t, peers[:2], []uint64{result.requests[0].PeerID, result.requests[1].PeerID})
	assert.Equal(t, len(peers), ledger.Snapshot().RequestPeers)

	seen := make(map[string]uint64)
	for _, request := range result.requests {
		for _, nodeID := range request.NodeIDs {
			key := string(nodeID)
			if owner, duplicate := seen[key]; duplicate {
				t.Fatalf("node assigned to peers %d and %d", owner, request.PeerID)
			}
			seen[key] = request.PeerID
		}
	}
}

func TestProcessAcquisitionWork_ProgressTimerRefreshesPeerWindow(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	peers := []uint64{1, 101, 102, 103, 104}
	for _, peerID := range peers[1:] {
		require.True(t, ledger.AddPeer(peerID))
	}
	initial, _, err := ledger.CollectMissingAddedRequestsContext(t.Context(), peers)
	require.NoError(t, err)
	require.Len(t, initial, len(peers))

	progress := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkData, peerID: peers[0],
		data: &message.LedgerData{InfoType: message.LedgerInfoAsNode, Nodes: replies[:1]},
	}})
	require.NoError(t, progress.err)
	require.NotEmpty(t, progress.requests)

	now := time.Now()
	ledger.RearmTimer(now.Add(-time.Hour))
	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkTimerCheck,
		at:   now,
	}})
	require.NoError(t, result.err)
	require.Len(t, result.requests, len(peers))
	for _, request := range result.requests {
		assert.True(t, request.Blind)
		assert.LessOrEqual(t, len(request.NodeIDs), 12)
	}
}

func TestHandlePeerConnect_EmitsManifestBeforeAcquisitionTraversalCompletes(t *testing.T) {
	events := &orderedAcquisitionEvents{}
	manifestSender := &orderedAcquisitionManifestSender{
		fakeManifestSender: &fakeManifestSender{},
		events:             events,
	}
	router, _, _ := routerWithCache(t, manifestSender, 0x78, 10)
	engine := &blockingProposalEngine{
		mockEngine: &mockEngine{},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router.engine = engine
	router.inbox = inbox
	acquisitionSender := &orderedAcquisitionSender{events: events}
	router.acquisition = acquisitionSender
	ledger, _ := newWideWorkLedger(t)
	router.fetchTracker.Track(ledger)

	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, work []acquisitionWorkEvent) acquisitionWorkResult {
		close(entered)
		select {
		case <-ctx.Done():
			return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
		case <-release:
			return processAcquisitionWork(ctx, ledger, work)
		}
	}
	router.acquisitionWork = lane
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.Run(ctx)
	}()

	proposal := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Now()),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	proposal.NodePubKey[0] = 0x02
	inbox <- &peermanagement.InboundMessage{
		PeerID:  8,
		Type:    message.TypeProposeLedger,
		Payload: encodePayload(t, proposal),
	}
	select {
	case <-engine.entered:
	case <-time.After(time.Second):
		t.Fatal("proposal did not block Router.Run")
	}

	router.HandlePeerConnect(22)
	processedOffRouter := false
	select {
	case <-entered:
		processedOffRouter = true
	case <-time.After(50 * time.Millisecond):
	}
	assert.False(t, processedOffRouter, "peer connect was processed outside Router.Run")
	close(engine.release)
	if !processedOffRouter {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("queued peer connect was not admitted by Router.Run")
		}
	}
	require.Eventually(t, func() bool {
		got := events.snapshot()
		return len(got) == 1 && got[0] == "manifest"
	}, time.Second, time.Millisecond)
	close(release)

	require.Eventually(t, func() bool {
		got := events.snapshot()
		return len(got) == 2 && got[0] == "manifest" && got[1] == "state request"
	}, time.Second, time.Millisecond)
	assert.Equal(t, []string{"manifest", "state request"}, events.snapshot())
	assert.Contains(t, ledger.Peers(), uint64(22))
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Router.Run did not stop")
	}
}

func TestHandlePeerConnect_RollsBackAdmissionWhenWorkSubmissionFails(t *testing.T) {
	manifestSender := &fakeManifestSender{}
	router, _, _ := routerWithCache(t, manifestSender, 0x79, 11)
	recorder := &acqRecordingSender{}
	router.acquisition = recorder
	ledger, _ := newWideWorkLedger(t)
	router.fetchTracker.Track(ledger)
	router.acquisitionWork = newAcquisitionWorkLane(1)

	router.handlePeerConnect(22)

	assert.NotContains(t, ledger.Peers(), uint64(22))
	assert.Empty(t, recorder.queryDepths())
	require.Len(t, manifestSender.sends, 1)
	assert.Equal(t, peermanagement.PeerID(22), manifestSender.sends[0].peerID)
}

func TestHandlePeerConnect_AdmitsPeerToActiveAcquisition(t *testing.T) {
	recorder := &acqRecordingSender{}
	adaptor := newTestAdaptor(t)
	adaptor.sender = recorder
	router := newTestRouter(nil, adaptor, nil)
	ledger, _ := newWideWorkLedger(t)
	router.fetchTracker.Track(ledger)

	router.handlePeerConnect(22)

	assert.Contains(t, ledger.Peers(), uint64(22))
	assert.Equal(t, []uint32{0}, recorder.queryDepths())
}

func newWideWorkLedger(t *testing.T) (*inbound.Ledger, []message.LedgerNode) {
	t.Helper()
	source := newWideWorkSource(t, 4)
	ledger, base := newWantBaseWorkLedger(t, source, []uint64{1})
	require.NoError(t, ledger.GotBase(base))

	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	ancestors := make([]message.LedgerNode, 0, 68)
	replies := make([]message.LedgerNode, 0, acquisitionMaxUsefulPeers)
	for _, node := range wire {
		depth := node.NodeID[32]
		ledgerNode := message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data}
		if depth == 1 || depth == 2 {
			ancestors = append(ancestors, ledgerNode)
		} else if depth == 3 && len(replies) < acquisitionMaxUsefulPeers {
			replies = append(replies, ledgerNode)
		}
	}
	added, err := ledger.GotStateNodesUseful(ancestors)
	require.NoError(t, err)
	require.Equal(t, len(ancestors), added)
	require.Len(t, replies, acquisitionMaxUsefulPeers)
	return ledger, replies
}

func newWideWorkSource(t *testing.T, firstBranches byte) *shamap.SHAMap {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for first := byte(0); first < firstBranches; first++ {
		for second := byte(0); second < 16; second++ {
			for third := byte(0); third < 16; third++ {
				for fourth := byte(0); fourth < 2; fourth++ {
					var key [32]byte
					key[0] = first<<4 | second
					key[1] = third<<4 | fourth
					data := make([]byte, 12)
					copy(data, []byte{first, second, third, fourth})
					require.NoError(t, source.Put(key, data))
				}
			}
		}
	}
	return source
}

func newWantBaseWorkLedger(t *testing.T, source *shamap.SHAMap, peers []uint64) (*inbound.Ledger, []message.LedgerNode) {
	t.Helper()
	require.NotEmpty(t, peers)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 200, AccountHash: rootHash}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, h.LedgerIndex, peers[0], serveTestLogger())
	for _, peerID := range peers[1:] {
		require.True(t, ledger.AddPeer(peerID))
	}
	return ledger, []message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}
}

func TestApplyAcquisitionData_DuplicateBaseIsNotUseful(t *testing.T) {
	service := newTestLedgerService(t)
	closed := service.GetClosedLedger()
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	ledger := inbound.New(closed.Hash(), closed.Sequence(), 1, serveTestLogger())
	data := &message.LedgerData{InfoType: message.LedgerInfoBase, Nodes: router.buildLedgerBaseNodes(closed)}
	first, _, _, _, err := applyAcquisitionData(t.Context(), ledger, data)
	require.NoError(t, err)
	assert.Equal(t, 2, first)
	duplicate, _, _, _, err := applyAcquisitionData(t.Context(), ledger, data)
	require.NoError(t, err)
	assert.Zero(t, duplicate)
}

func TestApplyAcquisitionData_BaseUsefulBytesExcludeIgnoredNodes(t *testing.T) {
	service := newTestLedgerService(t)
	closed := service.GetClosedLedger()
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	ledger := inbound.New(closed.Hash(), closed.Sequence(), 1, serveTestLogger())
	nodes := router.buildLedgerBaseNodes(closed)
	usefulBytes := 0
	for i := range nodes {
		usefulBytes += len(nodes[i].NodeData)
	}
	nodes = append(nodes, message.LedgerNode{NodeData: make([]byte, 257)})

	stats, _, _, _, err := applyAcquisitionDataMeasured(t.Context(), ledger, &message.LedgerData{
		InfoType: message.LedgerInfoBase,
		Nodes:    nodes,
	})
	require.NoError(t, err)
	assert.Equal(t, usefulBytes, stats.UsefulBytes)
	assert.Equal(t, usefulBytes+257, stats.ReceivedBytes)
}

func TestProcessAcquisitionWork_BaseReplyCannotPoisonSharedAcquisition(t *testing.T) {
	service := newTestLedgerService(t)
	closed := service.GetClosedLedger()
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	valid := router.buildLedgerBaseNodes(closed)
	require.NotEmpty(t, valid)

	tests := []struct {
		name       string
		firstNodes []message.LedgerNode
		badData    bool
	}{
		{name: "malformed", firstNodes: []message.LedgerNode{{NodeData: []byte{0x01}}}, badData: true},
		{name: "header only", firstNodes: valid[:1]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := inbound.New(closed.Hash(), closed.Sequence(), 11, serveTestLogger())
			first := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
				kind:   acquisitionWorkData,
				peerID: 11,
				data: &message.LedgerData{
					InfoType: message.LedgerInfoBase,
					Nodes:    tt.firstNodes,
				},
			}})

			require.NoError(t, first.err)
			assert.False(t, first.remove)
			assert.Equal(t, inbound.StateWantBase, ledger.State())
			if tt.badData {
				assert.Equal(t, []acquisitionBadData{{peerID: 11, kind: "ledger-data-base"}}, first.badData)
			} else {
				assert.Empty(t, first.badData)
			}

			require.True(t, ledger.AddPeer(22))
			second := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
				kind:   acquisitionWorkData,
				peerID: 22,
				data: &message.LedgerData{
					InfoType: message.LedgerInfoBase,
					Nodes:    valid,
				},
			}})
			require.NoError(t, second.err)
			assert.False(t, second.remove)
			assert.Empty(t, second.badData)
			assert.NotEqual(t, inbound.StateWantBase, ledger.State())
		})
	}
}

func TestAcquisitionWorkResultPromotesResolvedHashOnlyConsensusLedger(t *testing.T) {
	router, _, svc := makeProvisionalWarmRouter(t)
	closed := svc.GetClosedLedgerIndex()
	rootHash, rootData, _ := buildSelfHealSourceState(t)
	pivotHeader := header.LedgerHeader{
		LedgerIndex: closed + maxForwardDeltaGap + 1,
		ParentHash:  [32]byte{0x91},
		AccountHash: rootHash,
		CloseTime:   time.Unix(1_700_000_200, 0),
	}
	pivotHeader.Hash = header.CalculateHash(pivotHeader)

	require.NoError(t, router.requestConsensusLedger(consensus.LedgerID(pivotHeader.Hash)))
	pivotAcquisition := router.fetchTracker.Find(pivotHeader.Hash)
	require.NotNil(t, pivotAcquisition)
	require.True(t, pivotAcquisition.SequenceInitiallyUnknown())

	lane := newAcquisitionWorkLane(1)
	lane.start(t.Context())
	t.Cleanup(lane.stop)
	require.True(t, lane.submit(pivotAcquisition, acquisitionWorkEvent{
		kind:   acquisitionWorkData,
		peerID: 7,
		data: &message.LedgerData{
			LedgerHash: pivotHeader.Hash[:],
			InfoType:   message.LedgerInfoBase,
			Nodes: []message.LedgerNode{
				{NodeData: header.AddRaw(pivotHeader, false)},
				{NodeData: rootData},
			},
		},
	}))

	result := <-lane.results()
	require.Equal(t, pivotHeader.LedgerIndex, pivotAcquisition.Seq())
	require.False(t, router.standardReplay.active)
	router.handleAcquisitionWorkResult(result)

	require.True(t, router.standardReplay.active)
	require.Equal(t, pivotHeader.LedgerIndex, router.standardReplay.pivotSeq)
	require.Equal(t, pivotHeader.Hash, router.standardReplay.pivotHash)
	require.Same(t, pivotAcquisition, router.fetchTracker.Find(pivotHeader.Hash))
}

func TestProcessAcquisitionWork_DuplicatePartialBaseDoesNotRetry(t *testing.T) {
	service := newTestLedgerService(t)
	closed := service.GetClosedLedger()
	headerOnly := &message.LedgerData{
		InfoType: message.LedgerInfoBase,
		Nodes:    []message.LedgerNode{{NodeData: header.AddRaw(closed.Header(), false)}},
	}
	ledger := inbound.New(closed.Hash(), closed.Sequence(), 1, serveTestLogger())

	first := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkData, peerID: 1, data: headerOnly,
	}})
	require.NoError(t, first.err)
	assert.True(t, first.retryBase)
	assert.Equal(t, inbound.StateWantBase, ledger.State())

	duplicate := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkData, peerID: 1, data: headerOnly,
	}})
	require.NoError(t, duplicate.err)
	assert.False(t, duplicate.retryBase)
	assert.Empty(t, duplicate.requests)
}

func TestLedgerQueryDepthPresence(t *testing.T) {
	recorder := &acqRecordingSender{}
	router := newTestRouter(nil, New(Config{Sender: &fixedLatencySender{
		acqRecordingSender: recorder,
		latencies:          map[uint64]time.Duration{2: 300 * time.Millisecond},
	}}), nil)
	assert.Equal(t, 1, router.ledgerQueryDepth(1, &message.GetLedger{}))
	assert.Equal(t, 2, router.ledgerQueryDepth(2, &message.GetLedger{}))
	assert.Equal(t, 0, router.ledgerQueryDepth(2, &message.GetLedger{QueryDepthSet: true}))
	assert.Equal(t, 1, router.ledgerQueryDepth(2, &message.GetLedger{QueryDepth: 1}))
}

func TestSendMissingReplyRequest_UsesPeerLatencyDepth(t *testing.T) {
	recorder := &acqRecordingSender{}
	sender := &fixedLatencySender{
		acqRecordingSender: recorder,
		latencies:          map[uint64]time.Duration{1: 299 * time.Millisecond, 2: 300 * time.Millisecond},
	}
	router := newTestRouter(nil, New(Config{Sender: sender}), nil)
	ledger := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	router.sendMissingReplyRequest(ledger, inbound.MissingRequest{PeerID: 1, NodeIDs: [][]byte{make([]byte, 33)}})
	router.sendMissingReplyRequest(ledger, inbound.MissingRequest{PeerID: 2, NodeIDs: [][]byte{make([]byte, 33)}})
	assert.Equal(t, []uint32{1, 2}, recorder.queryDepths())
}

func TestAcquisitionWork_PersistenceFailureDoesNotAdoptOrRecordPeerFailure(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	ledger := inbound.New([32]byte{0xB3}, 51, 7, serveTestLogger())
	router.fetchTracker.Track(ledger)

	router.handleAcquisitionWorkResult(acquisitionWorkResult{
		ledger:         ledger,
		complete:       true,
		persistenceErr: errors.New("store failed"),
	})

	assert.Nil(t, router.fetchTracker.Find(ledger.Hash()))
	assert.Empty(t, router.fetchTracker.Info(),
		"a local store failure must not enter the network-failure cooldown")
}

func TestAcquisitionWorkLane_BoundsAndCoalesces(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(1, 1)
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	calls := make(map[*inbound.Ledger]int)
	var order []*inbound.Ledger
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		mu.Lock()
		calls[ledger]++
		order = append(order, ledger)
		mu.Unlock()
		once.Do(func() {
			close(firstEntered)
			select {
			case <-ctx.Done():
			case <-release:
			}
		})
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lane.start(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case result := <-lane.results():
				close(result.ack)
			}
		}
	}()

	a := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	b := inbound.New([32]byte{2}, 2, 2, serveTestLogger())
	c := inbound.New([32]byte{3}, 3, 3, serveTestLogger())
	require.True(t, lane.submit(a, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-firstEntered
	for range 1000 {
		require.True(t, lane.submit(a, acquisitionWorkEvent{kind: acquisitionWorkTimer}))
	}
	require.True(t, lane.submit(b, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	assert.False(t, lane.submit(c, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	lane.mu.Lock()
	ready := len(lane.ready)
	lane.mu.Unlock()
	assert.LessOrEqual(t, ready, 1)

	close(release)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls[a] == 2 && calls[b] == 1
	}, time.Second, time.Millisecond)
	cancel()
	lane.stop()
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, calls[c])
	assert.Equal(t, []*inbound.Ledger{a, b, a}, order)
}

func TestAcquisitionWorkLane_BackpressuresFullDataBatch(t *testing.T) {
	lane := newAcquisitionWorkLane(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lane.ctx = ctx

	ledger := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	batch := &acquisitionWorkBatch{ledger: ledger}
	for range acquisitionWorkBatchLimit {
		batch.events = append(batch.events, acquisitionWorkEvent{kind: acquisitionWorkData})
	}
	lane.pending[ledger] = batch

	assert.False(t, lane.canAcceptData(), "the router must stop draining before a decoded reply would be dropped")
	batch.events = batch.events[1:]
	assert.True(t, lane.canAcceptData())
}

func TestAcquisitionWorkLane_YieldRunsAnotherLedger(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(2, 1)
	slow := inbound.New([32]byte{0xA0}, 10, 1, serveTestLogger())
	fast := inbound.New([32]byte{0xB0}, 11, 2, serveTestLogger())
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	slowCalls := 0
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		if ledger == slow {
			slowCalls++
			if slowCalls == 1 {
				close(slowStarted)
				<-releaseSlow
				return acquisitionWorkResult{ledger: ledger, err: shamap.ErrTraversalBudget}
			}
		}
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()

	require.True(t, lane.submit(slow, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-slowStarted
	require.True(t, lane.submit(fast, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	close(releaseSlow)

	first := <-lane.results()
	require.Same(t, slow, first.ledger)
	require.True(t, first.yielded)
	close(first.ack)
	second := <-lane.results()
	close(second.ack)
	third := <-lane.results()
	close(third.ack)
	assert.ElementsMatch(t, []*inbound.Ledger{fast, slow}, []*inbound.Ledger{second.ledger, third.ledger},
		"a yielded walk must not starve another ledger")
	require.Equal(t, 2, slowCalls)
}

func TestAcquisitionWorkLane_ProcessesDifferentLedgersConcurrently(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(2, 2)
	started := make(chan *inbound.Ledger, 2)
	release := make(chan struct{}, 2)
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		started <- ledger
		<-release
		return acquisitionWorkResult{ledger: ledger}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)

	first := inbound.New([32]byte{0xC1}, 21, 1, serveTestLogger())
	second := inbound.New([32]byte{0xC2}, 22, 2, serveTestLogger())
	require.True(t, lane.submit(first, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	require.True(t, lane.submit(second, acquisitionWorkEvent{kind: acquisitionWorkLocal}))

	seen := map[*inbound.Ledger]bool{}
	for range 2 {
		select {
		case ledger := <-started:
			seen[ledger] = true
		case <-time.After(time.Second):
			t.Fatal("different-ledger acquisition work did not run concurrently")
		}
	}
	assert.True(t, seen[first])
	assert.True(t, seen[second])
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		result := <-lane.results()
		close(result.ack)
	}
}

func TestAcquisitionWorkLane_SerializesOneLedger(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(2, 2)
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		started <- struct{}{}
		<-release
		return acquisitionWorkResult{ledger: ledger}
	}
	lane.start(t.Context())
	t.Cleanup(lane.stop)

	ledger := inbound.New([32]byte{0xC3}, 23, 1, serveTestLogger())
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first acquisition batch did not start")
	}
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer}))
	select {
	case <-started:
		t.Fatal("same-ledger acquisition work ran concurrently")
	case <-time.After(50 * time.Millisecond):
	}

	release <- struct{}{}
	first := <-lane.results()
	close(first.ack)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queued same-ledger batch did not start after the first completed")
	}
	release <- struct{}{}
	second := <-lane.results()
	close(second.ack)
}

func TestAcquisitionWorkLane_YieldProcessesNewWorkBeforeResume(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(1, 1)
	ledger := inbound.New([32]byte{0xA3}, 13, 7, serveTestLogger())
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEvents := make(chan []acquisitionWorkEvent, 1)
	calls := 0
	lane.process = func(_ context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		calls++
		if calls == 1 {
			close(firstStarted)
			<-releaseFirst
			return acquisitionWorkResult{ledger: current, err: shamap.ErrTraversalBudget}
		}
		secondEvents <- append([]acquisitionWorkEvent(nil), events...)
		return acquisitionWorkResult{ledger: current}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()

	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-firstStarted
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkData,
		data: &message.LedgerData{InfoType: message.LedgerInfoAsNode},
	}))
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkTimerCheck,
		at:   time.Now(),
	}))
	close(releaseFirst)

	first := <-lane.results()
	require.True(t, first.yielded)
	close(first.ack)
	second := <-lane.results()
	require.False(t, second.yielded)
	close(second.ack)

	events := <-secondEvents
	require.Len(t, events, 3)
	assert.Equal(t, acquisitionWorkData, events[0].kind)
	assert.Equal(t, acquisitionWorkTimerCheck, events[1].kind)
	assert.Equal(t, acquisitionWorkLocal, events[2].kind)
}

func TestAcquisitionWork_ResumePreservesDataUsefulness(t *testing.T) {
	ledger := inbound.New([32]byte{0xA2}, 12, 7, serveTestLogger())
	base := &message.LedgerData{InfoType: message.LedgerInfoBase}

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkData, data: base, peerID: 1, resume: true,
	}})
	require.False(t, result.retryBase, "an originally useless reply must remain useless after a yield")

	result = processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkData, data: base, peerID: 1, resume: true, useful: 1,
	}})
	require.True(t, result.retryBase, "an originally useful partial base reply must retain its follow-up")
}

func TestProcessAcquisitionWork_UsesNewestTimerCheck(t *testing.T) {
	ledger := inbound.New([32]byte{0xA4}, 14, 7, serveTestLogger())
	clock := time.Now()
	ledger.RearmTimer(clock)
	newest := clock.Add(4 * time.Second)

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{
		{kind: acquisitionWorkTimerCheck, at: newest},
		{kind: acquisitionWorkTimerCheck, at: newest.Add(-time.Second)},
	})

	require.NoError(t, result.err)
	require.True(t, result.timerEscalate)
	assert.Equal(t, newest, result.timerAt)
}

func TestProcessAcquisitionWork_UsefulDataPreventsTimerFailure(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	timerAt := primeAcquisitionForTerminalTimer(t, ledger)

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{
		{
			kind:   acquisitionWorkData,
			peerID: 7,
			data: &message.LedgerData{
				InfoType: message.LedgerInfoAsNode,
				Nodes:    replies[:1],
			},
		},
		{kind: acquisitionWorkTimerCheck, at: timerAt},
	})

	require.NoError(t, result.err)
	assert.False(t, result.remove)
	assert.False(t, result.timerFailure)
	assert.False(t, result.timerEscalate)
	assert.Equal(t, 6, ledger.Timeouts())
}

func TestRouter_PendingAcquisitionStillReceivesTimerCheck(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	ledger := inbound.New([32]byte{0xA1}, 12, 7, serveTestLogger())
	clock := time.Now()
	ledger.RearmTimer(clock)
	for range 6 {
		clock = clock.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, ledger.OnTimer(clock))
		ledger.RearmTimer(clock)
	}
	timerAt := clock.Add(4 * time.Second)
	router.fetchTracker.Track(ledger)

	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		if first {
			first = false
			close(entered)
			<-release
		}
		return processAcquisitionWork(ctx, current, events)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()
	router.acquisitionWork = lane

	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered
	router.retryInboundLedgerAcquisitions(timerAt)
	close(release)

	result := <-lane.results()
	router.handleAcquisitionWorkResult(result)
	result = <-lane.results()
	require.True(t, result.timerFailure)
	router.handleAcquisitionWorkResult(result)
	require.Equal(t, 7, ledger.Timeouts())
	require.Nil(t, router.fetchTracker.Find(ledger.Hash()))
}

func TestAcquisitionWork_FailureControlPreemptsBatchedData(t *testing.T) {
	ledger := inbound.New([32]byte{0xF1}, 42, 7, serveTestLogger())
	require.Error(t, ledger.GotBase([]message.LedgerNode{{NodeData: []byte{1}}}))

	result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{
		{kind: acquisitionWorkFailure},
		{kind: acquisitionWorkData, peerID: 8, data: &message.LedgerData{
			InfoType: message.LedgerInfoAsNode,
			Nodes:    []message.LedgerNode{{NodeID: make([]byte, 33), NodeData: []byte{1}}},
		}},
	})

	require.NoError(t, result.err)
	assert.True(t, result.remove)
	assert.True(t, result.timerFailure)
	assert.Empty(t, result.badData)
}

func TestAcquisitionWork_FailureSnapshotDoesNotScanNodeStore(t *testing.T) {
	family := newAcquisitionStoreTestFamily()
	rootHash, rootData, _ := buildSelfHealSourceState(t)
	headerData := header.AddRaw(header.LedgerHeader{LedgerIndex: 42, AccountHash: rootHash}, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, 42, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))

	family.mu.Lock()
	family.fetchCalls = 0
	family.cacheCalls = 0
	family.mu.Unlock()

	result := processAcquisitionWorkBudgeted(t.Context(), ledger, []acquisitionWorkEvent{{
		kind: acquisitionWorkFailure,
	}})
	require.NoError(t, result.err)
	require.True(t, result.remove)
	require.True(t, result.timerFailure)
	require.True(t, result.haveSnapshot)

	family.mu.Lock()
	defer family.mu.Unlock()
	require.Zero(t, family.fetchCalls)
	require.Zero(t, family.cacheCalls)
}

func TestAcquisitionWorkLane_PersistenceFailureStopsYieldedBatch(t *testing.T) {
	wantErr := errors.New("checkpoint failed")
	family := failingAcquisitionCheckpointFamily{
		Family: backend.NewMemory(),
		err:    wantErr,
	}
	ledger := inbound.New([32]byte{0xF3}, 44, 7, serveTestLogger(), inbound.WithFamily(family))
	lane := newAcquisitionWorkLane(1)
	var mu sync.Mutex
	calls := 0
	lane.process = func(_ context.Context, current *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		mu.Lock()
		calls++
		mu.Unlock()
		return acquisitionWorkResult{
			ledger:  current,
			err:     shamap.ErrTraversalBudget,
			replies: []acquisitionReplyStat{{useful: 1 << 20}},
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()

	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	result := <-lane.results()
	require.True(t, result.yielded)
	require.True(t, result.remove)
	require.ErrorIs(t, result.persistenceErr, wantErr)
	close(result.ack)

	require.Eventually(t, func() bool {
		return !lane.has(ledger)
	}, time.Second, time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls)
}

func TestAcquisitionWork_CompletionFlushesBeforeResult(t *testing.T) {
	lane := newAcquisitionWorkLane(1)
	flushEntered := make(chan struct{})
	releaseFlush := make(chan struct{})
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger, complete: true}
	}
	ledger := inbound.New([32]byte{0xF2}, 43, 7, serveTestLogger())
	lane.flush = func(ctx context.Context, got *inbound.Ledger) error {
		require.Same(t, ledger, got)
		close(flushEntered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseFlush:
			return nil
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-flushEntered
	select {
	case <-lane.results():
		t.Fatal("completion reached Router before persistence barrier")
	default:
	}
	close(releaseFlush)
	result := <-lane.results()
	close(result.ack)
	cancel()
	lane.stop()
}

func TestAcquisitionWorkLane_ShutdownCancelsAndJoins(t *testing.T) {
	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	exited := make(chan struct{})
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		close(entered)
		<-ctx.Done()
		close(exited)
		return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	a := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	require.True(t, lane.submit(a, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered
	cancel()
	lane.stop()
	select {
	case <-exited:
	default:
		t.Fatal("active traversal did not observe cancellation before stop returned")
	}
}

func TestAcquisitionWorkLane_CancelLedgerPreemptsObsoleteTraversal(t *testing.T) {
	lane := newAcquisitionWorkLaneWithWorkers(1, 1)
	staleStarted := make(chan struct{})
	exactStarted := make(chan struct{})
	stale := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	exact := inbound.New([32]byte{2}, 2, 2, serveTestLogger())
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		if ledger == stale {
			close(staleStarted)
			<-ctx.Done()
			return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
		}
		close(exactStarted)
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lane.start(ctx)

	require.True(t, lane.submit(stale, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-staleStarted
	require.True(t, lane.submit(exact, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	require.True(t, lane.cancelLedger(stale))

	staleResult := <-lane.results()
	assert.Same(t, stale, staleResult.ledger)
	assert.ErrorIs(t, staleResult.err, context.Canceled)
	close(staleResult.ack)
	exactResult := <-lane.results()
	assert.Same(t, exact, exactResult.ledger)
	close(exactResult.ack)
	select {
	case <-exactStarted:
	case <-time.After(time.Second):
		t.Fatal("exact acquisition remained queued behind canceled traversal")
	}

	lane.stop()
}

func TestRouterClearFetchInfoCancelsActiveAcquisitionWork(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	router.acquisitionWork = lane

	started := make(chan struct{})
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		close(started)
		<-ctx.Done()
		return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
	}
	lane.start(t.Context())
	defer lane.stop()

	ledger := inbound.New([32]byte{0x91}, 42, 7, serveTestLogger())
	router.fetchTracker.Track(ledger)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-started

	router.ClearFetchInfo()
	result := <-lane.results()
	assert.Same(t, ledger, result.ledger)
	assert.ErrorIs(t, result.err, context.Canceled)
	close(result.ack)
	require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
}

func TestAcquisitionWork_PendingReservesTimerEvent(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-ctx.Done():
			return acquisitionWorkResult{ledger: ledger, err: ctx.Err()}
		case <-release:
			return acquisitionWorkResult{ledger: ledger}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lane.start(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case result := <-lane.results():
				close(result.ack)
			}
		}
	}()
	router.acquisitionWork = lane
	running := inbound.New([32]byte{8}, 8, 8, serveTestLogger())
	queued := inbound.New([32]byte{9}, 9, 9, serveTestLogger())
	base := time.Now()
	for i := 1; i <= 6; i++ {
		require.Equal(t, inbound.TimerEscalate, queued.OnTimer(base.Add(time.Duration(i)*4*time.Second)))
	}
	router.fetchTracker.Track(queued)
	require.True(t, lane.submit(running, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered
	require.True(t, lane.submit(queued, acquisitionWorkEvent{kind: acquisitionWorkData}))

	now := base.Add(time.Hour)
	before := queued.Timeouts()
	router.retryInboundLedgerAcquisitions(now)
	assert.Equal(t, before, queued.Timeouts())
	assert.True(t, lane.has(queued))
	assert.NotEqual(t, inbound.StateFailed, queued.State(),
		"queued data must not be overtaken by a terminal timer event")

	close(release)
	require.Eventually(t, func() bool { return !lane.has(queued) }, time.Second, time.Millisecond)
	cancel()
	lane.stop()
}

func TestAcquisitionWork_UsefulLargeReplyPrecedesTerminalTimerCheck(t *testing.T) {
	source := newWideWorkSource(t, 16)
	ledger, baseNodes := newWantBaseWorkLedger(t, source, []uint64{7})
	require.NoError(t, ledger.GotBase(baseNodes))
	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	require.Greater(t, len(wire), 8_301)
	nodes := make([]message.LedgerNode, 0, 8_300)
	for _, node := range wire[1:8_301] {
		nodes = append(nodes, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}

	timerAt := primeAcquisitionForTerminalTimer(t, ledger)

	router := newTestRouter(nil, newTestAdaptor(t), nil)
	router.fetchTracker.Track(ledger)
	lane := newAcquisitionWorkLane(1)
	blocker := inbound.New([32]byte{0xB7}, 201, 8, serveTestLogger())
	blockerEntered := make(chan struct{})
	releaseBlocker := make(chan struct{})
	workEntered := make(chan struct{})
	releaseWork := make(chan struct{})
	lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		switch current {
		case blocker:
			close(blockerEntered)
			select {
			case <-ctx.Done():
				return acquisitionWorkResult{ledger: current, err: ctx.Err()}
			case <-releaseBlocker:
			}
		case ledger:
			close(workEntered)
			select {
			case <-ctx.Done():
				return acquisitionWorkResult{ledger: current, err: ctx.Err()}
			case <-releaseWork:
			}
		}
		return processAcquisitionWork(ctx, current, events)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	router.acquisitionWork = lane

	require.True(t, lane.submit(blocker, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-blockerEntered
	router.retryInboundLedgerAcquisitions(timerAt)
	require.True(t, lane.has(ledger))
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind:   acquisitionWorkData,
		peerID: 7,
		data: &message.LedgerData{
			InfoType: message.LedgerInfoAsNode,
			Nodes:    nodes,
		},
	}))

	close(releaseBlocker)
	blockerResult := <-lane.results()
	close(blockerResult.ack)
	<-workEntered
	assert.Equal(t, 6, ledger.Timeouts(), "pending data/persistence work must suppress timer evaluation")

	close(releaseWork)
	result := <-lane.results()
	assert.False(t, result.remove)
	assert.False(t, result.timerEscalate)
	assert.False(t, result.complete)
	router.handleAcquisitionWorkResult(result)
	require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)

	assert.Same(t, ledger, router.fetchTracker.Find(ledger.Hash()))
	assert.NotEqual(t, inbound.StateFailed, ledger.State())
	assert.Equal(t, 6, ledger.Timeouts(), "rippled keeps prior timeouts cumulative but does not count a progressing interval")
	duplicate, err := ledger.GotStateNodesUseful(nodes)
	require.NoError(t, err)
	assert.Zero(t, duplicate, "the full 8,300-node reply must have been processed before the timer check")

	cancel()
	lane.stop()
}

func TestAcquisitionWork_TerminalTimerPreemptsRetainedTraversal(t *testing.T) {
	ledger, _ := newWideWorkLedger(t)
	timerAt := primeAcquisitionForTerminalTimer(t, ledger)

	result := processAcquisitionWorkBudgeted(t.Context(), ledger, []acquisitionWorkEvent{
		{kind: acquisitionWorkTimer, fetch: func([32]byte) ([]byte, bool) { return nil, false }},
		{kind: acquisitionWorkTimerCheck, at: timerAt},
	})

	require.NoError(t, result.err)
	require.True(t, result.remove)
	require.True(t, result.timerFailure)
	require.Equal(t, 7, ledger.Timeouts())
}

func TestAcquisitionWork_YieldedLocalTraversalRearmsTimer(t *testing.T) {
	source := newWideWorkSource(t, 16)
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 204, AccountHash: rootHash}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	family := backend.NewMemory()
	packNodes, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	entries := make([]shamap.FlushEntry, 0, len(packNodes))
	for _, node := range packNodes {
		entries = append(entries, shamap.FlushEntry{Hash: node.Hash, Data: node.Data})
	}
	ledger := inbound.New(ledgerHash, h.LedgerIndex, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	require.NoError(t, family.StoreBatch(t.Context(), entries))
	base := time.Now()
	ledger.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(base.Add(4*time.Second)))
	timerAt := base.Add(8 * time.Second)

	lane := newAcquisitionWorkLane(1)
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		return processAcquisitionWorkWithBudget(ctx, ledger, events, 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	defer func() {
		cancel()
		lane.stop()
	}()
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind:  acquisitionWorkLocal,
		fetch: func([32]byte) ([]byte, bool) { return nil, false },
	}))

	first := <-lane.results()
	require.True(t, first.yielded, "result: %+v", first)
	require.True(t, first.rearmTimer)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(timerAt))
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkTimerCheck,
		at:   timerAt.Add(4 * time.Second),
	}))
	first.ledger.RearmTimer(timerAt.Add(4 * time.Second))
	close(first.ack)

	second := <-lane.results()
	require.True(t, second.yielded)
	require.True(t, second.rearmTimer)
	require.False(t, second.timerEscalate)
	require.Equal(t, 0, ledger.Timeouts())
	close(second.ack)
}

func TestAcquisitionWork_EscalationDoesNotRearmPreemptedTraversal(t *testing.T) {
	ledger := inbound.New([32]byte{0xc1}, 204, 7, serveTestLogger())
	base := time.Now()
	ledger.RearmTimer(base)

	lane := newAcquisitionWorkLane(1)
	lane.ctx = t.Context()
	batchCtx, cancel := context.WithCancel(t.Context())
	batch := &acquisitionWorkBatch{
		ledger: ledger,
		ctx:    batchCtx,
		cancel: cancel,
		events: []acquisitionWorkEvent{
			{kind: acquisitionWorkTimer, fetch: func([32]byte) ([]byte, bool) { return nil, false }},
			{kind: acquisitionWorkTimerCheck, at: base.Add(4 * time.Second)},
		},
	}
	lane.pending[ledger] = batch
	done := make(chan bool, 1)
	go func() { done <- lane.runBatch(batch) }()

	result := <-lane.results()
	require.True(t, result.timerEscalate)
	require.False(t, result.rearmTimer)
	close(result.ack)
	require.True(t, <-done)
}

func TestAcquisitionWork_YieldedWantStateDefersEscalation(t *testing.T) {
	source := newWideWorkSource(t, 16)
	require.NoError(t, source.Delete([32]byte{}))
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	h := header.LedgerHeader{LedgerIndex: 205, AccountHash: rootHash}
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	family := backend.NewMemory()
	ledger := inbound.New(ledgerHash, h.LedgerIndex, 7, serveTestLogger(), inbound.WithFamily(family))
	require.NoError(t, ledger.GotBase([]message.LedgerNode{{NodeData: headerData}, {NodeData: rootData}}))
	base := time.Now().Add(-time.Hour)
	ledger.RearmTimer(base)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(base.Add(4*time.Second)))
	timerAt := base.Add(8 * time.Second)

	router := newTestRouter(nil, newTestAdaptor(t), nil)
	router.fetchTracker.Track(ledger)
	lane := newAcquisitionWorkLane(1)
	lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		return processAcquisitionWorkWithBudget(ctx, current, events, 1)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	router.acquisitionWork = lane
	defer func() {
		cancel()
		lane.stop()
	}()

	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind:  acquisitionWorkTimer,
		fetch: func([32]byte) ([]byte, bool) { return nil, false },
	}))
	first := <-lane.results()
	require.True(t, first.yielded)
	require.True(t, first.rearmTimer)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{
		kind: acquisitionWorkTimerCheck,
		at:   timerAt,
	}))
	router.handleAcquisitionWorkResult(first)

	second := <-lane.results()
	require.True(t, second.yielded)
	require.True(t, second.rearmTimer)
	require.False(t, second.timerEscalate)
	require.Zero(t, ledger.Timeouts())
	router.handleAcquisitionWorkResult(second)
	assert.False(t, ledger.TimerDue(time.Now()), "timer was not rearmed after the traversal slice")
}

func TestRouter_MaintenanceDrainsBufferedReplyBeforeTerminalTimer(t *testing.T) {
	ledger, replies := newWideWorkLedger(t)
	timerAt := primeAcquisitionForTerminalTimer(t, ledger)
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	router.fetchTracker.Track(ledger)
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router.SetAcqInbox(inbox)
	lane := newAcquisitionWorkLaneWithWorkers(1, 1)
	blocker := inbound.New([32]byte{0xB8}, 202, 8, serveTestLogger())
	entered := make(chan struct{})
	release := make(chan struct{})
	lane.process = func(ctx context.Context, current *inbound.Ledger, events []acquisitionWorkEvent) acquisitionWorkResult {
		if current == blocker {
			close(entered)
			select {
			case <-ctx.Done():
				return acquisitionWorkResult{ledger: current, err: ctx.Err()}
			case <-release:
			}
		}
		return processAcquisitionWork(ctx, current, events)
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	router.acquisitionWork = lane
	require.True(t, lane.submit(blocker, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered

	ledgerHash := ledger.Hash()
	inbox <- &peermanagement.InboundMessage{
		PeerID: 7,
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: ledgerHash[:],
			InfoType:   message.LedgerInfoAsNode,
			Nodes:      replies[:1],
		}),
	}
	require.Equal(t, 1, router.drainAcquisitionInboxBeforeMaintenance(lane))
	require.True(t, lane.has(ledger))
	router.retryInboundLedgerAcquisitions(timerAt)
	assert.Equal(t, 6, ledger.Timeouts(), "a buffered useful reply must reserve the ledger before its terminal timer")

	close(release)
	blockerResult := <-lane.results()
	close(blockerResult.ack)
	result := <-lane.results()
	router.handleAcquisitionWorkResult(result)
	require.Eventually(t, func() bool { return !lane.has(ledger) }, time.Second, time.Millisecond)
	assert.NotEqual(t, inbound.StateFailed, ledger.State())
	assert.Same(t, ledger, router.fetchTracker.Find(ledger.Hash()))

	cancel()
	lane.stop()
}

func TestRouter_MaintenanceRunsUnderSustainedAcquisitionInput(t *testing.T) {
	recorder := &acqRecordingSender{}
	adaptor := newTestAdaptor(t)
	adaptor.sender = recorder
	router := newTestRouter(nil, adaptor, nil)
	ledger := inbound.New([32]byte{0xB9}, 203, 7, serveTestLogger())
	ledger.RearmTimer(time.Now().Add(-time.Hour))
	router.fetchTracker.Track(ledger)
	inbox := make(chan *peermanagement.InboundMessage, 64)
	router.SetAcqInbox(inbox)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.Run(ctx)
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case inbox <- &peermanagement.InboundMessage{Type: 0xffff}:
			}
		}
	}()

	require.Eventually(t, func() bool { return len(recorder.baseIndirects()) > 0 }, 2*time.Second, time.Millisecond,
		"bounded acquisition draining must return to maintenance under sustained input")
	assert.GreaterOrEqual(t, ledger.Timeouts(), 1)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Router.Run did not stop")
	}
}

func primeAcquisitionForTerminalTimer(t *testing.T, ledger *inbound.Ledger) time.Time {
	t.Helper()
	clock := time.Now()
	ledger.RearmTimer(clock)
	clock = clock.Add(4 * time.Second)
	require.Equal(t, inbound.TimerRefresh, ledger.OnTimer(clock))
	for range 6 {
		clock = clock.Add(4 * time.Second)
		require.Equal(t, inbound.TimerEscalate, ledger.OnTimer(clock))
		ledger.RearmTimer(clock)
	}
	require.Equal(t, 6, ledger.Timeouts())
	return clock.Add(4 * time.Second)
}

func TestAcquisitionWorkLane_RearmsAfterResultHandling(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)

	ledger := inbound.New([32]byte{0xA2}, 43, 7, serveTestLogger())
	ledger.RearmTimer(time.Now().Add(-time.Hour))
	router.fetchTracker.Track(ledger)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimer}))
	result := <-lane.results()
	router.handleAcquisitionWorkResult(result)
	assert.Equal(t, inbound.TimerNone, ledger.OnTimer(time.Now()),
		"result handling must start the next acquisition timeout interval")

	cancel()
	lane.stop()
}

func TestAcquisitionWorkLane_BaseTimerCheckRearmsAfterRequest(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	router.acquisitionWork = lane

	ledger := inbound.New([32]byte{0xA5}, 46, 7, serveTestLogger())
	ledger.RearmTimer(time.Now().Add(-time.Hour))
	router.fetchTracker.Track(ledger)
	now := time.Now()
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkTimerCheck, at: now}))
	result := <-lane.results()
	require.True(t, result.timerEscalate)
	router.handleAcquisitionWorkResult(result)
	assert.False(t, ledger.TimerDue(time.Now()), "base request must start a fresh timeout interval")

	cancel()
	lane.stop()
}

func TestAcquisitionWorkLane_UselessDataDoesNotRearmTimer(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)

	ledger := inbound.New([32]byte{0xA3}, 44, 7, serveTestLogger())
	old := time.Now().Add(-time.Hour)
	ledger.RearmTimer(old)
	router.fetchTracker.Track(ledger)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkData}))
	result := <-lane.results()
	router.handleAcquisitionWorkResult(result)
	assert.Equal(t, inbound.TimerEscalate, ledger.OnTimer(time.Now()),
		"an unusable peer reply must not postpone acquisition escalation")

	cancel()
	lane.stop()
}

func TestAcquisitionWorkLane_UselessLocalWorkDoesNotRearmTimer(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	lane.process = func(_ context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)

	ledger := inbound.New([32]byte{0xA4}, 45, 7, serveTestLogger())
	old := time.Now().Add(-time.Hour)
	ledger.RearmTimer(old)
	router.fetchTracker.Track(ledger)
	require.True(t, lane.submit(ledger, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	result := <-lane.results()
	router.handleAcquisitionWorkResult(result)
	assert.Equal(t, inbound.TimerEscalate, ledger.OnTimer(time.Now()),
		"an unproductive local scan must not postpone acquisition escalation")

	cancel()
	lane.stop()
}

func TestAcquisitionWork_SaturationDefersTimer(t *testing.T) {
	router := newTestRouter(nil, newTestAdaptor(t), nil)
	lane := newAcquisitionWorkLane(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	lane.process = func(ctx context.Context, ledger *inbound.Ledger, _ []acquisitionWorkEvent) acquisitionWorkResult {
		once.Do(func() {
			close(entered)
			select {
			case <-ctx.Done():
			case <-release:
			}
		})
		return acquisitionWorkResult{ledger: ledger}
	}
	ctx, cancel := context.WithCancel(t.Context())
	lane.start(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case result := <-lane.results():
				close(result.ack)
			}
		}
	}()
	router.acquisitionWork = lane

	running := inbound.New([32]byte{1}, 1, 1, serveTestLogger())
	queued := inbound.New([32]byte{2}, 2, 2, serveTestLogger())
	waiting := inbound.New([32]byte{3}, 3, 3, serveTestLogger())
	require.True(t, lane.submit(running, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	<-entered
	require.True(t, lane.submit(queued, acquisitionWorkEvent{kind: acquisitionWorkLocal}))
	require.False(t, lane.canAcceptNew())
	router.fetchTracker.Track(waiting)

	router.retryInboundLedgerAcquisitions(time.Now().Add(time.Hour))
	assert.Equal(t, 0, waiting.Timeouts())

	close(release)
	cancel()
	lane.stop()
}

var _ consensus.Engine = (*mockEngine)(nil)

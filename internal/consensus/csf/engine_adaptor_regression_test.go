package csf

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/rcl"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestPeerInjectionMutatesAcceptedLedgerOnly(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	injected := Tx{ID: 91}
	peer.InjectTx(0, injected)

	var proposed *TxSet
	sim.AddCollector(CollectorFunc(func(id PeerID, _ SimTime, event Event) {
		if id != peer.ID {
			return
		}
		if closed, ok := event.(CloseLedgerEvent); ok {
			proposed = closed.TxSet
		}
	}))

	if err := sim.Run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = sim.Stop() })

	if proposed == nil {
		t.Fatal("consensus did not close a transaction set")
	}
	if proposed.ContainsTx(injected) {
		t.Fatal("injected transaction entered the consensus transaction set")
	}
	if !peer.LastClosedLedger().Transactions().ContainsTx(injected) {
		t.Fatal("injected transaction is missing from the accepted ledger")
	}
}

func TestPeerAncestryAcquiresOnlyFromLocalNetwork(t *testing.T) {
	const delay = 5 * time.Millisecond
	sim := NewSim()
	group := sim.CreateGroup(2)
	source, target := group.Get(0), group.Get(1)
	group.Connect(group, delay)
	startIdlePeers(t, source, target)
	t.Cleanup(func() { _ = sim.Stop() })

	set := NewTxSetFrom([]Tx{{ID: 7}})
	ledger, err := source.BuildLedger(source.LastClosedLedger(), set, source.Now(), true, nil)
	if err != nil {
		t.Fatalf("BuildLedger: %v", err)
	}

	if _, ok := target.LedgerByID(ledger.ID()); ok {
		t.Fatal("peer resolved a ledger that was not in its local store")
	}
	if _, ok := target.acquiringLedgers[ledger.ID()]; !ok {
		t.Fatal("missing local ancestry did not start ledger acquisition")
	}

	sim.Scheduler.StepFor(2 * delay)
	acquired, ok := target.LedgerByID(ledger.ID())
	if !ok || acquired.ID() != ledger.ID() {
		t.Fatal("peer did not resolve ancestry after network acquisition")
	}
}

func TestPeerAcquisitionRetryWindowAndReconnect(t *testing.T) {
	const delay = 4 * time.Millisecond
	sim := NewSim()
	group := sim.CreateGroup(2)
	source, target := group.Get(0), group.Get(1)
	group.Connect(group, delay)
	startIdlePeers(t, source, target)
	t.Cleanup(func() { _ = sim.Stop() })

	ledgerID := consensus.LedgerID{0xA1}
	if err := target.RequestLedger(ledgerID); err != nil {
		t.Fatalf("RequestLedger: %v", err)
	}
	firstLedgerDeadline := target.acquiringLedgers[ledgerID]
	if want := sim.Now() + SimTime(2*delay); firstLedgerDeadline != want {
		t.Fatalf("ledger deadline = %v, want %v", firstLedgerDeadline, want)
	}
	if err := target.RequestLedger(ledgerID); err != nil {
		t.Fatalf("suppressed RequestLedger: %v", err)
	}
	if got := target.acquiringLedgers[ledgerID]; got != firstLedgerDeadline {
		t.Fatalf("suppressed ledger request changed deadline to %v", got)
	}

	txSetID := consensus.TxSetID{0xB2}
	if err := target.RequestTxSet(txSetID); err != nil {
		t.Fatalf("RequestTxSet: %v", err)
	}
	firstTxSetDeadline := target.acquiringTxSets[txSetID]
	if want := sim.Now() + SimTime(2*delay); firstTxSetDeadline != want {
		t.Fatalf("transaction-set deadline = %v, want %v", firstTxSetDeadline, want)
	}
	if err := target.RequestTxSet(txSetID); err != nil {
		t.Fatalf("suppressed RequestTxSet: %v", err)
	}
	if got := target.acquiringTxSets[txSetID]; got != firstTxSetDeadline {
		t.Fatalf("suppressed transaction-set request changed deadline to %v", got)
	}

	sim.Scheduler.StepFor(2 * delay)
	if err := target.RequestLedger(ledgerID); err != nil {
		t.Fatalf("retry RequestLedger: %v", err)
	}
	if got := target.acquiringLedgers[ledgerID]; got != sim.Now()+SimTime(2*delay) {
		t.Fatalf("ledger retry deadline = %v", got)
	}
	if err := target.RequestTxSet(txSetID); err != nil {
		t.Fatalf("retry RequestTxSet: %v", err)
	}
	if got := target.acquiringTxSets[txSetID]; got != sim.Now()+SimTime(2*delay) {
		t.Fatalf("transaction-set retry deadline = %v", got)
	}

	if !target.Disconnect(source) {
		t.Fatal("Disconnect returned false")
	}
	if len(target.acquiringLedgers) != 0 || len(target.acquiringTxSets) != 0 {
		t.Fatal("disconnect left acquisitions in flight")
	}
	if !target.Connect(source, delay) {
		t.Fatal("Connect returned false")
	}
	if err := target.RequestLedger(ledgerID); err != nil {
		t.Fatalf("RequestLedger after reconnect: %v", err)
	}
}

func TestPeerAcquisitionAcceptsSlowReplyAfterRetryWindow(t *testing.T) {
	const (
		fastDelay = time.Millisecond
		slowDelay = 10 * time.Millisecond
	)
	sim := NewSim()
	group := sim.CreateGroup(3)
	target, fast, slow := group.Get(0), group.Get(1), group.Get(2)
	if !target.Connect(fast, fastDelay) || !target.Connect(slow, slowDelay) {
		t.Fatal("connect failed")
	}
	startIdlePeers(t, target)
	t.Cleanup(func() { _ = sim.Stop() })

	ledger, err := slow.BuildLedger(
		slow.LastClosedLedger(),
		NewTxSetFrom([]Tx{{ID: 88}}),
		slow.Now(),
		true,
		nil,
	)
	if err != nil {
		t.Fatalf("BuildLedger: %v", err)
	}

	if err := target.RequestLedger(ledger.ID()); err != nil {
		t.Fatalf("RequestLedger: %v", err)
	}
	sim.Scheduler.StepFor(2 * fastDelay)
	if _, ok := target.ledgers[ledger.ID()]; ok {
		t.Fatal("slow ledger arrived before its round trip completed")
	}
	if _, acquiring := target.acquiringLedgers[ledger.ID()]; !acquiring {
		t.Fatal("retry deadline terminally failed an in-flight acquisition")
	}

	sim.Scheduler.StepFor(2*slowDelay - 2*fastDelay)
	if acquired := target.ledgers[ledger.ID()]; acquired == nil {
		t.Fatal("late slow-holder reply was not accepted")
	}
}

func TestPeerStaleValidationDoesNotStartLedgerAcquisition(t *testing.T) {
	sim := NewSim()
	group := sim.CreateGroup(2)
	source, target := group.Get(0), group.Get(1)
	group.TrustAndConnect(group, time.Millisecond)
	startIdlePeers(t, source, target)
	t.Cleanup(func() { _ = sim.Stop() })

	ledgerID := consensus.LedgerID{0xA5}
	validation := &consensus.Validation{
		LedgerSeq: 10,
		LedgerID:  ledgerID,
		NodeID:    source.nodeID,
		SignTime:  target.Now().Add(-time.Hour),
		SeenTime:  target.Now(),
		Full:      true,
	}
	if err := source.SignValidation(validation); err != nil {
		t.Fatalf("SignValidation: %v", err)
	}
	var disposition consensus.ValidationDisposition
	if !target.withEngine(func(engine *rcl.Engine) {
		var err error
		disposition, err = engine.ProcessVerifiedValidation(
			validation,
			consensus.ValidationOrigin{PeerID: wirePeerID(source.ID)},
		)
		if err != nil {
			t.Fatalf("ProcessVerifiedValidation: %v", err)
		}
	}) {
		t.Fatal("target engine is not active")
	}
	if disposition.Status != consensus.ValidationStale {
		t.Fatalf("validation disposition = %v, want stale", disposition.Status)
	}
	target.receiveValidation(validation, source.ID)

	if err := target.asyncError(); err != nil {
		t.Fatalf("receive stale validation: %v", err)
	}
	if _, acquiring := target.acquiringLedgers[ledgerID]; acquiring {
		t.Fatal("stale validation started ledger acquisition")
	}
}

func TestCollectorCanReenterPeerAndGroupAPIs(t *testing.T) {
	sim := NewSim()
	group := sim.CreateGroup(2)
	group.TrustAndConnect(group, time.Millisecond)
	tx := Tx{ID: 0xCAFE}
	fired := false
	sim.AddCollector(CollectorFunc(func(peer PeerID, _ SimTime, event Event) {
		if fired || peer != group.Get(0).ID {
			return
		}
		if _, ok := event.(StartRoundEvent); !ok {
			return
		}
		fired = true
		group.Disconnect(group)
		group.Connect(group, time.Millisecond)
		group.Get(0).Submit(tx)
	}))

	if err := sim.Run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = sim.Stop() })
	if !fired {
		t.Fatal("collector did not run")
	}
	if !sim.Net.IsConnected(group.Get(0).ID, group.Get(1).ID) {
		t.Fatal("collector group reconnect did not persist")
	}
	if !group.Get(0).LastClosedLedger().Transactions().ContainsTx(tx) {
		t.Fatal("collector-submitted transaction was not accepted")
	}
}

func TestCollectorCanStopPeer(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	var stopErr error
	sim.AddCollector(CollectorFunc(func(id PeerID, _ SimTime, event Event) {
		if id != peer.ID || stopErr != nil {
			return
		}
		if _, ok := event.(StartRoundEvent); ok {
			stopErr = peer.Stop()
		}
	}))

	if err := sim.Run(1); err == nil {
		t.Fatal("Run succeeded after its only peer stopped before completing")
	}
	if stopErr != nil {
		t.Fatalf("collector Stop: %v", stopErr)
	}
	if !peer.isStopped() || !sim.Scheduler.Empty() {
		t.Fatal("collector Stop did not fully tear down the peer")
	}
}

func TestCollectorPanicDoesNotWedgePeerWork(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	startIdlePeers(t, peer)
	t.Cleanup(func() { _ = sim.Stop() })
	sim.AddCollector(CollectorFunc(func(_ PeerID, _ SimTime, event Event) {
		if _, ok := event.(ReceiveValidationEvent); ok {
			panic("collector panic")
		}
	}))

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("collector did not panic")
			}
		}()
		peer.receiveValidation(&consensus.Validation{}, peer.ID)
	}()
	if !peer.workMu.TryLock() {
		t.Fatal("collector panic left peer work locked")
	}
	peer.workMu.Unlock()
}

func TestStopCancelsDelayedValidationCallbacks(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	if err := peer.SetValidationReceiveDelay(time.Hour); err != nil {
		t.Fatalf("SetValidationReceiveDelay: %v", err)
	}
	startIdlePeers(t, peer)
	peer.receiveValidation(&consensus.Validation{}, peer.ID)
	if len(peer.validationTimers) != 1 {
		t.Fatalf("delayed validation timers = %d, want 1", len(peer.validationTimers))
	}
	if err := peer.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(peer.validationTimers) != 0 || !sim.Scheduler.Empty() {
		t.Fatalf("Stop left delayed validation work: timers=%d pending=%d", len(peer.validationTimers), sim.Scheduler.PendingCount())
	}
}

func TestConcurrentDelayedValidationAndStopLeavesNoCallback(t *testing.T) {
	for range 50 {
		sim := NewSim()
		peer := sim.CreateGroup(1).Get(0)
		if err := peer.SetValidationReceiveDelay(time.Hour); err != nil {
			t.Fatalf("SetValidationReceiveDelay: %v", err)
		}
		startIdlePeers(t, peer)
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			<-start
			peer.receiveValidation(&consensus.Validation{}, peer.ID)
			close(done)
		}()
		close(start)
		if err := peer.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		<-done
		if len(peer.validationTimers) != 0 || !sim.Scheduler.Empty() {
			t.Fatal("validation receive raced Stop and left scheduled work")
		}
	}
}

func TestConnectRejectsStoppedEndpoint(t *testing.T) {
	sim := NewSim()
	group := sim.CreateGroup(2)
	a, b := group.Get(0), group.Get(1)
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if a.Connect(b, time.Millisecond) || b.Connect(a, time.Millisecond) {
		t.Fatal("Connect accepted a stopped endpoint")
	}
	if sim.Net.IsConnected(a.ID, b.ID) {
		t.Fatal("stopped endpoint remained connected")
	}
}

func TestConcurrentConnectAndStopLeavesNoConnection(t *testing.T) {
	for range 50 {
		sim := NewSim()
		group := sim.CreateGroup(2)
		a, b := group.Get(0), group.Get(1)
		start := make(chan struct{})
		done := make(chan struct{})
		go func() {
			<-start
			a.Connect(b, time.Millisecond)
			close(done)
		}()
		close(start)
		if err := a.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		<-done
		if sim.Net.IsConnected(a.ID, b.ID) {
			t.Fatal("Connect raced Stop and left a terminal connection")
		}
	}
}

func TestPeerStartDefaultsAndStandaloneAreStable(t *testing.T) {
	sim := NewSim()
	isolated := sim.CreateGroup(1).Get(0)
	if isolated.TargetLedgers() != int(^uint(0)>>1) {
		t.Fatalf("default target = %d", isolated.TargetLedgers())
	}
	if err := isolated.Start(); err != nil {
		t.Fatalf("start isolated peer: %v", err)
	}
	if !isolated.IsStandalone() {
		t.Fatal("initially isolated peer was not configured standalone")
	}
	if !isolated.ticking || sim.Scheduler.PendingCount() == 0 {
		t.Fatal("direct Start did not schedule the default heartbeat")
	}
	if err := sim.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	connectedSim := NewSim()
	group := connectedSim.CreateGroup(2)
	a, b := group.Get(0), group.Get(1)
	group.Connect(group, time.Millisecond)
	startIdlePeers(t, a, b)
	if a.IsStandalone() || b.IsStandalone() {
		t.Fatal("connected peers were configured standalone")
	}
	a.Disconnect(b)
	if a.IsStandalone() || b.IsStandalone() {
		t.Fatal("transient disconnection changed standalone configuration")
	}
	if err := connectedSim.Stop(); err != nil {
		t.Fatalf("connected Stop: %v", err)
	}
}

func TestSimRunZeroStartsPeersAndRunsOneHeartbeat(t *testing.T) {
	sim := NewSim()
	peers := sim.CreateGroup(2)
	timing := fastSimTiming()
	setPeerTiming(t, peers.Peers(), timing)
	t.Cleanup(func() { _ = sim.Stop() })

	starts := make(map[PeerID]int)
	sim.AddCollector(CollectorFunc(func(peer PeerID, _ SimTime, event Event) {
		if _, ok := event.(StartRoundEvent); ok {
			starts[peer]++
		}
	}))

	if err := sim.Run(0); err != nil {
		t.Fatalf("Run(0): %v", err)
	}
	if got, want := sim.Now(), SimTime(timing.LedgerGranularity); got != want {
		t.Fatalf("simulation time after Run(0) = %v, want %v", got, want)
	}
	for _, peer := range peers.Peers() {
		if !peer.started {
			t.Fatalf("peer %d was not started", peer.ID)
		}
		if peer.ticking {
			t.Fatalf("peer %d kept ticking after its target was reached", peer.ID)
		}
		if got := peer.CompletedLedgers(); got != 0 {
			t.Fatalf("peer %d completed %d ledgers, want 0", peer.ID, got)
		}
		if got := starts[peer.ID]; got != 1 {
			t.Fatalf("peer %d start-round events = %d, want 1", peer.ID, got)
		}
	}
	if !sim.Scheduler.Empty() {
		t.Fatalf("Run(0) left %d callbacks queued", sim.Scheduler.PendingCount())
	}
}

func TestSimRunZeroRestartsTargetReachedPeerAndRunsOneHeartbeat(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	timing := fastSimTiming()
	timing.LedgerGranularity = timing.LedgerIdleInterval
	setPeerTiming(t, []*Peer{peer}, timing)
	t.Cleanup(func() { _ = sim.Stop() })
	starts := 0
	sim.AddCollector(CollectorFunc(func(id PeerID, _ SimTime, event Event) {
		if id == peer.ID {
			if _, ok := event.(StartRoundEvent); ok {
				starts++
			}
		}
	}))

	peer.Submit(Tx{ID: 1})
	if err := sim.Run(0); err != nil {
		t.Fatalf("first Run(0): %v", err)
	}
	if got := peer.engine.Phase(); got != consensus.PhaseEstablish {
		t.Fatalf("phase after first Run(0) = %s, want %s", got, consensus.PhaseEstablish)
	}
	if starts != 1 {
		t.Fatalf("start-round events after first Run(0) = %d, want 1", starts)
	}
	peer.mu.Lock()
	clear(peer.openTxs)
	peer.mu.Unlock()
	before := sim.Now()

	if err := sim.Run(0); err != nil {
		t.Fatalf("second Run(0): %v", err)
	}
	if got, want := sim.Now(), before+SimTime(timing.LedgerGranularity); got != want {
		t.Fatalf("simulation time after repeated Run(0) = %v, want %v", got, want)
	}
	if starts != 2 {
		t.Fatalf("start-round events after repeated Run(0) = %d, want 2", starts)
	}
	if got := peer.CompletedLedgers(); got != 0 {
		t.Fatalf("completed ledgers after repeated Run(0) = %d, want 0", got)
	}
	if peer.ticking {
		t.Fatal("target-reached peer kept ticking after the one-shot heartbeat")
	}
	if !sim.Scheduler.Empty() {
		t.Fatalf("Run(0) left %d callbacks queued", sim.Scheduler.PendingCount())
	}
}

func TestSimRunDrainsPendingWork(t *testing.T) {
	sim := NewSim()
	sim.CreateGroup(1)
	drained := false
	sim.Scheduler.In(time.Hour, func() { drained = true })

	if err := sim.Run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { _ = sim.Stop() })
	if !drained {
		t.Fatal("Run returned before pending work was drained")
	}
	if !sim.Scheduler.Empty() {
		t.Fatal("Run returned with scheduled work remaining")
	}
}

func TestSimRunStopsAfterDrainedCallbackError(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	sim.Scheduler.In(time.Hour, func() {
		peer.recordAsyncError(errors.New("drained callback failed"))
	})

	err := sim.Run(1)
	if err == nil || !strings.Contains(err.Error(), "drained callback failed") {
		t.Fatalf("Run error = %v, want drained callback failure", err)
	}
	if !peer.isStopped() || !sim.Scheduler.Empty() {
		t.Fatal("Run error did not stop and drain the simulation")
	}
}

func TestStoppedPeerRejectsInboundAndOutboundWork(t *testing.T) {
	sim := NewSim()
	group := sim.CreateGroup(2)
	peer, other := group.Get(0), group.Get(1)
	group.Connect(group, time.Millisecond)
	startIdlePeers(t, peer, other)

	if err := peer.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sim.Net.IsConnected(peer.ID, other.ID) {
		t.Fatal("stopped peer remained connected")
	}
	pending := sim.Scheduler.PendingCount()
	proposal := &consensus.Proposal{Round: consensus.RoundID{Seq: 1}}
	if err := peer.BroadcastProposal(proposal); err != nil {
		t.Fatalf("BroadcastProposal after stop: %v", err)
	}
	if sim.Scheduler.PendingCount() != pending {
		t.Fatal("stopped peer scheduled outbound proposal work")
	}
	peer.receiveProposal(proposal, other.ID)
	peer.receiveTx(Tx{ID: 44}.Bytes(), nil)
	if len(peer.seenProposals) != 0 || peer.HasTx(Tx{ID: 44}.TxID()) {
		t.Fatal("stopped peer accepted inbound work")
	}
	if err := sim.Stop(); err != nil {
		t.Fatalf("sim Stop: %v", err)
	}
}

func TestTargetReachedPeerStillReceivesAndRelaysConsensusMessages(t *testing.T) {
	sim := NewSim()
	group := sim.CreateGroup(3)
	source, target, sink := group.Get(0), group.Get(1), group.Get(2)
	group.Trust(group)
	source.Connect(target, time.Millisecond)
	target.Connect(sink, time.Millisecond)
	startIdlePeers(t, source, target, sink)
	t.Cleanup(func() { _ = sim.Stop() })

	proposal := &consensus.Proposal{
		Round:          consensus.RoundID{Seq: 1, ParentHash: source.LastClosedLedger().ID()},
		TxSet:          NewTxSet().ID(),
		PreviousLedger: source.LastClosedLedger().ID(),
		CloseTime:      source.Now(),
		Timestamp:      source.Now(),
	}
	if err := source.SignProposal(proposal); err != nil {
		t.Fatalf("SignProposal: %v", err)
	}
	if err := source.BroadcastProposal(proposal); err != nil {
		t.Fatalf("BroadcastProposal: %v", err)
	}
	sim.Scheduler.StepFor(2 * time.Millisecond)
	if _, ok := target.seenProposals[proposalKey(proposal)]; !ok {
		t.Fatal("target-reached peer discarded a proposal")
	}
	if _, ok := sink.seenProposals[proposalKey(proposal)]; !ok {
		t.Fatal("target-reached peer did not relay a proposal")
	}

	validation := &consensus.Validation{
		LedgerID:  source.LastClosedLedger().ID(),
		LedgerSeq: source.LastClosedLedger().Seq(),
		SignTime:  source.Now(),
		SeenTime:  source.Now(),
		Full:      true,
	}
	if err := source.SignValidation(validation); err != nil {
		t.Fatalf("SignValidation: %v", err)
	}
	if err := source.BroadcastValidation(validation); err != nil {
		t.Fatalf("BroadcastValidation: %v", err)
	}
	sim.Scheduler.StepFor(2 * time.Millisecond)
	if _, ok := target.seenValidation[validationKey(validation)]; !ok {
		t.Fatal("target-reached peer discarded a validation")
	}
	if _, ok := sink.seenValidation[validationKey(validation)]; !ok {
		t.Fatal("target-reached peer did not relay a validation")
	}
}

func TestPeerDelaysValidationProcessingAfterReceipt(t *testing.T) {
	const (
		networkDelay = time.Millisecond
		processDelay = 10 * time.Millisecond
	)
	sim := NewSim()
	group := sim.CreateGroup(3)
	source, target, sink := group.Get(0), group.Get(1), group.Get(2)
	group.Trust(group)
	source.Connect(target, networkDelay)
	target.Connect(sink, networkDelay)
	if err := target.SetValidationReceiveDelay(processDelay); err != nil {
		t.Fatalf("SetValidationReceiveDelay: %v", err)
	}
	if err := target.SetValidationReceiveDelay(-time.Nanosecond); err == nil {
		t.Fatal("negative validation receive delay was accepted")
	}
	startIdlePeers(t, source, target, sink)
	t.Cleanup(func() { _ = sim.Stop() })

	var receivedAt SimTime = -1
	sim.AddCollector(CollectorFunc(func(peer PeerID, when SimTime, event Event) {
		if peer == target.ID {
			if _, ok := event.(ReceiveValidationEvent); ok {
				receivedAt = when
			}
		}
	}))
	validation := &consensus.Validation{
		LedgerID:  source.LastClosedLedger().ID(),
		LedgerSeq: source.LastClosedLedger().Seq(),
		SignTime:  source.Now(),
		SeenTime:  source.Now(),
		Full:      true,
	}
	if err := source.SignValidation(validation); err != nil {
		t.Fatalf("SignValidation: %v", err)
	}
	if err := source.BroadcastValidation(validation); err != nil {
		t.Fatalf("BroadcastValidation: %v", err)
	}

	sim.Scheduler.StepFor(networkDelay)
	if receivedAt != SimTime(networkDelay) {
		t.Fatalf("receive event time = %v, want %v", receivedAt, networkDelay)
	}
	if _, ok := sink.seenValidation[validationKey(validation)]; ok {
		t.Fatal("validation was relayed before its processing delay")
	}
	sim.Scheduler.StepFor(processDelay - networkDelay)
	if _, ok := sink.seenValidation[validationKey(validation)]; ok {
		t.Fatal("validation was relayed before delayed processing ran")
	}
	sim.Scheduler.StepFor(2 * networkDelay)
	if _, ok := sink.seenValidation[validationKey(validation)]; !ok {
		t.Fatal("validation was not relayed after delayed processing")
	}
}

func TestPeerDefersLedgerAcceptAndStopCancelsCompletion(t *testing.T) {
	const delay = 5 * time.Millisecond

	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	if err := peer.SetLedgerAcceptDelay(delay); err != nil {
		t.Fatalf("SetLedgerAcceptDelay: %v", err)
	}
	if err := peer.SetLedgerAcceptDelay(-time.Nanosecond); err == nil {
		t.Fatal("negative ledger accept delay was accepted")
	}
	completed := false
	if !peer.DeferLedgerAccept(func() { completed = true }) {
		t.Fatal("non-zero ledger accept delay was not deferred")
	}
	sim.Scheduler.StepFor(delay - time.Nanosecond)
	if completed {
		t.Fatal("ledger accept completed before the configured delay")
	}
	sim.Scheduler.StepFor(time.Nanosecond)
	if !completed {
		t.Fatal("ledger accept did not complete after the configured delay")
	}

	stoppedSim := NewSim()
	stoppedPeer := stoppedSim.CreateGroup(1).Get(0)
	if err := stoppedPeer.SetLedgerAcceptDelay(delay); err != nil {
		t.Fatalf("SetLedgerAcceptDelay before stop: %v", err)
	}
	completedAfterStop := false
	if !stoppedPeer.DeferLedgerAccept(func() { completedAfterStop = true }) {
		t.Fatal("ledger accept was not deferred before stop")
	}
	if err := stoppedPeer.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	stoppedSim.Scheduler.Step()
	if completedAfterStop {
		t.Fatal("stopped peer completed a canceled ledger accept")
	}
}

func TestPeerNowTruncatesAfterClockSkew(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	sim.Scheduler.StepFor(600 * time.Millisecond)
	peer.SetClockSkew(600 * time.Millisecond)

	want := time.Unix(protocol.RippleEpochUnix, 0).UTC().Add(24*time.Hour + time.Second)
	if got := peer.Now(); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
}

func TestPeerSuppressesTransactionAlreadyInLCL(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	tx := Tx{ID: 73}
	ledger, err := peer.BuildLedger(peer.LastClosedLedger(), NewTxSetFrom([]Tx{tx}), peer.Now(), true, nil)
	if err != nil {
		t.Fatalf("BuildLedger: %v", err)
	}
	peer.OnConsensusReached(ledger, nil, 0)
	peer.receiveTx(tx.Bytes(), nil)
	if peer.HasTx(tx.TxID()) {
		t.Fatal("transaction already in the LCL re-entered the open ledger")
	}
}

func TestConsensusCommitPromotesPendingFullyValidatedLedger(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	genesis := sim.Oracle.Genesis()
	ledger := sim.Oracle.Accept(
		genesis,
		NewTxSet(),
		genesis.CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)

	peer.OnLedgerFullyValidated(ledger.ID(), ledger.Seq())
	if got := peer.FullyValidatedLedger(); got.ID() != genesis.ID() {
		t.Fatalf("fully validated ledger advanced before commit: got %x", got.ID())
	}

	peer.OnConsensusReached(ledger, nil, 0)
	if got := peer.FullyValidatedLedger(); got.ID() != ledger.ID() {
		t.Fatalf("fully validated ledger after commit = %x, want %x", got.ID(), ledger.ID())
	}
}

func TestPeerOperatingModeRemainsFull(t *testing.T) {
	peer := NewSim().CreateGroup(1).Get(0)
	peer.SetOperatingMode(consensus.OpModeConnected)
	if got := peer.GetOperatingMode(); got != consensus.OpModeFull {
		t.Fatalf("operating mode = %v, want Full", got)
	}
}

func TestPeerBuildLedgerUsesExplicitParentResolution(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	parent := &Ledger{
		id:         consensus.LedgerID{0x20},
		seq:        16,
		txs:        NewTxSet(),
		closeTime:  peer.Now(),
		closeAgree: true,
		resolution: 20 * time.Second,
	}

	built, err := peer.BuildLedger(parent, NewTxSet(), parent.CloseTime().Add(time.Second), true, nil)
	if err != nil {
		t.Fatalf("BuildLedger: %v", err)
	}
	if got := built.(*Ledger).CloseTimeResolution(); got != 20*time.Second {
		t.Fatalf("close-time resolution = %s, want explicit parent's 20s", got)
	}
}

func startIdlePeers(t *testing.T, peers ...*Peer) {
	t.Helper()
	for _, peer := range peers {
		peer.SetTargetLedgers(0)
		if err := peer.Start(); err != nil {
			t.Fatalf("start peer %d: %v", peer.ID, err)
		}
	}
}

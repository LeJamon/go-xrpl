package csf

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestSchedulerContract(t *testing.T) {
	scheduler := NewScheduler()
	var order []int
	scheduler.In(2*time.Second, func() { order = append(order, 2) })
	scheduler.In(time.Second, func() { order = append(order, 1) })
	scheduler.In(8*time.Second, func() { order = append(order, 8) })

	if got := scheduler.Step(); got != 3 {
		t.Fatalf("Step processed %d events, want 3", got)
	}
	if !slices.Equal(order, []int{1, 2, 8}) {
		t.Fatalf("delivery order = %v, want [1 2 8]", order)
	}
	if got := scheduler.Now(); got != SimTime(8*time.Second) {
		t.Fatalf("Now = %v, want 8s", got)
	}

	cancelled := false
	cancel := scheduler.In(time.Second, func() { cancelled = true })
	cancel()
	if got := scheduler.StepFor(time.Second); got != 0 {
		t.Fatalf("StepFor processed %d cancelled events", got)
	}
	if cancelled {
		t.Fatal("cancelled event ran")
	}

	for name, operation := range map[string]func(){
		"past At":            func() { scheduler.At(scheduler.Now()-1, func() {}) },
		"negative In":        func() { scheduler.In(-1, func() {}) },
		"negative StepFor":   func() { scheduler.StepFor(-1) },
		"backward StepUntil": func() { scheduler.StepUntil(scheduler.Now() - 1) },
	} {
		t.Run(name, func(t *testing.T) {
			now := scheduler.Now()
			pending := scheduler.PendingCount()
			assertPanics(t, operation)
			if scheduler.Now() != now {
				t.Fatalf("Now changed from %v to %v", now, scheduler.Now())
			}
			if scheduler.PendingCount() != pending {
				t.Fatalf("pending count changed from %d to %d", pending, scheduler.PendingCount())
			}
		})
	}
}

func TestSchedulerSerializesDrivers(t *testing.T) {
	scheduler := NewScheduler()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	scheduler.In(time.Second, func() {
		close(firstStarted)
		<-releaseFirst
	})
	scheduler.In(2*time.Second, func() {
		close(secondRan)
	})

	firstDone := make(chan struct{})
	go func() {
		scheduler.StepOne()
		close(firstDone)
	}()
	<-firstStarted

	secondDone := make(chan struct{})
	go func() {
		scheduler.StepOne()
		close(secondDone)
	}()
	select {
	case <-secondRan:
		t.Fatal("second driver ran while the first handler was active")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	<-firstDone
	<-secondDone
	if scheduler.Now() != SimTime(2*time.Second) {
		t.Fatalf("Now = %v, want 2s", scheduler.Now())
	}
}

func TestBasicNetworkConnectionGeneration(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	if !network.Connect(1, 2, time.Second) {
		t.Fatal("initial connect failed")
	}

	var delivered []string
	if !network.Send(1, 2, func() { delivered = append(delivered, "old") }) {
		t.Fatal("old send failed")
	}
	if !network.Disconnect(1, 2) {
		t.Fatal("disconnect failed")
	}
	if !network.Connect(1, 2, time.Second) {
		t.Fatal("reconnect failed")
	}
	if !network.Send(1, 2, func() { delivered = append(delivered, "new") }) {
		t.Fatal("new send failed")
	}

	scheduler.Step()
	if !slices.Equal(delivered, []string{"old", "new"}) {
		t.Fatalf("delivered = %v, want [old new]", delivered)
	}
}

func TestBasicNetworkDisconnectDropsBothDirections(t *testing.T) {
	scheduler := NewScheduler()
	network := NewBasicNetwork(scheduler)
	network.Connect(1, 2, time.Second)

	deliveries := 0
	network.Send(1, 2, func() { deliveries++ })
	network.Send(2, 1, func() { deliveries++ })
	network.Disconnect(1, 2)
	scheduler.Step()
	if deliveries != 0 {
		t.Fatalf("deliveries = %d, want 0", deliveries)
	}
}

func TestLedgerOracleIdentity(t *testing.T) {
	oracle := NewLedgerOracle()
	genesis := oracle.Genesis()
	txs := NewTxSetFrom([]Tx{{ID: 1}})

	firstClose := genesis.CloseTime().Add(time.Second)
	first := oracle.Accept(genesis, txs, firstClose, true, 30*time.Second)
	same := oracle.Accept(genesis, txs.Clone(), firstClose.Add(9*time.Second), true, 30*time.Second)
	if first != same {
		t.Fatal("canonically equal inputs did not intern to the same ledger")
	}
	if first.CloseTime() != firstClose {
		t.Fatalf("effective close time = %v, want 1s", first.CloseTime())
	}

	resolutionOnly := oracle.Accept(genesis, txs, firstClose, true, 20*time.Second)
	if resolutionOnly.ID() == first.ID() {
		t.Fatal("resolution-only difference collapsed ledger identity")
	}
	agreementOnly := oracle.Accept(genesis, txs, firstClose, false, 30*time.Second)
	if agreementOnly.ID() == first.ID() {
		t.Fatal("agreement-only difference collapsed ledger identity")
	}
	if agreementOnly.CloseTime() != genesis.CloseTime().Add(time.Second) {
		t.Fatalf("disagreed close time = %v, want parent+1s", agreementOnly.CloseTime())
	}

	secondTxs := NewTxSetFrom([]Tx{{ID: 2}})
	child := oracle.Accept(first, secondTxs, genesis.CloseTime().Add(31*time.Second), true, 30*time.Second)
	contents := child.Transactions()
	if !contents.ContainsTx(Tx{ID: 1}) || !contents.ContainsTx(Tx{ID: 2}) {
		t.Fatalf("child transactions = %v, want parent and child transactions", contents.Transactions())
	}

	contents.Insert(Tx{ID: 99})
	if child.Transactions().ContainsTx(Tx{ID: 99}) {
		t.Fatal("ledger transaction accessor exposed mutable state")
	}
	if branches := oracle.Branches([]*Ledger{first, resolutionOnly}); branches != 2 {
		t.Fatalf("branches = %d, want 2", branches)
	}
	if child.MinSeq() != 0 ||
		child.Ancestor(0) != genesis.ID() ||
		child.Ancestor(1) != first.ID() ||
		child.Ancestor(2) != child.ID() {
		t.Fatal("child ledger ancestry does not match its canonical chain")
	}
	if resolved, ok := oracle.LedgerByID(child.ID()); !ok || resolved.ID() != child.ID() {
		t.Fatal("ledger oracle did not resolve ancestry by ledger ID")
	}
}

func TestEffectiveCloseTime(t *testing.T) {
	parent := time.Unix(protocol.RippleEpochUnix, 0).UTC()
	tests := []struct {
		close     int64
		closeNano int64
		parent    int64
		want      int64
	}{
		{close: 10, parent: 0, want: 1},
		{close: 14, closeNano: int64(time.Second - 1), parent: 0, want: 1},
		{close: 16, parent: 0, want: 30},
		{close: 16, parent: 30, want: 31},
		{close: 16, parent: 60, want: 61},
		{close: 31, parent: 0, want: 30},
	}
	for _, test := range tests {
		got := effectiveCloseTime(
			time.Unix(protocol.RippleEpochUnix+test.close, test.closeNano).UTC(),
			30*time.Second,
			parent.Add(time.Duration(test.parent)*time.Second),
		)
		if got != parent.Add(time.Duration(test.want)*time.Second) {
			t.Errorf(
				"effectiveCloseTime(%ds, 30s, %ds) = %v, want %ds",
				test.close,
				test.parent,
				got,
				test.want,
			)
		}
	}
	if got := effectiveCloseTime(time.Time{}, 30*time.Second, parent); !got.IsZero() {
		t.Fatalf("effectiveCloseTime(zero) = %v, want zero", got)
	}
}

func TestTrustGraphReturnsStableOrder(t *testing.T) {
	graph := NewTrustGraph()
	graph.Trust(1, 3)
	graph.Trust(1, 2)
	graph.Trust(1, 4)
	if got := graph.TrustedPeers(1); !slices.Equal(got, []PeerID{2, 3, 4}) {
		t.Fatalf("trusted peers = %v, want [2 3 4]", got)
	}
	graph.Untrust(1, 3)
	if graph.Trusts(1, 3) {
		t.Fatal("peer 1 still trusts peer 3")
	}
}

func TestCollectors(t *testing.T) {
	collectors := NewCollectors()
	var events []Event
	collectors.Add(CollectorFunc(func(_ PeerID, _ SimTime, event Event) {
		events = append(events, event)
	}))
	collectors.On(0, 100, StartRoundEvent{Ledger: MakeGenesis(), Proposer: true})
	collectors.On(0, 200, AcceptLedgerEvent{Ledger: MakeGenesis()})
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	duration := &SimDurationCollector{}
	duration.On(0, 0, StartRoundEvent{})
	duration.On(1, 200, AcceptLedgerEvent{})
	duration.On(2, 150, CloseLedgerEvent{})
	if duration.Duration() != 200 {
		t.Fatalf("duration = %v, want 200", duration.Duration())
	}
}

func TestProductionEngineCollectorLifecycle(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peer := sim.CreateGroup(1).Get(0)
	setPeerTiming(t, []*Peer{peer}, fastSimTiming())
	var starts, closes, accepts int
	sim.AddCollector(CollectorFunc(func(_ PeerID, _ SimTime, event Event) {
		switch event.(type) {
		case StartRoundEvent:
			starts++
		case CloseLedgerEvent:
			closes++
		case AcceptLedgerEvent:
			accepts++
		}
	}))

	if err := sim.Run(2); err != nil {
		t.Fatalf("run: %v", err)
	}
	if starts < 2 || closes != 2 || accepts != 2 {
		t.Fatalf(
			"collector lifecycle starts=%d closes=%d accepts=%d, want at least 2/2/2",
			starts,
			closes,
			accepts,
		)
	}
}

func TestQuorumUsesCeiling(t *testing.T) {
	tests := []struct {
		trusted int
		want    int
	}{
		{trusted: 1, want: 1},
		{trusted: 2, want: 2},
		{trusted: 3, want: 3},
		{trusted: 4, want: 4},
		{trusted: 5, want: 4},
		{trusted: 6, want: 5},
		{trusted: 7, want: 6},
		{trusted: 8, want: 7},
		{trusted: 9, want: 8},
		{trusted: 10, want: 8},
	}
	for _, test := range tests {
		if got := quorumFor(test.trusted); got != test.want {
			t.Errorf("quorumFor(%d) = %d, want %d", test.trusted, got, test.want)
		}
	}
}

func TestPeerLifecycleAndRepeatedRun(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	setPeerTiming(t, []*Peer{peer}, fastSimTiming())

	peer.SetTargetLedgers(1)
	if err := peer.Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	pending := sim.Scheduler.PendingCount()
	if err := peer.Start(); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if got := sim.Scheduler.PendingCount(); got != pending {
		t.Fatalf("second start changed pending callbacks from %d to %d", pending, got)
	}

	if err := sim.Run(1); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := peer.LastClosedLedger()
	if first.Seq() != 1 {
		t.Fatalf("sequence after first Run(1) = %d, want 1", first.Seq())
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := peer.LastClosedLedger().Seq(); got != first.Seq()+1 {
		t.Fatalf("sequence after repeated run = %d, want %d", got, first.Seq()+1)
	}

	if err := sim.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	completed := peer.CompletedLedgers()
	sim.Scheduler.StepFor(time.Second)
	if peer.CompletedLedgers() != completed {
		t.Fatal("stopped peer processed another heartbeat")
	}
	if !sim.Scheduler.Empty() {
		t.Fatalf("scheduler has %d callbacks after teardown", sim.Scheduler.PendingCount())
	}
	if err := sim.Stop(); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestSimFullMeshUsesProductionEngine(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peers := sim.SetupFullyConnected(4, time.Millisecond)
	setPeerTiming(t, peers.Peers(), fastSimTiming())
	for _, peer := range peers.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID) + 1})
	}

	if err := sim.Run(2); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sim.SynchronizedAll() {
		t.Fatal("full-mesh peers did not synchronize")
	}
	for _, peer := range peers.Peers() {
		ledger := peer.LastClosedLedger()
		if ledger.Seq() != 2 {
			t.Fatalf("peer %d sequence = %d, want 2", peer.ID, ledger.Seq())
		}
		for id := uint32(1); id <= 4; id++ {
			if !ledger.Transactions().ContainsTx(Tx{ID: id}) {
				t.Fatalf("peer %d ledger missing transaction %d", peer.ID, id)
			}
		}
	}
}

func TestSimSlowPeer(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	slow := sim.CreateGroup(1)
	fast := sim.CreateGroup(4)
	all := slow.Union(fast)
	all.Trust(all)
	fast.Connect(fast, time.Millisecond)
	slow.Connect(fast, 40*time.Millisecond)

	timing := fastSimTiming()
	timing.LedgerMinClose = 200 * time.Millisecond
	setPeerTiming(t, all.Peers(), timing)
	for _, peer := range all.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID) + 10})
	}

	if err := sim.Run(2); err != nil {
		t.Fatalf("run: %v", err)
	}
	runUntilSynchronized(t, sim, 5*time.Second)
	if !sim.SynchronizedAll() {
		t.Fatal("slow topology did not synchronize")
	}
	for _, peer := range all.Peers() {
		for id := uint32(10); id < 15; id++ {
			if !peer.LastClosedLedger().Transactions().ContainsTx(Tx{ID: id}) {
				t.Fatalf("peer %d ledger missing transaction %d", peer.ID, id)
			}
		}
	}
}

func TestObserverHubRelaysConsensusTraffic(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	hub, spokes := sim.SetupHubAndSpokes(5, time.Millisecond)
	hub.SetRunAsValidator(false)
	spokes.Untrust(NewPeerGroupSingle(hub))
	setPeerTiming(t, sim.Peers(), fastSimTiming())
	for _, peer := range spokes.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID) + 100})
	}

	if err := sim.Run(2); err != nil {
		t.Fatalf("run: %v", err)
	}
	runUntilSynchronized(t, sim, 5*time.Second)
	if !sim.SynchronizedAll() {
		logPeerState(t, sim.Peers())
		t.Fatal("observer-hub topology did not synchronize")
	}
	if hub.FullyValidatedLedger().Seq() == 0 {
		t.Fatal("observer hub never saw a fully validated ledger")
	}
}

func TestPartitionHealingConverges(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	all := sim.SetupFullyConnected(6, time.Millisecond)
	setPeerTiming(t, all.Peers(), fastSimTiming())
	if err := sim.Run(1); err != nil {
		t.Fatalf("initial run: %v", err)
	}

	peers := all.Peers()
	groupA := NewPeerGroupFrom(peers[:3])
	groupB := NewPeerGroupFrom(peers[3:])
	sim.PartitionNetwork(groupA, groupB)
	for _, peer := range groupA.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID) + 1000})
	}
	for _, peer := range groupB.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID) + 2000})
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("partitioned run: %v", err)
	}
	if groupA.Get(0).LastClosedLedger().ID() == groupB.Get(0).LastClosedLedger().ID() {
		t.Fatal("partitioned groups did not create distinct branches")
	}

	sim.HealPartition(groupA, groupB, time.Millisecond)
	if err := sim.Run(3); err != nil {
		t.Fatalf("healed run: %v", err)
	}
	runUntilSynchronized(t, sim, 10*time.Second)
	if !sim.SynchronizedAll() {
		logPeerState(t, sim.Peers())
		t.Fatal("peers did not converge after partition healing")
	}
	if sim.BranchesAll() != 1 {
		t.Fatalf("branches after healing = %d, want 1", sim.BranchesAll())
	}
}

func TestPeerTrafficDoesNotCrossDisconnect(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peers := sim.SetupFullyConnected(2, 50*time.Millisecond)
	source, target := peers.Get(0), peers.Get(1)
	timing := fastSimTiming()
	timing.LedgerGranularity = time.Second
	setPeerTiming(t, peers.Peers(), timing)
	for _, peer := range peers.Peers() {
		peer.SetTargetLedgers(1)
		if err := peer.Start(); err != nil {
			t.Fatalf("start peer %d: %v", peer.ID, err)
		}
	}

	proposal := &consensus.Proposal{
		Round:          consensus.RoundID{Seq: 1, ParentHash: source.PrevLedgerID()},
		PreviousLedger: source.PrevLedgerID(),
		TxSet:          NewTxSet().ID(),
		Timestamp:      source.Now(),
		CloseTime:      source.Now(),
	}
	if err := source.SignProposal(proposal); err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	if err := source.BroadcastProposal(proposal); err != nil {
		t.Fatalf("broadcast proposal: %v", err)
	}
	validation := &consensus.Validation{
		LedgerID:  source.PrevLedgerID(),
		LedgerSeq: 0,
		SignTime:  source.Now(),
		SeenTime:  source.Now(),
		Full:      true,
	}
	if err := source.SignValidation(validation); err != nil {
		t.Fatalf("sign validation: %v", err)
	}
	if err := source.BroadcastValidation(validation); err != nil {
		t.Fatalf("broadcast validation: %v", err)
	}
	source.Submit(Tx{ID: 77})
	set, err := source.BuildTxSet([][]byte{Tx{ID: 88}.Bytes()})
	if err != nil {
		t.Fatalf("build tx set: %v", err)
	}
	if err := target.RequestTxSet(set.ID()); err != nil {
		t.Fatalf("request tx set: %v", err)
	}

	source.Disconnect(target)
	sim.Scheduler.StepFor(100 * time.Millisecond)
	target.mu.Lock()
	proposals := len(target.seenProposals)
	validations := len(target.seenValidation)
	target.mu.Unlock()
	if proposals != 0 || validations != 0 {
		t.Fatalf("delivered proposals=%d validations=%d across disconnect", proposals, validations)
	}
	if target.HasTx(Tx{ID: 77}.TxID()) {
		t.Fatal("transaction crossed disconnect")
	}
	if _, err := target.GetTxSet(set.ID()); !errors.Is(err, errNotFound) {
		t.Fatalf("tx-set request after disconnect error = %v, want not found", err)
	}
}

func TestAcquisitionAndStorageFailures(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peers := sim.SetupFullyConnected(2, time.Millisecond)
	source, target := peers.Get(0), peers.Get(1)
	setPeerTiming(t, peers.Peers(), fastSimTiming())
	for _, peer := range peers.Peers() {
		peer.SetTargetLedgers(1)
		if err := peer.Start(); err != nil {
			t.Fatalf("start peer %d: %v", peer.ID, err)
		}
	}

	set, err := source.BuildTxSet([][]byte{Tx{ID: 5}.Bytes()})
	if err != nil {
		t.Fatalf("build tx set: %v", err)
	}
	if err := target.RequestTxSet(set.ID()); err != nil {
		t.Fatalf("request tx set: %v", err)
	}

	ledger := sim.Oracle.Accept(
		sim.Oracle.Genesis(),
		NewTxSetFrom([]Tx{{ID: 9}}),
		sim.Oracle.Genesis().CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	if err := source.StoreLedger(ledger); err != nil {
		t.Fatalf("store source ledger: %v", err)
	}
	if err := target.RequestLedger(ledger.ID()); err != nil {
		t.Fatalf("request ledger: %v", err)
	}
	sim.Scheduler.StepFor(5 * time.Millisecond)
	if _, err := target.GetTxSet(set.ID()); err != nil {
		t.Fatalf("acquired tx set missing: %v", err)
	}
	if _, err := target.GetLedger(ledger.ID()); err != nil {
		t.Fatalf("acquired ledger missing: %v", err)
	}

	source.Disconnect(target)
	if err := target.RequestTxSet(consensus.TxSetID{1}); !errors.Is(err, errNotFound) {
		t.Fatalf("unavailable tx set error = %v, want not found", err)
	}
	if err := target.RequestLedger(consensus.LedgerID{1}); !errors.Is(err, errNotFound) {
		t.Fatalf("unavailable ledger error = %v, want not found", err)
	}
	if err := target.StoreLedger(foreignLedger{}); err == nil {
		t.Fatal("unexpected ledger type was silently accepted")
	}
}

func TestStoppedPeerIgnoresInFlightLedgerAcquisition(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peers := sim.SetupFullyConnected(2, 10*time.Millisecond)
	source, target := peers.Get(0), peers.Get(1)
	timing := fastSimTiming()
	timing.LedgerGranularity = time.Second
	setPeerTiming(t, peers.Peers(), timing)
	for _, peer := range peers.Peers() {
		peer.SetTargetLedgers(math.MaxInt)
		if err := peer.Start(); err != nil {
			t.Fatalf("start peer %d: %v", peer.ID, err)
		}
	}

	ledger := sim.Oracle.Accept(
		sim.Oracle.Genesis(),
		NewTxSetFrom([]Tx{{ID: 901}}),
		sim.Oracle.Genesis().CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	if err := source.StoreLedger(ledger); err != nil {
		t.Fatalf("store source ledger: %v", err)
	}
	if err := target.RequestLedger(ledger.ID()); err != nil {
		t.Fatalf("request ledger: %v", err)
	}
	if err := target.Stop(); err != nil {
		t.Fatalf("stop target: %v", err)
	}
	sim.Scheduler.StepFor(25 * time.Millisecond)
	if _, err := target.GetLedger(ledger.ID()); !errors.Is(err, errNotFound) {
		t.Fatalf("stopped peer acquired ledger: %v", err)
	}
	if err := sim.Stop(); err != nil {
		t.Fatalf("stop simulation: %v", err)
	}
	if !sim.Scheduler.Empty() {
		t.Fatalf("scheduler has %d callbacks after teardown", sim.Scheduler.PendingCount())
	}
}

func TestFullyValidatedLedgerStaysOnCurrentBranch(t *testing.T) {
	sim := NewSim()
	peer := sim.CreateGroup(1).Get(0)
	genesis := sim.Oracle.Genesis()
	branchA := sim.Oracle.Accept(
		genesis,
		NewTxSetFrom([]Tx{{ID: 1}}),
		genesis.CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	branchB := sim.Oracle.Accept(
		genesis,
		NewTxSetFrom([]Tx{{ID: 2}}),
		genesis.CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	branchAChild := sim.Oracle.Accept(
		branchA,
		NewTxSet(),
		branchA.CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	branchBChild := sim.Oracle.Accept(
		branchB,
		NewTxSet(),
		branchB.CloseTime().Add(time.Second),
		true,
		30*time.Second,
	)
	for _, ledger := range []*Ledger{branchA, branchB, branchAChild, branchBChild} {
		if err := peer.StoreLedger(ledger); err != nil {
			t.Fatalf("store ledger %d: %v", ledger.Seq(), err)
		}
	}

	peer.promoteFullyValidated(branchA, branchA.Seq())
	peer.promoteFullyValidated(branchBChild, branchBChild.Seq())
	if got := peer.FullyValidatedLedger(); got.ID() != branchA.ID() {
		t.Fatal("fully validated ledger switched to a conflicting branch")
	}
	peer.promoteFullyValidated(branchAChild, branchAChild.Seq())
	if got := peer.FullyValidatedLedger(); got.ID() != branchAChild.ID() {
		t.Fatal("fully validated ledger did not advance on its current branch")
	}
}

func TestSimulationEnablesAmendmentsByDefault(t *testing.T) {
	peer := NewSim().CreateGroup(1).Get(0)
	if !peer.IsFeatureEnabled("HardenedValidations") ||
		!peer.IsFeatureEnabledOnLedger(peer.LastClosedLedger(), "HardenedValidations") {
		t.Fatal("simulation adaptor unexpectedly disabled an amendment")
	}
}

func TestSimulationSignaturesRejectMutation(t *testing.T) {
	sim := NewSim()
	peers := sim.CreateGroup(2)
	source, target := peers.Get(0), peers.Get(1)

	proposal := &consensus.Proposal{
		Round:          consensus.RoundID{Seq: 1},
		PreviousLedger: source.PrevLedgerID(),
		Timestamp:      time.Unix(1, 0),
		CloseTime:      time.Unix(1, 0),
	}
	if err := source.SignProposal(proposal); err != nil {
		t.Fatalf("sign proposal: %v", err)
	}
	if err := target.VerifyProposal(proposal); err != nil {
		t.Fatalf("verify proposal: %v", err)
	}
	proposal.Position++
	if err := target.VerifyProposal(proposal); err == nil {
		t.Fatal("mutated proposal signature verified")
	}

	validation := &consensus.Validation{
		LedgerID:  source.PrevLedgerID(),
		LedgerSeq: 1,
		SignTime:  time.Unix(1, 0),
		SeenTime:  time.Unix(1, 0),
		Full:      true,
	}
	if err := source.SignValidation(validation); err != nil {
		t.Fatalf("sign validation: %v", err)
	}
	if err := target.VerifyValidation(validation); err != nil {
		t.Fatalf("verify validation: %v", err)
	}
	validation.LedgerSeq++
	if err := target.VerifyValidation(validation); err == nil {
		t.Fatal("mutated validation signature verified")
	}
}

func TestSimDeterministic(t *testing.T) {
	run := func() (uint32, consensus.LedgerID) {
		sim := NewSim()
		peers := sim.SetupFullyConnected(4, time.Millisecond)
		setPeerTiming(t, peers.Peers(), fastSimTiming())
		for _, peer := range peers.Peers() {
			peer.Submit(Tx{ID: uint32(peer.ID) + 1})
		}
		if err := sim.Run(2); err != nil {
			t.Fatalf("run: %v", err)
		}
		ledger := peers.Get(0).LastClosedLedger()
		if err := sim.Stop(); err != nil {
			t.Fatalf("stop: %v", err)
		}
		return ledger.Seq(), ledger.ID()
	}
	seqA, idA := run()
	seqB, idB := run()
	if seqA != seqB || idA != idB {
		t.Fatalf("runs differ: (%d, %x) != (%d, %x)", seqA, idA, seqB, idB)
	}
}

func fastSimTiming() consensus.Timing {
	return consensus.Timing{
		LedgerMinClose:               50 * time.Millisecond,
		LedgerMinConsensus:           100 * time.Millisecond,
		LedgerMaxConsensus:           2 * time.Second,
		LedgerAbandonConsensus:       10 * time.Second,
		LedgerAbandonConsensusFactor: 10,
		LedgerIdleInterval:           100 * time.Millisecond,
		LedgerGranularity:            10 * time.Millisecond,
		ProposeFreshness:             time.Second,
		ProposeInterval:              50 * time.Millisecond,
		ValidationFreshness:          time.Second,
	}
}

func setPeerTiming(t *testing.T, peers []*Peer, timing consensus.Timing) {
	t.Helper()
	for _, peer := range peers {
		if err := peer.SetParms(timing); err != nil {
			t.Fatalf("set peer %d timing: %v", peer.ID, err)
		}
	}
}

func runUntilSynchronized(t *testing.T, sim *Sim, limit SimDuration) {
	t.Helper()
	if sim.SynchronizedAll() {
		return
	}
	deadline := sim.Now() + SimTime(limit)
	for _, peer := range sim.Peers() {
		peer.SetTargetLedgers(math.MaxInt)
		if err := peer.Start(); err != nil {
			t.Fatalf("resume peer %d: %v", peer.ID, err)
		}
	}
	sim.Scheduler.StepWhile(func() bool {
		return !sim.SynchronizedAll() && sim.Now() < deadline
	})
}

func logPeerState(t *testing.T, peers []*Peer) {
	t.Helper()
	for _, peer := range peers {
		lcl := peer.LastClosedLedger()
		fvl := peer.FullyValidatedLedger()
		lclID := lcl.ID()
		fvlID := fvl.ID()
		t.Logf(
			"peer %d LCL=(%d,%x) FVL=(%d,%x) completed=%d",
			peer.ID,
			lcl.Seq(),
			lclID[:4],
			fvl.Seq(),
			fvlID[:4],
			peer.CompletedLedgers(),
		)
	}
}

type foreignLedger struct{}

func (foreignLedger) ID() consensus.LedgerID       { return consensus.LedgerID{1} }
func (foreignLedger) Seq() uint32                  { return 1 }
func (foreignLedger) ParentID() consensus.LedgerID { return consensus.LedgerID{} }
func (foreignLedger) CloseTime() time.Time         { return time.Unix(1, 0) }
func (foreignLedger) TxSetID() consensus.TxSetID   { return consensus.TxSetID{} }
func (foreignLedger) Bytes() []byte                { return []byte{1} }

func assertPanics(t *testing.T, operation func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("operation did not panic")
		}
	}()
	operation()
}

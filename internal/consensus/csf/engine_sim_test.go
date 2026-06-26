package csf

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// shortSimTiming returns consensus timings compressed for simulation:
// real consensus cadence relative scaling, but small absolute virtual
// durations so a multi-ledger run completes in a bounded number of ticks.
func shortSimTiming() consensus.Timing {
	return consensus.Timing{
		LedgerMinClose:               1 * time.Second,
		LedgerMinConsensus:           1 * time.Second,
		LedgerMaxConsensus:           5 * time.Second,
		LedgerAbandonConsensus:       30 * time.Second,
		LedgerAbandonConsensusFactor: 10,
		LedgerIdleInterval:           1 * time.Second,
		LedgerGranularity:            100 * time.Millisecond,
		ProposeFreshness:             20 * time.Second,
		ProposeInterval:              12 * time.Second,
		ValidationFreshness:          20 * time.Second,
	}
}

// engineSim wires N EnginePeers into a fully-connected network on a shared
// discrete-event scheduler, each driving a real rcl.Engine.
type engineSim struct {
	sched *Scheduler
	net   *BasicNetwork
	reg   *engineRegistry
	peers []*EnginePeer
}

func newEngineSim(numPeers int, delay SimDuration) *engineSim {
	sched := NewScheduler()
	net := NewBasicNetwork(sched)
	reg := newEngineRegistry()

	trusted := make([]consensus.NodeID, numPeers)
	for i := range numPeers {
		trusted[i] = nodeIDFor(PeerID(i))
	}
	// Reachable quorum for full mesh: a peer hears the other N-1 validations
	// (and possibly its own), so N-1 is always attainable.
	quorum := max(numPeers-1, 1)

	timing := shortSimTiming()
	peers := make([]*EnginePeer, numPeers)
	for i := range numPeers {
		peers[i] = newEnginePeer(PeerID(i), sched, net, reg, trusted, quorum, timing.LedgerGranularity, timing)
		reg.add(peers[i])
	}
	for i := range numPeers {
		for j := i + 1; j < numPeers; j++ {
			net.Connect(PeerID(i), PeerID(j), delay)
		}
	}
	return &engineSim{sched: sched, net: net, reg: reg, peers: peers}
}

func (s *engineSim) run(t *testing.T, until SimTime) {
	t.Helper()
	ctx := t.Context()
	for _, p := range s.peers {
		if err := p.begin(ctx); err != nil {
			t.Fatalf("peer %d begin: %v", p.id, err)
		}
	}
	s.sched.StepUntil(until)
	for _, p := range s.peers {
		p.stop()
	}
}

// TestEngineSim_PeersConvergeOnLedgerChain proves the csf framework drives
// the REAL rcl.Engine: four engine-backed peers must close several ledgers
// and agree, byte-for-byte, on every ledger up to the lowest common
// sequence. A fork (different ledger ID at the same seq) fails the test.
func TestEngineSim_PeersConvergeOnLedgerChain(t *testing.T) {
	sim := newEngineSim(4, 50*time.Millisecond)
	sim.run(t, SimTime(20*time.Second))

	minSeq := ^uint32(0)
	for _, p := range sim.peers {
		l, _ := p.GetLastClosedLedger()
		if l.Seq() < minSeq {
			minSeq = l.Seq()
		}
	}
	if minSeq < 2 {
		t.Fatalf("expected every peer to close at least 2 ledgers; lowest LCL seq = %d", minSeq)
	}

	for seq := uint32(1); seq <= minSeq; seq++ {
		want, err := sim.peers[0].GetLedgerBySeq(seq)
		if err != nil {
			t.Fatalf("peer 0 missing ledger seq %d: %v", seq, err)
		}
		for i, p := range sim.peers {
			got, err := p.GetLedgerBySeq(seq)
			if err != nil {
				t.Fatalf("peer %d missing ledger seq %d: %v", i, seq, err)
			}
			if got.ID() != want.ID() {
				t.Fatalf("fork at seq %d: peer %d has %x, peer 0 has %x",
					seq, i, got.ID(), want.ID())
			}
		}
	}

	// The validation pipeline must also run end to end: every peer crosses
	// the trusted-validation quorum on at least one ledger, and they agree
	// on the first one they fully validate.
	var firstValidated consensus.LedgerID
	for i, p := range sim.peers {
		p.mu.Lock()
		fv := append([]consensus.LedgerID(nil), p.fullyValidated...)
		p.mu.Unlock()
		if len(fv) == 0 {
			t.Fatalf("peer %d fully-validated no ledger", i)
		}
		if i == 0 {
			firstValidated = fv[0]
		} else if fv[0] != firstValidated {
			t.Fatalf("peers disagree on first fully-validated ledger: peer %d %x vs peer 0 %x",
				i, fv[0], firstValidated)
		}
	}
}

// TestEngineSim_Deterministic pins that the virtual-clock-driven engine is
// reproducible: two independent runs of the same scenario must produce the
// identical ledger chain. This is the guarantee the engine clock seam buys
// — no wall-clock leakage into consensus decisions.
func TestEngineSim_Deterministic(t *testing.T) {
	run := func() (uint32, consensus.LedgerID) {
		sim := newEngineSim(4, 50*time.Millisecond)
		sim.run(t, SimTime(20*time.Second))
		l, _ := sim.peers[0].GetLastClosedLedger()
		return l.Seq(), l.ID()
	}
	seq1, id1 := run()
	seq2, id2 := run()
	if seq1 != seq2 || id1 != id2 {
		t.Fatalf("non-deterministic run: #1 seq=%d id=%x, #2 seq=%d id=%x", seq1, id1, seq2, id2)
	}
}

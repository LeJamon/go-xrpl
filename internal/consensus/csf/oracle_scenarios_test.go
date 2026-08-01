package csf

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestCollectorLedgerSnapshots(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	peer := sim.CreateGroup(1).Get(0)
	setPeerTiming(t, []*Peer{peer}, fastSimTiming())

	type transition struct {
		ledger *Ledger
		prior  *Ledger
	}
	var accepts, validations []transition
	sim.AddCollector(CollectorFunc(func(_ PeerID, _ SimTime, event Event) {
		switch event := event.(type) {
		case AcceptLedgerEvent:
			accepts = append(accepts, transition{ledger: event.Ledger, prior: event.Prior})
		case FullyValidateLedgerEvent:
			validations = append(validations, transition{ledger: event.Ledger, prior: event.Prior})
		}
	}))

	if err := sim.Run(3); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(accepts) != 3 {
		t.Fatalf("accept transitions = %d, want 3", len(accepts))
	}
	for i, transition := range accepts {
		wantSeq := uint32(i + 1)
		if transition.ledger.Seq() != wantSeq || transition.prior.Seq() != wantSeq-1 {
			t.Fatalf(
				"accept transition %d = %d <- %d, want %d <- %d",
				i,
				transition.ledger.Seq(),
				transition.prior.Seq(),
				wantSeq,
				wantSeq-1,
			)
		}
		if transition.ledger.ParentID() != transition.prior.ID() {
			t.Fatalf("accept transition %d prior is not the accepted ledger parent", i)
		}
	}
	if len(validations) != 3 {
		t.Fatalf("fully validated transitions = %d, want 3", len(validations))
	}
	for i, transition := range validations {
		if transition.prior == nil {
			t.Fatalf("fully validated transition %d has no prior snapshot", i)
		}
		wantSeq := uint32(i + 1)
		if transition.ledger.Seq() != wantSeq || transition.prior.Seq() != wantSeq-1 {
			t.Fatalf(
				"fully validated transition %d = %d <- %d, want %d <- %d",
				i,
				transition.ledger.Seq(),
				transition.prior.Seq(),
				wantSeq,
				wantSeq-1,
			)
		}
		if !transition.ledger.IsAncestor(transition.prior, sim.Oracle) {
			t.Fatalf("fully validated transition %d changed branch", i)
		}
	}
}

func TestSlowPeersExcludeLateTransactionsFromFirstLedger(t *testing.T) {
	tests := []struct {
		name          string
		slowPeers     int
		slowValidator bool
	}{
		{name: "one_slow_validator", slowPeers: 1, slowValidator: true},
		{name: "two_slow_validators", slowPeers: 2, slowValidator: true},
		{name: "two_slow_observers", slowPeers: 2, slowValidator: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sim := NewSim()
			t.Cleanup(func() {
				if err := sim.Stop(); err != nil {
					t.Errorf("stop: %v", err)
				}
			})
			slow := sim.CreateGroup(test.slowPeers)
			fast := sim.CreateGroup(4)
			all := slow.Union(fast)
			all.Trust(all)
			fast.Connect(fast, 2*time.Millisecond)
			slow.Connect(all, 11*time.Millisecond)
			for _, peer := range slow.Peers() {
				peer.SetRunAsValidator(test.slowValidator)
			}
			timing := fastSimTiming()
			timing.LedgerMinClose = 50 * time.Millisecond
			setPeerTiming(t, all.Peers(), timing)

			for _, peer := range all.Peers() {
				peer.Submit(Tx{ID: uint32(peer.ID)})
			}
			if err := sim.Run(1); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !sim.SynchronizedAll() {
				logPeerState(t, all.Peers())
				t.Fatal("slow topology did not synchronize")
			}
			for _, peer := range all.Peers() {
				ledger := peer.LastClosedLedger()
				for id := uint32(0); id < uint32(slow.Size()); id++ {
					if ledger.Transactions().ContainsTx(Tx{ID: id}) {
						t.Fatalf("peer %d accepted late transaction %d", peer.ID, id)
					}
				}
				for id := uint32(slow.Size()); id < uint32(all.Size()); id++ {
					if !ledger.Transactions().ContainsTx(Tx{ID: id}) {
						t.Fatalf("peer %d omitted fast transaction %d", peer.ID, id)
					}
				}
			}
		})
	}
}

func TestConsensusAgreesToDisagreeOnCloseTime(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	groupA := sim.CreateGroup(2)
	groupB := sim.CreateGroup(2)
	groupC := sim.CreateGroup(2)
	all := groupA.Union(groupB).Union(groupC)
	all.TrustAndConnect(all, 2*time.Millisecond)
	timing := fastSimTiming()
	timing.ProposeFreshness = 20 * time.Second
	timing.ValidationFreshness = 20 * time.Second
	setPeerTiming(t, all.Peers(), timing)

	for range 16 {
		if groupA.Get(0).LastClosedLedger().CloseTimeResolution() < timing.ProposeFreshness {
			break
		}
		if err := sim.Run(1); err != nil {
			t.Fatalf("advance resolution: %v", err)
		}
	}
	if ledger := groupA.Get(0).LastClosedLedger(); ledger.CloseTimeResolution() >= timing.ProposeFreshness {
		t.Fatalf(
			"resolution did not narrow below proposal freshness: seq=%d resolution=%s close_agree=%t",
			ledger.Seq(),
			ledger.CloseTimeResolution(),
			ledger.CloseAgree(),
		)
	}
	for _, peer := range groupA.Peers() {
		peer.SetClockSkew(timing.ProposeFreshness / 2)
	}
	for _, peer := range groupB.Peers() {
		peer.SetClockSkew(timing.ProposeFreshness)
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("skewed round: %v", err)
	}
	if !sim.SynchronizedAll() {
		t.Fatal("peers did not agree on a common ledger")
	}
	for _, peer := range all.Peers() {
		if peer.LastClosedLedger().CloseAgree() {
			t.Fatalf("peer %d unexpectedly reported close-time agreement", peer.ID)
		}
	}
}

func TestConsensusCloseTimeRoundingTransition(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	slow := sim.CreateGroup(2)
	fast := sim.CreateGroup(4)
	all := slow.Union(fast)
	all.Trust(all)
	fast.Connect(fast, 2*time.Millisecond)
	slow.Connect(all, 11*time.Millisecond)
	setPeerTiming(t, all.Peers(), fastSimTiming())

	if err := sim.Run(6); err != nil {
		t.Fatalf("advance to resolution boundary: %v", err)
	}
	if got := all.Get(0).LastClosedLedger().CloseTimeResolution(); got != 30*time.Second {
		t.Fatalf("resolution before transition = %s, want 30s", got)
	}

	before := all.Get(0).Now()
	when := before
	for when.Unix()%30 != 15 || when.Unix()%20 != 15 {
		when = when.Add(time.Second)
	}
	sim.Scheduler.StepFor(when.Sub(before))
	if got := all.Get(0).Now(); !got.Equal(when) {
		t.Fatalf("scheduler advanced to %s, want %s", got, when)
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("last 30-second round: %v", err)
	}
	for _, peer := range all.Peers() {
		ledger := peer.LastClosedLedger()
		if !ledger.CloseAgree() {
			t.Fatalf("peer %d did not agree on the close time", peer.ID)
		}
		if !ledger.CloseTime().After(peer.Now()) {
			t.Fatalf("peer %d close time %s is not ahead of %s", peer.ID, ledger.CloseTime(), peer.Now())
		}
	}

	for _, peer := range all.Peers() {
		if phase := peer.engine.Phase(); phase != consensus.PhaseOpen {
			t.Fatalf("peer %d phase before child submit = %v, want open", peer.ID, phase)
		}
		if mode := peer.engine.Mode(); mode != consensus.ModeProposing {
			t.Fatalf("peer %d mode before child submit = %v, want proposing", peer.ID, mode)
		}
		peer.Submit(Tx{ID: uint32(peer.ID)})
		if pending := peer.GetPendingTxs(); len(pending) != 1 {
			t.Fatalf("peer %d has %d pending transactions after isolated submit, want 1", peer.ID, len(pending))
		}
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("20-second transition round: %v", err)
	}
	if !sim.SynchronizedAll() {
		logPeerState(t, all.Peers())
		t.Fatal("close-time rounding transition forked the network")
	}
	if got := all.Get(0).LastClosedLedger().CloseTimeResolution(); got != 20*time.Second {
		t.Fatalf("resolution after transition = %s, want 20s", got)
	}
}

func TestUNLOverlapForkSweep(t *testing.T) {
	const numPeers = 10
	for overlap := 0; overlap <= numPeers; overlap++ {
		t.Run(fmt.Sprintf("overlap_%d", overlap), func(t *testing.T) {
			sim := NewSim()
			t.Cleanup(func() {
				if err := sim.Stop(); err != nil {
					t.Errorf("stop: %v", err)
				}
			})
			numA := (numPeers - overlap) / 2
			numB := numPeers - numA - overlap
			aOnly := sim.CreateGroup(numA)
			bOnly := sim.CreateGroup(numB)
			common := sim.CreateGroup(overlap)
			a := aOnly.Union(common)
			b := bOnly.Union(common)
			all := a.Union(b)
			a.TrustAndConnect(a, 2*time.Millisecond)
			b.TrustAndConnect(b, 2*time.Millisecond)
			setPeerTiming(t, all.Peers(), fastSimTiming())

			if err := sim.Run(1); err != nil {
				t.Fatalf("seed round: %v", err)
			}
			for _, peer := range all.Peers() {
				seedOpenTx(peer, Tx{ID: uint32(peer.ID)})
				for _, trusted := range sim.TrustGraph.TrustedPeers(peer.ID) {
					seedOpenTx(peer, Tx{ID: uint32(trusted)})
				}
			}
			if err := sim.Run(1); err != nil {
				t.Fatalf("fork round: %v", err)
			}
			if overlap > 4 && !sim.SynchronizedAll() {
				logPeerState(t, all.Peers())
				t.Fatal("network forked with more than 40% UNL overlap")
			}
			if branches := sim.BranchesAll(); overlap <= 4 && branches > 3 {
				t.Fatalf("branches = %d, want at most 3", branches)
			}
		})
	}
}

func TestProductionEngineResolvesDisputedTransactions(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	all := sim.SetupFullyConnected(5, 2*time.Millisecond)
	setPeerTiming(t, all.Peers(), fastSimTiming())
	txMinority := Tx{ID: 98}
	txMajority := Tx{ID: 99}
	for i, peer := range all.Peers() {
		if i < 2 {
			seedOpenTx(peer, txMinority)
		} else {
			seedOpenTx(peer, txMajority)
		}
	}

	if err := sim.Run(1); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !sim.SynchronizedAll() {
		logPeerState(t, all.Peers())
		t.Fatal("disputed transaction positions did not converge")
	}
	for _, peer := range all.Peers() {
		ledger := peer.LastClosedLedger()
		if ledger.Transactions().ContainsTx(txMinority) {
			t.Fatalf("peer %d accepted the minority transaction", peer.ID)
		}
		if !ledger.Transactions().ContainsTx(txMajority) {
			t.Fatalf("peer %d omitted the majority transaction", peer.ID)
		}
	}
}

func TestPreferredBranchSurvivesDisruption(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	groupABD := sim.CreateGroup(2)
	groupCFast := sim.CreateGroup(1)
	groupCSplit := sim.CreateGroup(7)
	notFastC := groupABD.Union(groupCSplit)
	all := notFastC.Union(groupCFast)
	all.Trust(all)
	timing := groupABD.Get(0).timing
	fastDelay := timing.LedgerGranularity / 10
	delay := timing.LedgerGranularity / 5
	all.Connect(groupCFast, fastDelay)
	notFastC.Connect(notFastC, delay)

	disruptor := &preferredBranchDisruptor{
		all:    all,
		fastC:  groupCFast,
		splitC: groupCSplit,
		delay:  delay,
	}
	sim.AddCollector(disruptor)

	if err := sim.Run(1); err != nil {
		t.Fatalf("common-ledger round: %v", err)
	}
	if !sim.SynchronizedAll() {
		t.Fatal("network did not establish a common ledger")
	}
	for _, peer := range groupABD.Peers() {
		peer.InjectTx(peer.LastClosedLedger().Seq(), Tx{ID: 42})
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("split-ledger round: %v", err)
	}
	if sim.SynchronizedAll() {
		t.Fatal("disruption did not split last-closed-ledger state")
	}
	if branches := sim.BranchesAll(); branches != 1 {
		t.Fatalf("validated branches after split = %d, want 1", branches)
	}
	for _, peer := range groupCFast.Union(groupCSplit).Peers() {
		if connected := sim.Net.Peers(peer.ID); len(connected) != 0 {
			t.Fatalf("disrupted C peer %d still has connections: %v", peer.ID, connected)
		}
	}

	for _, peer := range all.Peers() {
		peer.Submit(Tx{ID: uint32(peer.ID)})
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("child-ledger round: %v", err)
	}
	if sim.SynchronizedAll() {
		t.Fatal("child-ledger round unexpectedly synchronized the network")
	}
	if branches := sim.BranchesAll(); branches != 1 {
		t.Fatalf("validated branches after child-ledger round = %d, want 1", branches)
	}
	children := make(map[LedgerID]PeerID)
	for _, peer := range groupCFast.Union(groupCSplit).Peers() {
		ledger := peer.LastClosedLedger()
		if prior, duplicate := children[ledger.ID()]; duplicate {
			t.Fatalf("isolated C peers %d and %d accepted the same child ledger %x (tx set %x, txs %v)",
				prior, peer.ID, ledger.ID(), ledger.TxSetID(), ledger.Transactions().Transactions())
		}
		children[ledger.ID()] = peer.ID
	}
	if err := sim.Run(1); err != nil {
		t.Fatalf("recovery round: %v", err)
	}
	if branches := sim.BranchesAll(); branches != 1 {
		t.Fatalf("validated branches after recovery = %d, want 1", branches)
	}
	if !sim.SynchronizedAll() {
		logPeerState(t, all.Peers())
		t.Fatal("network did not converge on the preferred branch")
	}
}

func TestWrongLCLRecovery(t *testing.T) {
	for _, test := range []struct {
		name              string
		validationDelay   time.Duration
		useLedgerMinClose bool
		requireFVLJump    bool
		closeJumps        int
	}{
		{name: "zero_validation_delay", closeJumps: 2},
		{name: "ledger_min_close_validation_delay", useLedgerMinClose: true, requireFVLJump: true, closeJumps: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sim := NewSim()
			t.Cleanup(func() {
				if err := sim.Stop(); err != nil {
					t.Errorf("stop: %v", err)
				}
			})
			minority := sim.CreateGroup(2)
			majorityA := sim.CreateGroup(3)
			majorityB := sim.CreateGroup(5)
			majority := majorityA.Union(majorityB)
			all := minority.Union(majority)
			timing := all.Get(0).timing
			minority.TrustAndConnect(minority.Union(majorityA), timing.LedgerGranularity/5)
			majority.TrustAndConnect(majority, timing.LedgerGranularity/5)

			jumps := make(map[PeerID]*ledgerJumps)
			wrongLCL := make(map[PeerID]int)
			sim.AddCollector(CollectorFunc(func(peer PeerID, _ SimTime, event Event) {
				peerJumps := jumps[peer]
				if peerJumps == nil {
					peerJumps = &ledgerJumps{}
					jumps[peer] = peerJumps
				}
				switch event := event.(type) {
				case AcceptLedgerEvent:
					if event.Prior != nil && event.Ledger.ParentID() != event.Prior.ID() {
						peerJumps.closed = append(peerJumps.closed, ledgerTransition{from: event.Prior, to: event.Ledger})
					}
				case FullyValidateLedgerEvent:
					if event.Prior != nil && event.Ledger.ParentID() != event.Prior.ID() {
						peerJumps.validated = append(peerJumps.validated, ledgerTransition{from: event.Prior, to: event.Ledger})
					}
				case WrongPrevLedgerEvent:
					wrongLCL[peer]++
				}
			}))
			if err := sim.Run(1); err != nil {
				t.Fatalf("seed round: %v", err)
			}
			validationDelay := test.validationDelay
			if test.useLedgerMinClose {
				validationDelay = timing.LedgerMinClose
			}
			for _, peer := range all.Peers() {
				if err := peer.SetValidationReceiveDelay(validationDelay); err != nil {
					t.Fatalf("peer %d validation delay: %v", peer.ID, err)
				}
			}
			for _, peer := range minority.Union(majorityA).Peers() {
				seedOpenTx(peer, Tx{ID: 0})
			}
			for _, peer := range majorityB.Peers() {
				seedOpenTx(peer, Tx{ID: 1})
			}
			if err := sim.Run(3); err != nil {
				t.Fatalf("recovery rounds: %v", err)
			}
			if branches := sim.BranchesAll(); branches != 1 {
				logPeerState(t, all.Peers())
				t.Fatalf("validated branches = %d, want 1", branches)
			}
			for _, peer := range majority.Peers() {
				assertNoLedgerJumps(t, peer, jumps[peer.ID])
			}
			for _, peer := range minority.Peers() {
				assertWrongLCLJumps(t, sim, peer, jumps[peer.ID], test.closeJumps, test.requireFVLJump)
				if wrongLCL[peer.ID] == 0 {
					t.Fatalf("minority peer %d never detected the wrong prior ledger", peer.ID)
				}
			}
		})
	}
}

func TestWrongLCLSwitchDuringEstablish(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	loner := sim.CreateGroup(1)
	friends := sim.CreateGroup(3)
	others := sim.CreateGroup(6)
	clique := friends.Union(others)
	all := loner.Union(clique)
	loner.Trust(loner.Union(friends))
	clique.Trust(clique)
	timing := all.Get(0).timing
	all.Connect(all, timing.LedgerGranularity/5)

	if err := sim.Run(1); err != nil {
		t.Fatalf("seed round: %v", err)
	}
	for _, peer := range loner.Union(friends).Peers() {
		seedOpenTx(peer, Tx{ID: 0})
	}
	for _, peer := range others.Peers() {
		seedOpenTx(peer, Tx{ID: 1})
	}
	for _, peer := range all.Peers() {
		if err := peer.SetValidationReceiveDelay(timing.LedgerGranularity); err != nil {
			t.Fatalf("peer %d validation delay: %v", peer.ID, err)
		}
	}
	if err := sim.Run(2); err != nil {
		t.Fatalf("recovery rounds: %v", err)
	}
	// The bounded run completes before the delayed sequence-3 validations are
	// processed. Exercise the next start's one-shot heartbeat before comparing
	// each engine's selected prior ledger.
	if err := sim.Run(0); err != nil {
		t.Fatalf("recovery heartbeat: %v", err)
	}
	want := all.Get(0).PrevLedgerID()
	for _, peer := range all.Peers()[1:] {
		if got := peer.PrevLedgerID(); got != want {
			logPeerState(t, all.Peers())
			t.Fatalf("peer %d prior ledger = %x, want %x", peer.ID, got, want)
		}
	}
}

func TestPauseForLaggards(t *testing.T) {
	sim := NewSim()
	t.Cleanup(func() {
		if err := sim.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	behind := sim.CreateGroup(3)
	ahead := sim.CreateGroup(2)
	all := ahead.Union(behind)
	all.TrustAndConnect(all, all.Get(0).timing.LedgerGranularity/5)

	if err := sim.Run(1); err != nil {
		t.Fatalf("seed round: %v", err)
	}
	delayConfiguredAt := sim.Now()
	for _, peer := range behind.Peers() {
		if err := peer.SetLedgerAcceptDelay(20 * time.Second); err != nil {
			t.Fatalf("peer %d ledger accept delay: %v", peer.ID, err)
		}
	}
	firstDelayedAccept := make(map[PeerID]SimTime)
	sim.AddCollector(CollectorFunc(func(peerID PeerID, when SimTime, event Event) {
		if _, ok := event.(AcceptLedgerEvent); !ok {
			return
		}
		for _, peer := range behind.Peers() {
			if peer.ID != peerID {
				continue
			}
			if _, seen := firstDelayedAccept[peerID]; !seen {
				firstDelayedAccept[peerID] = when
			}
			if err := peer.SetLedgerAcceptDelay(0); err != nil {
				t.Errorf("reset peer %d ledger accept delay: %v", peer.ID, err)
			}
			return
		}
	}))

	start := sim.Now()
	submitTo := [...]int{3, 1, 3, 4, 0, 2, 1, 0, 2, 1, 1, 2, 0, 2, 2, 4, 2, 2, 3, 1, 1}
	for i := range submitTo {
		sim.Scheduler.At(start+SimTime(time.Duration(i)*5*time.Second), func() {
			all.Get(submitTo[i]).Submit(Tx{ID: uint32(i)})
		})
	}
	for _, peer := range all.Peers() {
		peer.SetTargetLedgers(math.MaxInt)
		if err := peer.Start(); err != nil {
			t.Fatalf("start peer %d: %v", peer.ID, err)
		}
	}
	sim.Scheduler.StepFor(100 * time.Second)

	for _, peer := range behind.Peers() {
		acceptedAt, ok := firstDelayedAccept[peer.ID]
		if !ok {
			t.Fatalf("behind peer %d never completed its delayed ledger accept", peer.ID)
		}
		if elapsed := time.Duration(acceptedAt - delayConfiguredAt); elapsed < 20*time.Second {
			t.Fatalf("behind peer %d accepted after %s, want at least 20s", peer.ID, elapsed)
		}
	}
	if !sim.SynchronizedAll() {
		logPeerState(t, all.Peers())
		t.Fatal("network did not recover after laggards caught up")
	}
}

type ledgerTransition struct {
	from *Ledger
	to   *Ledger
}

type ledgerJumps struct {
	closed    []ledgerTransition
	validated []ledgerTransition
}

func assertNoLedgerJumps(t *testing.T, peer *Peer, jumps *ledgerJumps) {
	t.Helper()
	if jumps != nil && (len(jumps.closed) != 0 || len(jumps.validated) != 0) {
		t.Fatalf("majority peer %d jumps = closed:%d validated:%d, want none", peer.ID, len(jumps.closed), len(jumps.validated))
	}
}

func assertWrongLCLJumps(t *testing.T, sim *Sim, peer *Peer, jumps *ledgerJumps, wantClose int, requireFVLJump bool) {
	t.Helper()
	if jumps == nil || len(jumps.closed) != wantClose {
		got := 0
		if jumps != nil {
			got = len(jumps.closed)
		}
		t.Fatalf("minority peer %d closed-ledger jumps = %d, want %d", peer.ID, got, wantClose)
	}
	for _, closed := range jumps.closed {
		if closed.from.Seq() > closed.to.Seq() || closed.to.IsAncestor(closed.from, sim.Oracle) {
			t.Fatalf("minority peer %d invalid cross-branch close jump %d -> %d", peer.ID, closed.from.Seq(), closed.to.Seq())
		}
	}
	if len(jumps.validated) == 0 {
		if requireFVLJump {
			t.Fatalf("minority peer %d did not jump its fully validated ledger", peer.ID)
		}
		return
	}
	if len(jumps.validated) != 1 {
		t.Fatalf("minority peer %d validated-ledger jumps = %d, want at most 1", peer.ID, len(jumps.validated))
	}
	validated := jumps.validated[0]
	if validated.from.Seq() >= validated.to.Seq() || !validated.to.IsAncestor(validated.from, sim.Oracle) {
		t.Fatalf("minority peer %d invalid same-branch validation jump %d -> %d", peer.ID, validated.from.Seq(), validated.to.Seq())
	}
}

type preferredBranchDisruptor struct {
	all          *PeerGroup
	fastC        *PeerGroup
	splitC       *PeerGroup
	delay        SimDuration
	disconnected bool
	reconnected  bool
}

func (d *preferredBranchDisruptor) On(peer PeerID, _ SimTime, event Event) {
	switch event := event.(type) {
	case FullyValidateLedgerEvent:
		if !d.disconnected && peer == d.fastC.Get(0).ID && event.Ledger.Seq() == 2 {
			d.disconnected = true
			d.all.Disconnect(d.splitC)
			d.all.Disconnect(d.fastC)
		}
	case AcceptLedgerEvent:
		if d.disconnected && !d.reconnected && event.Ledger.Seq() == 3 {
			d.reconnected = true
			d.all.Connect(d.splitC, d.delay)
		}
	}
}

func seedOpenTx(peer *Peer, tx Tx) {
	blob := tx.Bytes()
	id := tx.TxID()
	peer.mu.Lock()
	peer.openTxs[id] = blob
	peer.seenTxs[id] = struct{}{}
	peer.mu.Unlock()
}

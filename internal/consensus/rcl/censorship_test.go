package rcl

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func censorTxID(n int) consensus.TxID {
	var id consensus.TxID
	binary.BigEndian.PutUint32(id[:4], uint32(n))
	return id
}

func censorTxIDs(ns []int) []consensus.TxID {
	ids := make([]consensus.TxID, len(ns))
	for i, n := range ns {
		ids[i] = censorTxID(n)
	}
	return ids
}

func censorTxIDSet(ns []int) map[consensus.TxID]struct{} {
	s := make(map[consensus.TxID]struct{}, len(ns))
	for _, n := range ns {
		s[censorTxID(n)] = struct{}{}
	}
	return s
}

// TestCensorshipDetector ports rippled's RCLCensorshipDetector_test: after a
// propose/check round, every tx listed in remove is force-dropped, and every
// tx listed in remain must still be tracked (so pred visits it and empties the
// set).
func TestCensorshipDetector(t *testing.T) {
	var cdet censorshipDetector
	round := 0

	run := func(proposed, accepted, remain, remove []int) {
		t.Helper()
		round++
		cdet.propose(censorTxIDs(proposed), uint32(round))

		removeSet := censorTxIDSet(remove)
		remainSet := censorTxIDSet(remain)
		cdet.check(censorTxIDs(accepted), func(id consensus.TxID, _ uint32) bool {
			if _, ok := removeSet[id]; ok {
				return true
			}
			delete(remainSet, id)
			return false
		})
		if len(remainSet) != 0 {
			t.Fatalf("round %d: %d expected-remaining tx not tracked", round, len(remainSet))
		}
	}

	//   proposed              accepted   remain        remove
	run([]int{}, []int{}, []int{}, []int{})
	run([]int{10, 11, 12, 13}, []int{11, 2}, []int{10, 13}, []int{})
	run([]int{10, 13, 14, 15}, []int{14}, []int{10, 13, 15}, []int{})
	run([]int{10, 13, 15, 16}, []int{15, 16}, []int{10, 13}, []int{})
	run([]int{10, 13}, []int{17, 18}, []int{10, 13}, []int{})
	run([]int{10, 19}, []int{}, []int{10, 19}, []int{})
	run([]int{10, 19, 20}, []int{20}, []int{10}, []int{19})
	run([]int{21}, []int{21}, []int{}, []int{})
	run([]int{}, []int{22}, []int{}, []int{})
	run([]int{23, 24, 25, 26}, []int{25, 27}, []int{23, 26}, []int{24})
	run([]int{23, 26, 28}, []int{26, 28}, []int{23}, []int{})

	for i := 0; i != 10; i++ {
		run([]int{23}, []int{}, []int{23}, []int{})
	}

	run([]int{23, 29}, []int{29}, []int{23}, []int{})
	run([]int{30, 31}, []int{31}, []int{30}, []int{})
	run([]int{30}, []int{30}, []int{}, []int{})
	run([]int{}, []int{}, []int{}, []int{})
}

// TestCensorshipDetectorSeqPreserved verifies a carried-over tx keeps its
// original tracking seq across rounds — the property that lets the wait grow
// so a persistently-excluded tx eventually crosses the warn interval.
func TestCensorshipDetectorSeqPreserved(t *testing.T) {
	var cdet censorshipDetector
	x := censorTxID(42)

	cdet.propose([]consensus.TxID{x}, 5)
	cdet.propose([]consensus.TxID{x}, 6)
	cdet.propose([]consensus.TxID{x}, 7)

	var gotSeq uint32
	var seen bool
	cdet.check(nil, func(id consensus.TxID, seq uint32) bool {
		if id == x {
			gotSeq = seq
			seen = true
		}
		return false
	})
	if !seen {
		t.Fatal("tx not tracked after repeated proposes")
	}
	if gotSeq != 5 {
		t.Fatalf("tracking seq = %d, want 5 (original round preserved)", gotSeq)
	}
}

// TestCensorshipDetectorReset clears the tracker.
func TestCensorshipDetectorReset(t *testing.T) {
	var cdet censorshipDetector
	cdet.propose(censorTxIDs([]int{1, 2, 3}), 10)
	cdet.reset()

	called := false
	cdet.check(nil, func(consensus.TxID, uint32) bool {
		called = true
		return false
	})
	if called {
		t.Fatal("pred invoked after reset; tracker not cleared")
	}
}

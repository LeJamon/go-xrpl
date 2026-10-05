package shamap

import "testing"

func TestBackedWalkRetrySuspensionPreservesPath(t *testing.T) {
	sm := New(TypeState)
	cursor := &backedWalkCursor{}
	lane := backedWalkLane{cursor: cursor}
	preexisting := backedWalkFrame{
		item:       backedWalkItem{hash: [32]byte{1}, depth: 2},
		nextBranch: 7,
	}
	retry := backedWalkFrame{
		item:       backedWalkItem{hash: [32]byte{2}, depth: 3, parentHash: [32]byte{1}},
		topLevel:   true,
		proofStart: 1,
		retry:      true,
	}
	lane.stack = []backedWalkFrame{preexisting}
	lane.retryStack = []backedWalkFrame{retry, {item: backedWalkItem{hash: [32]byte{3}}}}
	lane.proofs.add([32]byte{4}, 2)
	lane.proofs.add([32]byte{5}, 3)

	if !sm.suspendBackedWalkRetry(&lane) {
		t.Fatal("retry frame was not found")
	}
	if len(lane.stack) != 1 || lane.stack[0].item.hash != preexisting.item.hash || lane.stack[0].nextBranch != 7 {
		t.Fatalf("suspended path = %+v", lane.stack)
	}
	if lane.proofs.count() != 1 {
		t.Fatalf("suspended proofs = %d, want one prefix proof", lane.proofs.count())
	}
	if !lane.retryHeld || lane.retryHold.hash != retry.item.hash || len(lane.retryStack) != 0 {
		t.Fatalf("suspended retry hold = held=%t item=%+v stack=%d", lane.retryHeld, lane.retryHold, len(lane.retryStack))
	}
}

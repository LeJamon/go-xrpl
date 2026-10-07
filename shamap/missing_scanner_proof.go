package shamap

import "unsafe"

// These bits belong to the cursor's root and durable generation. Unlike the
// shared cache, admitted proofs remain available until their branch completes.
type backedScanProof struct {
	children [BranchFactor]*backedScanProof
	complete uint16
	seen     uint16
}

const (
	backedScanProofBytes = 2 * uint64(unsafe.Sizeof(backedScanProof{}))
	backedScanProofLimit = 1 << 16
)

// Only an unresolved path needs retained metadata. Completed siblings before
// the first missing child are carried by the DFS frames until this admission.
func (sm *SHAMap) retainBackedScanProofs(cursor *backedScanCursor, lane *backedScanLane) {
	var parent *backedScanProof
	for i := 0; i < len(lane.stack)-1; i++ {
		frame := &lane.stack[i]
		if frame.proof == nil {
			if cursor.proofCount >= backedScanProofLimit {
				return
			}
			// Leave room for DFS stacks. Untracked branches continue through
			// the bounded walk and shared cache when proof admission fails.
			if cursor.budget != nil && (cursor.proofCount+1)*backedScanProofBytes > cursor.budget.limit/16 {
				return
			}
			if !sm.reserveCursorBytes(scanCursorBudget(cursor), backedScanProofBytes) {
				return
			}
			frame.proof = &backedScanProof{complete: frame.complete, seen: frame.seen}
			cursor.proofCount++
			if parent == nil {
				lane.proof = frame.proof
			} else {
				parent.children[frame.item.branch] = frame.proof
			}
		}
		parent = frame.proof
	}
}

func (sm *SHAMap) releaseBackedScanProof(cursor *backedScanCursor, proof *backedScanProof) {
	if proof == nil {
		return
	}
	for _, child := range proof.children {
		sm.releaseBackedScanProof(cursor, child)
	}
	cursor.proofCount--
	sm.releaseCursorBytes(scanCursorBudget(cursor), backedScanProofBytes)
}

func (sm *SHAMap) completeBackedScanProof(cursor *backedScanCursor, lane *backedScanLane, frame *backedScanFrame) {
	sm.releaseBackedScanProof(cursor, frame.proof)
	if len(lane.stack) == 0 {
		lane.proof = nil
		return
	}
	parent := &lane.stack[len(lane.stack)-1]
	if frame.stored && !frame.usedBase {
		parent.complete |= 1 << frame.item.branch
	}
	if parent.proof != nil {
		parent.proof.children[frame.item.branch] = nil
		parent.proof.complete = parent.complete
	}
}

func (sm *SHAMap) observeBackedScanFrame(lane *backedScanLane, frame *backedScanFrame) {
	if frame.fresh {
		sm.acquisition.stats.traversalDiscoveries.Add(1)
	}
	if len(lane.stack) == 1 {
		lane.seen = true
		return
	}
	parent := &lane.stack[len(lane.stack)-2]
	parent.seen |= 1 << frame.item.branch
	if parent.proof != nil {
		parent.proof.seen = parent.seen
	}
}

package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// completedCatchUpAcquisition builds a StateComplete InboundLedger for a ledger
// at `seq` whose parent hash is deliberately absent from local history — the
// exact shape a deep catch-up produces. The state and transaction trees are
// empty so the acquisition completes on GotBase alone, letting the test exercise
// the router's completion path without driving a full node-by-node sync.
func completedCatchUpAcquisition(t *testing.T, seq uint32) *inbound.Ledger {
	t.Helper()

	var parentHash [32]byte
	parentHash[0] = 0xEE // not in local history — forces the parentless path

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	hdr := header.LedgerHeader{
		LedgerIndex: seq,
		ParentHash:  parentHash,
		AccountHash: rootHash,
		// TxHash left zero: empty tx tree, complete once the state tree is filled.
	}
	data := header.AddRaw(hdr, false)
	ledgerHash := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), data)

	il := inbound.New(ledgerHash, seq, 7, serveTestLogger())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: data},
		{NodeData: rootData},
	}))
	require.NoError(t, il.GotStateNodes(wire))
	require.True(t, il.IsComplete(),
		"state + empty tx acquisition must be complete after its nodes arrive")
	return il
}

// buildSelfHealSourceState builds a multi-level state SHAMap and returns its root
// hash, serialized root, and the wire nodes that complete the tree.
func buildSelfHealSourceState(t *testing.T) (rootHash [32]byte, rootData []byte, wire []message.LedgerNode) {
	t.Helper()
	source := shamap.New(shamap.TypeState)
	for branch := range byte(4) {
		for sub := range byte(4) {
			for i := range byte(4) {
				var key [32]byte
				key[0] = (branch << 4) | sub
				key[1] = i << 4
				key[31] = 0xA5 // TypeState rejects zero keys at the leaf
				require.NoError(t, source.Put(key,
					[]byte{branch, sub, i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}))
			}
		}
	}
	rootHash, err := source.Hash()
	require.NoError(t, err)
	rootData, err = source.SerializeRoot()
	require.NoError(t, err)
	wireNodes, err := source.WalkWireNodes()
	require.NoError(t, err)
	for _, w := range wireNodes {
		wire = append(wire, message.LedgerNode{NodeID: w.NodeID, NodeData: w.Data})
	}
	return rootHash, rootData, wire
}

// TestCompleteInboundLedger_CatchUpJumpAdoptsTip pins the issue #1161 self-heal
// fix: when a legacy catch-up acquisition completes for a ledger two or more
// ahead of our working ledger (its parent chain absent), the router adopts the
// acquired tip directly — jumping the closed ledger forward so consensus can
// rejoin — instead of stashing it and arming a backward parent chase that a busy
// network outruns. Against the pre-fix behaviour the tip stashed (closed stayed
// pinned behind the gap) and a backward parent acquisition was armed; both
// assertions below flip.
func TestCompleteInboundLedger_CatchUpJumpAdoptsTip(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closedSeq := svc.GetClosedLedgerIndex()

	tipSeq := closedSeq + 30 // deep gap: the parent chain is absent
	il := completedCatchUpAcquisition(t, tipSeq)
	r.fetchTracker.Track(il)

	r.completeInboundLedger(il)

	assert.Equal(t, tipSeq, svc.GetClosedLedgerIndex(),
		"closed ledger must jump straight to the acquired tip, not stay pinned behind the gap")
	require.NotNil(t, svc.GetClosedLedger())
	assert.Equal(t, il.Hash(), svc.GetClosedLedger().Hash(),
		"the adopted working ledger must be the acquired tip")

	assert.Empty(t, rs.legacyCalls(),
		"catch-up jump must not arm a backward parent chase")
	assert.Empty(t, rs.replayCalls(),
		"catch-up jump must not arm a backward parent chase")
}

// TestCompleteInboundLedger_SingleLedgerCatchUpUsesHeldSeam verifies the fix is
// scoped: a completion only one ledger ahead of our working ledger (gap == 1,
// whose parent is our current closed ledger) is NOT treated as a jump. It routes
// through the held-adoption seam and adopts against the present parent, so the
// out-of-order cascade machinery the service unit tests pin is preserved.
func TestCompleteInboundLedger_SingleLedgerCatchUpUsesHeldSeam(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)

	rootHash, rootData, wire := buildSelfHealSourceState(t)
	hdr := header.LedgerHeader{
		LedgerIndex: parent.Sequence() + 1,
		ParentHash:  parent.Hash(), // parent present → fast-path adopt, not a jump
		AccountHash: rootHash,
	}
	data := header.AddRaw(hdr, false)
	ledgerHash := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), data)

	il := inbound.New(ledgerHash, parent.Sequence()+1, 7, serveTestLogger())
	require.NoError(t, il.GotBase([]message.LedgerNode{
		{NodeData: data},
		{NodeData: rootData},
	}))
	require.NoError(t, il.GotStateNodes(wire))
	require.True(t, il.IsComplete())
	r.fetchTracker.Track(il)

	r.completeInboundLedger(il)

	assert.Equal(t, parent.Sequence()+1, svc.GetClosedLedgerIndex(),
		"a single-ledger catch-up must still advance the working ledger via the held seam")
	assert.Empty(t, rs.legacyCalls(), "an in-order single-ledger adopt must not chase a parent")
	assert.Empty(t, rs.replayCalls(), "an in-order single-ledger adopt must not chase a parent")
}

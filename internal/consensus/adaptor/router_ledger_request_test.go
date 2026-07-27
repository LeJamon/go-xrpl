package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRouter_RequestLedger_TriggersGenericAcquisition covers the ledger_request
// coordinator: with a connected peer, a request for a missing ledger selects
// that peer, issues a base fetch, registers a ReasonGeneric acquisition, and
// reports the in-flight snapshot. A repeat request joins the same acquisition.
func TestRouter_RequestLedger_TriggersGenericAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	// Register a peer reporting our own tip — matching hash means no catch-up
	// acquisition fires on its own, so any later base request is ours.
	r.handleMessage(statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash()))
	require.Empty(t, rs.legacyCalls())

	var target [32]byte
	target[0] = 0x42

	snap, started, reference := r.RequestLedger(target, 0)
	require.True(t, started)
	require.False(t, reference, "a by-hash request acquires the target itself, not a reference ledger")
	require.NotNil(t, snap)
	assert.Equal(t, false, snap["have_header"])

	calls := rs.legacyCalls()
	require.Len(t, calls, 1, "exactly one base fetch must be issued")
	assert.Equal(t, target, calls[0].hash)
	assert.Equal(t, uint64(7), calls[0].peerID)

	il := r.fetchTracker.Find(target)
	require.NotNil(t, il, "the acquisition must be registered")
	assert.Equal(t, inbound.ReasonGeneric, il.Reason())

	// A second request joins the in-flight acquisition; no duplicate fetch.
	_, started2, _ := r.RequestLedger(target, 0)
	assert.True(t, started2)
	assert.Len(t, rs.legacyCalls(), 1, "repeat request must not re-issue the fetch")
}

// TestRouter_RequestLedger_NoPeers verifies that an acquisition remains
// available for a peer that connects after the RPC request.
func TestRouter_RequestLedger_NoPeers(t *testing.T) {
	r, _, rs, _ := makeRouter(t)

	var target [32]byte
	target[0] = 0x99

	snap, started, _ := r.RequestLedger(target, 0)
	require.True(t, started)
	require.NotNil(t, snap)
	assert.Empty(t, rs.legacyCalls())
	il := r.fetchTracker.Find(target)
	require.NotNil(t, il)
	assert.Equal(t, inbound.ReasonGeneric, il.Reason())
	assert.Empty(t, il.Peers())

	trackCatchupPeer(r, 7, 1)
	r.escalateAcquisition(il, time.Now().Add(4*time.Second))

	calls := rs.legacyCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(7), calls[0].peerID)
	assert.Equal(t, []uint64{7}, il.Peers())
}

func TestGenericAcquisitionJoinedByConsensusNotifiesExactTarget(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	engine := &mockEngine{switchResult: consensus.LedgerSwitchAccepted}
	r.engine = engine
	rootHash, rootData, wire := buildSelfHealSourceState(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	hdr := header.LedgerHeader{
		LedgerIndex: closed.Sequence() + 10,
		ParentHash:  closed.Hash(),
		AccountHash: rootHash,
	}
	headerData := header.AddRaw(hdr, false)
	target := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	acquired := inbound.NewGeneric(target, hdr.LedgerIndex, 7, serveTestLogger())
	require.NoError(t, acquired.GotBase([]message.LedgerNode{
		{NodeData: headerData},
		{NodeData: rootData},
	}))
	require.NoError(t, acquired.GotStateNodes(wire))
	acquired.CollectMissingRequest(false)
	require.True(t, acquired.IsComplete())
	r.fetchTracker.Track(acquired)
	r.consensusRecovery = consensusRecovery{targetHash: target, stepHash: target}

	r.completeInboundLedger(acquired)

	require.Equal(t, []consensus.LedgerID{consensus.LedgerID(target)}, engine.getLedgers())
	require.Equal(t, consensusRecovery{anchorHash: target, anchorSeq: hdr.LedgerIndex}, r.consensusRecovery)
}

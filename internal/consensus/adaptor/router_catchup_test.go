// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package adaptor

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statusChangeMessage builds a wire-framed TMStatusChange for the given
// seq/hash and returns the InboundMessage the Router's dispatch expects.
func statusChangeMessage(t *testing.T, peerID peermanagement.PeerID, seq uint32, hash [32]byte) *peermanagement.InboundMessage {
	t.Helper()
	sc := &message.StatusChange{
		NewStatus:  message.NodeStatus(0),
		NewEvent:   message.NodeEventClosingLedger,
		LedgerSeq:  seq,
		LedgerHash: hash[:],
	}
	encoded, err := message.Encode(sc)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, message.WriteMessage(&buf, message.TypeStatusChange, encoded))
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    uint16(message.TypeStatusChange),
		Payload: encoded,
	}
}

// TestRouter_HashDivergenceAtSameSeq_AcquiresPeerTip verifies the
// continuous-catchup hole we closed: when a peer reports the same seq
// as ours but with a different ledger hash, the router must acquire
// the peer's ledger — otherwise isolated consensus never reconverges.
//
// Mirrors rippled's wrongLedger recovery path where a node that
// disagrees with the majority on its LCL asks a peer for their fork.
func TestRouter_HashDivergenceAtSameSeq_AcquiresPeerTip(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	ourHash := closed.Hash()
	ourSeq := closed.Sequence()

	// Construct a peer hash that deliberately differs from ours at the
	// same seq — simulating a peer on the real network fork.
	var peerHash [32]byte
	copy(peerHash[:], ourHash[:])
	peerHash[0] ^= 0xFF

	msg := statusChangeMessage(t, peermanagement.PeerID(7), ourSeq, peerHash)
	r.handleMessage(msg)

	// Either replay-delta (if parent is local) or legacy must have been
	// issued for peerHash. Coordinator or InboundLedger must now hold
	// an acquisition.
	replayCalls := rs.replayCalls()
	legacyCalls := rs.legacyCalls()
	totalCalls := len(replayCalls) + len(legacyCalls)
	require.Equal(t, 1, totalCalls,
		"exactly one acquisition request must fire on hash divergence (replay=%d legacy=%d)",
		len(replayCalls), len(legacyCalls))

	assert.True(t, r.replayer.Has(peerHash) || r.inboundLedger != nil,
		"an acquisition state machine must be armed for the peer's hash")

	if len(replayCalls) > 0 {
		assert.Equal(t, peerHash, replayCalls[0].hash)
		assert.Equal(t, uint64(7), replayCalls[0].peerID)
	} else {
		assert.Equal(t, peerHash, legacyCalls[0].hash)
		assert.Equal(t, uint64(7), legacyCalls[0].peerID)
		assert.Equal(t, ourSeq, legacyCalls[0].seq)
	}
}

// TestRouter_SameHashAtSameSeq_NoAcquisition verifies the negative
// case: when the peer's hash matches ours at the same seq, no
// acquisition fires. Otherwise every status-change heartbeat would
// trigger redundant acquisition requests.
func TestRouter_SameHashAtSameSeq_NoAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	msg := statusChangeMessage(t, peermanagement.PeerID(7), closed.Sequence(), closed.Hash())
	r.handleMessage(msg)

	assert.Empty(t, rs.replayCalls(), "no replay-delta request when hashes agree")
	assert.Empty(t, rs.legacyCalls(), "no legacy request when hashes agree")
	assert.Equal(t, 0, r.replayer.Count())
	assert.Nil(t, r.inboundLedger)
}

// TestRouter_CheckBehindArmsAcquisition verifies the checkBehind fix:
// when a peer is far ahead, the router must arm a real acquisition
// (via startLedgerAcquisition), not just broadcast an unresponded
// mtGET_LEDGER. The pre-fix path called RequestLedgerByHashAndSeq
// which broadcasts without arming an InboundLedger, so responses
// arrived with has_inbound=false and got dropped.
func TestRouter_CheckBehindArmsAcquisition(t *testing.T) {
	r, _, rs, svc := makeRouter(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	// Peer reports a seq way ahead of ours; none of the earlier
	// branches (NeedsInitialSync, Full-behind, !Full-behind-by-1)
	// fire because the service has finished initial sync in this
	// test setup and we're not in Full mode. checkBehind is the
	// final branch and must arm the acquisition.
	r.adaptor.operatingMode = 1 // OpModeTracking — not Full, not initial
	var peerHash [32]byte
	peerHash[0] = 0xAB

	msg := statusChangeMessage(t, peermanagement.PeerID(9), closed.Sequence()+100, peerHash)
	r.handleMessage(msg)

	replayCalls := rs.replayCalls()
	legacyCalls := rs.legacyCalls()
	totalCalls := len(replayCalls) + len(legacyCalls)
	require.GreaterOrEqual(t, totalCalls, 1,
		"checkBehind must arm an acquisition when peer is far ahead")
}

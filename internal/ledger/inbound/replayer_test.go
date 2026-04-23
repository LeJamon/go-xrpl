// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package inbound

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/inbound/inboundtest"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hashN returns a deterministic 32-byte test hash derived from an
// integer. Useful when a test needs many distinct hashes and doesn't
// care about their content.
func hashN(n int) [32]byte {
	var h [32]byte
	h[0] = byte(n)
	h[1] = byte(n >> 8)
	h[2] = byte(n >> 16)
	h[3] = byte(n >> 24)
	return h
}

// TestReplayer_Acquire_Unique exercises the base happy path: two
// distinct hashes both register and show up in InFlight.
func TestReplayer_Acquire_Unique(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)

	h1 := hashN(1)
	h2 := hashN(2)

	rd1, err := rep.Acquire(h1, 7, parent)
	require.NoError(t, err)
	require.NotNil(t, rd1)

	rd2, err := rep.Acquire(h2, 8, parent)
	require.NoError(t, err)
	require.NotNil(t, rd2)

	assert.Equal(t, 2, rep.Count())
	in := rep.InFlight()
	require.Len(t, in, 2)
	// InFlight is sorted by Seq (both parents share seq → stable order
	// isn't strictly hash-dependent, so just check both are present).
	got := map[[32]byte]bool{in[0]: true, in[1]: true}
	assert.True(t, got[h1])
	assert.True(t, got[h2])
}

// TestReplayer_Acquire_Duplicate_Rejected verifies that a second
// Acquire for an already-in-flight hash returns ErrAcquisitionExists
// rather than silently double-registering.
func TestReplayer_Acquire_Duplicate_Rejected(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)

	h := hashN(42)
	_, err := rep.Acquire(h, 7, parent)
	require.NoError(t, err)

	rd, err := rep.Acquire(h, 8, parent)
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrAcquisitionExists)
	assert.Equal(t, 1, rep.Count(), "failed duplicate must not bump count")
}

// TestReplayer_Acquire_CapacityFull verifies that the configured cap
// is enforced: the (cap+1)-th Acquire returns ErrCapacityFull.
func TestReplayer_Acquire_CapacityFull(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 2)

	_, err := rep.Acquire(hashN(1), 7, parent)
	require.NoError(t, err)
	_, err = rep.Acquire(hashN(2), 7, parent)
	require.NoError(t, err)

	rd, err := rep.Acquire(hashN(3), 7, parent)
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrCapacityFull)
	assert.Equal(t, 2, rep.Count())
}

// TestReplayer_Stop_Drains pins R5.15: Stop() clears all in-flight
// acquisitions and returns the prior count. Protects against leaking
// map entries across a shutdown→restart cycle and gives operators a
// single "pending at shutdown" number for diagnostics.
func TestReplayer_Stop_Drains(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 10)

	_, err := rep.Acquire(hashN(1), 7, parent)
	require.NoError(t, err)
	_, err = rep.Acquire(hashN(2), 8, parent)
	require.NoError(t, err)
	_, err = rep.Acquire(hashN(3), 9, parent)
	require.NoError(t, err)
	require.Equal(t, 3, rep.Count())

	remaining := rep.Stop()
	assert.Equal(t, 3, remaining,
		"Stop must return the pre-drain in-flight count")
	assert.Equal(t, 0, rep.Count(),
		"Stop must leave the replayer empty so subsequent reuse starts clean")

	// Idempotent: Stop on an empty replayer returns 0, doesn't panic.
	assert.Equal(t, 0, rep.Stop())
}

// TestReplayer_Acquire_PerPeerCap pins R5.14: a single peer cannot
// hold more than MaxPerPeerReplays (=2) concurrent replay-delta
// acquisitions. Mirrors rippled LedgerReplayer.h:55 MAX_PEERS_PER_LEDGER.
// Without this cap, all DefaultMaxInFlightReplays=16 slots could end
// up targeting one silent peer and burning the whole catchup budget.
func TestReplayer_Acquire_PerPeerCap(t *testing.T) {
	parent := makeGenesisLedger(t)
	// Global cap well above per-peer cap so we can exercise the latter.
	rep := NewReplayer(nil, nil, 10)

	// Peer 7: first two acquisitions succeed.
	_, err := rep.Acquire(hashN(1), 7, parent)
	require.NoError(t, err)
	_, err = rep.Acquire(hashN(2), 7, parent)
	require.NoError(t, err)

	// Third on peer 7 hits the per-peer cap.
	rd, err := rep.Acquire(hashN(3), 7, parent)
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrPerPeerCapacityFull,
		"3rd acquisition on same peer must hit per-peer cap")

	// But a different peer can still acquire.
	_, err = rep.Acquire(hashN(4), 8, parent)
	assert.NoError(t, err,
		"different peer must NOT be blocked by another peer's per-peer count")

	assert.Equal(t, 3, rep.Count())
}

// TestReplayer_HandleResponse_RoutesByHash installs two in-flight
// acquisitions and feeds a response for one. Only that one advances;
// the other remains in StateWantBase. This is the core guarantee of
// the coordinator: N responses never cross-pollinate.
func TestReplayer_HandleResponse_RoutesByHash(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)

	// Build a real, verifiable response for one acquisition. The other
	// acquisition uses a synthetic hash that nothing will match.
	blob, id := makeTxWithMetaBlob(t, []byte("tx-blob-A--padding-to-pass-shamap-min"), 0)
	resp, expectedHash := buildDeltaResponse(t, parent, [][]byte{blob}, [][32]byte{id})

	_, err := rep.Acquire(expectedHash, 7, parent)
	require.NoError(t, err)
	otherHash := hashN(999)
	_, err = rep.Acquire(otherHash, 9, parent)
	require.NoError(t, err)

	rd, err := rep.HandleResponse(resp)
	require.NoError(t, err)
	require.NotNil(t, rd)
	assert.Equal(t, expectedHash, rd.Hash())
	assert.Equal(t, StateComplete, rd.State())

	// The untouched acquisition must still be in StateWantBase.
	rep.mu.Lock()
	otherRD := rep.inFlight[otherHash]
	rep.mu.Unlock()
	require.NotNil(t, otherRD)
	assert.Equal(t, StateWantBase, otherRD.State())

	// Both remain in-flight — HandleResponse doesn't Complete.
	assert.Equal(t, 2, rep.Count())
}

// TestReplayer_HandleResponse_NoMatch confirms that a response for a
// hash no one is waiting on yields ErrNoMatchingAcquisition and
// doesn't perturb state.
func TestReplayer_HandleResponse_NoMatch(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)
	_, err := rep.Acquire(hashN(1), 7, parent)
	require.NoError(t, err)

	unknown := hashN(2)
	resp := &message.ReplayDeltaResponse{LedgerHash: unknown[:]}
	rd, err := rep.HandleResponse(resp)
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrNoMatchingAcquisition)
	assert.Equal(t, 1, rep.Count(), "unmatched response must not touch state")
}

// TestReplayer_HandleResponse_NilAndBadHash covers the defensive edge
// cases: nil response and wrong-length hash both bounce with
// ErrNoMatchingAcquisition rather than panicking.
func TestReplayer_HandleResponse_NilAndBadHash(t *testing.T) {
	rep := NewReplayer(nil, nil, 0)

	rd, err := rep.HandleResponse(nil)
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrNoMatchingAcquisition)

	rd, err = rep.HandleResponse(&message.ReplayDeltaResponse{LedgerHash: []byte{0x01}})
	assert.Nil(t, rd)
	assert.ErrorIs(t, err, ErrNoMatchingAcquisition)
}

// TestReplayer_Complete_Removes verifies that Complete frees the slot
// so a subsequent Acquire of the same hash succeeds.
func TestReplayer_Complete_Removes(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)
	h := hashN(1)

	_, err := rep.Acquire(h, 7, parent)
	require.NoError(t, err)

	rep.Complete(h)
	assert.Equal(t, 0, rep.Count())
	assert.Empty(t, rep.InFlight())

	// Re-Acquire the same hash now succeeds.
	_, err = rep.Acquire(h, 7, parent)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Count())

	// Complete of an unknown hash is a no-op.
	rep.Complete(hashN(42))
	assert.Equal(t, 1, rep.Count())
}

// TestReplayer_Abandon_IsCompleteSynonym pins the documented
// equivalence so a future refactor doesn't silently decouple the two.
func TestReplayer_Abandon_IsCompleteSynonym(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)
	h := hashN(1)

	_, err := rep.Acquire(h, 7, parent)
	require.NoError(t, err)
	rep.Abandon(h)
	assert.Equal(t, 0, rep.Count())
}

// TestReplayer_TimedOut_Surfaces installs a FakeClock, ages an
// acquisition past the timeout, and confirms TimedOut() reports it
// with its full summary (hash/seq/peerID) so the router can re-issue.
func TestReplayer_TimedOut_Surfaces(t *testing.T) {
	parent := makeGenesisLedger(t)
	clock := inboundtest.NewFakeClock(time.Now())
	rep := NewReplayer(nil, clock, 0)

	h := hashN(7)
	_, err := rep.Acquire(h, 42, parent)
	require.NoError(t, err)

	assert.Empty(t, rep.TimedOut(), "fresh acquisition cannot be timed out")

	clock.Advance(2 * replayDeltaTimeout)
	timed := rep.TimedOut()
	require.Len(t, timed, 1)
	assert.Equal(t, h, timed[0].Hash)
	assert.Equal(t, uint64(42), timed[0].PeerID)
	assert.Equal(t, parent.Sequence()+1, timed[0].Seq)

	// TimedOut is read-only; the slot must still be occupied until
	// the caller explicitly Abandons.
	assert.Equal(t, 1, rep.Count())
}

// TestReplayer_Concurrent_Acquires races 100 goroutines against a cap
// of 50; exactly 50 must succeed and the remainder must return
// ErrCapacityFull (never ErrAcquisitionExists — the hashes are
// pairwise distinct). Regression guard for lock-ordering bugs in
// Acquire.
func TestReplayer_Concurrent_Acquires(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 50)

	const total = 100
	var (
		wg        sync.WaitGroup
		succeeded int64
		capFull   int64
		exists    int64
		other     int64
	)
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := rep.Acquire(hashN(i), uint64(i), parent)
			switch err {
			case nil:
				atomic.AddInt64(&succeeded, 1)
			case ErrCapacityFull:
				atomic.AddInt64(&capFull, 1)
			case ErrAcquisitionExists:
				atomic.AddInt64(&exists, 1)
			default:
				atomic.AddInt64(&other, 1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int64(50), atomic.LoadInt64(&succeeded))
	assert.Equal(t, int64(50), atomic.LoadInt64(&capFull))
	assert.Equal(t, int64(0), atomic.LoadInt64(&exists),
		"distinct hashes must never collide")
	assert.Equal(t, int64(0), atomic.LoadInt64(&other))
	assert.Equal(t, 50, rep.Count())
}

// TestReplayer_Has pins the Has accessor so callers can cheap-skip a
// duplicate Acquire.
func TestReplayer_Has(t *testing.T) {
	parent := makeGenesisLedger(t)
	rep := NewReplayer(nil, nil, 0)
	h := hashN(1)

	assert.False(t, rep.Has(h))
	_, err := rep.Acquire(h, 7, parent)
	require.NoError(t, err)
	assert.True(t, rep.Has(h))
	rep.Complete(h)
	assert.False(t, rep.Has(h))
}

package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLpNewLedgerProvider covers NewLedgerProvider using a real *service.Service.
func TestLpNewLedgerProvider(t *testing.T) {
	svc := newTestLedgerService(t)
	p := NewLedgerProvider(svc)
	require.NotNil(t, p)
}

// TestLpGetLedgerHeader_ByHash exercises GetLedgerHeader with a known hash.
func TestLpGetLedgerHeader_ByHash(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	hdr, err := p.GetLedgerHeader(h[:], 0)
	require.NoError(t, err)
	require.NotNil(t, hdr)
	assert.Equal(t, closed.SerializeHeader(), hdr)
}

// TestLpGetLedgerHeader_UnknownHash returns nil when no ledger matches.
func TestLpGetLedgerHeader_UnknownHash(t *testing.T) {
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	unknown := fixedKey32(0x55)
	hdr, err := p.GetLedgerHeader(unknown[:], 0)
	require.NoError(t, err)
	assert.Nil(t, hdr)
}

// TestLpGetLedgerHeader_ShortHashFallsToSeq tests the fallback branch: a
// non-32-byte hash triggers sequence-based lookup. We use a real service so
// both lookup paths are exercised.
func TestLpGetLedgerHeader_ShortHashFallsToSeq(t *testing.T) {
	svc := newTestLedgerService(t)
	p := NewLedgerProvider(svc)

	// The genesis ledger is at seq 2 (standalone service after Start()).
	// Pass a short (invalid) hash so toHash32 fails and the seq path runs.
	hdr, err := p.GetLedgerHeader([]byte{0x01}, 2)
	require.NoError(t, err)
	require.NotNil(t, hdr, "seq-based fallback should resolve genesis at seq 2")
}

// TestLpGetLedgerHeader_SeqZeroNoFallback verifies that when seq==0 and the
// hash is invalid, the lookup returns nil without panicking.
func TestLpGetLedgerHeader_SeqZeroNoFallback(t *testing.T) {
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	// Short hash → toHash32 fails; seq==0 → seq branch skipped.
	hdr, err := p.GetLedgerHeader([]byte{0xFF}, 0)
	require.NoError(t, err)
	assert.Nil(t, hdr)
}

// TestLpGetAccountStateNode_Found retrieves a state node from genesis.
func TestLpGetAccountStateNode_Found(t *testing.T) {
	closed := makeGenesisLedger(t)

	// Discover one key from genesis state map.
	var targetKey [32]byte
	require.NoError(t, closed.ForEach(func(key [32]byte, _ []byte) bool {
		targetKey = key
		return false
	}))

	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	data, err := p.GetAccountStateNode(h[:], targetKey[:])
	require.NoError(t, err)
	require.NotNil(t, data)
}

// TestLpGetAccountStateNode_UnknownLedger returns nil without error.
func TestLpGetAccountStateNode_UnknownLedger(t *testing.T) {
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	unknown := fixedKey32(0xAA)
	key := fixedKey32(0x11)
	data, err := p.GetAccountStateNode(unknown[:], key[:])
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpGetAccountStateNode_KeyAbsent returns nil for an absent key.
func TestLpGetAccountStateNode_KeyAbsent(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	missing := fixedKey32(0xEE)
	data, err := p.GetAccountStateNode(h[:], missing[:])
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpGetAccountStateNode_ShortNodeID covers the non-32-byte nodeID branch
// in lookupLeaf (returns nil, nil).
func TestLpGetAccountStateNode_ShortNodeID(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	// 16-byte nodeID — not 32 bytes → lookupLeaf short-circuits.
	data, err := p.GetAccountStateNode(h[:], make([]byte, 16))
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpGetTransactionNode_Found retrieves a tx node from a closed ledger.
func TestLpGetTransactionNode_Found(t *testing.T) {
	txKey := fixedKey32(0x10)
	closed := makeClosedLedgerWithTxs(t, []struct {
		key  [32]byte
		blob []byte
	}{
		{txKey, []byte("tx-node-data-padded")},
	})

	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	data, err := p.GetTransactionNode(h[:], txKey[:])
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, []byte("tx-node-data-padded"), data)
}

// TestLpGetTransactionNode_UnknownLedger returns nil.
func TestLpGetTransactionNode_UnknownLedger(t *testing.T) {
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	unknown := fixedKey32(0xBB)
	key := fixedKey32(0x22)
	data, err := p.GetTransactionNode(unknown[:], key[:])
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpGetTransactionNode_KeyAbsent returns nil for a missing key.
func TestLpGetTransactionNode_KeyAbsent(t *testing.T) {
	closed := makeClosedLedgerWithTxs(t, nil)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	missing := fixedKey32(0xDD)
	data, err := p.GetTransactionNode(h[:], missing[:])
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpLookupLedger_SeqFallback exercises the sequence-based fallback path in
// lookupLedger via GetLedgerHeader with an invalid hash but a valid seq.
// This path uses a real *service.Service so GetLedgerBySequence works.
func TestLpLookupLedger_SeqFallback(t *testing.T) {
	svc := newTestLedgerService(t)
	// Accept one more ledger so seq 3 is in history.
	_, err := svc.AcceptLedger(context.Background())
	require.NoError(t, err)

	p := NewLedgerProvider(svc)

	// Use a wrong-length hash so hash path fails, then seq path fires.
	hdr, err := p.GetLedgerHeader([]byte{0x01, 0x02}, 2)
	require.NoError(t, err)
	require.NotNil(t, hdr, "seq fallback should find ledger at seq 2")
}

// TestLpLookupLedger_BothMiss returns nil when neither hash nor seq resolve.
func TestLpLookupLedger_BothMiss(t *testing.T) {
	svc := newTestLedgerService(t)
	p := NewLedgerProvider(svc)

	// Bad hash + seq 9999 which does not exist.
	hdr, err := p.GetLedgerHeader([]byte{0xAA}, 9999)
	require.NoError(t, err)
	assert.Nil(t, hdr)
}

// TestLpGetProofPath_BadHashLength covers the short-hash branch in GetProofPath
// that returns ErrLedgerNotFound immediately.
func TestLpGetProofPath_BadHashLength(t *testing.T) {
	lookup := newFakeLookup()
	p := newLedgerProviderForTest(lookup)

	shortHash := []byte{0x01, 0x02}
	key := fixedKey32(0x11)
	hdr, path, err := p.GetProofPath(shortHash, key[:], message.LedgerMapAccountState)
	require.ErrorIs(t, err, peermanagement.ErrLedgerNotFound)
	assert.Nil(t, hdr)
	assert.Nil(t, path)
}

// TestLpGetProofPath_BadKeyLength covers the short-key branch (returns ErrKeyNotFound).
func TestLpGetProofPath_BadKeyLength(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	shortKey := []byte{0x01, 0x02}
	hdr, path, err := p.GetProofPath(h[:], shortKey, message.LedgerMapAccountState)
	require.ErrorIs(t, err, peermanagement.ErrKeyNotFound)
	assert.Nil(t, hdr)
	assert.Nil(t, path)
}

// TestLpNewLedgerProvider_ViaService verifies the public constructor wires the
// service correctly by exercising a full round-trip through it.
func TestLpNewLedgerProvider_ViaService(t *testing.T) {
	svc := newTestLedgerService(t)
	p := NewLedgerProvider(svc)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	h := closed.Hash()

	hdr, err := p.GetLedgerHeader(h[:], 0)
	require.NoError(t, err)
	require.NotNil(t, hdr)
	assert.Equal(t, closed.SerializeHeader(), hdr)
}

// TestLpWrapLedger_CloseTime verifies LedgerWrapper.CloseTime is non-zero
// on a closed ledger (which carries a real timestamp from the close call).
func TestLpWrapLedger_CloseTime(t *testing.T) {
	before := time.Now().Truncate(time.Second)
	closed := makeClosedLedgerWithTxs(t, nil)

	w := WrapLedger(closed)
	ct := w.CloseTime()

	// CloseTime is XRPL-epoch-rounded; check it is in a plausible range
	// relative to the test execution window. We accept any non-zero time.
	assert.False(t, ct.IsZero(), "CloseTime must not be zero for a closed ledger")
	assert.True(t, ct.After(before.Add(-2*time.Minute)),
		"CloseTime %v should be within 2 min of test start %v", ct, before)
}

// TestLpWrapLedger_TxSetID verifies LedgerWrapper.TxSetID is populated for
// a closed ledger that has a computable tx-map hash.
func TestLpWrapLedger_TxSetID(t *testing.T) {
	// A ledger with no transactions still has a valid (all-zeros) tx hash.
	closed := makeClosedLedgerWithTxs(t, nil)
	w := WrapLedger(closed)
	_ = w.TxSetID() // just exercise the code path; hash may be zero for empty map

	// A ledger WITH transactions should yield a non-zero TxSetID.
	txKey := fixedKey32(0x05)
	withTx := makeClosedLedgerWithTxs(t, []struct {
		key  [32]byte
		blob []byte
	}{
		{txKey, []byte("some-tx-blob-padded")},
	})
	w2 := WrapLedger(withTx)
	assert.NotEqual(t, consensus.TxSetID{}, w2.TxSetID(),
		"TxSetID must be non-zero for a ledger with at least one transaction")
}

// TestLpGetCandidateLedger exercises the package-private getCandidateLedger
// helper indirectly via a Router whose RequestLedger method calls it.
// This also covers Router.FetchInfo and Router.ClearFetchInfo.
func TestLpGetCandidateLedger(t *testing.T) {
	tests := []struct {
		seq  uint32
		want uint32
	}{
		{1, 256},
		{255, 256},
		{256, 256},
		{257, 512},
		{512, 512},
		{513, 768},
		{1024, 1024},
		{1025, 1280},
	}
	for _, tc := range tests {
		got := getCandidateLedger(tc.seq)
		assert.Equal(t, tc.want, got, "getCandidateLedger(%d)", tc.seq)
	}
}

// TestLpRouter_FetchInfoAndClear verifies that FetchInfo returns a map and
// ClearFetchInfo does not panic, covering those two zero-% Router methods.
func TestLpRouter_FetchInfoAndClear(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, a, nil, inbox)

	info := router.FetchInfo()
	assert.NotNil(t, info, "FetchInfo must return a non-nil map")

	router.ClearFetchInfo()
	info2 := router.FetchInfo()
	assert.NotNil(t, info2, "FetchInfo after ClearFetchInfo must still return a non-nil map")
}

// TestLpRouter_OurLCLMatchesPeers_NoPeers verifies the no-peer-data case
// (returns true to avoid blocking startup).
func TestLpRouter_OurLCLMatchesPeers_NoPeers(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, a, nil, inbox)

	// No peer states registered → must return true.
	assert.True(t, router.ourLCLMatchesPeers(),
		"ourLCLMatchesPeers with no peer data must return true (bootstrap safety)")
}

// TestLpRouter_OurLCLMatchesPeers_MajorityAgrees registers peer states whose
// hash matches our closed ledger and expects true.
func TestLpRouter_OurLCLMatchesPeers_MajorityAgrees(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, a, nil, inbox)

	svc := a.LedgerService()
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	ourSeq := svc.GetClosedLedgerIndex()
	ourHash := closed.Hash()

	router.peersMu.Lock()
	router.peerStates[1] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: ourHash}
	router.peerStates[2] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: ourHash}
	router.peerStates[3] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: fixedKey32(0xFF)}
	router.peersMu.Unlock()

	assert.True(t, router.ourLCLMatchesPeers(),
		"majority-matching peers must return true")
}

// TestLpRouter_OurLCLMatchesPeers_MajorityDisagrees registers peer states that
// differ from our closed ledger and expects false.
func TestLpRouter_OurLCLMatchesPeers_MajorityDisagrees(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, a, nil, inbox)

	svc := a.LedgerService()
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	ourSeq := svc.GetClosedLedgerIndex()
	foreign := fixedKey32(0xCC)

	router.peersMu.Lock()
	router.peerStates[1] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: foreign}
	router.peerStates[2] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: foreign}
	router.peerStates[3] = &peerLedgerState{LedgerSeq: ourSeq, LedgerHash: foreign}
	router.peersMu.Unlock()

	assert.False(t, router.ourLCLMatchesPeers(),
		"majority-disagreeing peers must return false")
}

// TestLpRouter_OurLCLMatchesPeers_NoPeersAtOurSeq covers the branch where
// peers exist but none report our seq — returns true (allow transition).
func TestLpRouter_OurLCLMatchesPeers_NoPeersAtOurSeq(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 4)
	router := NewRouter(engine, a, nil, inbox)

	svc := a.LedgerService()
	ourSeq := svc.GetClosedLedgerIndex()

	// Peers report a different (future) seq.
	router.peersMu.Lock()
	router.peerStates[1] = &peerLedgerState{LedgerSeq: ourSeq + 10, LedgerHash: fixedKey32(0x01)}
	router.peerStates[2] = &peerLedgerState{LedgerSeq: ourSeq + 10, LedgerHash: fixedKey32(0x02)}
	router.peersMu.Unlock()

	assert.True(t, router.ourLCLMatchesPeers(),
		"peers reporting a different seq should not block transition (they may have advanced)")
}

// TestLpToHash32 exercises toHash32 edge cases: exact 32-byte input and
// wrong-length input.
func TestLpToHash32(t *testing.T) {
	// Right length.
	input := make([]byte, 32)
	for i := range input {
		input[i] = byte(i)
	}
	arr, ok := toHash32(input)
	require.True(t, ok)
	for i, b := range arr {
		assert.Equal(t, byte(i), b)
	}

	// Wrong length.
	_, ok2 := toHash32(make([]byte, 31))
	assert.False(t, ok2)

	_, ok3 := toHash32(make([]byte, 33))
	assert.False(t, ok3)

	_, ok4 := toHash32(nil)
	assert.False(t, ok4)
}

// TestLpNewLedgerProvider_ServiceLookupBySeq verifies that a provider built on
// a real service can answer a GetLedgerHeader query using the seq fallback
// (non-32-byte hash + valid seq).
func TestLpNewLedgerProvider_ServiceLookupBySeq(t *testing.T) {
	svc := newTestLedgerService(t)
	p := NewLedgerProvider(svc)

	// Short (invalid) hash forces seq path; genesis is at seq 2.
	hdr, err := p.GetLedgerHeader([]byte("tooshort"), 2)
	require.NoError(t, err)
	assert.NotNil(t, hdr)
}

// TestLpLookupLeaf_ShortKey exercises lookupLeaf with a non-32-byte key.
func TestLpLookupLeaf_ShortKey(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	// 8-byte key → lookupLeaf returns (nil, nil).
	data, err := p.GetAccountStateNode(h[:], make([]byte, 8))
	require.NoError(t, err)
	assert.Nil(t, data)
}

// TestLpWrapLedger_CloseTime_ViaService verifies CloseTime through the service
// path (uses a real ledger accepted via AcceptLedger so closeTime is set).
func TestLpWrapLedger_CloseTime_ViaService(t *testing.T) {
	svc := newTestLedgerService(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	w := WrapLedger(closed)
	assert.False(t, w.CloseTime().IsZero(),
		"LedgerWrapper.CloseTime must not be zero on the service's closed ledger")
}

// TestLpGetCandidateLedger_ZeroSeq verifies getCandidateLedger(0) == 0.
func TestLpGetCandidateLedger_ZeroSeq(t *testing.T) {
	// seq=0: (0 + 255) &^ 255 = 255 &^ 255 = 0
	assert.Equal(t, uint32(0), getCandidateLedger(0))
}

// TestLpGetLedgerHeader_ValidHashAndSeq exercises the hash-wins branch
// (both hash and seq provided; hash matches so seq is irrelevant).
func TestLpGetLedgerHeader_ValidHashAndSeq(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	p := newLedgerProviderForTest(lookup)

	h := closed.Hash()
	// Pass a non-zero seq that doesn't exist; hash path must win.
	hdr, err := p.GetLedgerHeader(h[:], 9999)
	require.NoError(t, err)
	require.NotNil(t, hdr)
	assert.Equal(t, closed.SerializeHeader(), hdr)
}

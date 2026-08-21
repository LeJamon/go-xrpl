package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestLpWrapLedger_TxSetID(t *testing.T) {
	// A ledger with no transactions still has a valid (all-zeros) tx hash.
	closed := makeClosedLedgerWithTxs(t, nil)
	w := WrapLedger(closed)
	_ = w.TxSetID() // just exercise the code path; hash may be zero for empty map

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

func TestLpGetCandidateLedger(t *testing.T) {
	tests := []struct {
		seq  uint32
		want uint32
	}{
		{0, 0},
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

func TestLpRouter_OurLCLMatchesPeers(t *testing.T) {
	tests := []struct {
		name    string
		peers   func(uint32, [32]byte) []*peerLedgerState
		matches bool
	}{
		{name: "no peers", matches: true},
		{
			name: "majority agrees",
			peers: func(seq uint32, hash [32]byte) []*peerLedgerState {
				return []*peerLedgerState{
					{LedgerSeq: seq, LedgerHash: hash},
					{LedgerSeq: seq, LedgerHash: hash},
					{LedgerSeq: seq, LedgerHash: fixedKey32(0xFF)},
				}
			},
			matches: true,
		},
		{
			name: "majority disagrees",
			peers: func(seq uint32, _ [32]byte) []*peerLedgerState {
				foreign := fixedKey32(0xCC)
				return []*peerLedgerState{
					{LedgerSeq: seq, LedgerHash: foreign},
					{LedgerSeq: seq, LedgerHash: foreign},
					{LedgerSeq: seq, LedgerHash: foreign},
				}
			},
		},
		{
			name: "peers at another sequence",
			peers: func(seq uint32, _ [32]byte) []*peerLedgerState {
				return []*peerLedgerState{
					{LedgerSeq: seq + 10, LedgerHash: fixedKey32(0x01)},
					{LedgerSeq: seq + 10, LedgerHash: fixedKey32(0x02)},
				}
			},
			matches: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := newTestAdaptor(t)
			router := newTestRouter(&mockEngine{}, a, make(chan *peermanagement.InboundMessage, 4))
			closed := a.LedgerService().GetClosedLedger()
			require.NotNil(t, closed)

			if test.peers != nil {
				for i, state := range test.peers(closed.Sequence(), closed.Hash()) {
					router.peerStates[peermanagement.PeerID(i+1)] = state
				}
			}
			assert.Equal(t, test.matches, router.ourLCLMatchesPeers())
		})
	}
}

func TestLpToHash32(t *testing.T) {
	input := make([]byte, 32)
	for i := range input {
		input[i] = byte(i)
	}
	arr, ok := inbound.ToHash32(input)
	require.True(t, ok)
	for i, b := range arr {
		assert.Equal(t, byte(i), b)
	}

	_, ok2 := inbound.ToHash32(make([]byte, 31))
	assert.False(t, ok2)

	_, ok3 := inbound.ToHash32(make([]byte, 33))
	assert.False(t, ok3)

	_, ok4 := inbound.ToHash32(nil)
	assert.False(t, ok4)
}

func TestLpWrapLedger_CloseTime_ViaService(t *testing.T) {
	svc := newTestLedgerService(t)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)

	w := WrapLedger(closed)
	assert.False(t, w.CloseTime().IsZero(),
		"LedgerWrapper.CloseTime must not be zero on the service's closed ledger")
}

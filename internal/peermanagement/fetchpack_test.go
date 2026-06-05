package peermanagement

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFetchPackProvider implements LedgerProvider; only MakeFetchPack is
// exercised, the rest are inert stubs.
type fakeFetchPackProvider struct {
	objects []message.IndexedObject
	gotHave [32]byte
	calls   int
}

func (f *fakeFetchPackProvider) GetLedgerHeader(_ []byte, _ uint32) ([]byte, error) {
	return nil, nil
}
func (f *fakeFetchPackProvider) GetAccountStateNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeFetchPackProvider) GetTransactionNode(_ []byte, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeFetchPackProvider) GetReplayDelta(_ []byte) ([]byte, [][]byte, error) {
	return nil, nil, nil
}
func (f *fakeFetchPackProvider) GetProofPath(_ []byte, _ []byte, _ message.LedgerMapType) ([]byte, [][]byte, error) {
	return nil, nil, nil
}
func (f *fakeFetchPackProvider) MakeFetchPack(have [32]byte, _ int) ([]message.IndexedObject, error) {
	f.calls++
	f.gotHave = have
	return f.objects, nil
}

func newFetchPackTestOverlay(t *testing.T, prov LedgerProvider) (*Overlay, *Peer) {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 8)
	o := &Overlay{
		cfg:        Config{},
		peers:      make(map[PeerID]*Peer),
		events:     events,
		messages:   make(chan *InboundMessage, 8),
		cluster:    cluster.New(),
		ledgerSync: NewLedgerSyncHandler(events),
	}
	o.ledgerSync.SetProvider(prov)
	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer
	return o, peer
}

// TestServeFetchPack_RepliesWithPack pins the serve path: an otFETCH_PACK
// request invokes the provider and ships a query=false reply carrying the pack.
func TestServeFetchPack_RepliesWithPack(t *testing.T) {
	prov := &fakeFetchPackProvider{objects: []message.IndexedObject{
		{Hash: bytes.Repeat([]byte{0xAB}, 32), Data: []byte{1, 2, 3}, LedgerSeq: 42},
		{Hash: bytes.Repeat([]byte{0xCD}, 32), Data: []byte{4, 5, 6}, LedgerSeq: 42},
	}}
	o, peer := newFetchPackTestOverlay(t, prov)

	haveHash := bytes.Repeat([]byte{0x11}, 32)
	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      true,
		LedgerHash: haveHash,
	})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeGetObjects),
		Payload:     payload,
	})

	require.Equal(t, 1, prov.calls, "MakeFetchPack must be invoked once")
	require.Equal(t, haveHash, prov.gotHave[:], "the have-hash must be forwarded to the provider")

	select {
	case frame := <-peer.send:
		require.GreaterOrEqual(t, len(frame), message.HeaderSizeUncompressed)
		msgType := (uint16(frame[4]) << 8) | uint16(frame[5])
		require.Equal(t, uint16(message.TypeGetObjects), msgType)
		decoded, err := message.Decode(message.TypeGetObjects, frame[message.HeaderSizeUncompressed:])
		require.NoError(t, err)
		gob, ok := decoded.(*message.GetObjectByHash)
		require.True(t, ok)
		assert.False(t, gob.Query, "reply must be query=false")
		assert.Equal(t, message.ObjectTypeFetchPack, gob.ObjType)
		assert.Equal(t, haveHash, gob.LedgerHash)
		assert.Len(t, gob.Objects, 2)
	default:
		t.Fatal("no fetch-pack reply was sent to the peer")
	}
}

// TestServeFetchPack_EmptyPackNoReply: an empty pack (unknown ledger) yields no
// reply, but the valid request is still charged feeHeavyBurdenPeer up front —
// mirroring rippled's doFetchPack, which sets the heavy-burden fee before the
// build regardless of whether the ledger is found (PeerImp.cpp:2773).
func TestServeFetchPack_EmptyPackNoReply(t *testing.T) {
	prov := &fakeFetchPackProvider{objects: nil}
	o, peer := newFetchPackTestOverlay(t, prov)

	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      true,
		LedgerHash: bytes.Repeat([]byte{0x22}, 32),
	})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeGetObjects),
		Payload:     payload,
	})

	require.Equal(t, 1, prov.calls)
	select {
	case <-peer.send:
		t.Fatal("an empty pack must not produce a reply")
	default:
	}
	assert.NotZero(t, peer.BadDataCount(),
		"a valid fetch-pack request is charged feeHeavyBurdenPeer up front even when the pack is empty")
}

// TestServeFetchPack_BadHashCharged: a malformed (non-32-byte) ledger hash is
// charged as bad data and never reaches the provider.
func TestServeFetchPack_BadHashCharged(t *testing.T) {
	prov := &fakeFetchPackProvider{}
	o, peer := newFetchPackTestOverlay(t, prov)

	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      true,
		LedgerHash: []byte{0x01, 0x02, 0x03},
	})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeGetObjects),
		Payload:     payload,
	})

	assert.Zero(t, prov.calls, "a malformed hash must not reach the provider")
	assert.NotZero(t, peer.BadDataCount(), "a malformed fetch-pack hash must be charged")
}

// TestHandleGetObjects_FetchPackReplyForwardedToRouter: a query=false
// fetch-pack reply is forwarded to the overlay→router channel for consumption.
func TestHandleGetObjects_FetchPackReplyForwardedToRouter(t *testing.T) {
	o, peer := newFetchPackTestOverlay(t, &fakeFetchPackProvider{})

	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType: message.ObjectTypeFetchPack,
		Query:   false,
		Objects: []message.IndexedObject{{Hash: bytes.Repeat([]byte{0x01}, 32), Data: []byte{0x09}}},
	})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeGetObjects),
		Payload:     payload,
	})

	select {
	case got := <-o.messages:
		assert.Equal(t, uint16(message.TypeGetObjects), got.Type)
		assert.Equal(t, peer.ID(), got.PeerID)
	default:
		t.Fatal("fetch-pack reply was not forwarded to the router channel")
	}
}

package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleNodeReply_AcceptsByHashStateNodes confirms the reply handler now
// accepts the otSTATE_NODE nodes served for a by-hash acquisition escalation
// (issue #985) and caches them, so CheckLocal can place them — the same
// receiver path the bulk fetch-pack reply uses.
func TestHandleNodeReply_AcceptsByHashStateNodes(t *testing.T) {
	t.Parallel()
	source := shamap.New(shamap.TypeState)
	for i := byte(1); i <= 12; i++ {
		var key [32]byte
		key[0] = i
		key[31] = 0xA5
		require.NoError(t, source.Put(key, []byte{i, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB}))
	}
	_, err := source.Hash()
	require.NoError(t, err)
	valid, err := source.WalkFetchPackNodes(1 << 20)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(valid), 2)

	objects := make([]message.IndexedObject, 0, len(valid))
	for _, n := range valid {
		objects = append(objects, message.IndexedObject{
			Hash: append([]byte(nil), n.Hash[:]...),
			Data: n.Data,
		})
	}
	payload, err := message.Encode(&message.GetObjectByHash{
		ObjType: message.ObjectTypeStateNode,
		Query:   false,
		Objects: objects,
	})
	require.NoError(t, err)

	r := NewRouter(nil, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	r.handleFetchPackReply(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(5),
		Type:    uint16(message.TypeGetObjects),
		Payload: payload,
	})

	assert.Equal(t, len(valid), r.fetchPacks.size(), "by-hash state-node reply nodes must be cached")
	for _, n := range valid {
		if _, ok := r.fetchPacks.get(n.Hash, time.Now()); !ok {
			t.Errorf("by-hash node %x not cached", n.Hash[:8])
		}
	}
}

// TestFailInboundAcquisition_NotifiesEngineForConsensus pins issue #985 part C's
// wiring: a consensus-driven acquisition that exhausts its retry budget is
// reaped AND the engine is told, so a node pinned in wrongLedger can recover
// instead of starving the ledger loop into a fatal watchdog abort.
func TestFailInboundAcquisition_NotifiesEngineForConsensus(t *testing.T) {
	t.Parallel()
	eng := &mockEngine{}
	r := NewRouter(eng, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	hash := [32]byte{0xDE, 0xAD, 0xBE, 0xEF}
	il := inbound.New(hash, 50, 7, serveTestLogger())
	r.fetchTracker.Track(il)

	r.failInboundAcquisition(il)

	got := eng.getAcquireFailed()
	require.Len(t, got, 1, "engine must be notified of the failed consensus acquisition")
	assert.Equal(t, consensus.LedgerID(hash), got[0])
	assert.Nil(t, r.fetchTracker.Find(hash), "failed acquisition must be removed from the tracker")
}

// TestFailInboundAcquisition_SkipsEngineForGeneric confirms an RPC-driven
// (ReasonGeneric) acquisition failure does not disturb consensus.
func TestFailInboundAcquisition_SkipsEngineForGeneric(t *testing.T) {
	t.Parallel()
	eng := &mockEngine{}
	r := NewRouter(eng, newTestAdaptor(t), make(chan *peermanagement.InboundMessage, 1))
	hash := [32]byte{0x0B, 0x0E}
	il := inbound.NewGeneric(hash, 50, 7, serveTestLogger())
	r.fetchTracker.Track(il)

	r.failInboundAcquisition(il)

	assert.Empty(t, eng.getAcquireFailed(), "a generic acquisition must not notify consensus")
	assert.Nil(t, r.fetchTracker.Find(hash), "failed acquisition must be removed from the tracker")
}

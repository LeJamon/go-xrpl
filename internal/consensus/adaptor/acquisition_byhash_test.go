package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

type byHashPipelineSender struct {
	acqRecordingSender
	frames []sentFrame
}

func (s *byHashPipelineSender) SendPriorityToPeer(peer uint64, frame []byte) error {
	s.frames = append(s.frames, sentFrame{peerID: peer, frame: frame})
	return nil
}

// Peers in this simulation serve only requested objects, not full ledgers or
// path-based replies. After the initial escalation no timer events are sent.
func TestAcquisitionByHash_WorkerRefillsOnCacheReplies(t *testing.T) {
	source := newWideWorkSource(t, 4)
	il, base := newWantBaseWorkLedger(t, source, []uint64{1, 2, 3, 4, 5, 6, 7, 8})
	require.NoError(t, il.GotBase(base))
	pack, err := source.WalkFetchPackNodes(1 << 24)
	require.NoError(t, err)
	objects := make(map[[32]byte][]byte)
	for _, n := range pack {
		objects[n.Hash] = n.Data
	}
	for tick := 1; tick <= 6; tick++ {
		il.OnTimer(time.Now().Add(time.Duration(tick) * 4 * time.Second))
	}
	require.True(t, il.ByHashMode())
	cache := make(map[[32]byte][]byte)
	fetch := func(hash [32]byte) ([]byte, bool) {
		data, ok := cache[hash]
		delete(cache, hash)
		return data, ok
	}
	sender := &byHashPipelineSender{}
	router := newTestRouter(nil, New(Config{Sender: sender}), nil)
	events := []acquisitionWorkEvent{{kind: acquisitionWorkTimer, peers: il.Peers(), fetch: fetch}}
	for round := 0; round < 128 && !il.IsComplete(); round++ {
		result := processAcquisitionWork(t.Context(), il, events)
		require.NoError(t, result.err)
		require.False(t, result.remove)
		require.Empty(t, result.stateIDs, "must not fall back to ledger-context requests")
		require.Empty(t, result.byHashState, "must not use the legacy four-hash broadcast")
		if result.complete {
			break
		}
		if len(result.requests) == 0 && result.continueRequests {
			events = []acquisitionWorkEvent{{kind: acquisitionWorkContinue}}
			continue
		}
		require.NotEmpty(t, result.requests, "reply must refill the window")
		seen := make(map[[32]byte]bool)
		for _, req := range result.requests {
			require.True(t, req.ByHash)
			sender.frames = nil
			require.False(t, router.catchupReplay.sendMissingReplyRequest(il, req))
			require.Len(t, sender.frames, 1, "send a batch only to its assigned peer")
			require.Equal(t, req.PeerID, sender.frames[0].peerID)
			kind, decoded := decodeFrame(t, sender.frames[0].frame)
			require.Equal(t, message.TypeGetObjects, kind)
			query := decoded.(*message.GetObjectByHash)
			require.True(t, query.Query)
			require.Equal(t, message.ObjectTypeStateNode, query.ObjType)
			require.Len(t, query.Objects, len(req.NodeHashes))
			for _, obj := range query.Objects {
				hash := [32]byte(obj.Hash)
				require.False(t, seen[hash])
				seen[hash] = true
				require.Contains(t, objects, hash)
				cache[hash] = objects[hash]
			}
		}
		events = []acquisitionWorkEvent{{kind: acquisitionWorkLocal, fetch: fetch}}
	}
	require.True(t, il.IsComplete())
	require.Empty(t, sender.stateInd, "peers never supplied path-based state")
	require.Greater(t, il.Snapshot().StateCacheUseful, uint64(2048))
	require.Zero(t, il.Snapshot().OutstandingNodes)
}

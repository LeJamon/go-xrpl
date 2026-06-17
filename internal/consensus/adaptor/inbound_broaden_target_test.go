package adaptor

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// peerTargetSender records the peer id of each legacy node-fetch and serves a
// configurable PeersWithLedger answer, so the broaden (issue #985 M2) and
// reply-targeting (M3) paths can be pinned. Other NetworkSender methods inherit
// from noopSender.
type peerTargetSender struct {
	noopSender
	mu         sync.Mutex
	statePeers []uint64 // peer of each RequestStateNodes call, in order
	broadenRet []uint64 // what PeersWithLedger returns
	broadenEx  []uint64 // excluded set captured from the last PeersWithLedger call
}

func (s *peerTargetSender) RequestStateNodes(peerID uint64, _ [32]byte, _ [][]byte, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statePeers = append(s.statePeers, peerID)
	return nil
}

func (s *peerTargetSender) RequestTransactionNodes(uint64, [32]byte, [][]byte, bool) error {
	return nil
}

func (s *peerTargetSender) PeersWithLedger(_ [32]byte, _ uint32, excluded []uint64, _ int) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadenEx = append([]uint64(nil), excluded...)
	return append([]uint64(nil), s.broadenRet...)
}

func (s *peerTargetSender) statePeerList() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.statePeers...)
}

func (s *peerTargetSender) excludedSet() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.broadenEx...)
}

func newPeerTargetRouter(t *testing.T, rs *peerTargetSender) (*Router, *service.Service) {
	t.Helper()
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	return NewRouter(&mockEngine{}, a, nil), svc
}

// TestBroadenAcquisitionPeers_AddsMultiple pins issue #985 M2: a no-progress
// broaden adds every fresh peer PeersWithLedger returns (mirroring rippled's
// peerCountAdd), excluding the acquisition's current set, instead of one.
func TestBroadenAcquisitionPeers_AddsMultiple(t *testing.T) {
	rs := &peerTargetSender{broadenRet: []uint64{8, 9, 10}}
	router, _ := newPeerTargetRouter(t, rs)

	il := inbound.New([32]byte{0xAB}, 42, 7, serveTestLogger())
	router.broadenAcquisitionPeers(il)

	assert.ElementsMatch(t, []uint64{7, 8, 9, 10}, il.Peers(),
		"broaden must add all peers PeersWithLedger returned")
	assert.Equal(t, []uint64{7}, rs.excludedSet(),
		"the current source set must be excluded from selection")
}

// TestRequestMissingAcquisitionNodes_ReplyTargetsReplier pins issue #985 M3: on
// a reply the re-request goes to just the replying peer (rippled trigger(peer)),
// while the no-progress timeout path (target 0) fans out to the whole set.
func TestRequestMissingAcquisitionNodes_ReplyTargetsReplier(t *testing.T) {
	rs := &peerTargetSender{}
	router, svc := newPeerTargetRouter(t, rs)

	l := svc.GetClosedLedger()
	require.NotNil(t, l)
	il := inbound.New(l.Hash(), l.Sequence(), 7, serveTestLogger())
	require.NoError(t, il.GotBase(router.buildLedgerBaseNodes(l)))
	require.NotEmpty(t, il.NeedsMissingNodeIDs(), "acquisition must have outstanding state nodes")
	il.AddPeer(8)
	il.AddPeer(9)

	// Reply path: target the replier only.
	router.requestMissingAcquisitionNodes(il, 9)
	assert.Equal(t, []uint64{9}, rs.statePeerList(),
		"a reply must re-request from only the peer that answered")

	// Timeout path: fan out to the whole broadened set.
	rs.statePeers = nil
	router.requestMissingAcquisitionNodes(il, 0)
	assert.ElementsMatch(t, []uint64{7, 8, 9}, rs.statePeerList(),
		"the no-progress path must fan out to every source peer")
}

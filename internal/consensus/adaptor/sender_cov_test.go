package adaptor

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type snd_fakeOverlay struct {
	mu sync.Mutex

	broadcasts       [][]byte
	sends            map[uint64][][]byte
	relaySlotCalls   []snd_relaySlotCall
	peersThatHave    map[[32]byte][]uint64
	replayCaps       map[uint64]bool
	badDataCounts    map[uint64]int
	shedResult       bool
	shedResultPeerID uint64
}

type snd_relaySlotCall struct {
	ValidatorKey []byte
	OriginPeer   uint64
	SeenPeers    []uint64
}

func (f *snd_fakeOverlay) BroadcastProposal(p *consensus.Proposal) error {
	return nil
}

func (f *snd_fakeOverlay) BroadcastValidation(v *consensus.Validation) error {
	return nil
}

func (f *snd_fakeOverlay) BroadcastStatusChange(sc *message.StatusChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	frame, err := encodeFrame(message.TypeStatusChange, sc)
	if err != nil {
		return err
	}
	f.broadcasts = append(f.broadcasts, frame)
	return nil
}

func (f *snd_fakeOverlay) RelayProposal(p *consensus.Proposal, except uint64) error {
	return nil
}

func (f *snd_fakeOverlay) RelayValidation(v *consensus.Validation, except uint64) error {
	return nil
}

func (f *snd_fakeOverlay) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(validatorKey))
	copy(cp, validatorKey)
	seenCp := append([]uint64(nil), seenPeers...)
	f.relaySlotCalls = append(f.relaySlotCalls, snd_relaySlotCall{
		ValidatorKey: cp,
		OriginPeer:   originPeer,
		SeenPeers:    seenCp,
	})
}

func (f *snd_fakeOverlay) RequestTxSet(id consensus.TxSetID) error {
	return nil
}

func (f *snd_fakeOverlay) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool) error {
	return nil
}

func (f *snd_fakeOverlay) RequestLedger(id consensus.LedgerID) error {
	return nil
}

func (f *snd_fakeOverlay) RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcasts = append(f.broadcasts, []byte("RequestLedgerByHashAndSeq"))
	return nil
}

func (f *snd_fakeOverlay) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], []byte("RequestLedgerBaseFromPeer"))
	return nil
}

func (f *snd_fakeOverlay) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], []byte("RequestReplayDelta"))
	return nil
}

func (f *snd_fakeOverlay) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], []byte("RequestStateNodes"))
	return nil
}

func (f *snd_fakeOverlay) RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], []byte("RequestTransactionNodes"))
	return nil
}

func (f *snd_fakeOverlay) SendToPeer(peerID uint64, frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], frame)
	return nil
}

func (f *snd_fakeOverlay) PeerSupportsReplay(peerID uint64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replayCaps[peerID]
}

func (f *snd_fakeOverlay) ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	excludeSet := make(map[uint64]struct{}, len(excluded))
	for _, id := range excluded {
		excludeSet[id] = struct{}{}
	}
	var out []uint64
	for id, capable := range f.replayCaps {
		if !capable {
			continue
		}
		if _, skip := excludeSet[id]; skip {
			continue
		}
		out = append(out, id)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func (f *snd_fakeOverlay) IncPeerBadData(peerID uint64, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.badDataCounts == nil {
		f.badDataCounts = make(map[uint64]int)
	}
	f.badDataCounts[peerID]++
}

func (f *snd_fakeOverlay) PeersThatHave(suppressionHash [32]byte) []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.peersThatHave[suppressionHash]...)
}

func (f *snd_fakeOverlay) ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shedResult && peerID == f.shedResultPeerID
}

func (f *snd_fakeOverlay) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool) { return 0, false }
func (f *snd_fakeOverlay) PeerWithTxSet([32]byte, uint64) (uint64, bool)          { return 0, false }
func (f *snd_fakeOverlay) NotePeerHasTxSet(uint64, [32]byte)                      {}

func snd_newAdaptorWithFake(t *testing.T, fake *snd_fakeOverlay) *Adaptor {
	t.Helper()
	svc := newTestLedgerService(t)
	return New(Config{
		LedgerService: svc,
		Sender:        fake,
	})
}

func snd_newOverlaySender(t *testing.T) *OverlaySender {
	t.Helper()
	overlay, err := peermanagement.New()
	require.NoError(t, err)
	return NewOverlaySender(overlay)
}

func TestSndAdaptor_BroadcastStatusChange_ViaOnPhaseChange(t *testing.T) {
	// BroadcastStatusChange is not public on *Adaptor; it's called
	// internally by OnPhaseChange when transitioning to Establish/Accepted.
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)
	// No error to check — the method is fire-and-forget.
}

func TestSndAdaptor_NetworkSenderBroadcastStatusChange(t *testing.T) {
	// Exercise the NetworkSender.BroadcastStatusChange path directly
	// through the noopSender (wired by default when Sender is nil).
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc})
	a.OnPhaseChange(consensus.PhaseOpen, consensus.PhaseEstablish)
}

func TestSndAdaptor_UpdateRelaySlot(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	key := []byte{0x02, 0x01, 0x02, 0x03}
	a.UpdateRelaySlot(key, 7, []uint64{1, 2, 3})
	calls := fake.relaySlotCalls
	require.Len(t, calls, 1)
	assert.Equal(t, uint64(7), calls[0].OriginPeer)
	assert.ElementsMatch(t, []uint64{1, 2, 3}, calls[0].SeenPeers)
}

func TestSndAdaptor_RequestTxSetMissingNodes(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	id := consensus.TxSetID{0xAB}
	nodeIDs := [][]byte{make([]byte, 33)}
	err := a.RequestTxSetMissingNodes(id, nodeIDs, nil)
	assert.NoError(t, err)
}

func TestSndAdaptor_RequestLedgerByHashAndSeq(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	var hash [32]byte
	hash[0] = 0xDE
	err := a.RequestLedgerByHashAndSeq(hash, 42)
	assert.NoError(t, err)
}

func TestSndAdaptor_RequestStateNodes(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	var hash [32]byte
	err := a.RequestStateNodes(99, hash, [][]byte{make([]byte, 33)})
	assert.NoError(t, err)
	require.Len(t, fake.sends[99], 1)
}

func TestSndAdaptor_RequestTransactionNodes(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	var hash [32]byte
	err := a.RequestTransactionNodes(77, hash, [][]byte{make([]byte, 33)})
	assert.NoError(t, err)
	require.Len(t, fake.sends[77], 1)
}

func TestSndAdaptor_SendToPeer(t *testing.T) {
	fake := &snd_fakeOverlay{}
	a := snd_newAdaptorWithFake(t, fake)
	frame := []byte{0x01, 0x02, 0x03}
	err := a.SendToPeer(55, frame)
	assert.NoError(t, err)
	require.Len(t, fake.sends[55], 1)
	assert.Equal(t, frame, fake.sends[55][0])
}

func TestSndAdaptor_ShouldShedLedgerRequest(t *testing.T) {
	fake := &snd_fakeOverlay{shedResult: true, shedResultPeerID: 11}
	a := snd_newAdaptorWithFake(t, fake)
	assert.True(t, a.ShouldShedLedgerRequest(11, false))
	assert.False(t, a.ShouldShedLedgerRequest(99, false))
}

func TestSndAdaptor_ReplayCapablePeersExcluding(t *testing.T) {
	fake := &snd_fakeOverlay{
		replayCaps: map[uint64]bool{1: true, 2: true, 3: false},
	}
	a := snd_newAdaptorWithFake(t, fake)
	peers := a.ReplayCapablePeersExcluding([]uint64{1}, 10)
	assert.Contains(t, peers, uint64(2))
	assert.NotContains(t, peers, uint64(1))
	assert.NotContains(t, peers, uint64(3))
}

func TestSndAdaptor_ReplayCapablePeersExcluding_ZeroMax(t *testing.T) {
	// The adaptor delegates directly; the max=0 guard lives in OverlaySender.
	fake := &snd_fakeOverlay{
		replayCaps: map[uint64]bool{1: true},
	}
	a := snd_newAdaptorWithFake(t, fake)
	peers := a.ReplayCapablePeersExcluding(nil, 1)
	assert.Len(t, peers, 1)
}

func TestSndAdaptor_PeersThatHave(t *testing.T) {
	fake := &snd_fakeOverlay{
		peersThatHave: map[[32]byte][]uint64{
			{0x01}: {10, 20},
		},
	}
	a := snd_newAdaptorWithFake(t, fake)
	var hash [32]byte
	hash[0] = 0x01
	got := a.PeersThatHave(hash)
	assert.ElementsMatch(t, []uint64{10, 20}, got)

	assert.Nil(t, a.PeersThatHave([32]byte{}))
}

func TestSndOverlaySender_BroadcastProposal_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	proposal := &consensus.Proposal{
		Round:          consensus.RoundID{Seq: 1},
		NodeID:         consensus.NodeID{0x01},
		SigningPubKey:  consensus.SigningPubKey{0x02},
		TxSet:          consensus.TxSetID{0x03},
		PreviousLedger: consensus.LedgerID{0x04},
		Signature:      make([]byte, 64),
	}
	err := s.BroadcastProposal(proposal)
	assert.NoError(t, err)
}

func TestSndOverlaySender_BroadcastValidation_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x01},
		LedgerSeq: 3,
		NodeID:    consensus.NodeID{0x02},
		Signature: make([]byte, 64),
	}
	err := s.BroadcastValidation(validation)
	assert.NoError(t, err)
}

func TestSndOverlaySender_RelayProposal_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	proposal := &consensus.Proposal{
		NodeID:         consensus.NodeID{0x01},
		SigningPubKey:  consensus.SigningPubKey{0x02},
		TxSet:          consensus.TxSetID{0x03},
		PreviousLedger: consensus.LedgerID{0x04},
		Signature:      make([]byte, 64),
	}
	err := s.RelayProposal(proposal, 0)
	assert.NoError(t, err)
}

func TestSndOverlaySender_RelayValidation_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x01},
		LedgerSeq: 3,
		NodeID:    consensus.NodeID{0x02},
		Signature: make([]byte, 64),
	}
	err := s.RelayValidation(validation, 0)
	assert.NoError(t, err)
}

func TestSndOverlaySender_UpdateRelaySlot(t *testing.T) {
	s := snd_newOverlaySender(t)
	s.UpdateRelaySlot([]byte{0x01, 0x02}, 1, []uint64{2, 3})
}

func TestSndOverlaySender_RequestTxSet_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	err := s.RequestTxSet(consensus.TxSetID{0xAB})
	assert.NoError(t, err)
}

func TestSndOverlaySender_RequestTxSetMissingNodes_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	nodeIDs := [][]byte{make([]byte, 33)}
	err := s.RequestTxSetMissingNodes(consensus.TxSetID{0x01}, nodeIDs, nil)
	assert.NoError(t, err)
}

func TestSndOverlaySender_RequestTxSetMissingNodes_EmptyNodeIDs(t *testing.T) {
	s := snd_newOverlaySender(t)
	err := s.RequestTxSetMissingNodes(consensus.TxSetID{0x01}, nil, nil)
	assert.Error(t, err)
}

func TestSndOverlaySender_RequestTxSetMissingNodes_WithExcluded_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	nodeIDs := [][]byte{make([]byte, 33)}
	excluded := map[uint64]bool{1: true, 2: true}
	err := s.RequestTxSetMissingNodes(consensus.TxSetID{0x01}, nodeIDs, excluded)
	assert.NoError(t, err)
}

func TestSndOverlaySender_BroadcastStatusChange_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	sc := &message.StatusChange{LedgerSeq: 10}
	err := s.BroadcastStatusChange(sc)
	assert.NoError(t, err)
}

func TestSndOverlaySender_RequestLedger_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	err := s.RequestLedger(consensus.LedgerID{0x01})
	assert.NoError(t, err)
}

func TestSndOverlaySender_RequestLedgerByHashAndSeq_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	hash[0] = 0xAB
	err := s.RequestLedgerByHashAndSeq(hash, 5)
	assert.NoError(t, err)
}

func TestSndOverlaySender_SendToPeer_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	err := s.SendToPeer(999, []byte{0x01})
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_ShouldShedLedgerRequest_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	result := s.ShouldShedLedgerRequest(999, true)
	assert.False(t, result)
}

func TestSndOverlaySender_RequestLedgerBaseFromPeer_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	err := s.RequestLedgerBaseFromPeer(999, hash, 3)
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_PeerSupportsReplay_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	assert.False(t, s.PeerSupportsReplay(999))
}

func TestSndOverlaySender_ReplayCapablePeersExcluding_NoPeers(t *testing.T) {
	s := snd_newOverlaySender(t)
	peers := s.ReplayCapablePeersExcluding(nil, 5)
	assert.Empty(t, peers)
}

func TestSndOverlaySender_ReplayCapablePeersExcluding_ZeroMax(t *testing.T) {
	s := snd_newOverlaySender(t)
	peers := s.ReplayCapablePeersExcluding(nil, 0)
	assert.Nil(t, peers)
}

func TestSndOverlaySender_IncPeerBadData(t *testing.T) {
	s := snd_newOverlaySender(t)
	s.IncPeerBadData(999, "test-reason")
}

func TestSndOverlaySender_PeersThatHave_UnknownHash(t *testing.T) {
	s := snd_newOverlaySender(t)
	assert.Nil(t, s.PeersThatHave([32]byte{0xFF}))
}

func TestSndOverlaySender_RequestReplayDelta_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	err := s.RequestReplayDelta(999, hash)
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_RequestStateNodes_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	err := s.RequestStateNodes(999, hash, [][]byte{make([]byte, 33)})
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_RequestTransactionNodes_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	err := s.RequestTransactionNodes(999, hash, [][]byte{make([]byte, 33)})
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

package adaptor

import (
	"sync"
	"testing"
	"time"

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
	frame, err := message.EncodeFrame(sc)
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

func (f *snd_fakeOverlay) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error {
	return nil
}

func (f *snd_fakeOverlay) RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error {
	return nil
}

func (f *snd_fakeOverlay) RequestLedger(id consensus.LedgerID) error {
	return nil
}

func (f *snd_fakeOverlay) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error {
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

func (f *snd_fakeOverlay) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends == nil {
		f.sends = make(map[uint64][][]byte)
	}
	f.sends[peerID] = append(f.sends[peerID], []byte("RequestStateNodes"))
	return nil
}

func (f *snd_fakeOverlay) RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error {
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

func (f *snd_fakeOverlay) SendPriorityToPeer(peerID uint64, frame []byte) error {
	return f.SendToPeer(peerID, frame)
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

func (f *snd_fakeOverlay) RecordMessageSource([32]byte, uint64) {}

func (f *snd_fakeOverlay) MessageRelayedRecently([32]byte) bool { return false }

func (f *snd_fakeOverlay) PeerLatency(uint64) (time.Duration, bool) { return 0, false }

func (f *snd_fakeOverlay) ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shedResult && peerID == f.shedResultPeerID
}

func (f *snd_fakeOverlay) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool) { return 0, false }
func (f *snd_fakeOverlay) SelectLedgerPeers([32]byte, uint32, []uint64, int) []uint64 {
	return nil
}
func (f *snd_fakeOverlay) PeerWithTxSet([32]byte, uint64) (uint64, bool) { return 0, false }
func (f *snd_fakeOverlay) NotePeerHasTxSet(uint64, [32]byte) bool        { return true }
func (f *snd_fakeOverlay) CheckTracking(uint32)                          {}

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

func TestSndOverlaySender_RequestTxSetMissingNodes_EmptyNodeIDs(t *testing.T) {
	s := snd_newOverlaySender(t)
	err := s.RequestTxSetMissingNodes(consensus.TxSetID{0x01}, nil, nil, false)
	assert.Error(t, err)
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
	err := s.RequestLedgerBaseFromPeer(999, hash, 3, false)
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_PeerSupportsReplay_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	assert.False(t, s.PeerSupportsReplay(999))
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
	err := s.RequestStateNodes(999, hash, [][]byte{make([]byte, 33)}, 1, false)
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

func TestSndOverlaySender_RequestTransactionNodes_UnknownPeer(t *testing.T) {
	s := snd_newOverlaySender(t)
	var hash [32]byte
	err := s.RequestTransactionNodes(999, hash, [][]byte{make([]byte, 33)}, 1, false)
	assert.ErrorIs(t, err, peermanagement.ErrPeerNotFound)
}

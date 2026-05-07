package adaptor

import (
	"bytes"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// OverlaySender implements NetworkSender using the P2P overlay.
type OverlaySender struct {
	overlay *peermanagement.Overlay
}

// NewOverlaySender creates a new OverlaySender.
func NewOverlaySender(overlay *peermanagement.Overlay) *OverlaySender {
	return &OverlaySender{overlay: overlay}
}

// BroadcastProposal sends OUR OWN proposal to every connected peer
// WITHOUT applying the squelch filter. Rippled skips the filter for
// self-originated broadcasts (OverlayImpl.cpp:1133-1137); a peer that
// squelches our own pubkey should NOT cause our own proposals to
// disappear from the network.
func (s *OverlaySender) BroadcastProposal(proposal *consensus.Proposal) error {
	msg := ProposalToMessage(proposal)
	frame, err := encodeFrame(message.TypeProposeLedger, msg)
	if err != nil {
		return fmt.Errorf("encode proposal: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

// BroadcastValidation sends OUR OWN validation to every connected peer
// WITHOUT applying the squelch filter. Same rationale as
// BroadcastProposal.
func (s *OverlaySender) BroadcastValidation(validation *consensus.Validation) error {
	msg := ValidationToMessage(validation)
	frame, err := encodeFrame(message.TypeValidation, msg)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

// RelayProposal forwards a peer-originated proposal to other peers,
// honoring the per-peer squelch filter on the ORIGINATING validator's
// pubkey (so peers that have signaled they no longer need that
// validator's gossip are skipped) and excluding the originating peer
// itself. Mirrors rippled's OverlayImpl::relay for TMProposeSet.
//
// proposal.SuppressionHash is the router-level dedup key (populated
// by the consensus router from the canonical proposalUniqueId hash at
// parse time). The overlay registers each recipient against that key
// in its reverse index; the index is queried by the consensus router
// on a later duplicate arrival so the slot is fed with the full set
// of known-havers (B3, PeerImp.cpp:3010-3017).
func (s *OverlaySender) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	msg := ProposalToMessage(proposal)
	frame, err := encodeFrame(message.TypeProposeLedger, msg)
	if err != nil {
		return fmt.Errorf("encode proposal: %w", err)
	}
	return s.overlay.RelayFromValidator(proposal.NodeID[:], proposal.SuppressionHash, peermanagement.PeerID(exceptPeer), frame)
}

// RelayValidation forwards a peer-originated validation to other peers
// with the same filter semantics as RelayProposal. Uses
// validation.SuppressionHash for the reverse-index record.
func (s *OverlaySender) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	msg := ValidationToMessage(validation)
	frame, err := encodeFrame(message.TypeValidation, msg)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	return s.overlay.RelayFromValidator(validation.NodeID[:], validation.SuppressionHash, peermanagement.PeerID(exceptPeer), frame)
}

// UpdateRelaySlot feeds the overlay's reduce-relay state machine with
// an inbound validator message. Mirrors rippled's
// PeerImp::updateSlotAndSquelch call in onMessage(TMProposeSet/TMValidation).
//
// seenPeers is the set of peers already known to have this message
// (from Overlay.PeersThatHave). Rippled's overlay_.relay returns that
// set as haveMessage and PeerImp passes it whole to
// updateSlotAndSquelch (PeerImp.cpp:3013-3017) so multi-path delivery
// evidence is counted — not just the current duplicate's origin. We
// dedupe originPeer out of seenPeers so no peer is double-counted.
func (s *OverlaySender) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	s.overlay.OnValidatorMessage(validatorKey, peermanagement.PeerID(originPeer))
	for _, p := range seenPeers {
		if p == originPeer {
			continue
		}
		s.overlay.OnValidatorMessage(validatorKey, peermanagement.PeerID(p))
	}
}

// RequestTxSet asks peers for the contents of a transaction set we know
// the hash of but don't yet hold. Sends TMGetLedger with itype=
// liTS_CANDIDATE, ledger_hash=txSetID, query_depth=3, and a single
// node_ids entry pointing at the SHAMap root.
//
// Mirrors rippled's TransactionAcquire::trigger initial request
// (TransactionAcquire.cpp:128-137):
//
//   tmGL.set_ledgerhash(hash);
//   tmGL.set_itype(protocol::liTS_CANDIDATE);
//   tmGL.set_querydepth(3);
//   *(tmGL.add_nodeids()) = SHAMapNodeID().getRawString();
//
// The node_ids field is REQUIRED for any itype != liBASE — rippled's
// PeerImp::onMessage(TMGetLedger) at PeerImp.cpp:1435-1438 rejects
// the message with "Invalid ledger node IDs" if nodeids_size() <= 0.
// Without the root node ID + query_depth, rippled silently drops the
// request and never sends back the tx-set, so goxrpl never acquires
// peer tx-sets, never creates per-tx disputes, and the avalanche
// resolution can't converge across goxrpl/rippled. Issue #401 layer 3.
//
// SHAMapNodeID root encoding (rippled SHAMapNodeID::getRawString):
//   33 bytes total = 32 bytes path (zero for root) + 1 byte depth (0).
func (s *OverlaySender) RequestTxSet(id consensus.TxSetID) error {
	rootNodeID := make([]byte, 33) // 32 zeros + 1 depth byte (0)
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: id[:],
		QueryDepth: 3,
		NodeIDs:    [][]byte{rootNodeID},
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	if err != nil {
		return fmt.Errorf("encode txset request: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) BroadcastStatusChange(sc *message.StatusChange) error {
	frame, err := encodeFrame(message.TypeStatusChange, sc)
	if err != nil {
		return fmt.Errorf("encode status change: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) RequestLedger(id consensus.LedgerID) error {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LedgerHash: id[:],
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LType:      message.LedgerTypeClosed,
		LedgerHash: hash[:],
		LedgerSeq:  seq,
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) SendToPeer(peerID uint64, frame []byte) error {
	return s.overlay.Send(peermanagement.PeerID(peerID), frame)
}

// RequestLedgerBaseFromPeer sends a GetLedger(LedgerInfoBase) to a specific peer.
func (s *OverlaySender) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LType:      message.LedgerTypeClosed,
		LedgerHash: hash[:],
		LedgerSeq:  seq,
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger (base): %w", err)
	}
	return s.overlay.Send(peermanagement.PeerID(peerID), frame)
}

// PeerSupportsReplay reports whether the peer advertised the ledger-replay
// feature via the X-Protocol-Ctl handshake header. False when the peer is
// unknown, the handshake hasn't completed, or the peer opted out.
func (s *OverlaySender) PeerSupportsReplay(peerID uint64) bool {
	return s.overlay.PeerSupports(peermanagement.PeerID(peerID), peermanagement.FeatureLedgerReplay)
}

// ReplayCapablePeersExcluding returns up to `max` peer IDs that
// advertised ledger-replay via handshake, omitting IDs in `excluded`.
// Uses the overlay's Peers() snapshot and filters by PeerSupports.
// O(n*m) in peers × excluded, which is fine for realistic n (< 100)
// and m (< subTaskRetryMax = 10).
func (s *OverlaySender) ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64 {
	if max <= 0 {
		return nil
	}
	excludedSet := make(map[uint64]struct{}, len(excluded))
	for _, id := range excluded {
		excludedSet[id] = struct{}{}
	}
	peers := s.overlay.Peers()
	out := make([]uint64, 0, max)
	for _, p := range peers {
		id := uint64(p.ID)
		if _, skip := excludedSet[id]; skip {
			continue
		}
		if !s.overlay.PeerSupports(p.ID, peermanagement.FeatureLedgerReplay) {
			continue
		}
		out = append(out, id)
		if len(out) >= max {
			break
		}
	}
	return out
}

// IncPeerBadData forwards to Overlay.IncPeerBadData. Called by the
// consensus router via Adaptor.IncPeerBadData when it detects malformed
// or invalid data from a peer (e.g., replay-delta verification
// failures, ledger-data hash/root mismatches). Safe no-op for unknown
// peers.
func (s *OverlaySender) IncPeerBadData(peerID uint64, reason string) {
	s.overlay.IncPeerBadData(peermanagement.PeerID(peerID), reason)
}

// PeersThatHave returns the peer IDs the overlay knows have the
// message with this suppression hash. Populated by the overlay as
// messages are relayed outward (see Overlay.RelayFromValidator); the
// consensus router queries this on duplicate arrivals so the
// reduce-relay slot gets fed with every known-haver (B3,
// PeerImp.cpp:3010-3017).
func (s *OverlaySender) PeersThatHave(suppressionHash [32]byte) []uint64 {
	raw := s.overlay.PeersThatHave(suppressionHash)
	if len(raw) == 0 {
		return nil
	}
	out := make([]uint64, len(raw))
	for i, p := range raw {
		out[i] = uint64(p)
	}
	return out
}

// RequestReplayDelta asks a specific peer for a fast-catchup replay delta
// (header + every tx blob, in tx-map order) for the given ledger hash.
// Mirrors rippled's LedgerDeltaAcquire::trigger which sends a
// TMReplayDeltaRequest via PeerSet::sendRequest
// (rippled/src/xrpld/app/ledger/detail/LedgerDeltaAcquire.cpp:124-156).
func (s *OverlaySender) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	msg := &message.ReplayDeltaRequest{LedgerHash: hash[:]}
	frame, err := encodeFrame(message.TypeReplayDeltaReq, msg)
	if err != nil {
		return fmt.Errorf("encode replay delta request: %w", err)
	}
	return s.overlay.Send(peermanagement.PeerID(peerID), frame)
}

// RequestStateNodes sends a GetLedger request for account state SHAMap nodes.
func (s *OverlaySender) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoAsNode,
		LedgerHash: ledgerHash[:],
		NodeIDs:    nodeIDs,
		QueryDepth: 2, // Return fat nodes (node + 2 levels of descendants)
	}
	frame, err := encodeFrame(message.TypeGetLedger, msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger (state nodes): %w", err)
	}
	return s.overlay.Send(peermanagement.PeerID(peerID), frame)
}

// encodeFrame serializes a message and wraps it with the wire protocol header.
// The result can be passed directly to Overlay.Broadcast() or Overlay.Send().
func encodeFrame(msgType message.MessageType, msg message.Message) ([]byte, error) {
	payload, err := message.Encode(msg)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := message.WriteMessage(&buf, msgType, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

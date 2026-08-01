package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// OverlaySender implements NetworkSender using the P2P overlay.
type OverlaySender struct {
	overlay *peermanagement.Overlay
}

func (s *OverlaySender) PeerLatency(peerID uint64) (time.Duration, bool) {
	return s.overlay.PeerLatency(peermanagement.PeerID(peerID))
}

// NewOverlaySender creates a new OverlaySender.
func NewOverlaySender(overlay *peermanagement.Overlay) *OverlaySender {
	return &OverlaySender{overlay: overlay}
}

func (s *OverlaySender) CheckTracking(validSeq uint32) {
	s.overlay.CheckTracking(validSeq)
}

// BroadcastProposal sends OUR OWN proposal to every connected peer
// WITHOUT applying the squelch filter: a peer that squelches our own
// pubkey should NOT cause our own proposals to disappear from the
// network.
func (s *OverlaySender) BroadcastProposal(proposal *consensus.Proposal) error {
	msg := ProposalToMessage(proposal)
	frame, err := message.EncodeFrame(msg)
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
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

// RelayProposal forwards a peer-originated proposal to other peers,
// honoring the per-peer squelch filter on the ORIGINATING validator's
// pubkey (so peers that have signaled they no longer need that
// validator's gossip are skipped) and excluding the originating peer
// itself.
//
// proposal.SuppressionHash is the router-level dedup key populated at
// parse time. The overlay uses it to exclude every peer that delivered
// this message to us.
func (s *OverlaySender) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	msg := ProposalToMessage(proposal)
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode proposal: %w", err)
	}
	return s.overlay.RelayFromValidator(proposal.SigningPubKey[:], proposal.SuppressionHash, peermanagement.PeerID(exceptPeer), frame)
}

// RelayValidation forwards a peer-originated validation to other peers
// with the same filter semantics as RelayProposal. Uses
// validation.SuppressionHash for the reverse-index record.
func (s *OverlaySender) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	msg := ValidationToMessage(validation)
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	return s.overlay.RelayFromValidator(validation.SigningPubKey[:], validation.SuppressionHash, peermanagement.PeerID(exceptPeer), frame)
}

// UpdateRelaySlot feeds the overlay's reduce-relay state machine with
// an inbound validator message.
//
// seenPeers is the set of peers already known to have this message
// (from Overlay.PeersThatHave); feeding it whole counts multi-path
// delivery evidence, not just the current duplicate's origin. We dedupe
// originPeer out of seenPeers so no peer is double-counted.
func (s *OverlaySender) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	s.overlay.OnValidatorMessage(validatorKey, peermanagement.PeerID(originPeer))
	for _, p := range seenPeers {
		if p == originPeer {
			continue
		}
		s.overlay.OnValidatorMessage(validatorKey, peermanagement.PeerID(p))
	}
}

// RequestTxSet asks peers for a known-hash tx-set we don't hold via
// TMGetLedger{itype=liTS_CANDIDATE, ledger_hash=txSetID, query_depth=3,
// node_ids=[SHAMapNodeID root]}. node_ids is required for itype != liBASE
// or peers reject the request. SHAMap root encoding is 33 zero bytes
// (zero path + depth=0). Issue #401.
func (s *OverlaySender) RequestTxSet(id consensus.TxSetID) error {
	rootNodeID := make([]byte, 33) // 32 zeros + 1 depth byte (0)
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: id[:],
		QueryDepth: 3,
		NodeIDs:    [][]byte{rootNodeID},
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode txset request: %w", err)
	}
	return s.overlay.BroadcastPriority(frame)
}

// RequestTxSetMissingNodes requests specific SHAMap nodes after a partial
// tree. Each nodeID is 33 bytes (32 path + 1 depth) per shamap.NodeID.Bytes.
// excluded carries peer IDs to skip (router-supplied list of peers that
// have repeatedly returned non-progressing TMLedgerData replies for this
// acquisition); a nil or empty map falls through to a plain broadcast.
// When indirect is set, query_type=qtINDIRECT marks the request relayable
// by intermediary peers (see indirectQueryType). Issue #420.
func (s *OverlaySender) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error {
	if len(nodeIDs) == 0 {
		return fmt.Errorf("RequestTxSetMissingNodes: nodeIDs must be non-empty")
	}
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: id[:],
		QueryDepth: 3,
		NodeIDs:    nodeIDs,
		QueryType:  indirectQueryType(indirect),
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode txset missing-nodes request: %w", err)
	}
	if len(excluded) == 0 {
		return s.overlay.BroadcastPriority(frame)
	}
	skip := make(map[peermanagement.PeerID]bool, len(excluded))
	for id := range excluded {
		skip[peermanagement.PeerID(id)] = true
	}
	return s.overlay.BroadcastPriorityExceptSet(skip, frame)
}

// RequestTxSetMissingNodesFromPeer unicasts the missing-nodes request to the
// single replying peer (see the NetworkSender interface doc). indirect sets
// query_type=qtINDIRECT.
func (s *OverlaySender) RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error {
	if len(nodeIDs) == 0 {
		return fmt.Errorf("RequestTxSetMissingNodesFromPeer: nodeIDs must be non-empty")
	}
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoTsCandidate,
		LedgerHash: id[:],
		QueryDepth: 3,
		NodeIDs:    nodeIDs,
		QueryType:  indirectQueryType(indirect),
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode txset missing-nodes (unicast) request: %w", err)
	}
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

func (s *OverlaySender) BroadcastStatusChange(sc *message.StatusChange) error {
	frame, err := message.EncodeFrame(sc)
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
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger: %w", err)
	}
	return s.overlay.BroadcastPriority(frame)
}

func (s *OverlaySender) RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LType:      message.LedgerTypeClosed,
		LedgerHash: hash[:],
		LedgerSeq:  seq,
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger: %w", err)
	}
	return s.overlay.BroadcastPriority(frame)
}

func (s *OverlaySender) SendToPeer(peerID uint64, frame []byte) error {
	return s.overlay.Send(peermanagement.PeerID(peerID), frame)
}

func (s *OverlaySender) SendPriorityToPeer(peerID uint64, frame []byte) error {
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

// ShouldShedLedgerRequest forwards to the overlay's load gate. See
// NetworkSender.ShouldShedLedgerRequest.
func (s *OverlaySender) ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool {
	return s.overlay.ShouldShedLedgerRequest(peermanagement.PeerID(peerID), loadedLocal)
}

// RequestLedgerBaseFromPeer sends a GetLedger(LedgerInfoBase) to a specific peer.
func (s *OverlaySender) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error {
	frame, err := encodeLedgerBaseRequest(hash, seq, indirect)
	if err != nil {
		return err
	}
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

func encodeLedgerBaseRequest(hash [32]byte, seq uint32, indirect bool) ([]byte, error) {
	msg := &message.GetLedger{
		InfoType:   message.LedgerInfoBase,
		LType:      message.LedgerTypeClosed,
		LedgerHash: hash[:],
		LedgerSeq:  seq,
	}
	if indirect {
		qt := message.QueryTypeIndirect
		msg.QueryType = &qt
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return nil, fmt.Errorf("encode get_ledger (base): %w", err)
	}
	return frame, nil
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

// PeerWithLedger forwards to Overlay.PeerWithLedger: selects a peer that
// can serve ledger (target, seq) (excluding `exclude`) to relay an
// unsatisfiable GetLedger to.
func (s *OverlaySender) PeerWithLedger(target [32]byte, seq uint32, exclude uint64) (uint64, bool) {
	id, ok := s.overlay.PeerWithLedger(target, seq, peermanagement.PeerID(exclude))
	return uint64(id), ok
}

func (s *OverlaySender) SelectLedgerPeers(target [32]byte, seq uint32, excluded []uint64, max int) []uint64 {
	ex := make([]peermanagement.PeerID, len(excluded))
	for i, id := range excluded {
		ex[i] = peermanagement.PeerID(id)
	}
	ids := s.overlay.SelectLedgerPeers(target, seq, ex, max)
	out := make([]uint64, len(ids))
	for i, id := range ids {
		out[i] = uint64(id)
	}
	return out
}

// PeerWithTxSet forwards to Overlay.PeerWithTxSet: selects a peer that
// advertised tx-set root target (excluding `exclude`) to relay an
// unsatisfiable liTS_CANDIDATE GetLedger to.
func (s *OverlaySender) PeerWithTxSet(target [32]byte, exclude uint64) (uint64, bool) {
	id, ok := s.overlay.PeerWithTxSet(target, peermanagement.PeerID(exclude))
	return uint64(id), ok
}

// NotePeerHasTxSet forwards to Overlay.NotePeerHasTxSet, recording a
// peer's tsHAVE tx-set advertisement for later relay selection.
func (s *OverlaySender) NotePeerHasTxSet(peerID uint64, hash [32]byte) bool {
	return s.overlay.NotePeerHasTxSet(peermanagement.PeerID(peerID), hash)
}

// IncPeerBadData forwards to Overlay.IncPeerBadData. Called by the
// consensus router via Adaptor.IncPeerBadData when it detects malformed
// or invalid data from a peer (e.g., replay-delta verification
// failures, ledger-data hash/root mismatches). Safe no-op for unknown
// peers.
func (s *OverlaySender) IncPeerBadData(peerID uint64, reason string) {
	s.overlay.IncPeerBadData(peermanagement.PeerID(peerID), reason)
}

func (s *OverlaySender) RecordMessageSource(suppressionHash [32]byte, peerID uint64) {
	s.overlay.RecordMessageSource(suppressionHash, peermanagement.PeerID(peerID))
}

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

func (s *OverlaySender) MessageRelayedRecently(suppressionHash [32]byte) bool {
	return s.overlay.MessageRelayedRecently(suppressionHash)
}

// RequestReplayDelta asks a specific peer for a fast-catchup replay delta
// (header + every tx blob, in tx-map order) for the given ledger hash via
// a TMReplayDeltaRequest.
func (s *OverlaySender) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	msg := &message.ReplayDeltaRequest{LedgerHash: hash[:]}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode replay delta request: %w", err)
	}
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

// RequestStateNodes sends a GetLedger request for account state SHAMap nodes.
// indirect sets query_type=qtINDIRECT (see indirectQueryType).
func (s *OverlaySender) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error {
	msg := &message.GetLedger{
		InfoType:      message.LedgerInfoAsNode,
		LedgerHash:    ledgerHash[:],
		NodeIDs:       nodeIDs,
		QueryDepth:    queryDepth,
		QueryDepthSet: true,
		QueryType:     indirectQueryType(indirect),
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger (state nodes): %w", err)
	}
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

// RequestTransactionNodes sends a GetLedger request for transaction SHAMap
// nodes. indirect sets query_type=qtINDIRECT (see indirectQueryType).
func (s *OverlaySender) RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error {
	msg := &message.GetLedger{
		InfoType:      message.LedgerInfoTxNode,
		LedgerHash:    ledgerHash[:],
		NodeIDs:       nodeIDs,
		QueryDepth:    queryDepth,
		QueryDepthSet: true,
		QueryType:     indirectQueryType(indirect),
	}
	frame, err := message.EncodeFrame(msg)
	if err != nil {
		return fmt.Errorf("encode get_ledger (tx nodes): %w", err)
	}
	return s.overlay.SendPriority(peermanagement.PeerID(peerID), frame)
}

// indirectQueryType returns the GetLedger query_type for an acquisition
// request: a presence-aware pointer to qtINDIRECT when indirect is set, nil
// otherwise. A relayer only forwards GetLedger requests that carry a
// query_type, so setting it lets peers fetch on our behalf. Mirrors rippled's
// InboundLedger::trigger / TransactionAcquire::trigger, which escalate to
// qtINDIRECT once an acquisition has timed out at least once (timeouts_ != 0)
// rather than on the first, directly-routed attempt.
func indirectQueryType(indirect bool) *message.LedgerQueryType {
	if !indirect {
		return nil
	}
	qt := message.QueryTypeIndirect
	return &qt
}

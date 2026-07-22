package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// NetworkSender abstracts the P2P overlay for sending messages.
type NetworkSender interface {
	// BroadcastProposal / BroadcastValidation send our own traffic, unfiltered
	// (the squelch filter applies only to relayed peer traffic).
	BroadcastProposal(proposal *consensus.Proposal) error
	BroadcastValidation(validation *consensus.Validation) error
	BroadcastStatusChange(sc *message.StatusChange) error
	CheckTracking(validSeq uint32)
	// RelayProposal / RelayValidation forward a peer-originated message to
	// other peers, subject to the squelch filter, excluding exceptPeer (the
	// originator; 0 = unfiltered, test-only). SuppressionHash keys the
	// overlay's reverse index so a later duplicate can recover the
	// known-haver set for the reduce-relay slot.
	RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error
	RelayValidation(validation *consensus.Validation, exceptPeer uint64) error
	// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with
	// originPeer and seenPeers (known-havers), driving squelch selection.
	// Implementations dedupe originPeer from seenPeers.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)
	RequestTxSet(id consensus.TxSetID) error
	// RequestTxSetMissingNodes requests specific SHAMap nodes (by 33-byte path
	// NodeID: 32 path + 1 depth) for an in-progress tx-set acquisition.
	// excluded peers are skipped (nil = unrestricted). indirect sets
	// query_type=qtINDIRECT; set it once the acquisition has timed out at
	// least once (see RequestStateNodes).
	RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error
	// RequestTxSetMissingNodesFromPeer is the unicast variant of
	// RequestTxSetMissingNodes: the request is sent only to the replying peer.
	// The inbound acquire pipeline uses it so a progressing reply re-requests
	// from the peer that just served; the broadcast variant stays the timer's
	// stalled-acquire fallback. nodeIDs may carry the 33-byte zero root ID to
	// (re)fetch the root.
	RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error
	RequestLedger(id consensus.LedgerID) error
	RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32, indirect bool) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	// RequestStateNodes / RequestTransactionNodes fetch outstanding
	// account-state / transaction SHAMap nodes of an in-flight acquisition.
	// queryDepth is 1 for reply-driven requests and 0 for timeout fanout;
	// indirect (query_type=qtINDIRECT) must be false on the first attempt and
	// true once the acquisition has timed out at least once
	// (rippled InboundLedger::trigger timeouts_ != 0).
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error
	RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, queryDepth uint32, indirect bool) error
	SendToPeer(peerID uint64, frame []byte) error
	SendPriorityToPeer(peerID uint64, frame []byte) error
	// PeerSupportsReplay reports whether the peer advertised the ledger-replay
	// feature during handshake (false for unknown/incomplete handshakes), so
	// the catchup policy can skip peers that would silently drop the request.
	PeerSupportsReplay(peerID uint64) bool
	// ReplayCapablePeersExcluding returns up to max peer IDs that advertised
	// ledger-replay, omitting those in excluded. Used by the replay-delta
	// retry loop to rotate peers on a sub-task timeout.
	ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64
	// IncPeerBadData attributes a malformed/invalid-data event to the peer so
	// the overlay can charge it toward the eviction threshold. No-op for
	// unknown peers; reason is a short stable label for logs.
	IncPeerBadData(peerID uint64, reason string)
	// PeersThatHave returns the peer IDs the overlay knows have the message
	// keyed by suppressionHash (nil if unknown or aged out). The router feeds
	// these known-havers into the reduce-relay slot on a duplicate arrival.
	PeersThatHave(suppressionHash [32]byte) []uint64
	// ShouldShedLedgerRequest reports whether a ledger-BODY request (liBASE /
	// liAS_NODE / liTX_NODE) from peerID should be dropped under load: peer
	// send-queue saturated, or local node fee-loaded and peer not clustered.
	// NEVER call for liTS_CANDIDATE (tx-set) requests — those serve on a
	// separate branch so consensus liveness isn't starved. False for unknown peers.
	ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool
	// PeerWithLedger returns a connected peer (other than exclude) that can
	// serve ledger (target, seq), to relay an unsatisfiable GetLedger.
	// ok is false when none qualifies.
	PeerWithLedger(target [32]byte, seq uint32, exclude uint64) (uint64, bool)
	// SelectLedgerPeers returns up to max connected peers (other than excluded)
	// for an inbound-ledger peer set, ranking known holders ahead of fallbacks.
	SelectLedgerPeers(target [32]byte, seq uint32, excluded []uint64, max int) []uint64
	// PeerWithTxSet returns a connected peer (other than exclude) that
	// advertised tx-set root target, to relay an unsatisfiable
	// liTS_CANDIDATE GetLedger.
	PeerWithTxSet(target [32]byte, exclude uint64) (uint64, bool)
	// NotePeerHasTxSet records that peerID advertised tx-set root hash,
	// feeding PeerWithTxSet.
	NotePeerHasTxSet(peerID uint64, hash [32]byte)
}

// noopSender is a no-op NetworkSender for standalone or test use.
type noopSender struct{}

func (n *noopSender) BroadcastProposal(*consensus.Proposal) error         { return nil }
func (n *noopSender) BroadcastValidation(*consensus.Validation) error     { return nil }
func (n *noopSender) BroadcastStatusChange(*message.StatusChange) error   { return nil }
func (n *noopSender) CheckTracking(uint32)                                {}
func (n *noopSender) RelayProposal(*consensus.Proposal, uint64) error     { return nil }
func (n *noopSender) RelayValidation(*consensus.Validation, uint64) error { return nil }
func (n *noopSender) UpdateRelaySlot([]byte, uint64, []uint64)            {}
func (n *noopSender) RequestTxSet(consensus.TxSetID) error                { return nil }
func (n *noopSender) RequestTxSetMissingNodes(consensus.TxSetID, [][]byte, map[uint64]bool, bool) error {
	return nil
}
func (n *noopSender) RequestTxSetMissingNodesFromPeer(consensus.TxSetID, [][]byte, uint64, bool) error {
	return nil
}
func (n *noopSender) RequestLedger(consensus.LedgerID) error                         { return nil }
func (n *noopSender) RequestLedgerByHashAndSeq([32]byte, uint32) error               { return nil }
func (n *noopSender) RequestLedgerBaseFromPeer(uint64, [32]byte, uint32, bool) error { return nil }
func (n *noopSender) RequestReplayDelta(uint64, [32]byte) error                      { return nil }
func (n *noopSender) RequestStateNodes(uint64, [32]byte, [][]byte, uint32, bool) error {
	return nil
}
func (n *noopSender) RequestTransactionNodes(uint64, [32]byte, [][]byte, uint32, bool) error {
	return nil
}
func (n *noopSender) SendToPeer(uint64, []byte) error                            { return nil }
func (n *noopSender) SendPriorityToPeer(uint64, []byte) error                    { return nil }
func (n *noopSender) PeerSupportsReplay(uint64) bool                             { return false }
func (n *noopSender) ReplayCapablePeersExcluding([]uint64, int) []uint64         { return nil }
func (n *noopSender) IncPeerBadData(uint64, string)                              {}
func (n *noopSender) PeersThatHave([32]byte) []uint64                            { return nil }
func (n *noopSender) ShouldShedLedgerRequest(uint64, bool) bool                  { return false }
func (n *noopSender) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool)     { return 0, false }
func (n *noopSender) SelectLedgerPeers([32]byte, uint32, []uint64, int) []uint64 { return nil }
func (n *noopSender) PeerWithTxSet([32]byte, uint64) (uint64, bool)              { return 0, false }
func (n *noopSender) NotePeerHasTxSet(uint64, [32]byte)                          {}

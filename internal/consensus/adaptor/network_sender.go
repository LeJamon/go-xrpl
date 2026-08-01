package adaptor

import (
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

type consensusNetwork interface {
	BroadcastProposal(proposal *consensus.Proposal) error
	BroadcastValidation(validation *consensus.Validation) error
	BroadcastStatusChange(sc *message.StatusChange) error
	CheckTracking(validSeq uint32)
	RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error
	RelayValidation(validation *consensus.Validation, exceptPeer uint64) error
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)
	RequestTxSet(id consensus.TxSetID) error
	RequestLedger(id consensus.LedgerID) error
}

type noopSender struct{}

var (
	_ consensusNetwork         = (*noopSender)(nil)
	_ gossipNetwork            = (*noopSender)(nil)
	_ txSetNetwork             = (*noopSender)(nil)
	_ ledgerAcquisitionNetwork = (*noopSender)(nil)
	_ ledgerServeNetwork       = (*noopSender)(nil)
)

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
func (n *noopSender) RecordMessageSource([32]byte, uint64)                       {}
func (n *noopSender) MessageRelayedRecently([32]byte) bool                       { return false }
func (n *noopSender) PeerLatency(uint64) (time.Duration, bool)                   { return 0, false }
func (n *noopSender) ShouldShedLedgerRequest(uint64, bool) bool                  { return false }
func (n *noopSender) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool)     { return 0, false }
func (n *noopSender) SelectLedgerPeers([32]byte, uint32, []uint64, int) []uint64 { return nil }
func (n *noopSender) PeerWithTxSet([32]byte, uint64) (uint64, bool)              { return 0, false }
func (n *noopSender) NotePeerHasTxSet(uint64, [32]byte) bool                     { return true }

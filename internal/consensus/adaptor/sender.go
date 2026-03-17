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

func (s *OverlaySender) BroadcastProposal(proposal *consensus.Proposal) error {
	msg := ProposalToMessage(proposal)
	frame, err := encodeFrame(message.TypeProposeLedger, msg)
	if err != nil {
		return fmt.Errorf("encode proposal: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) BroadcastValidation(validation *consensus.Validation) error {
	msg := ValidationToMessage(validation)
	frame, err := encodeFrame(message.TypeValidation, msg)
	if err != nil {
		return fmt.Errorf("encode validation: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) RelayProposal(proposal *consensus.Proposal) error {
	// RelayProposal is the same as BroadcastProposal for now.
	// In a full implementation, we'd exclude the originating peer.
	return s.BroadcastProposal(proposal)
}

func (s *OverlaySender) RequestTxSet(id consensus.TxSetID) error {
	msg := HaveSetToMessage(id, message.TxSetStatusNeed)
	frame, err := encodeFrame(message.TypeHaveSet, msg)
	if err != nil {
		return fmt.Errorf("encode txset request: %w", err)
	}
	return s.overlay.Broadcast(frame)
}

func (s *OverlaySender) RequestLedger(id consensus.LedgerID) error {
	// Request ledger data from peers using GetLedger message.
	// For now, this is a placeholder — full implementation in Phase 4.
	_ = id
	return nil
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

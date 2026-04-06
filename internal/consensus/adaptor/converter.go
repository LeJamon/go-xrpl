package adaptor

import (
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// ProposalFromMessage converts a decoded ProposeSet message to a consensus.Proposal.
func ProposalFromMessage(msg *message.ProposeSet) *consensus.Proposal {
	p := &consensus.Proposal{
		Position:  msg.ProposeSeq,
		Signature: msg.Signature,
		Timestamp: time.Now(),
	}

	// CloseTime: XRPL epoch seconds → time.Time
	p.CloseTime = xrplEpochToTime(msg.CloseTime)

	// NodeID from public key (33 bytes compressed)
	if len(msg.NodePubKey) == 33 {
		copy(p.NodeID[:], msg.NodePubKey)
	}

	// TxSet hash
	if len(msg.CurrentTxHash) == 32 {
		copy(p.TxSet[:], msg.CurrentTxHash)
	}

	// PreviousLedger hash
	if len(msg.PreviousLedger) == 32 {
		copy(p.PreviousLedger[:], msg.PreviousLedger)
		p.Round = consensus.RoundID{
			ParentHash: p.PreviousLedger,
		}
	}

	return p
}

// ProposalToMessage converts a consensus.Proposal to a ProposeSet message.
func ProposalToMessage(p *consensus.Proposal) *message.ProposeSet {
	return &message.ProposeSet{
		ProposeSeq:     p.Position,
		CurrentTxHash:  p.TxSet[:],
		NodePubKey:     p.NodeID[:],
		CloseTime:      timeToXrplEpoch(p.CloseTime),
		Signature:      p.Signature,
		PreviousLedger: p.PreviousLedger[:],
	}
}

// ValidationFromMessage parses a decoded Validation message (containing an
// XRPL-binary-encoded STValidation) into a consensus.Validation.
func ValidationFromMessage(msg *message.Validation) (*consensus.Validation, error) {
	v, err := parseSTValidation(msg.Validation)
	if err != nil {
		return nil, err
	}
	v.SeenTime = time.Now()
	return v, nil
}

// ValidationToMessage serializes a consensus.Validation to an XRPL-binary-encoded
// STValidation suitable for the TMValidation protobuf wire format.
func ValidationToMessage(v *consensus.Validation) *message.Validation {
	return &message.Validation{
		Validation: serializeSTValidation(v),
	}
}

// TransactionFromMessage extracts the raw transaction blob from a Transaction message.
func TransactionFromMessage(msg *message.Transaction) []byte {
	return msg.RawTransaction
}

// TransactionToMessage wraps a raw transaction blob into a Transaction message.
func TransactionToMessage(txBlob []byte) *message.Transaction {
	return &message.Transaction{
		RawTransaction:   txBlob,
		Status:           message.TxStatusNew,
		ReceiveTimestamp: uint64(time.Now().UnixNano()),
	}
}

// HaveSetFromMessage converts a decoded HaveTransactionSet message.
func HaveSetFromMessage(msg *message.HaveTransactionSet) (consensus.TxSetID, message.TxSetStatus) {
	var id consensus.TxSetID
	if len(msg.Hash) == 32 {
		copy(id[:], msg.Hash)
	}
	return id, msg.Status
}

// HaveSetToMessage creates a HaveTransactionSet message.
func HaveSetToMessage(id consensus.TxSetID, status message.TxSetStatus) *message.HaveTransactionSet {
	return &message.HaveTransactionSet{
		Status: status,
		Hash:   id[:],
	}
}

func xrplEpochToTime(epoch uint32) time.Time {
	return time.Unix(int64(epoch)+xrplEpochOffset, 0)
}

func timeToXrplEpoch(t time.Time) uint32 {
	return uint32(t.Unix() - xrplEpochOffset)
}

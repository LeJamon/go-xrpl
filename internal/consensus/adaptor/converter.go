package adaptor

import (
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
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

	// SigningPubKey carries the ephemeral 33-byte compressed key the
	// proposal was signed with (the wire's TMProposeSet.nodepubkey).
	// NodeID is derived from it via calcNodeID; the consensus router
	// substitutes the master-derived NodeID via the manifest cache
	// when a mapping exists (see Router.handleProposal).
	if len(msg.NodePubKey) == 33 {
		copy(p.SigningPubKey[:], msg.NodePubKey)
		p.NodeID = consensus.CalcNodeID(p.SigningPubKey)
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
// The wire's NodePubKey field carries the 33-byte ephemeral signing key
// (sfSigningPubKey semantics), not the 20-byte master-derived NodeID.
func ProposalToMessage(p *consensus.Proposal) *message.ProposeSet {
	return &message.ProposeSet{
		ProposeSeq:     p.Position,
		CurrentTxHash:  p.TxSet[:],
		NodePubKey:     p.SigningPubKey[:],
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
//
// Caches the wire bytes on v.Raw if not already populated, so downstream
// consumers (the validation archive, suppression-hash computation) can
// reuse the canonical blob without a second serialize pass.
func ValidationToMessage(v *consensus.Validation) *message.Validation {
	blob := SerializeSTValidation(v)
	if len(v.Raw) == 0 {
		v.Raw = append([]byte(nil), blob...)
	}
	return &message.Validation{
		Validation: blob,
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
	return time.Unix(int64(epoch)+protocol.RippleEpochUnix, 0)
}

func timeToXrplEpoch(t time.Time) uint32 {
	return uint32(t.Unix() - protocol.RippleEpochUnix)
}

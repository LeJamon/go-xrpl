package adaptor

import (
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
)

// proposalFromMessage converts a decoded ProposeSet message to a consensus.Proposal.
func proposalFromMessage(msg *message.ProposeSet) *consensus.Proposal {
	p := &consensus.Proposal{
		Position:  msg.ProposeSeq,
		Signature: msg.Signature,
		Timestamp: time.Now(),
	}

	p.CloseTime = xrplEpochToTime(msg.CloseTime)

	// SigningPubKey carries the ephemeral 33-byte compressed key the
	// proposal was signed with. NodeID is derived from it; the consensus
	// router substitutes the master-derived NodeID via the manifest cache
	// when a mapping exists.
	if len(msg.NodePubKey) == 33 {
		copy(p.SigningPubKey[:], msg.NodePubKey)
		p.NodeID = consensus.CalcNodeID(p.SigningPubKey)
	}

	if len(msg.CurrentTxHash) == 32 {
		copy(p.TxSet[:], msg.CurrentTxHash)
	}

	if len(msg.PreviousLedger) == 32 {
		copy(p.PreviousLedger[:], msg.PreviousLedger)
		p.Round = consensus.RoundID{
			ParentHash: p.PreviousLedger,
		}
	}

	return p
}

// proposalToMessage converts a consensus.Proposal to a ProposeSet message.
// The wire's NodePubKey field carries the 33-byte ephemeral signing key
// (sfSigningPubKey semantics), not the 20-byte master-derived NodeID.
func proposalToMessage(p *consensus.Proposal) *message.ProposeSet {
	return &message.ProposeSet{
		ProposeSeq:     p.Position,
		CurrentTxHash:  p.TxSet[:],
		NodePubKey:     p.SigningPubKey[:],
		CloseTime:      timeToXrplEpoch(p.CloseTime),
		Signature:      p.Signature,
		PreviousLedger: p.PreviousLedger[:],
	}
}

// validationFromMessage parses a decoded Validation message (containing an
// XRPL-binary-encoded STValidation) into a consensus.Validation. seen
// becomes the validation's SeenTime; pass the network-adjusted clock so
// freshness checks compare it against the same clock they gate on.
func validationFromMessage(msg *message.Validation, seen time.Time) (*consensus.Validation, error) {
	v, err := parseSTValidation(msg.Validation)
	if err != nil {
		return nil, err
	}
	v.SeenTime = seen
	return v, nil
}

// validationToMessage serializes a consensus.Validation to an XRPL-binary-encoded
// STValidation suitable for the TMValidation protobuf wire format.
//
// Caches the wire bytes on v.Raw if not already populated, so downstream
// consumers (the validation archive, suppression-hash computation) can
// reuse the canonical blob without a second serialize pass.
func validationToMessage(v *consensus.Validation) *message.Validation {
	// Forward v.Raw verbatim — the signature only verifies against the
	// original preimage, so any re-serialization (VL encoding drift,
	// dropped optional fields, ordering) causes downstream peers to
	// reject with "invalid validation signature". When Raw is empty we
	// just signed locally; serializeSTValidation is canonical.
	if len(v.Raw) > 0 {
		return &message.Validation{
			Validation: append([]byte(nil), v.Raw...),
		}
	}
	blob := serializeSTValidation(v)
	v.Raw = append([]byte(nil), blob...)
	return &message.Validation{
		Validation: blob,
	}
}

// transactionFromMessage extracts the raw transaction blob from a Transaction message.
func transactionFromMessage(msg *message.Transaction) []byte {
	return msg.RawTransaction
}

// haveSetFromMessage converts a decoded HaveTransactionSet message.
func haveSetFromMessage(msg *message.HaveTransactionSet) (consensus.TxSetID, message.TxSetStatus, error) {
	var id consensus.TxSetID
	if len(msg.Hash) != len(id) {
		return id, msg.Status, fmt.Errorf("have_set hash must be %d bytes: got %d", len(id), len(msg.Hash))
	}
	copy(id[:], msg.Hash)
	return id, msg.Status, nil
}

func xrplEpochToTime(epoch uint32) time.Time {
	return protocol.FromRippleTime(epoch)
}

func timeToXrplEpoch(t time.Time) uint32 {
	return protocol.ToRippleTime(t)
}

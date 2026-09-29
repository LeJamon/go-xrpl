package state

import (
	"fmt"
	"strings"

	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// SerializeEscrow serializes an Escrow ledger entry from its creation inputs.
// For XRP escrows Amount is a drops string, for IOU escrows the full IOU object
// (value/currency/issuer), for MPT escrows {value, mpt_issuance_id}. transferRate
// is stored only when non-zero and not the parity rate (1_000_000_000). Optional
// fields are carried as pointers so nil (absent) is distinct from a present zero,
// matching the transaction's own field presence. sequence is emitted only when
// non-nil (fixIncludeKeyletFields).
func SerializeEscrow(ownerID, destID [20]byte, amount Amount, transferRate uint32,
	ownerNode, destNode uint64, hasDestNode bool, issuerNode uint64, hasIssuerNode bool,
	finishAfter, cancelAfter *uint32, condition string,
	sourceTag, destinationTag *uint32, sequence *uint32) ([]byte, error) {
	entry := &ledgerfields.Escrow{}
	if err := entry.SetAccountValue(ownerID); err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}
	if err := entry.SetDestinationValue(destID); err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}
	if err := entry.SetAmountValue(amount.LedgerValue()); err != nil {
		return nil, err
	}
	entry.SetOwnerNodeValue(ownerNode)
	entry.SetFlagsValue(0)

	if hasDestNode {
		entry.SetDestinationNodeValue(destNode)
	}
	if hasIssuerNode {
		entry.SetIssuerNodeValue(issuerNode)
	}
	if finishAfter != nil {
		entry.SetFinishAfterValue(*finishAfter)
	}
	if cancelAfter != nil {
		entry.SetCancelAfterValue(*cancelAfter)
	}
	if condition != "" {
		entry.SetCondition(condition)
	}
	if sourceTag != nil {
		entry.SetSourceTagValue(*sourceTag)
	}
	if destinationTag != nil {
		entry.SetDestinationTagValue(*destinationTag)
	}
	if transferRate > 0 && transferRate != 1_000_000_000 {
		entry.SetTransferRateValue(transferRate)
	}
	if sequence != nil {
		entry.SetSequenceValue(*sequence)
	}

	return entry.Encode()
}

// EscrowData represents an Escrow ledger entry
type EscrowData struct {
	Account         [20]byte
	DestinationID   [20]byte
	Amount          uint64  // XRP drops (only valid when IsXRP is true)
	IsXRP           bool    // true if the escrow Amount is XRP
	IOUAmount       *Amount // non-nil for IOU escrows (the full Amount with currency/issuer)
	MPTAmount       *int64  // non-nil for MPT escrows (raw int64 value)
	MPTIssuanceID   string  // hex-encoded MPT issuance ID (set when MPT)
	Condition       string
	CancelAfter     uint32
	FinishAfter     uint32
	SourceTag       uint32
	HasSourceTag    bool
	DestinationTag  uint32
	HasDestTag      bool
	OwnerNode       uint64
	DestinationNode uint64
	HasDestNode     bool
	IssuerNode      uint64
	HasIssuerNode   bool
	TransferRate    uint32
	HasTransferRate bool
	Flags           uint32
}

// ParseEscrow parses an Escrow ledger entry from binary data
func ParseEscrow(data []byte) (*EscrowData, error) {
	entry := &ledgerfields.Escrow{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	var err error
	escrow := &EscrowData{
		Condition:       strings.ToLower(entry.Condition),
		HasSourceTag:    entry.HasSourceTag(),
		HasDestTag:      entry.HasDestinationTag(),
		HasDestNode:     entry.HasDestinationNode(),
		HasIssuerNode:   entry.HasIssuerNode(),
		HasTransferRate: entry.HasTransferRate(),
	}
	if entry.HasFlags() {
		escrow.Flags, err = entry.GetFlags()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasCancelAfter() {
		escrow.CancelAfter, err = entry.GetCancelAfter()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasFinishAfter() {
		escrow.FinishAfter, err = entry.GetFinishAfter()
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasSourceTag {
		escrow.SourceTag, err = entry.GetSourceTag()
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasDestTag {
		escrow.DestinationTag, err = entry.GetDestinationTag()
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasTransferRate {
		escrow.TransferRate, err = entry.GetTransferRate()
		if err != nil {
			return nil, err
		}
	}

	if entry.HasAccount() {
		escrow.Account, err = entry.GetAccount()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasDestination() {
		escrow.DestinationID, err = entry.GetDestination()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasOwnerNode() {
		escrow.OwnerNode, err = entry.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasDestNode {
		escrow.DestinationNode, err = entry.GetDestinationNode()
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasIssuerNode {
		escrow.IssuerNode, err = entry.GetIssuerNode()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasAmount() {
		amountValue, err := entry.GetAmount()
		if err != nil {
			return nil, err
		}
		amount, err := decodeLedgerAmount("Escrow.Amount", amountValue)
		if err != nil {
			return nil, err
		}
		switch {
		case amount.IsNative():
			escrow.Amount, err = nonNegativeNativeDrops("Escrow.Amount", amount)
			if err != nil {
				return nil, err
			}
			escrow.IsXRP = true
		case amount.IsMPT():
			escrow.IOUAmount = &amount
			if raw, ok := amount.MPTRaw(); ok {
				escrow.MPTAmount = &raw
			}
			escrow.MPTIssuanceID = amount.MPTIssuanceID()
		default:
			escrow.IOUAmount = &amount
		}
	}

	return escrow, nil
}

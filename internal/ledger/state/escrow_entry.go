package state

import (
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
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
	sourceTag, destinationTag *uint32, sequence *uint32, sponsor ...string) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	var amountVal any
	if amount.IsNative() {
		amountVal = fmt.Sprintf("%d", amount.Drops())
	} else if amount.IsMPT() {
		// MPT amounts are whole numbers — use MPTRaw() to avoid IOU
		// normalization which loses precision for large values (>16 digits).
		mptValue := amount.Value()
		if raw, ok := amount.MPTRaw(); ok {
			mptValue = fmt.Sprintf("%d", raw)
		}
		amountVal = map[string]any{
			"value":           mptValue,
			"mpt_issuance_id": amount.MPTIssuanceID(),
		}
	} else {
		amountVal = map[string]any{
			"value":    amount.Value(),
			"currency": amount.Currency,
			"issuer":   amount.Issuer,
		}
	}

	entry := &ledgerfields.Escrow{}
	entry.SetAccount(ownerAddress)
	entry.SetDestination(destAddress)
	entry.SetAmount(amountVal)
	entry.SetOwnerNode(fmt.Sprintf("%x", ownerNode))
	entry.SetFlags(0)

	if hasDestNode {
		entry.SetDestinationNode(fmt.Sprintf("%x", destNode))
	}
	if hasIssuerNode {
		entry.SetIssuerNode(fmt.Sprintf("%x", issuerNode))
	}
	if finishAfter != nil {
		entry.SetFinishAfter(*finishAfter)
	}
	if cancelAfter != nil {
		entry.SetCancelAfter(*cancelAfter)
	}
	if condition != "" {
		entry.SetCondition(condition)
	}
	if sourceTag != nil {
		entry.SetSourceTag(*sourceTag)
	}
	if destinationTag != nil {
		entry.SetDestinationTag(*destinationTag)
	}
	if transferRate > 0 && transferRate != 1_000_000_000 {
		entry.SetTransferRate(transferRate)
	}
	if sequence != nil {
		entry.SetSequence(*sequence)
	}
	if len(sponsor) > 0 && sponsor[0] != "" {
		entry.SetSponsor(sponsor[0])
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
	Sponsor         string
}

// ParseEscrow parses an Escrow ledger entry from binary data
func ParseEscrow(data []byte) (*EscrowData, error) {
	entry := &ledgerfields.Escrow{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	fields := entry.ToMap()
	escrow := &EscrowData{
		Condition:       strings.ToLower(entry.Condition),
		CancelAfter:     entry.CancelAfter,
		FinishAfter:     entry.FinishAfter,
		SourceTag:       entry.SourceTag,
		HasSourceTag:    fields["SourceTag"] != nil,
		DestinationTag:  entry.DestinationTag,
		HasDestTag:      fields["DestinationTag"] != nil,
		HasDestNode:     fields["DestinationNode"] != nil,
		HasIssuerNode:   fields["IssuerNode"] != nil,
		TransferRate:    entry.TransferRate,
		HasTransferRate: fields["TransferRate"] != nil,
		Flags:           entry.Flags,
		Sponsor:         entry.Sponsor,
	}

	var err error
	if fields["Account"] != nil {
		escrow.Account, err = decodeLedgerAccount("Escrow.Account", entry.Account)
		if err != nil {
			return nil, err
		}
	}
	if fields["Destination"] != nil {
		escrow.DestinationID, err = decodeLedgerAccount("Escrow.Destination", entry.Destination)
		if err != nil {
			return nil, err
		}
	}
	if fields["OwnerNode"] != nil {
		escrow.OwnerNode, err = parseLedgerUint64("Escrow.OwnerNode", entry.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasDestNode {
		escrow.DestinationNode, err = parseLedgerUint64("Escrow.DestinationNode", entry.DestinationNode)
		if err != nil {
			return nil, err
		}
	}
	if escrow.HasIssuerNode {
		escrow.IssuerNode, err = parseLedgerUint64("Escrow.IssuerNode", entry.IssuerNode)
		if err != nil {
			return nil, err
		}
	}
	if fields["Amount"] != nil {
		amount, err := decodeLedgerAmount("Escrow.Amount", entry.Amount)
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

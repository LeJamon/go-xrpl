package state

import (
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// CheckData represents a Check ledger entry
type CheckData struct {
	Account           [20]byte
	DestinationID     [20]byte
	SendMax           uint64 // XRP drops (when IsNativeSendMax is true)
	SendMaxAmount     Amount // Full Amount representation (for both XRP and IOU)
	IsNativeSendMax   bool
	Sequence          uint32
	Expiration        uint32
	InvoiceID         [32]byte
	HasInvoiceID      bool
	DestinationTag    uint32
	HasDestTag        bool
	SourceTag         uint32
	HasSourceTag      bool
	OwnerNode         uint64
	DestinationNode   uint64
	HasDestNode       bool
	Sponsor           string
	HasSponsor        bool
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// ParseCheck parses a Check ledger entry from binary data
func ParseCheck(data []byte) (*CheckData, error) {
	entry := &ledgerfields.Check{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	fields := entry.ToMap()
	check := &CheckData{
		Sequence:          entry.Sequence,
		Expiration:        entry.Expiration,
		SourceTag:         entry.SourceTag,
		DestinationTag:    entry.DestinationTag,
		PreviousTxnLgrSeq: entry.PreviousTxnLgrSeq,
		HasSourceTag:      fields["SourceTag"] != nil,
		HasDestTag:        fields["DestinationTag"] != nil,
		HasDestNode:       fields["DestinationNode"] != nil,
		HasInvoiceID:      fields["InvoiceID"] != nil,
		Sponsor:           entry.Sponsor,
		HasSponsor:        fields["Sponsor"] != nil,
	}

	var err error
	if fields["Account"] != nil {
		check.Account, err = decodeLedgerAccount("Check.Account", entry.Account)
		if err != nil {
			return nil, err
		}
	}
	if fields["Destination"] != nil {
		check.DestinationID, err = decodeLedgerAccount("Check.Destination", entry.Destination)
		if err != nil {
			return nil, err
		}
	}
	if fields["OwnerNode"] != nil {
		check.OwnerNode, err = parseLedgerUint64("Check.OwnerNode", entry.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if check.HasDestNode {
		check.DestinationNode, err = parseLedgerUint64("Check.DestinationNode", entry.DestinationNode)
		if err != nil {
			return nil, err
		}
	}
	if check.HasInvoiceID {
		if err := decodeLedgerHex("Check.InvoiceID", entry.InvoiceID, check.InvoiceID[:]); err != nil {
			return nil, err
		}
	}
	if fields["PreviousTxnID"] != nil {
		if err := decodeLedgerHex("Check.PreviousTxnID", entry.PreviousTxnID, check.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	if fields["SendMax"] != nil {
		check.SendMaxAmount, err = decodeLedgerAmount("Check.SendMax", entry.SendMax)
		if err != nil {
			return nil, err
		}
		check.IsNativeSendMax = check.SendMaxAmount.IsNative()
		if check.IsNativeSendMax {
			check.SendMax, err = nonNegativeNativeDrops("Check.SendMax", check.SendMaxAmount)
			if err != nil {
				return nil, err
			}
		}
	}

	return check, nil
}

// SerializeCheckFromData serializes a Check ledger entry from CheckData.
func SerializeCheckFromData(check *CheckData) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(check.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(check.DestinationID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	entry := &ledgerfields.Check{}
	entry.SetAccount(ownerAddress)
	entry.SetDestination(destAddress)
	entry.SetSequence(check.Sequence)
	entry.SetOwnerNode(fmt.Sprintf("%x", check.OwnerNode))
	entry.SetDestinationNode(fmt.Sprintf("%x", check.DestinationNode))
	entry.SetFlags(0)
	if check.HasSponsor || check.Sponsor != "" {
		entry.SetSponsor(check.Sponsor)
	}

	if check.IsNativeSendMax {
		entry.SetSendMax(fmt.Sprintf("%d", check.SendMax))
	} else if check.SendMaxAmount.IsMPT() {
		entry.SetSendMax(map[string]any{
			"value":           check.SendMaxAmount.Value(),
			"mpt_issuance_id": check.SendMaxAmount.MPTIssuanceID(),
		})
	} else {
		entry.SetSendMax(map[string]any{
			"value":    check.SendMaxAmount.Value(),
			"currency": check.SendMaxAmount.Currency,
			"issuer":   check.SendMaxAmount.Issuer,
		})
	}

	if check.Expiration > 0 {
		entry.SetExpiration(check.Expiration)
	}

	if check.HasDestTag {
		entry.SetDestinationTag(check.DestinationTag)
	}

	if check.HasSourceTag {
		entry.SetSourceTag(check.SourceTag)
	}

	if check.HasInvoiceID {
		entry.SetInvoiceID(fmt.Sprintf("%X", check.InvoiceID[:]))
	}

	if check.PreviousTxnID != ([32]byte{}) {
		entry.SetPreviousTxnID(fmt.Sprintf("%X", check.PreviousTxnID[:]))
		entry.SetPreviousTxnLgrSeq(check.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

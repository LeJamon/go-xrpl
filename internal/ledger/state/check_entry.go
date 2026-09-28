package state

import (
	"fmt"
	"strconv"

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
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	decoded           ledgerfields.Check
}

// ParseCheck parses a Check ledger entry from binary data
func ParseCheck(data []byte) (*CheckData, error) {
	entry := &ledgerfields.Check{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	check := &CheckData{
		Sequence:          entry.Sequence,
		Expiration:        entry.Expiration,
		SourceTag:         entry.SourceTag,
		DestinationTag:    entry.DestinationTag,
		PreviousTxnLgrSeq: entry.PreviousTxnLgrSeq,
		decoded:           *entry,
		HasSourceTag:      entry.HasSourceTag(),
		HasDestTag:        entry.HasDestinationTag(),
		HasDestNode:       entry.HasDestinationNode(),
		HasInvoiceID:      entry.HasInvoiceID(),
	}

	var err error
	if entry.HasAccount() {
		check.Account, err = entry.GetAccount()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasDestination() {
		check.DestinationID, err = entry.GetDestination()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasOwnerNode() {
		check.OwnerNode, err = entry.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if check.HasDestNode {
		check.DestinationNode, err = entry.GetDestinationNode()
		if err != nil {
			return nil, err
		}
	}
	if check.HasInvoiceID {
		check.InvoiceID, err = entry.GetInvoiceID()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasPreviousTxnID() {
		check.PreviousTxnID, err = entry.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasSendMax() {
		value, err := entry.GetSendMax()
		if err != nil {
			return nil, err
		}
		check.SendMaxAmount, err = decodeLedgerAmount("Check.SendMax", value)
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

	entry := check.decoded
	if !entry.HasAccount() || entry.Account != "" || check.Account != [20]byte{} {
		entry.SetAccount(ownerAddress)
	}
	if !entry.HasDestination() || entry.Destination != "" || check.DestinationID != [20]byte{} {
		entry.SetDestination(destAddress)
	}
	entry.SetSequence(check.Sequence)
	entry.SetOwnerNodeValue(check.OwnerNode)
	entry.SetDestinationNodeValue(check.DestinationNode)
	if !entry.HasFlags() {
		entry.SetFlags(0)
	}

	amount := ledgerfields.AmountValue{Value: check.SendMaxAmount.Value()}
	if check.IsNativeSendMax {
		amount.Value = strconv.FormatUint(check.SendMax, 10)
	} else if check.SendMaxAmount.IsMPT() {
		amount.MPTIssuanceID = check.SendMaxAmount.MPTIssuanceID()
	} else {
		amount.Currency = check.SendMaxAmount.Currency
		amount.Issuer = check.SendMaxAmount.Issuer
	}
	if err := entry.SetSendMaxValue(amount); err != nil {
		return nil, err
	}

	if check.Expiration > 0 || (check.decoded.HasExpiration() && check.Expiration == check.decoded.Expiration) {
		entry.SetExpiration(check.Expiration)
	} else {
		entry.ClearExpiration()
	}

	if check.HasDestTag {
		entry.SetDestinationTag(check.DestinationTag)
	} else {
		entry.ClearDestinationTag()
	}

	if check.HasSourceTag {
		entry.SetSourceTag(check.SourceTag)
	} else {
		entry.ClearSourceTag()
	}

	if check.HasInvoiceID {
		entry.SetInvoiceIDValue(check.InvoiceID)
	} else {
		entry.ClearInvoiceID()
	}

	if check.PreviousTxnID != ([32]byte{}) || check.decoded.HasPreviousTxnID() {
		entry.SetPreviousTxnIDValue(check.PreviousTxnID)
		entry.SetPreviousTxnLgrSeq(check.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

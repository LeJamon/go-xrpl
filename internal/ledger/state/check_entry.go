package state

import (
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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
}

// ParseCheck parses a Check ledger entry from binary data
func ParseCheck(data []byte) (*CheckData, error) {
	check := &CheckData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 3: // SourceTag
				check.SourceTag = f.UInt32()
				check.HasSourceTag = true
			case 4: // Sequence
				check.Sequence = f.UInt32()
			case 5: // PreviousTxnLgrSeq
				check.PreviousTxnLgrSeq = f.UInt32()
			case 10: // Expiration
				check.Expiration = f.UInt32()
			case 14: // DestinationTag
				check.DestinationTag = f.UInt32()
				check.HasDestTag = true
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode
				check.OwnerNode = f.UInt64()
			case 9: // DestinationNode
				check.DestinationNode = f.UInt64()
				check.HasDestNode = true
			}

		case stHash256:
			switch f.FieldCode {
			case 5: // PreviousTxnID
				check.PreviousTxnID = f.Hash256()
			case 17: // InvoiceID
				check.InvoiceID = f.Hash256()
				check.HasInvoiceID = true
			}

		case stAmount:
			if f.FieldCode == 9 { // SendMax
				if len(f.Value) == 8 {
					check.SendMax = xrpDrops(f.Value)
					check.IsNativeSendMax = true
					check.SendMaxAmount = NewXRPAmountFromInt(int64(check.SendMax))
				} else if len(f.Value) == 48 {
					iouAmount, err := ParseIOUAmountBinary(f.Value)
					if err != nil {
						return err
					}
					check.SendMaxAmount = iouAmount
					check.IsNativeSendMax = false
				}
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok {
				switch f.FieldCode {
				case 1: // Account
					check.Account = id
				case 3: // Destination
					check.DestinationID = id
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
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

	jsonObj := map[string]any{
		"LedgerEntryType": "Check",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Sequence":        check.Sequence,
		"OwnerNode":       fmt.Sprintf("%x", check.OwnerNode),
		"Flags":           uint32(0),
	}

	// Serialize SendMax
	if check.IsNativeSendMax {
		jsonObj["SendMax"] = fmt.Sprintf("%d", check.SendMax)
	} else {
		jsonObj["SendMax"] = map[string]any{
			"value":    check.SendMaxAmount.Value(),
			"currency": check.SendMaxAmount.Currency,
			"issuer":   check.SendMaxAmount.Issuer,
		}
	}

	// sfDestinationNode is soeREQUIRED on ltCHECK (ledger_entries.macro:67),
	// so rippled always serializes it — even at its default 0. The SLE template
	// makes the field present at construction; CreateCheck.cpp only overwrites it
	// (to the destination owner-dir page) for the non-self-send path. Omitting it
	// when zero diverges the SLE state (account_hash fork).
	jsonObj["DestinationNode"] = fmt.Sprintf("%x", check.DestinationNode)

	if check.Expiration > 0 {
		jsonObj["Expiration"] = check.Expiration
	}

	if check.HasDestTag {
		jsonObj["DestinationTag"] = check.DestinationTag
	}

	if check.HasSourceTag {
		jsonObj["SourceTag"] = check.SourceTag
	}

	if check.HasInvoiceID {
		jsonObj["InvoiceID"] = fmt.Sprintf("%X", check.InvoiceID[:])
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Check: %w", err)
	}

	return hex.DecodeString(hexStr)
}

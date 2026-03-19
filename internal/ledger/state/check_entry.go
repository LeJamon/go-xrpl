package state

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
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
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return check, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return check, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 3: // SourceTag
				check.SourceTag = value
				check.HasSourceTag = true
			case 4: // Sequence
				check.Sequence = value
			case 5: // PreviousTxnLgrSeq
				check.PreviousTxnLgrSeq = value
			case 10: // Expiration
				check.Expiration = value
			case 14: // DestinationTag
				check.DestinationTag = value
				check.HasDestTag = true
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return check, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 4: // OwnerNode
				check.OwnerNode = value
			case 9: // DestinationNode
				check.DestinationNode = value
				check.HasDestNode = true
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return check, nil
			}
			switch fieldCode {
			case 5: // PreviousTxnID
				copy(check.PreviousTxnID[:], data[offset:offset+32])
			case 17: // InvoiceID
				copy(check.InvoiceID[:], data[offset:offset+32])
				check.HasInvoiceID = true
			}
			offset += 32

		case FieldTypeAmount:
			if offset+8 > len(data) {
				return check, nil
			}
			if data[offset]&0x80 == 0 {
				// XRP amount
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				if fieldCode == 9 { // SendMax
					check.SendMax = rawAmount & 0x3FFFFFFFFFFFFFFF
					check.IsNativeSendMax = true
					check.SendMaxAmount = NewXRPAmountFromInt(int64(check.SendMax))
				}
				offset += 8
			} else {
				// IOU amount - 48 bytes total
				if offset+48 > len(data) {
					return check, nil
				}
				if fieldCode == 9 { // SendMax
					iouAmount, err := ParseIOUAmountBinary(data[offset : offset+48])
					if err == nil {
						check.SendMaxAmount = iouAmount
						check.IsNativeSendMax = false
					}
				}
				offset += 48
			}

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return check, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case 1: // Account
					copy(check.Account[:], data[offset:offset+20])
				case 3: // Destination
					copy(check.DestinationID[:], data[offset:offset+20])
				}
				offset += 20
			}

		case FieldTypeBlob:
			if offset >= len(data) {
				return check, nil
			}
			length := int(data[offset])
			offset++
			if offset+length > len(data) {
				return check, nil
			}
			offset += length

		default:
			return check, nil
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

	if check.HasDestNode {
		jsonObj["DestinationNode"] = fmt.Sprintf("%x", check.DestinationNode)
	}

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

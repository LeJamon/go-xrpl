package state

import (
	"encoding/binary"
)

// CheckData represents a Check ledger entry
type CheckData struct {
	Account        [20]byte
	DestinationID  [20]byte
	SendMax        uint64 // XRP drops (when IsNativeSendMax is true)
	SendMaxAmount  Amount // Full Amount representation (for both XRP and IOU)
	IsNativeSendMax bool
	Sequence       uint32
	Expiration     uint32
	InvoiceID      [32]byte
	DestinationTag uint32
	HasDestTag     bool
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
			case 4: // Sequence
				check.Sequence = value
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
			offset += 8

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return check, nil
			}
			if fieldCode == 17 { // InvoiceID
				copy(check.InvoiceID[:], data[offset:offset+32])
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

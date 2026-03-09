package state

import (
	"encoding/binary"
	"encoding/hex"
)

// EscrowData represents an Escrow ledger entry
type EscrowData struct {
	Account         [20]byte
	DestinationID   [20]byte
	Amount          uint64
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
	Flags           uint32
}

// ParseEscrow parses an Escrow ledger entry from binary data
func ParseEscrow(data []byte) (*EscrowData, error) {
	escrow := &EscrowData{}
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
				return escrow, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return escrow, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 2: // Flags
				escrow.Flags = value
			case 3: // SourceTag
				escrow.SourceTag = value
				escrow.HasSourceTag = true
			case 14: // DestinationTag
				escrow.DestinationTag = value
				escrow.HasDestTag = true
			case 36: // CancelAfter (nth=36 per definitions.json)
				escrow.CancelAfter = value
			case 37: // FinishAfter (nth=37 per definitions.json)
				escrow.FinishAfter = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return escrow, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 4: // OwnerNode (nth=4 per definitions.json)
				escrow.OwnerNode = value
			case 9: // DestinationNode (nth=9 per definitions.json)
				escrow.DestinationNode = value
				escrow.HasDestNode = true
			}

		case FieldTypeAmount:
			if offset+8 > len(data) {
				return escrow, nil
			}
			rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
			escrow.Amount = rawAmount & 0x3FFFFFFFFFFFFFFF
			offset += 8

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return escrow, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case 1: // Account
					copy(escrow.Account[:], data[offset:offset+20])
				case 3: // Destination
					copy(escrow.DestinationID[:], data[offset:offset+20])
				}
				offset += 20
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return escrow, nil
			}
			offset += 32

		case FieldTypeBlob:
			if offset >= len(data) {
				return escrow, nil
			}
			length := int(data[offset])
			offset++
			if offset+length > len(data) {
				return escrow, nil
			}
			if fieldCode == 17 { // Condition (nth=17 per definitions.json)
				escrow.Condition = hex.EncodeToString(data[offset : offset+length])
			}
			offset += length

		default:
			return escrow, nil
		}
	}

	return escrow, nil
}

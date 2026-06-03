package state

import (
	"encoding/binary"
	"encoding/hex"
)

// EscrowData represents an Escrow ledger entry
type EscrowData struct {
	Sequence        uint32 // sfSequence, optional (fixTokenEscrowV1)
	HasSequence     bool
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
			case 4: // Sequence (UInt32 nth=4; distinct from UInt64 OwnerNode nth=4)
				escrow.Sequence = value
				escrow.HasSequence = true
			case 2: // Flags
				escrow.Flags = value
			case 3: // SourceTag
				escrow.SourceTag = value
				escrow.HasSourceTag = true
			case 11: // TransferRate
				escrow.TransferRate = value
				escrow.HasTransferRate = true
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
			case 27: // IssuerNode
				escrow.IssuerNode = value
				escrow.HasIssuerNode = true
			}

		case FieldTypeAmount:
			if offset >= len(data) {
				return escrow, nil
			}
			firstByte := data[offset]
			// Detection order (matches binary codec):
			// 1. bit 0x80 set -> IOU (48 bytes)
			// 2. bit 0x20 set -> MPT (33 bytes)
			// 3. otherwise   -> XRP (8 bytes)
			if (firstByte & 0x80) != 0 {
				// IOU amount: 48 bytes (8 value + 20 currency + 20 issuer)
				if offset+48 > len(data) {
					return escrow, nil
				}
				if fieldCode == 1 { // sfAmount
					amt, err := ParseIOUAmountBinary(data[offset : offset+48])
					if err == nil {
						escrow.IOUAmount = &amt
						// IsXRP stays false (default)
					}
				}
				offset += 48
			} else if (firstByte & 0x20) != 0 {
				// MPT amount: 33 bytes (1 header + 8 value + 24 issuance ID)
				if offset+33 > len(data) {
					return escrow, nil
				}
				if fieldCode == 1 { // sfAmount
					mptAmt, err := ParseMPTAmountBinary(data[offset : offset+33])
					if err == nil {
						escrow.IOUAmount = &mptAmt
						if raw, ok := mptAmt.MPTRaw(); ok {
							escrow.MPTAmount = &raw
						}
						escrow.MPTIssuanceID = mptAmt.MPTIssuanceID()
						// IsXRP stays false (default)
					}
				}
				offset += 33
			} else {
				// XRP amount: 8 bytes
				if offset+8 > len(data) {
					return escrow, nil
				}
				rawAmount := binary.BigEndian.Uint64(data[offset : offset+8])
				if fieldCode == 1 { // sfAmount
					escrow.Amount = rawAmount & 0x3FFFFFFFFFFFFFFF
					escrow.IsXRP = true
				}
				offset += 8
			}

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

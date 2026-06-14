package state

import (
	"encoding/hex"
)

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
	escrow := &EscrowData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 2: // Flags
				escrow.Flags = f.UInt32()
			case 3: // SourceTag
				escrow.SourceTag = f.UInt32()
				escrow.HasSourceTag = true
			case 11: // TransferRate
				escrow.TransferRate = f.UInt32()
				escrow.HasTransferRate = true
			case 14: // DestinationTag
				escrow.DestinationTag = f.UInt32()
				escrow.HasDestTag = true
			case 36: // CancelAfter
				escrow.CancelAfter = f.UInt32()
			case 37: // FinishAfter
				escrow.FinishAfter = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode
				escrow.OwnerNode = f.UInt64()
			case 9: // DestinationNode
				escrow.DestinationNode = f.UInt64()
				escrow.HasDestNode = true
			case 27: // IssuerNode
				escrow.IssuerNode = f.UInt64()
				escrow.HasIssuerNode = true
			}

		case stAmount:
			if f.FieldCode != 1 { // sfAmount only
				return nil
			}
			// XRP (8), MPT (33), IOU (48) are distinguished by WalkFields' own
			// width discrimination, so len(Value) is the reliable selector.
			switch len(f.Value) {
			case 48: // IOU
				if amt, err := ParseIOUAmountBinary(f.Value); err == nil {
					escrow.IOUAmount = &amt
				}
			case 33: // MPT
				if mptAmt, err := ParseMPTAmountBinary(f.Value); err == nil {
					escrow.IOUAmount = &mptAmt
					if raw, ok := mptAmt.MPTRaw(); ok {
						escrow.MPTAmount = &raw
					}
					escrow.MPTIssuanceID = mptAmt.MPTIssuanceID()
				}
			case 8: // XRP
				escrow.Amount = xrpDrops(f.Value)
				escrow.IsXRP = true
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok {
				switch f.FieldCode {
				case 1: // Account
					escrow.Account = id
				case 3: // Destination
					escrow.DestinationID = id
				}
			}

		case stBlob:
			if f.FieldCode == 17 { // Condition
				escrow.Condition = hex.EncodeToString(f.VLBytes())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return escrow, nil
}

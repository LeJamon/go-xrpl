package state

import (
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
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

	jsonObj := map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Amount":          amountVal,
		"OwnerNode":       fmt.Sprintf("%x", ownerNode),
		"Flags":           uint32(0),
	}

	if hasDestNode {
		jsonObj["DestinationNode"] = fmt.Sprintf("%x", destNode)
	}
	if hasIssuerNode {
		jsonObj["IssuerNode"] = fmt.Sprintf("%x", issuerNode)
	}
	if finishAfter != nil {
		jsonObj["FinishAfter"] = *finishAfter
	}
	if cancelAfter != nil {
		jsonObj["CancelAfter"] = *cancelAfter
	}
	if condition != "" {
		jsonObj["Condition"] = condition
	}
	if sourceTag != nil {
		jsonObj["SourceTag"] = *sourceTag
	}
	if destinationTag != nil {
		jsonObj["DestinationTag"] = *destinationTag
	}
	if transferRate > 0 && transferRate != 1_000_000_000 {
		jsonObj["TransferRate"] = transferRate
	}
	if sequence != nil {
		jsonObj["Sequence"] = *sequence
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Escrow: %w", err)
	}

	return hex.DecodeString(hexStr)
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
				amt, err := ParseIOUAmountBinary(f.Value)
				if err != nil {
					return fmt.Errorf("Escrow IOU amount parse failed: %w", err)
				}
				escrow.IOUAmount = &amt
			case 33: // MPT
				mptAmt, err := ParseMPTAmountBinary(f.Value)
				if err != nil {
					return fmt.Errorf("Escrow MPT amount parse failed: %w", err)
				}
				escrow.IOUAmount = &mptAmt
				if raw, ok := mptAmt.MPTRaw(); ok {
					escrow.MPTAmount = &raw
				}
				escrow.MPTIssuanceID = mptAmt.MPTIssuanceID()
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

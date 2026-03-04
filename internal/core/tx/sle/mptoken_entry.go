package sle

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/internal/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
)

// Field type code for UInt8 (not defined in account_root.go)
const (
	FieldTypeUInt8   = 16
	FieldTypeHash192 = 21
)

// MPTokenIssuanceData holds parsed fields of an MPTokenIssuance ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN_ISSUANCE
type MPTokenIssuanceData struct {
	Issuer            [20]byte
	Sequence          uint32
	OwnerNode         uint64
	OutstandingAmount uint64
	TransferFee       uint16
	AssetScale        uint8
	MaximumAmount     *uint64
	LockedAmount      *uint64
	MPTokenMetadata   string // hex-encoded
	Flags             uint32
}

// MPTokenData holds parsed fields of an MPToken ledger entry.
// Reference: rippled LedgerFormats.h ltMPTOKEN
type MPTokenData struct {
	Account           [20]byte
	MPTokenIssuanceID [24]byte // Hash192 (24 bytes)
	OwnerNode         uint64
	MPTAmount         uint64
	LockedAmount      *uint64
	Flags             uint32
}

// ParseMPTokenIssuance parses an MPTokenIssuance ledger entry from binary data.
// Uses the same TLV parsing pattern as ParseEscrow.
func ParseMPTokenIssuance(data []byte) (*MPTokenIssuanceData, error) {
	issuance := &MPTokenIssuanceData{}
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
		case FieldTypeUInt8:
			if offset+1 > len(data) {
				return issuance, nil
			}
			value := data[offset]
			offset++
			switch fieldCode {
			case 5: // AssetScale (nth=5)
				issuance.AssetScale = value
			}

		case FieldTypeUInt16:
			if offset+2 > len(data) {
				return issuance, nil
			}
			value := binary.BigEndian.Uint16(data[offset : offset+2])
			offset += 2
			switch fieldCode {
			case 1: // LedgerEntryType - skip
			case 4: // TransferFee (nth=4)
				issuance.TransferFee = value
			}

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return issuance, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 2: // Flags
				issuance.Flags = value
			case 4: // Sequence
				issuance.Sequence = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return issuance, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 4: // OwnerNode (nth=4)
				issuance.OwnerNode = value
			case 24: // MaximumAmount (nth=24)
				v := value
				issuance.MaximumAmount = &v
			case 25: // OutstandingAmount (nth=25)
				issuance.OutstandingAmount = value
			case 29: // LockedAmount (nth=29)
				v := value
				issuance.LockedAmount = &v
			}

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return issuance, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case 4: // Issuer (nth=4)
					copy(issuance.Issuer[:], data[offset:offset+20])
				}
				offset += 20
			}

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return issuance, nil
			}
			offset += 32

		case FieldTypeBlob:
			if offset >= len(data) {
				return issuance, nil
			}
			length := int(data[offset])
			offset++
			if length >= 193 {
				// VL encoding: 2-byte length for values >= 193
				if offset >= len(data) {
					return issuance, nil
				}
				length = 193 + ((length-193)*256 + int(data[offset]))
				offset++
			}
			if offset+length > len(data) {
				return issuance, nil
			}
			switch fieldCode {
			case 30: // MPTokenMetadata (nth=30)
				issuance.MPTokenMetadata = hex.EncodeToString(data[offset : offset+length])
			}
			offset += length

		default:
			return issuance, nil
		}
	}

	return issuance, nil
}

// SerializeMPTokenIssuance serializes an MPTokenIssuance to binary format.
func SerializeMPTokenIssuance(issuance *MPTokenIssuanceData) ([]byte, error) {
	issuerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(issuance.Issuer[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode issuer address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType":  "MPTokenIssuance",
		"Flags":            issuance.Flags,
		"Issuer":           issuerAddress,
		"Sequence":         issuance.Sequence,
		"OwnerNode":        fmt.Sprintf("%X", issuance.OwnerNode),
		"OutstandingAmount": fmt.Sprintf("%X", issuance.OutstandingAmount),
	}

	if issuance.TransferFee > 0 {
		jsonObj["TransferFee"] = issuance.TransferFee
	}

	if issuance.AssetScale > 0 {
		jsonObj["AssetScale"] = issuance.AssetScale
	}

	if issuance.MaximumAmount != nil {
		jsonObj["MaximumAmount"] = fmt.Sprintf("%X", *issuance.MaximumAmount)
	}

	if issuance.LockedAmount != nil && *issuance.LockedAmount > 0 {
		jsonObj["LockedAmount"] = fmt.Sprintf("%X", *issuance.LockedAmount)
	}

	if issuance.MPTokenMetadata != "" {
		jsonObj["MPTokenMetadata"] = strings.ToUpper(issuance.MPTokenMetadata)
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode MPTokenIssuance: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ParseMPToken parses an MPToken ledger entry from binary data.
func ParseMPToken(data []byte) (*MPTokenData, error) {
	token := &MPTokenData{}
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
				return token, nil
			}
			offset += 2

		case FieldTypeUInt32:
			if offset+4 > len(data) {
				return token, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 2: // Flags
				token.Flags = value
			}

		case FieldTypeUInt64:
			if offset+8 > len(data) {
				return token, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case 4: // OwnerNode (nth=4)
				token.OwnerNode = value
			case 26: // MPTAmount (nth=26)
				token.MPTAmount = value
			case 29: // LockedAmount (nth=29)
				v := value
				token.LockedAmount = &v
			}

		case FieldTypeAccountID:
			if offset+21 > len(data) {
				return token, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case 1: // Account (nth=1)
					copy(token.Account[:], data[offset:offset+20])
				}
				offset += 20
			}

		case FieldTypeHash192:
			if offset+24 > len(data) {
				return token, nil
			}
			switch fieldCode {
			case 1: // MPTokenIssuanceID (nth=1)
				copy(token.MPTokenIssuanceID[:], data[offset:offset+24])
			}
			offset += 24

		case FieldTypeHash256:
			if offset+32 > len(data) {
				return token, nil
			}
			offset += 32

		case FieldTypeBlob:
			if offset >= len(data) {
				return token, nil
			}
			length := int(data[offset])
			offset++
			if length >= 193 {
				if offset >= len(data) {
					return token, nil
				}
				length = 193 + ((length-193)*256 + int(data[offset]))
				offset++
			}
			if offset+length > len(data) {
				return token, nil
			}
			offset += length

		default:
			return token, nil
		}
	}

	return token, nil
}

// SerializeMPToken serializes an MPToken to binary format.
func SerializeMPToken(token *MPTokenData) ([]byte, error) {
	accountAddress, err := addresscodec.EncodeAccountIDToClassicAddress(token.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType":  "MPToken",
		"Flags":            token.Flags,
		"Account":          accountAddress,
		"MPTokenIssuanceID": strings.ToUpper(hex.EncodeToString(token.MPTokenIssuanceID[:])),
		"OwnerNode":        fmt.Sprintf("%X", token.OwnerNode),
		"MPTAmount":        fmt.Sprintf("%X", token.MPTAmount),
	}

	if token.LockedAmount != nil && *token.LockedAmount > 0 {
		jsonObj["LockedAmount"] = fmt.Sprintf("%X", *token.LockedAmount)
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode MPToken: %w", err)
	}

	return hex.DecodeString(hexStr)
}

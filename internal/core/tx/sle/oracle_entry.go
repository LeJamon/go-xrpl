package sle

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/internal/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/internal/codec/binary-codec"
)

// OracleData holds parsed fields of an Oracle ledger entry.
// Reference: rippled LedgerFormats.h ltORACLE
type OracleData struct {
	Owner           [20]byte
	Provider        string // hex-encoded
	AssetClass      string // hex-encoded
	LastUpdateTime  uint32
	OwnerNode       uint64
	PriceDataSeries []OraclePriceData
	URI             string // hex-encoded, optional
	Flags           uint32
}

// OraclePriceData holds parsed fields of a single price data entry within an Oracle.
type OraclePriceData struct {
	BaseAsset  string // 3-letter currency code or hex
	QuoteAsset string // 3-letter currency code or hex
	AssetPrice uint64
	Scale      uint8
	HasPrice   bool
	HasScale   bool
}

// Field codes for Oracle-specific fields
const (
	// STObject type code
	fieldTypeSTObject = 14
	// STArray type code
	fieldTypeSTArray = 15
	// Currency type code
	fieldTypeCurrency = 26

	// Field nth values for Oracle fields
	fieldLastUpdateTime = 15 // UInt32, nth=15
	fieldOwnerNode      = 4  // UInt64, nth=4
	fieldAssetPrice     = 23 // UInt64, nth=23
	fieldScale          = 4  // UInt8, nth=4
	fieldOwner          = 2  // AccountID, nth=2
	fieldProvider       = 29 // Blob, nth=29
	fieldAssetClass     = 28 // Blob, nth=28
	fieldURI            = 5  // Blob, nth=5
	fieldBaseAsset      = 1  // Currency, nth=1
	fieldQuoteAsset     = 2  // Currency, nth=2
	fieldPriceData      = 32 // STObject, nth=32
	fieldPriceDataSer   = 24 // STArray, nth=24
)

// ParseOracle parses an Oracle ledger entry from binary data.
// Uses the same TLV parsing pattern as ParseEscrow.
func ParseOracle(data []byte) (*OracleData, error) {
	oracle := &OracleData{}
	offset := 0

	for offset < len(data) {
		if offset+1 > len(data) {
			break
		}

		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		// Handle extended type code
		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		// Handle extended field code
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		switch typeCode {
		case FieldTypeUInt16: // 1
			if offset+2 > len(data) {
				return oracle, nil
			}
			// LedgerEntryType — skip
			offset += 2

		case FieldTypeUInt32: // 2
			if offset+4 > len(data) {
				return oracle, nil
			}
			value := binary.BigEndian.Uint32(data[offset : offset+4])
			offset += 4
			switch fieldCode {
			case 2: // Flags
				oracle.Flags = value
			case fieldLastUpdateTime: // 15
				oracle.LastUpdateTime = value
			}

		case FieldTypeUInt64: // 3
			if offset+8 > len(data) {
				return oracle, nil
			}
			value := binary.BigEndian.Uint64(data[offset : offset+8])
			offset += 8
			switch fieldCode {
			case fieldOwnerNode: // 4 — OwnerNode
				oracle.OwnerNode = value
			}

		case FieldTypeAccountID: // 8
			if offset+21 > len(data) {
				return oracle, nil
			}
			length := data[offset]
			offset++
			if length == 20 {
				switch fieldCode {
				case fieldOwner: // 2 — Owner
					copy(oracle.Owner[:], data[offset:offset+20])
				}
			}
			offset += int(length)

		case FieldTypeBlob: // 7
			if offset >= len(data) {
				return oracle, nil
			}
			length, bytesRead := parseVLLength(data[offset:])
			offset += bytesRead
			if offset+length > len(data) {
				return oracle, nil
			}
			blobHex := hex.EncodeToString(data[offset : offset+length])
			switch fieldCode {
			case fieldProvider: // 29
				oracle.Provider = blobHex
			case fieldAssetClass: // 28
				oracle.AssetClass = blobHex
			case fieldURI: // 5
				oracle.URI = blobHex
			}
			offset += length

		case fieldTypeSTArray: // 15 — PriceDataSeries
			if fieldCode == fieldPriceDataSer { // 24
				var err error
				oracle.PriceDataSeries, offset, err = parseOraclePriceDataSeries(data, offset)
				if err != nil {
					return oracle, err
				}
			}

		case FieldTypeUInt8: // 16
			if offset >= len(data) {
				return oracle, nil
			}
			offset++ // skip UInt8 fields at top level

		case FieldTypeHash256: // 5
			if offset+32 > len(data) {
				return oracle, nil
			}
			offset += 32

		default:
			// Unknown type — stop parsing
			return oracle, nil
		}
	}

	return oracle, nil
}

// parseOraclePriceDataSeries parses the PriceDataSeries STArray from binary data.
// STArray format: repeated [STObject header][fields...][object-end 0xE1] then [array-end 0xF1]
func parseOraclePriceDataSeries(data []byte, offset int) ([]OraclePriceData, int, error) {
	var series []OraclePriceData

	for offset < len(data) {
		// Check for array end marker (0xF1)
		if data[offset] == 0xF1 {
			offset++
			break
		}

		// Read STObject header for PriceData
		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		// Handle extended type code
		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		// Handle extended field code
		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		if typeCode != fieldTypeSTObject {
			// Not an STObject — unexpected
			break
		}

		// Parse the inner PriceData object fields
		pd := OraclePriceData{}
		for offset < len(data) {
			// Check for object end marker (0xE1)
			if data[offset] == 0xE1 {
				offset++
				break
			}

			innerHeader := data[offset]
			offset++

			innerType := (innerHeader >> 4) & 0x0F
			innerField := innerHeader & 0x0F

			// Handle extended type code
			if innerType == 0 {
				if offset >= len(data) {
					break
				}
				innerType = data[offset]
				offset++
			}

			// Handle extended field code
			if innerField == 0 {
				if offset >= len(data) {
					break
				}
				innerField = data[offset]
				offset++
			}

			switch innerType {
			case FieldTypeUInt64: // 3 — AssetPrice
				if offset+8 > len(data) {
					return series, offset, nil
				}
				value := binary.BigEndian.Uint64(data[offset : offset+8])
				offset += 8
				if innerField == fieldAssetPrice { // 23
					pd.AssetPrice = value
					pd.HasPrice = true
				}

			case FieldTypeUInt8: // 16 — Scale
				if offset >= len(data) {
					return series, offset, nil
				}
				value := data[offset]
				offset++
				if innerField == fieldScale { // 4
					pd.Scale = value
					pd.HasScale = true
				}

			case fieldTypeCurrency: // 26 — BaseAsset/QuoteAsset (20 bytes each)
				if offset+20 > len(data) {
					return series, offset, nil
				}
				currencyBytes := data[offset : offset+20]
				currStr := parseCurrencyBytes(currencyBytes)
				offset += 20
				switch innerField {
				case fieldBaseAsset: // 1
					pd.BaseAsset = currStr
				case fieldQuoteAsset: // 2
					pd.QuoteAsset = currStr
				}

			default:
				// Unknown field type in PriceData — skip safely
				return series, offset, nil
			}
		}

		series = append(series, pd)
	}

	return series, offset, nil
}

// parseCurrencyBytes converts 20 binary currency bytes to a string.
// XRP = all zeros. Standard 3-letter ISO codes are at bytes 12-14.
func parseCurrencyBytes(b []byte) string {
	if len(b) != 20 {
		return hex.EncodeToString(b)
	}

	// Check if all zeros (XRP)
	allZero := true
	for _, v := range b {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "XRP"
	}

	// Check if it's a standard 3-letter code (bytes 12-14 non-zero, rest zero)
	isStandard := true
	for i := 0; i < 12; i++ {
		if b[i] != 0 {
			isStandard = false
			break
		}
	}
	if isStandard {
		for i := 15; i < 20; i++ {
			if b[i] != 0 {
				isStandard = false
				break
			}
		}
	}
	if isStandard && b[12] != 0 && b[13] != 0 && b[14] != 0 {
		return string(b[12:15])
	}

	return hex.EncodeToString(b)
}

// parseVLLength parses a variable-length field length prefix.
// Returns the length and number of bytes consumed for the prefix.
func parseVLLength(data []byte) (int, int) {
	if len(data) == 0 {
		return 0, 0
	}
	b1 := int(data[0])
	if b1 <= 192 {
		return b1, 1
	}
	if b1 <= 240 {
		if len(data) < 2 {
			return 0, 1
		}
		b2 := int(data[1])
		return 193 + ((b1 - 193) * 256) + b2, 2
	}
	if len(data) < 3 {
		return 0, 1
	}
	b2 := int(data[1])
	b3 := int(data[2])
	return 12481 + ((b1 - 241) * 65536) + (b2 * 256) + b3, 3
}

// SerializeOracle serializes an Oracle ledger entry to binary format.
// Pattern: Go struct → JSON map → binarycodec.Encode() → hex → bytes
func SerializeOracle(o *OracleData) ([]byte, error) {
	ownerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(o.Owner[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Oracle",
		"Owner":           ownerAddr,
		"Provider":        o.Provider,
		"AssetClass":      o.AssetClass,
		"LastUpdateTime":  o.LastUpdateTime,
		"OwnerNode":       fmt.Sprintf("%X", o.OwnerNode),
		"Flags":           uint32(0),
	}

	if o.URI != "" {
		jsonObj["URI"] = o.URI
	}

	// Build PriceDataSeries as []map[string]any
	var series []map[string]any
	for _, pd := range o.PriceDataSeries {
		entry := map[string]any{
			"BaseAsset":  pd.BaseAsset,
			"QuoteAsset": pd.QuoteAsset,
		}
		if pd.HasPrice {
			entry["AssetPrice"] = fmt.Sprintf("%X", pd.AssetPrice)
		}
		if pd.HasScale {
			entry["Scale"] = pd.Scale
		}
		series = append(series, map[string]any{"PriceData": entry})
	}
	jsonObj["PriceDataSeries"] = series

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Oracle: %w", err)
	}

	return hex.DecodeString(hexStr)
}

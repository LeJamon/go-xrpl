package tx

import (
	"encoding/hex"
	"fmt"
	"strconv"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// Field metadata flags matching rippled's SField metadata flags.
// Reference: rippled SField.h lines 145-155
const (
	// sMD_Never - never include in metadata
	sMD_Never uint8 = 0x00
	// sMD_ChangeOrig - include original value when field changes (goes to PreviousFields)
	sMD_ChangeOrig uint8 = 0x01
	// sMD_ChangeNew - include new value when field changes (goes to FinalFields)
	sMD_ChangeNew uint8 = 0x02
	// sMD_DeleteFinal - include final value when node is deleted
	sMD_DeleteFinal uint8 = 0x04
	// sMD_Create - include value when node is created (goes to NewFields)
	sMD_Create uint8 = 0x08
	// sMD_Always - always include if node is affected
	sMD_Always uint8 = 0x10
	// sMD_BaseTen - serialize UInt64 values as decimal strings in metadata JSON.
	// Reference: rippled STInteger.cpp lines 246-251
	sMD_BaseTen uint8 = 0x20
	// sMD_Default - default flags for most fields
	sMD_Default uint8 = sMD_ChangeOrig | sMD_ChangeNew | sMD_DeleteFinal | sMD_Create
)

// fieldMetadata maps field names to their metadata flags.
// Based on rippled's sfields.macro definitions.
// Only non-default flags are listed here; all other fields use sMD_Default.
// Verified complete against rippled sfields.macro — no additional non-default entries exist.
// Reference: rippled include/xrpl/protocol/detail/sfields.macro
var fieldMetadata = map[string]uint8{
	// sMD_Never (0x00) - never appear in metadata
	"LedgerEntryType": sMD_Never,
	"Indexes":         sMD_Never,

	// sMD_Always (0x10) - always appear if node is affected
	"RootIndex": sMD_Always,

	// sMD_DeleteFinal (0x04) - only in FinalFields when deleted
	// These fields are special: they are updated by threading logic,
	// not by standard metadata inclusion rules
	"PreviousTxnLgrSeq": sMD_DeleteFinal,
	"PreviousTxnID":     sMD_DeleteFinal,

	// sMD_BaseTen | sMD_Default - UInt64 amount fields displayed as decimal in metadata
	// Reference: rippled sfields.macro lines 142-147
	"MaximumAmount":     sMD_BaseTen | sMD_Default,
	"OutstandingAmount": sMD_BaseTen | sMD_Default,
	"MPTAmount":         sMD_BaseTen | sMD_Default,
	"LockedAmount":      sMD_BaseTen | sMD_Default,
}

// getFieldMetadata returns the metadata flags for a field
func getFieldMetadata(fieldName string) uint8 {
	if flags, ok := fieldMetadata[fieldName]; ok {
		return flags
	}
	return sMD_Default
}

// shouldIncludeInPreviousFields returns true if a changed field should be in PreviousFields
func shouldIncludeInPreviousFields(fieldName string) bool {
	flags := getFieldMetadata(fieldName)
	return (flags & sMD_ChangeOrig) != 0
}

// shouldIncludeInFinalFields returns true if a field should be in FinalFields
func shouldIncludeInFinalFields(fieldName string) bool {
	flags := getFieldMetadata(fieldName)
	return (flags & (sMD_Always | sMD_ChangeNew)) != 0
}

// shouldIncludeInCreate returns true if a field should be in NewFields for created nodes
func shouldIncludeInCreate(fieldName string) bool {
	flags := getFieldMetadata(fieldName)
	return (flags & (sMD_Create | sMD_Always)) != 0
}

// shouldIncludeInDeleteFinal returns true if a field should be in FinalFields for deleted nodes
func shouldIncludeInDeleteFinal(fieldName string) bool {
	flags := getFieldMetadata(fieldName)
	return (flags & (sMD_Always | sMD_DeleteFinal)) != 0
}

// isBaseTenField returns true if the field should be represented as decimal in metadata JSON.
func isBaseTenField(fieldName string) bool {
	return (getFieldMetadata(fieldName) & sMD_BaseTen) != 0
}

// convertHexToDecimal converts a hex string UInt64 value to its decimal representation.
// The binary codec decodes UInt64 fields as uppercase hex strings.
// Reference: rippled STInteger.cpp lines 246-251
func convertHexToDecimal(hexVal string) (string, error) {
	v, err := strconv.ParseUint(hexVal, 16, 64)
	if err != nil {
		return hexVal, fmt.Errorf("invalid hex UInt64 %q: %w", hexVal, err)
	}
	return strconv.FormatUint(v, 10), nil
}

// extractLedgerFields extracts fields from binary ledger entry data.
// Uses the binary codec to generically decode any ledger entry type.
// Fields with sMD_BaseTen are converted from hex to decimal representation.
func extractLedgerFields(data []byte, entryType string) (map[string]any, error) {
	// Convert binary data to hex string for the codec
	hexStr := hex.EncodeToString(data)

	// Use the binary codec to decode the ledger entry
	fields, err := binarycodec.Decode(hexStr)
	if err != nil {
		// If decoding fails, return empty map (graceful degradation)
		return make(map[string]any), nil
	}

	// Convert BaseTen fields from hex to decimal strings
	for name, value := range fields {
		if isBaseTenField(name) {
			if hexStr, ok := value.(string); ok {
				if decStr, err := convertHexToDecimal(hexStr); err == nil {
					fields[name] = decStr
				}
			}
		}
	}

	return fields, nil
}

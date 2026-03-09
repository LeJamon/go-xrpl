// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ledger

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
)

// LedgerReader is the interface needed for reading ledger state.
// This should match or be compatible with the existing ledger Reader interface.
type LedgerReader interface {
	Read(k keylet.Keylet) ([]byte, error)
	Exists(k keylet.Keylet) (bool, error)
}

// LoadAmendmentsFromLedger reads the Amendments ledger entry and returns
// a Rules instance with all enabled amendments.
func LoadAmendmentsFromLedger(reader LedgerReader) (*amendment.Rules, error) {
	// Get the Amendments keylet
	amendmentsKey := keylet.Amendments()

	// Check if the Amendments entry exists
	exists, err := reader.Exists(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check amendments existence: %w", err)
	}
	if !exists {
		// No amendments entry means no amendments enabled (genesis state)
		return amendment.EmptyRules(), nil
	}

	// Read the Amendments entry
	data, err := reader.Read(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read amendments entry: %w", err)
	}

	// Parse the Amendments entry to extract enabled amendment IDs
	enabledIDs, err := parseAmendmentsEntry(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse amendments entry: %w", err)
	}

	return amendment.NewRules(enabledIDs), nil
}

// parseAmendmentsEntry parses the binary Amendments ledger entry
// and returns the list of enabled amendment IDs.
// The Amendments field is a Vector256 (array of 256-bit hashes).
func parseAmendmentsEntry(data []byte) ([][32]byte, error) {
	enabledIDs := make([][32]byte, 0)

	if len(data) == 0 {
		return enabledIDs, nil
	}

	// Parse the serialized STObject data
	parser := serdes.NewBinaryParser(data, definitions.Get())

	for parser.HasMore() {
		// Read the field header
		field, err := parser.ReadField()
		if err != nil {
			return nil, fmt.Errorf("failed to read field: %w", err)
		}

		switch field.FieldName {
		case "Amendments":
			// Amendments field is a Vector256 (variable length encoded array of Hash256)
			// Read the length prefix
			length, err := parser.ReadVariableLength()
			if err != nil {
				return nil, fmt.Errorf("failed to read amendments length: %w", err)
			}

			// Each amendment ID is 32 bytes
			numAmendments := length / 32
			for i := 0; i < numAmendments; i++ {
				hashBytes, err := parser.ReadBytes(32)
				if err != nil {
					return nil, fmt.Errorf("failed to read amendment hash: %w", err)
				}
				var hash [32]byte
				copy(hash[:], hashBytes)
				enabledIDs = append(enabledIDs, hash)
			}

		case "LedgerEntryType":
			// Read 2 bytes for uint16
			_, err := parser.ReadBytes(2)
			if err != nil {
				return nil, fmt.Errorf("failed to read LedgerEntryType: %w", err)
			}

		case "Flags":
			// Read 4 bytes for uint32
			_, err := parser.ReadBytes(4)
			if err != nil {
				return nil, fmt.Errorf("failed to read Flags: %w", err)
			}

		case "Majorities":
			// Majorities is an STArray - skip it for now
			// STArrays end with 0xF1 (array end marker)
			if err := skipSTArray(parser); err != nil {
				return nil, fmt.Errorf("failed to skip Majorities array: %w", err)
			}

		case "index":
			// Read 32 bytes for Hash256
			_, err := parser.ReadBytes(32)
			if err != nil {
				return nil, fmt.Errorf("failed to read index: %w", err)
			}

		default:
			// Skip unknown fields based on their type
			if err := skipField(parser, field); err != nil {
				return nil, fmt.Errorf("failed to skip field %s: %w", field.FieldName, err)
			}
		}
	}

	return enabledIDs, nil
}

// skipSTArray skips an STArray field (ends with 0xF1 marker)
func skipSTArray(parser *serdes.BinaryParser) error {
	for parser.HasMore() {
		b, err := parser.Peek()
		if err != nil {
			return err
		}

		// Check for array end marker (0xF1)
		if b == 0xF1 {
			_, _ = parser.ReadByte() // consume the marker
			return nil
		}

		// Read and skip the field
		field, err := parser.ReadField()
		if err != nil {
			return err
		}

		// Check for object end marker within array (0xE1)
		if field.FieldName == "EndOfObject" || (field.FieldHeader.TypeCode == 14 && field.FieldHeader.FieldCode == 1) {
			continue
		}

		if err := skipField(parser, field); err != nil {
			return err
		}
	}
	return nil
}

// skipField skips a field based on its type
func skipField(parser *serdes.BinaryParser, field *definitions.FieldInstance) error {
	switch field.Type {
	case "UInt8":
		_, err := parser.ReadByte()
		return err
	case "UInt16":
		_, err := parser.ReadBytes(2)
		return err
	case "UInt32":
		_, err := parser.ReadBytes(4)
		return err
	case "UInt64":
		_, err := parser.ReadBytes(8)
		return err
	case "Hash128":
		_, err := parser.ReadBytes(16)
		return err
	case "Hash160", "AccountID":
		_, err := parser.ReadBytes(20)
		return err
	case "Hash192":
		_, err := parser.ReadBytes(24)
		return err
	case "Hash256":
		_, err := parser.ReadBytes(32)
		return err
	case "Amount":
		// Check first byte to determine if XRP or IOU
		b, err := parser.Peek()
		if err != nil {
			return err
		}
		if b&0x80 == 0 {
			// XRP amount: 8 bytes
			_, err = parser.ReadBytes(8)
		} else {
			// IOU amount: 8 bytes + 20 bytes currency + 20 bytes issuer = 48 bytes
			_, err = parser.ReadBytes(48)
		}
		return err
	case "Blob", "Vector256":
		// Variable length encoded
		length, err := parser.ReadVariableLength()
		if err != nil {
			return err
		}
		_, err = parser.ReadBytes(length)
		return err
	case "STObject":
		// Skip until end of object marker (0xE1)
		return skipSTObject(parser)
	case "STArray":
		return skipSTArray(parser)
	default:
		// For unknown types, try to read as variable length
		length, err := parser.ReadVariableLength()
		if err != nil {
			return err
		}
		_, err = parser.ReadBytes(length)
		return err
	}
}

// skipSTObject skips an STObject field (ends with 0xE1 marker)
func skipSTObject(parser *serdes.BinaryParser) error {
	for parser.HasMore() {
		b, err := parser.Peek()
		if err != nil {
			return err
		}

		// Check for object end marker (0xE1)
		if b == 0xE1 {
			_, _ = parser.ReadByte() // consume the marker
			return nil
		}

		// Read and skip the field
		field, err := parser.ReadField()
		if err != nil {
			return err
		}

		if err := skipField(parser, field); err != nil {
			return err
		}
	}
	return nil
}

// LoadAmendmentsFromLedgerEntry is a convenience function that parses
// the raw Amendments ledger entry data directly.
func LoadAmendmentsFromLedgerEntry(data []byte) (*amendment.Rules, error) {
	enabledIDs, err := parseAmendmentsEntry(data)
	if err != nil {
		return nil, err
	}
	return amendment.NewRules(enabledIDs), nil
}

// LoadAmendmentsFromHex parses a hex-encoded Amendments ledger entry.
func LoadAmendmentsFromHex(hexData string) (*amendment.Rules, error) {
	data, err := hex.DecodeString(hexData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex: %w", err)
	}
	return LoadAmendmentsFromLedgerEntry(data)
}

// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ledger

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LoadAmendmentsFromLedger reads the Amendments ledger entry into a Rules set.
func LoadAmendmentsFromLedger(reader Reader) (*amendment.Rules, error) {
	amendmentsKey := keylet.Amendments()

	exists, err := reader.Exists(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to check amendments existence: %w", err)
	}
	if !exists {
		// No SLE: only the permanently-enabled retired amendments are active
		// (never stored, but their code runs unconditionally in rippled).
		return amendment.NewRules(amendment.PermanentlyEnabledIDs()), nil
	}

	data, err := reader.Read(amendmentsKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read amendments entry: %w", err)
	}

	enabledIDs, err := parseAmendmentsEntry(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse amendments entry: %w", err)
	}

	// Retired amendments are permanently enabled but never stored in the SLE.
	enabledIDs = append(enabledIDs, amendment.PermanentlyEnabledIDs()...)
	return amendment.NewRules(enabledIDs), nil
}

// parseAmendmentsEntry returns the enabled amendment IDs from the binary
// Amendments entry (the Amendments field is a Vector256 of 256-bit hashes).
func parseAmendmentsEntry(data []byte) ([][32]byte, error) {
	enabledIDs := make([][32]byte, 0)

	if len(data) == 0 {
		return enabledIDs, nil
	}

	// Parse the serialized STObject data
	parser := serdes.NewBinaryParser(data, definitions.Get())

	for parser.HasMore() {
		field, err := parser.ReadField()
		if err != nil {
			return nil, fmt.Errorf("failed to read field: %w", err)
		}

		switch field.FieldName {
		case "Amendments":
			length, err := parser.ReadVariableLength()
			if err != nil {
				return nil, fmt.Errorf("failed to read amendments length: %w", err)
			}

			numAmendments := length / 32
			for range numAmendments {
				hashBytes, err := parser.ReadBytes(32)
				if err != nil {
					return nil, fmt.Errorf("failed to read amendment hash: %w", err)
				}
				var hash [32]byte
				copy(hash[:], hashBytes)
				enabledIDs = append(enabledIDs, hash)
			}

		case "LedgerEntryType":
			_, err := parser.ReadBytes(2)
			if err != nil {
				return nil, fmt.Errorf("failed to read LedgerEntryType: %w", err)
			}

		case "Flags":
			_, err := parser.ReadBytes(4)
			if err != nil {
				return nil, fmt.Errorf("failed to read Flags: %w", err)
			}

		case "Majorities":
			if err := skipSTArray(parser); err != nil {
				return nil, fmt.Errorf("failed to skip Majorities array: %w", err)
			}

		case "index":
			_, err := parser.ReadBytes(32)
			if err != nil {
				return nil, fmt.Errorf("failed to read index: %w", err)
			}

		default:
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

		if b == 0xF1 {
			_, _ = parser.ReadByte()
			return nil
		}

		field, err := parser.ReadField()
		if err != nil {
			return err
		}

		if field.FieldName == "EndOfObject" || (field.FieldHeader.TypeCode == 14 && field.FieldHeader.FieldCode == 1) {
			continue
		}

		if err := skipField(parser, field); err != nil {
			return err
		}
	}
	return nil
}

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
		// High bit of the first byte selects XRP (8 bytes) vs IOU (amount+currency+issuer = 48).
		b, err := parser.Peek()
		if err != nil {
			return err
		}
		if b&0x80 == 0 {
			_, err = parser.ReadBytes(8)
		} else {
			_, err = parser.ReadBytes(48)
		}
		return err
	case "Blob", "Vector256":
		length, err := parser.ReadVariableLength()
		if err != nil {
			return err
		}
		_, err = parser.ReadBytes(length)
		return err
	case "STObject":
		return skipSTObject(parser)
	case "STArray":
		return skipSTArray(parser)
	default:
		// Fail closed: guessing VL on an unknown type could consume a data byte
		// as a length prefix and silently desync the parser.
		return fmt.Errorf("unsupported field type %q", field.Type)
	}
}

// skipSTObject skips an STObject field (ends with 0xE1 marker)
func skipSTObject(parser *serdes.BinaryParser) error {
	for parser.HasMore() {
		b, err := parser.Peek()
		if err != nil {
			return err
		}

		if b == 0xE1 {
			_, _ = parser.ReadByte()
			return nil
		}

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

// LoadAmendmentsFromLedgerEntry parses raw Amendments ledger entry data directly.
func LoadAmendmentsFromLedgerEntry(data []byte) (*amendment.Rules, error) {
	enabledIDs, err := parseAmendmentsEntry(data)
	if err != nil {
		return nil, err
	}
	// Retired amendments are permanently enabled but never stored (see LoadAmendmentsFromLedger).
	enabledIDs = append(enabledIDs, amendment.PermanentlyEnabledIDs()...)
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

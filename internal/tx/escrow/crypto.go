package escrow

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// Crypto-condition types
// Reference: https://tools.ietf.org/html/draft-thomas-crypto-conditions-02
const (
	// conditionTypePreimageSha256 is the type for PREIMAGE-SHA-256 conditions
	conditionTypePreimageSha256 = 0

	// maxPreimageLength is the maximum allowed length for a preimage
	// Reference: rippled PreimageSha256.h maxPreimageLength
	maxPreimageLength = 128

	// maxSerializedCondition is the maximum allowed size of a serialized condition
	maxSerializedCondition = 128

	// maxSerializedFulfillment is the maximum allowed size of a serialized fulfillment
	maxSerializedFulfillment = 256
)

// ValidateConditionFormat validates that a hex-encoded condition is well-formed
// and is a supported type (PREIMAGE-SHA-256). Used during preflight/Validate().
// Reference: rippled Escrow.cpp preflight() condition deserialization check
func ValidateConditionFormat(conditionHex string) error {
	if conditionHex == "" {
		return errors.New("empty condition")
	}
	condBytes, err := hex.DecodeString(conditionHex)
	if err != nil || len(condBytes) == 0 {
		return errors.New("invalid condition encoding")
	}
	_, condType, consumed, err := parseConditionFull(condBytes)
	if err != nil {
		return err
	}
	// Reject trailing data — the condition must be exactly the right length
	if consumed != len(condBytes) {
		return errors.New("condition has trailing data")
	}
	// Only PREIMAGE-SHA-256 is supported without CryptoConditionsSuite amendment
	if condType != conditionTypePreimageSha256 {
		return errors.New("unsupported condition type")
	}
	return nil
}

// validateCryptoCondition verifies that a fulfillment matches its condition
// Reference: rippled Escrow.cpp checkCondition()
//
// Condition format (PREIMAGE-SHA-256):
//   - 0xA0: Type prefix (constructed, context-specific tag 0)
//   - length: VarInt length of condition body
//   - 0x80, 0x20: Fingerprint tag (primitive, context-specific tag 0) + length (32)
//   - <32 bytes>: SHA-256 hash of the preimage (the fingerprint)
//   - 0x81, 0x01: Cost tag (primitive, context-specific tag 1) + length (1)
//   - <cost>: The cost value (preimage length)
//
// Fulfillment format (PREIMAGE-SHA-256):
//   - 0xA0: Type prefix (constructed, context-specific tag 0)
//   - length: VarInt length of fulfillment body
//   - 0x80: Preimage tag (primitive, context-specific tag 0)
//   - length: Preimage length
//   - <preimage>: The actual preimage bytes
func validateCryptoCondition(fulfillmentHex, conditionHex string) error {
	fulfillment, err := hex.DecodeString(fulfillmentHex)
	if err != nil {
		return errors.New("invalid fulfillment encoding")
	}

	condition, err := hex.DecodeString(conditionHex)
	if err != nil {
		return errors.New("invalid condition encoding")
	}

	return checkCondition(fulfillment, condition)
}

// checkCondition verifies that a fulfillment matches the condition
// Reference: rippled Escrow.cpp checkCondition(Slice f, Slice c)
func checkCondition(fulfillment, condition []byte) error {
	// Validate sizes
	if len(condition) > maxSerializedCondition {
		return errors.New("condition too large")
	}
	if len(fulfillment) > maxSerializedFulfillment {
		return errors.New("fulfillment too large")
	}

	// Parse condition to extract fingerprint
	fingerprint, condType, condConsumed, err := parseConditionFull(condition)
	if err != nil {
		return fmt.Errorf("failed to parse condition: %w", err)
	}

	// Reject trailing data in condition
	if condConsumed != len(condition) {
		return errors.New("condition has trailing data")
	}

	// Only PREIMAGE-SHA-256 is supported
	if condType != conditionTypePreimageSha256 {
		return errors.New("unsupported condition type")
	}

	// Parse fulfillment to extract preimage
	preimage, fulfType, fulfConsumed, err := parseFulfillment(fulfillment)
	if err != nil {
		return fmt.Errorf("failed to parse fulfillment: %w", err)
	}

	// Reject trailing data in fulfillment
	if fulfConsumed != len(fulfillment) {
		return errors.New("fulfillment has trailing data")
	}

	// Types must match
	if condType != fulfType {
		return errors.New("condition and fulfillment type mismatch")
	}

	// For PREIMAGE-SHA-256: fingerprint = SHA-256(preimage)
	// Compute SHA-256 of preimage and compare to fingerprint
	hash := sha256.Sum256(preimage)
	if len(fingerprint) != 32 {
		return errors.New("invalid fingerprint length")
	}

	for i := 0; i < 32; i++ {
		if hash[i] != fingerprint[i] {
			return errors.New("fulfillment does not match condition")
		}
	}

	return nil
}

// parseCondition parses a crypto-condition and extracts the fingerprint and type
// Reference: rippled Condition.h/cpp deserialize
func parseCondition(data []byte) (fingerprint []byte, condType uint8, err error) {
	fp, ct, _, e := parseConditionFull(data)
	return fp, ct, e
}

// parseConditionFull parses a crypto-condition and returns fingerprint, type, and bytes consumed
func parseConditionFull(data []byte) (fingerprint []byte, condType uint8, consumed int, err error) {
	if len(data) < 4 {
		return nil, 0, 0, errors.New("condition too short")
	}

	offset := 0

	// Check type tag (0xA0 + type for constructed, context-specific)
	tag := data[offset]
	offset++

	// Extract condition type from tag
	// 0xA0 = 1010 0000 = constructed (0x20) + context-specific (0x80) + tag 0
	// Type is encoded in the low 5 bits after the class bits
	if (tag & 0xE0) != 0xA0 {
		return nil, 0, 0, errors.New("invalid condition tag")
	}
	condType = tag & 0x1F

	// Parse length
	length, bytesRead, err := parseASN1Length(data[offset:])
	if err != nil {
		return nil, 0, 0, err
	}
	offset += bytesRead

	if offset+length > len(data) {
		return nil, 0, 0, errors.New("condition length exceeds data")
	}

	// Total consumed = tag + length bytes + body
	consumed = offset + length

	// Parse fingerprint (tag 0x80)
	if offset >= len(data) || data[offset] != 0x80 {
		return nil, 0, 0, errors.New("expected fingerprint tag")
	}
	offset++

	// Parse fingerprint length
	fpLength, bytesRead, err := parseASN1Length(data[offset:])
	if err != nil {
		return nil, 0, 0, err
	}
	offset += bytesRead

	if fpLength != 32 {
		return nil, 0, 0, errors.New("invalid fingerprint length for PREIMAGE-SHA-256")
	}

	if offset+fpLength > len(data) {
		return nil, 0, 0, errors.New("fingerprint exceeds condition data")
	}

	fingerprint = make([]byte, fpLength)
	copy(fingerprint, data[offset:offset+fpLength])

	return fingerprint, condType, consumed, nil
}

// parseFulfillment parses a crypto-fulfillment and extracts the preimage and type.
// Also returns total bytes consumed for trailing-data validation.
// Reference: rippled Fulfillment.h/cpp deserialize, PreimageSha256.h deserialize
func parseFulfillment(data []byte) (preimage []byte, fulfType uint8, consumed int, err error) {
	if len(data) < 4 {
		return nil, 0, 0, errors.New("fulfillment too short")
	}

	offset := 0

	// Check type tag (0xA0 + type for constructed, context-specific)
	tag := data[offset]
	offset++

	// Extract fulfillment type from tag
	if (tag & 0xE0) != 0xA0 {
		return nil, 0, 0, errors.New("invalid fulfillment tag")
	}
	fulfType = tag & 0x1F

	// Parse length
	length, bytesRead, err := parseASN1Length(data[offset:])
	if err != nil {
		return nil, 0, 0, err
	}
	offset += bytesRead

	// Total consumed = tag + length bytes + body
	consumed = offset + length

	// For PREIMAGE-SHA-256, next is the preimage (tag 0x80)
	if fulfType == conditionTypePreimageSha256 {
		if offset >= len(data) || data[offset] != 0x80 {
			return nil, 0, 0, errors.New("expected preimage tag")
		}
		offset++

		// Parse preimage length
		preimageLength, bytesRead, err := parseASN1Length(data[offset:])
		if err != nil {
			return nil, 0, 0, err
		}
		offset += bytesRead

		if preimageLength > maxPreimageLength {
			return nil, 0, 0, errors.New("preimage too long")
		}

		if offset+preimageLength > len(data) {
			return nil, 0, 0, errors.New("preimage exceeds fulfillment data")
		}

		preimage = make([]byte, preimageLength)
		copy(preimage, data[offset:offset+preimageLength])

		return preimage, fulfType, consumed, nil
	}

	return nil, 0, 0, errors.New("unsupported fulfillment type")
}

// parseASN1Length parses a DER-encoded length
// Returns the length value and the number of bytes consumed
func parseASN1Length(data []byte) (int, int, error) {
	if len(data) < 1 {
		return 0, 0, errors.New("no length byte")
	}

	first := data[0]
	if first < 0x80 {
		// Short form: length is directly in the first byte
		return int(first), 1, nil
	}

	// Long form: first byte indicates number of length bytes
	numBytes := int(first & 0x7F)
	if numBytes == 0 {
		return 0, 0, errors.New("indefinite length not supported")
	}
	if numBytes > 4 {
		return 0, 0, errors.New("length too large")
	}
	if len(data) < 1+numBytes {
		return 0, 0, errors.New("insufficient length bytes")
	}

	length := 0
	for i := 0; i < numBytes; i++ {
		length = (length << 8) | int(data[1+i])
	}

	return length, 1 + numBytes, nil
}

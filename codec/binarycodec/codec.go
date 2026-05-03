package binarycodec

import (
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types"
)

var (
	// ErrSigningClaimFieldNotFound is returned when the channel and amount fields are not both present.
	ErrSigningClaimFieldNotFound = errors.New("channel and amount fields are required")
	// ErrBatchFlagsFieldNotFound is returned when the flags field is missing.
	ErrBatchFlagsFieldNotFound = errors.New("missing flags field")
	// ErrBatchTxIDsFieldNotFound is returned when the txIDs field is missing.
	ErrBatchTxIDsFieldNotFound = errors.New("missing txIDs field")
	// ErrBatchTxIDsNotArray is returned when the txIDs field is not an array.
	ErrBatchTxIDsNotArray = errors.New("txIDs field must be an array")
	// ErrBatchTxIDNotString is returned when a txID is not a string.
	ErrBatchTxIDNotString = errors.New("each txID must be a string")
	// ErrBatchFlagsNotUInt32 is returned when the flags field is not a uint32.
	ErrBatchFlagsNotUInt32 = errors.New("flags field must be a uint32")
	// ErrBatchTxIDsLengthTooLong is returned when the txIDs field is too long.
	ErrBatchTxIDsLengthTooLong = errors.New("txIDs length exceeds maximum uint32 value")
)

const (
	txMultiSigPrefix          = "534D5400"
	paymentChannelClaimPrefix = "434C4D00"
	txSigPrefix               = "53545800"
	batchPrefix               = "42434800"
)

// Encode converts a JSON transaction object to a hex string in the canonical binary format.
// The binary format is defined in XRPL's core codebase.
func Encode(json map[string]any) (string, error) {
	st := types.NewSTObject(serdes.NewBinarySerializer(serdes.NewFieldIDCodec(definitions.Get())))

	for k := range json {
		fh := definitions.Get().Fields[k]
		if fh == nil {
			delete(json, k)
			continue
		}
	}

	b, err := st.FromJSON(json)
	if err != nil {
		return "", err
	}

	return strings.ToUpper(hex.EncodeToString(b)), nil
}

// EncodeForMultisigning encodes a transaction into binary format in preparation for providing one
// signature towards a multi-signed transaction.
// Only encodes fields that are intended to be signed.
func EncodeForMultisigning(json map[string]any, xrpAccountID string) (string, error) {
	st := &types.AccountID{}

	// SigningPubKey is required for multi-signing but should be set to empty string.

	json["SigningPubKey"] = ""

	suffix, err := st.FromJSON(xrpAccountID)
	if err != nil {
		return "", err
	}

	encoded, err := Encode(removeNonSigningFields(json))

	if err != nil {
		return "", err
	}

	return strings.ToUpper(txMultiSigPrefix + encoded + hex.EncodeToString(suffix)), nil
}

// EncodeForSigning encodes a transaction into binary format in preparation for signing.
func EncodeForSigning(json map[string]any) (string, error) {
	encoded, err := Encode(removeNonSigningFields(json))

	if err != nil {
		return "", err
	}

	return strings.ToUpper(txSigPrefix + encoded), nil
}

// EncodeForSigningClaim encodes a payment channel claim into binary format in preparation for signing.
// The message format is: HashPrefix('CLM\0') + channel_id (32 bytes) + amount (8 bytes big-endian uint64)
// Note: Unlike normal XRP amounts, payment channel claim amounts use the full uint64 range as raw drops,
// without the XRP amount validation limits.
func EncodeForSigningClaim(json map[string]any) (string, error) {
	if json["Channel"] == nil || json["Amount"] == nil {
		return "", ErrSigningClaimFieldNotFound
	}

	channel, err := types.NewHash256().FromJSON(json["Channel"])

	if err != nil {
		return "", err
	}

	// Parse amount as raw uint64 drops (not using Amount type to avoid XRP validation limits)
	var amountStr string
	switch v := json["Amount"].(type) {
	case string:
		amountStr = v
	default:
		return "", errors.New("amount must be a string")
	}

	drops, err := strconv.ParseUint(amountStr, 10, 64)
	if err != nil {
		return "", err
	}

	// Serialize as 8-byte big-endian uint64
	amount := make([]byte, 8)
	amount[0] = byte(drops >> 56)
	amount[1] = byte(drops >> 48)
	amount[2] = byte(drops >> 40)
	amount[3] = byte(drops >> 32)
	amount[4] = byte(drops >> 24)
	amount[5] = byte(drops >> 16)
	amount[6] = byte(drops >> 8)
	amount[7] = byte(drops)

	return strings.ToUpper(paymentChannelClaimPrefix + hex.EncodeToString(channel) + hex.EncodeToString(amount)), nil
}

// EncodeForSigningBatch encodes a batch transaction into binary format in preparation for signing.
func EncodeForSigningBatch(json map[string]any) (string, error) {
	if json["flags"] == nil {
		return "", ErrBatchFlagsFieldNotFound
	}
	if json["txIDs"] == nil {
		return "", ErrBatchTxIDsFieldNotFound
	}

	txIDsInterface, ok := json["txIDs"].([]string)
	if !ok {
		return "", ErrBatchTxIDsNotArray
	}

	_, ok = json["flags"].(uint32)
	if !ok {
		return "", ErrBatchFlagsNotUInt32
	}

	flagsType := &types.UInt32{}
	flagsBytes, err := flagsType.FromJSON(json["flags"])
	if err != nil {
		return "", err
	}

	txIDsLengthType := &types.UInt32{}
	txIDsLength := len(txIDsInterface)
	if txIDsLength > math.MaxUint32 {
		return "", ErrBatchTxIDsLengthTooLong
	}
	txIDsLengthBytes, err := txIDsLengthType.FromJSON(uint32(txIDsLength))
	if err != nil {
		return "", err
	}

	result := batchPrefix + hex.EncodeToString(flagsBytes) + hex.EncodeToString(txIDsLengthBytes)

	for _, txID := range txIDsInterface {
		hash256 := types.NewHash256()
		txIDBytes, err := hash256.FromJSON(txID)
		if err != nil {
			return "", err
		}
		result += hex.EncodeToString(txIDBytes)
	}

	return strings.ToUpper(result), nil
}

// removeNonSigningFields removes the fields from a JSON transaction object that should not be signed.
func removeNonSigningFields(json map[string]any) map[string]any {
	for k := range json {
		fi, _ := definitions.Get().GetFieldInstanceByFieldName(k)

		if fi != nil && !fi.IsSigningField {
			delete(json, k)
		}
	}

	return json
}

// Decode decodes a hex string in the canonical binary format into a JSON transaction object.
func Decode(hexEncoded string) (map[string]any, error) {
	b, err := hex.DecodeString(hexEncoded)
	if err != nil {
		return nil, err
	}
	p := serdes.NewBinaryParser(b, definitions.Get())
	st := types.NewSTObject(serdes.NewBinarySerializer(serdes.NewFieldIDCodec(definitions.Get())))
	m, err := st.ToJSON(p)
	if err != nil {
		return nil, err
	}

	return m.(map[string]any), nil
}

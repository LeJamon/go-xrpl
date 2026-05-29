package binarycodec

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"

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
	// ErrUnknownField is returned when Encode receives a JSON key with no
	// matching field definition. Silently dropping unknown keys masks typos
	// (e.g. "Acount" vs "Account") that produce different binary transactions.
	ErrUnknownField = errors.New("unknown field")
)

const (
	txMultiSigPrefix          = "534D5400"
	paymentChannelClaimPrefix = "434C4D00"
	txSigPrefix               = "53545800"
	batchPrefix               = "42434800"
)

// hexUpperTable mirrors encoding/hex.encodeStd but in uppercase so we can
// write hex directly into a []byte without an extra ToUpper pass over the
// concatenated result. The signing helpers below run on every outbound tx,
// so avoiding the redundant copy/uppercase saves measurable allocs.
const hexUpperTable = "0123456789ABCDEF"

func appendHexUpper(dst, src []byte) []byte {
	for _, b := range src {
		dst = append(dst, hexUpperTable[b>>4], hexUpperTable[b&0x0f])
	}
	return dst
}

func hexUpper(src []byte) string {
	buf := make([]byte, 0, len(src)*2)
	return string(appendHexUpper(buf, src))
}

// EncodeBytes encodes a JSON transaction object directly to canonical binary
// bytes. Prefer this over Encode on hot paths that immediately re-decode.
// EncodeBytes does not mutate the caller-supplied map. An unknown field name
// is treated as an error rather than silently dropped — see [ErrUnknownField].
func EncodeBytes(json map[string]any) ([]byte, error) {
	defs := definitions.Get()
	for k := range json {
		if _, ok := defs.Fields[k]; !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownField, k)
		}
	}

	st := types.NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
	return st.FromJSON(json)
}

// Encode converts a JSON transaction object to a hex string in the canonical
// binary format. The binary format is defined in XRPL's core codebase.
func Encode(json map[string]any) (string, error) {
	b, err := EncodeBytes(json)
	if err != nil {
		return "", err
	}
	return hexUpper(b), nil
}

// EncodeForMultisigning encodes a transaction into binary format in preparation for providing one
// signature towards a multi-signed transaction. Only signing fields are
// encoded. The caller's map is never mutated.
func EncodeForMultisigning(json map[string]any, xrpAccountID string) (string, error) {
	st := &types.AccountID{}

	suffix, err := st.FromJSON(xrpAccountID)
	if err != nil {
		return "", err
	}

	// Build the signing-field projection with SigningPubKey overridden to "".
	signing := removeNonSigningFields(json)
	signing["SigningPubKey"] = ""

	encoded, err := Encode(signing)
	if err != nil {
		return "", err
	}

	return txMultiSigPrefix + encoded + hexUpper(suffix), nil
}

// EncodeForSigning encodes a transaction into binary format in preparation for signing.
func EncodeForSigning(json map[string]any) (string, error) {
	encoded, err := Encode(removeNonSigningFields(json))

	if err != nil {
		return "", err
	}

	return txSigPrefix + encoded, nil
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

	return paymentChannelClaimPrefix + hexUpper(channel) + hexUpper(amount), nil
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

	totalSize := len(batchPrefix) + 2*len(flagsBytes) + 2*len(txIDsLengthBytes) + txIDsLength*2*types.HashLengthBytes
	buf := make([]byte, 0, totalSize)
	buf = append(buf, batchPrefix...)
	buf = appendHexUpper(buf, flagsBytes)
	buf = appendHexUpper(buf, txIDsLengthBytes)

	for _, txID := range txIDsInterface {
		hash256 := types.NewHash256()
		txIDBytes, err := hash256.FromJSON(txID)
		if err != nil {
			return "", err
		}
		buf = appendHexUpper(buf, txIDBytes)
	}

	return string(buf), nil
}

// removeNonSigningFields returns a copy of the JSON transaction object with
// all non-signing fields stripped. The caller's map is never mutated.
func removeNonSigningFields(json map[string]any) map[string]any {
	defs := definitions.Get()
	out := make(map[string]any, len(json))
	for k, v := range json {
		fi, _ := defs.GetFieldInstanceByFieldName(k)
		if fi != nil && !fi.IsSigningField {
			continue
		}
		out[k] = v
	}
	return out
}

// DecodeBytes decodes canonical binary bytes into a JSON transaction object.
// Prefer this over Decode on hot paths that already hold the raw bytes.
func DecodeBytes(b []byte) (map[string]any, error) {
	p := serdes.NewBinaryParser(b, definitions.Get())
	st := types.NewSTObject(serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec()))
	// ToJSONStrict consumes fields until the parser is exhausted, mirroring
	// rippled's `while (!sit.empty())` loop (STObject.cpp:243): any trailing
	// bytes are read as a further field and rejected if they are malformed, so
	// no separate trailing-byte check is needed.
	return st.ToJSONStrict(p)
}

// Decode decodes a hex string in the canonical binary format into a JSON
// transaction object.
func Decode(hexEncoded string) (map[string]any, error) {
	b, err := hex.DecodeString(hexEncoded)
	if err != nil {
		return nil, err
	}
	return DecodeBytes(b)
}

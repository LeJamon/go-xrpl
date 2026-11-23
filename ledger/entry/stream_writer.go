package entry

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

// The append helpers below are the writing counterpart of streamReader. Each
// appends one field value, after its header, in exactly the form
// binarycodec.EncodeBytes produces for the same Go value, and fails on exactly
// the inputs it rejects. Values with intricate canonicalisation are handed to
// the codec's own serializers.

// maxVLLength is the largest payload a variable-length prefix can describe.
const maxVLLength = 918744

var zeroHash [32]byte

// isXAddress mirrors the codec's X-address detection, whose length gate makes
// it free for every other string.
func isXAddress(s string) bool {
	return len(s) == 47 && addresscodec.IsValidXAddress(s)
}

func isXAddressValue(v any) bool {
	s, ok := v.(string)
	return ok && isXAddress(s)
}

func appendUInt8(dst []byte, v int) ([]byte, error) {
	if v < 0 || v > math.MaxUint8 {
		return nil, fmt.Errorf("value %d out of range for UInt8", v)
	}
	return append(dst, byte(v)), nil
}

func appendUInt16(dst []byte, v int) ([]byte, error) {
	if v < 0 || v > math.MaxUint16 {
		return nil, fmt.Errorf("value %d out of range for UInt16", v)
	}
	return binary.BigEndian.AppendUint16(dst, uint16(v)), nil
}

func appendInt32(dst []byte, v int) ([]byte, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return nil, types.ErrInvalidInt32
	}
	return binary.BigEndian.AppendUint32(dst, uint32(int32(v))), nil
}

func appendUInt64Hex(dst []byte, s string) ([]byte, error) {
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return nil, err
	}
	return binary.BigEndian.AppendUint64(dst, n), nil
}

// parseUInt64Decimal reads a UInt64 field rippled renders in base ten. The
// codec parses these while collecting fields, before serializing any, so the
// writers parse them up front too.
func parseUInt64Decimal(field, s string) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return n, nil
}

// appendHash writes a fixed-width hash given as hex; "0" denotes all zeros.
func appendHash(dst []byte, s string, size int) ([]byte, error) {
	if s == "0" {
		return append(dst, zeroHash[:size]...), nil
	}
	if len(s) != 2*size {
		if _, err := hex.DecodeString(s); err != nil {
			return nil, &types.InvalidHexStringError{Err: err}
		}
		return nil, &types.InvalidHashLengthError{Expected: size}
	}
	dst, err := appendHexDecoded(dst, s)
	if err != nil {
		return nil, &types.InvalidHexStringError{Err: err}
	}
	return dst, nil
}

// appendBlob writes a length-prefixed blob given as hex. An odd-length string
// is read with an implied leading zero nibble.
func appendBlob(dst []byte, s string) ([]byte, error) {
	n := len(s)/2 + len(s)%2
	if n > maxVLLength {
		for i := range len(s) {
			if reverseHex[s[i]] > 0x0f {
				return nil, hex.InvalidByteError(s[i])
			}
		}
		return nil, serdes.ErrLengthPrefixTooLong
	}
	dst = appendVariableLength(dst, n)
	if len(s)%2 != 0 {
		low := reverseHex[s[0]]
		if low > 0x0f {
			return nil, hex.InvalidByteError(s[0])
		}
		dst = append(dst, low)
		s = s[1:]
	}
	return appendHexDecoded(dst, s)
}

// appendAccountID writes a length-prefixed account. The empty string is the
// default account, serialized with a zero length.
func appendAccountID(dst []byte, s string) ([]byte, error) {
	var id []byte
	var err error
	switch {
	case s == "":
		return append(dst, 0), nil
	case s[0] == 'r':
		_, id, err = addresscodec.DecodeClassicAddressToAccountID(s)
	case s[0] == 'X' || s[0] == 'T':
		id, _, _, err = addresscodec.DecodeXAddress(s)
	default:
		return nil, addresscodec.ErrInvalidAddressFormat
	}
	if err != nil {
		return nil, err
	}
	return append(appendVariableLength(dst, len(id)), id...), nil
}

// appendAmount writes an Amount. Native amounts in the canonical form the
// decoder produces are written inline; every other value goes through the
// codec's Amount serializer.
func appendAmount(dst []byte, v any) ([]byte, error) {
	if s, ok := v.(string); ok {
		if wire, ok := canonicalNativeAmount(s); ok {
			return binary.BigEndian.AppendUint64(dst, wire), nil
		}
	}
	return appendCodecValue(dst, &types.Amount{}, v)
}

func (a *AccountRoot) balanceForEncode() any {
	if a.decodedOverCap != nil && a.Balance == a.decodedOverCap.decimal {
		return a.decodedOverCap.raw
	}
	return a.Balance
}

// canonicalNativeAmount returns the wire form of a drop count written as "0"
// or as an optionally negative decimal without leading zeros, within the
// supply cap. It reports false for anything else, including values the codec
// accepts in another spelling.
func canonicalNativeAmount(s string) (uint64, bool) {
	digits := s
	negative := len(digits) > 0 && digits[0] == '-'
	if negative {
		digits = digits[1:]
	}
	if len(digits) == 0 || len(digits) > 18 {
		return 0, false
	}
	if digits[0] == '0' {
		if len(digits) != 1 || negative {
			return 0, false
		}
		return types.PosSignBitMask, true
	}
	var drops uint64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		drops = drops*10 + uint64(c-'0')
	}
	if drops > types.MaxDrops {
		return 0, false
	}
	if negative {
		return drops, true
	}
	return drops | types.PosSignBitMask, true
}

func appendVector256(dst []byte, field string, hashes []string) ([]byte, error) {
	if len(hashes) > types.MaxJSONArrayElements {
		return nil, &types.JSONArrayTooLargeError{Field: field}
	}
	dst = appendVariableLength(dst, len(hashes)*len(zeroHash))
	var err error
	for _, h := range hashes {
		if dst, err = appendHash(dst, h, len(zeroHash)); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

func appendSTArray(dst []byte, field string, elements []any) ([]byte, error) {
	if len(elements) > types.MaxJSONArrayElements {
		return nil, &types.JSONArrayTooLargeError{Field: field}
	}
	return appendCodecValue(dst, &types.STArray{}, elements)
}

// appendInnerObjectField writes a whole nested-object field: header, body and
// end marker. The codec applies an inner object's template only to a field of
// an enclosing object, so the field is encoded as the sole member of one.
func appendInnerObjectField(dst []byte, field string, object map[string]any) ([]byte, error) {
	encoded, err := binarycodec.EncodeBytes(map[string]any{field: object})
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}

func appendCodecValue(dst []byte, st types.SerializedType, v any) ([]byte, error) {
	encoded, err := st.FromJSON(v)
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}

// appendNFTokens writes the body of an NFTokens array: one NFToken object per
// token followed by the array end marker.
func appendNFTokens(dst []byte, tokens []NFTokenValue) ([]byte, error) {
	for i := range tokens {
		if tokens[i].HasURI() && len(tokens[i].URI) > maxVLLength {
			return nil, serdes.ErrLengthPrefixTooLong
		}
	}
	for i := range tokens {
		dst = append(dst, 0xEC, 0x5A)
		dst = append(dst, tokens[i].NFTokenID[:]...)
		if tokens[i].HasURI() {
			dst = append(dst, 0x75)
			dst = appendVariableLength(dst, len(tokens[i].URI))
			dst = append(dst, tokens[i].URI...)
		}
		dst = append(dst, types.ObjectEndMarker)
	}
	return append(dst, types.ArrayEndMarker), nil
}

// appendHexDecoded appends the bytes of the hex string s, failing exactly as
// hex.DecodeString does: on the first invalid character, then on odd length.
func appendHexDecoded(dst []byte, s string) ([]byte, error) {
	i := 0
	for ; i+1 < len(s); i += 2 {
		hi, lo := reverseHex[s[i]], reverseHex[s[i+1]]
		if hi > 0x0f {
			return nil, hex.InvalidByteError(s[i])
		}
		if lo > 0x0f {
			return nil, hex.InvalidByteError(s[i+1])
		}
		dst = append(dst, hi<<4|lo)
	}
	if len(s)%2 != 0 {
		if reverseHex[s[i]] > 0x0f {
			return nil, hex.InvalidByteError(s[i])
		}
		return nil, hex.ErrLength
	}
	return dst, nil
}

var reverseHex = func() (table [256]byte) {
	for i := range table {
		table[i] = 0xff
	}
	for i, c := range "0123456789" {
		table[c] = byte(i)
	}
	for i, c := range "abcdef" {
		table[c] = byte(10 + i)
		table[c-'a'+'A'] = byte(10 + i)
	}
	return table
}()

func variableLengthSize(length int) int {
	switch {
	case length <= 192:
		return 1
	case length <= 12480:
		return 2
	default:
		return 3
	}
}

func appendVariableLength(data []byte, length int) []byte {
	switch {
	case length <= 192:
		return append(data, byte(length))
	case length <= 12480:
		length -= 193
		return append(data, byte((length>>8)+193), byte(length))
	default:
		length -= 12481
		return append(data, byte((length>>16)+241), byte(length>>8), byte(length))
	}
}

func accountIDEncodedLen(s string) int {
	if s == "" {
		return 1
	}
	return 21
}

func blobEncodedLen(s string) int {
	if len(s) > 2*maxVLLength {
		return 0
	}
	n := (len(s) + 1) / 2
	return variableLengthSize(n) + n
}

func amountEncodedLen(v any) int {
	switch amount := v.(type) {
	case string:
		return types.NativeAmountByteLength
	case map[string]any:
		if _, ok := amount["mpt_issuance_id"]; ok {
			return types.MPTAmountByteLength
		}
	}
	return types.CurrencyAmountByteLength
}

func vector256EncodedLen(hashes []string) int {
	if len(hashes) > types.MaxJSONArrayElements {
		return 0
	}
	n := len(hashes) * len(zeroHash)
	return variableLengthSize(n) + n
}

func nftokensEncodedLen(tokens []NFTokenValue) int {
	n := 1
	for i := range tokens {
		n += 35
		if tokens[i].HasURI() {
			if len(tokens[i].URI) > maxVLLength {
				return 0
			}
			n += 1 + variableLengthSize(len(tokens[i].URI)) + len(tokens[i].URI)
		}
	}
	return n
}

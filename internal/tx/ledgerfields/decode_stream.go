package ledgerfields

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/types"
)

// streamReader is a zero-allocation cursor over an XRPL binary blob. It is
// the minimum surface needed by the per-entry-type streaming decoders: read
// the field header (typeCode, fieldCode), peek raw bytes for fixed-width
// fields, and decode VL prefixes. Unlike serdes.BinaryParser this never
// copies bytes — fixed-width readers consume sub-slices of the original blob
// in-place and translate to the canonical decoded form (uppercase hex string
// for Hash*, decimal string for XRP drops, base58 for AccountID).
type streamReader struct {
	data []byte
	pos  int
}

func newStreamReader(data []byte) *streamReader { return &streamReader{data: data} }

func (r *streamReader) hasMore() bool { return r.pos < len(r.data) }

// readFieldHeader matches the format documented at
// codec/binarycodec/serdes/binary_parser.go:readFieldHeader.
func (r *streamReader) readFieldHeader() (typeCode, fieldCode int, err error) {
	if r.pos >= len(r.data) {
		return 0, 0, errors.New("ledgerfields: out of bounds reading field header")
	}
	b := r.data[r.pos]
	r.pos++
	typeCode = int(b >> 4)
	fieldCode = int(b & 0x0F)
	if typeCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended typeCode")
		}
		typeCode = int(r.data[r.pos])
		r.pos++
	}
	if fieldCode == 0 {
		if r.pos >= len(r.data) {
			return 0, 0, errors.New("ledgerfields: out of bounds reading extended fieldCode")
		}
		fieldCode = int(r.data[r.pos])
		r.pos++
	}
	return typeCode, fieldCode, nil
}

func (r *streamReader) readUint16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt16")
	}
	v := binary.BigEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *streamReader) readUint32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt32")
	}
	v := binary.BigEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *streamReader) readUint8() (byte, error) {
	if r.pos+1 > len(r.data) {
		return 0, errors.New("ledgerfields: out of bounds reading UInt8")
	}
	v := r.data[r.pos]
	r.pos++
	return v, nil
}

// readUint64Hex reads 8 bytes and returns the uppercase hex string — the
// canonical decoded form used by binarycodec.Decode for UInt64 fields.
func (r *streamReader) readUint64Hex() (string, error) {
	if r.pos+8 > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading UInt64")
	}
	s := upperHex(r.data[r.pos : r.pos+8])
	r.pos += 8
	return s, nil
}

func (r *streamReader) readHash(n int) (string, error) {
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading Hash")
	}
	s := upperHex(r.data[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// readVariableLength decodes the 1-3 byte length prefix used for AccountID
// and Blob.
func (r *streamReader) readVariableLength() (int, error) {
	b1, err := r.readUint8()
	if err != nil {
		return 0, err
	}
	switch {
	case b1 <= 192:
		return int(b1), nil
	case b1 <= 240:
		b2, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		return 193 + (int(b1)-193)*256 + int(b2), nil
	case b1 <= 254:
		b2, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		b3, err := r.readUint8()
		if err != nil {
			return 0, err
		}
		return 12481 + (int(b1)-241)*65536 + int(b2)*256 + int(b3), nil
	default:
		return 0, errors.New("ledgerfields: invalid VL prefix")
	}
}

// readAccountID reads a VL-prefixed 20-byte payload and base58-encodes it to
// the classic XRPL address — matching the value binarycodec.Decode would
// have produced.
func (r *streamReader) readAccountID() (string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading AccountID")
	}
	s, err := addresscodec.Encode(r.data[r.pos:r.pos+n], []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
	r.pos += n
	return s, err
}

// readBlobHex reads a VL-prefixed blob and returns its uppercase hex
// representation.
func (r *streamReader) readBlobHex() (string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if r.pos+n > len(r.data) {
		return "", errors.New("ledgerfields: out of bounds reading Blob")
	}
	s := upperHex(r.data[r.pos : r.pos+n])
	r.pos += n
	return s, nil
}

// readAmount returns the decimal-drops string for an XRP amount, or
// errUnsupportedAmount for IOU/MPT. Callers that need IOU/MPT support should
// call readAmountAny instead.
func (r *streamReader) readAmount() (any, error) {
	if r.pos >= len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Amount")
	}
	first := r.data[r.pos]
	if first&0x80 == 0 {
		if r.pos+8 > len(r.data) {
			return nil, errors.New("ledgerfields: out of bounds reading XRP Amount")
		}
		raw := binary.BigEndian.Uint64(r.data[r.pos:])
		r.pos += 8
		positive := raw&0x4000000000000000 != 0
		val := raw & 0x3FFFFFFFFFFFFFFF
		if !positive {
			return "-" + strconv.FormatUint(val, 10), nil
		}
		return strconv.FormatUint(val, 10), nil
	}
	return nil, errUnsupportedAmount
}

// readAmountAny decodes any Amount variant (XRP, IOU, MPT) inline. Order
// matches types/amount.go ToJSON: IOU first (bit 0x80), then MPT (bit 0x20),
// else XRP. Used by entry types whose Amount fields can legitimately be
// non-XRP (e.g. Offer.TakerPays, RippleState.Balance/LowLimit/HighLimit).
func (r *streamReader) readAmountAny() (any, error) {
	if r.pos >= len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Amount")
	}
	first := r.data[r.pos]
	switch {
	case first&0x80 != 0:
		return r.readIOUAmount()
	case first&0x20 != 0:
		return r.readMPTAmount()
	default:
		return r.readAmount()
	}
}

var errUnsupportedAmount = errors.New("ledgerfields: non-XRP amount in streaming decode")

// iouZeroBytes is the canonical 8-byte representation of an IOU value of 0
// (matches types/amount.go ZeroCurrencyAmountHex = 0x8000000000000000).
var iouZeroBytes = []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

// readIOUAmount decodes a 48-byte issued-currency Amount inline. It avoids
// the allocations that types.Amount.ToJSON incurs through bigdecimal,
// strings.ToUpper and hex.EncodeToString. The returned map matches the shape
// the codec produces: {value, currency, issuer}.
func (r *streamReader) readIOUAmount() (map[string]any, error) {
	if r.pos+48 > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading IOU Amount")
	}
	data := r.data[r.pos : r.pos+48]
	r.pos += 48

	var value string
	if bytes.Equal(data[0:8], iouZeroBytes) {
		value = "0"
	} else {
		v, err := decodeIOUValue(data[0:8])
		if err != nil {
			return nil, err
		}
		value = v
	}

	currency, err := decodeCurrencyCode(data[8:28])
	if err != nil {
		return nil, err
	}

	issuer, err := addresscodec.Encode(
		data[28:48],
		[]byte{addresscodec.AccountAddressPrefix},
		addresscodec.AccountAddressLength,
	)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"value":    value,
		"currency": currency,
		"issuer":   issuer,
	}, nil
}

// IOU bounds — copied from codec/binarycodec/types/amount.go so the inline
// decoder can validate without importing the codec types package.
const (
	maxIOUPrecisionInline = 16
	minIOUExponentInline  = -96
	maxIOUExponentInline  = 80
)

var (
	errIOUInvalidZero = errors.New("ledgerfields: invalid zero IOU value")
	errIOUPrecision   = errors.New("ledgerfields: IOU precision out of range")
	errIOUExponent    = errors.New("ledgerfields: IOU exponent out of range")
)

// decodeIOUValue produces the decimal string that
// codec/binarycodec/types/amount.go's deserializeValue → bigdecimal
// GetScaledValue path would return, but without constructing a big.Float or
// invoking the bigdecimal package. It also performs the equivalent of
// verifyIOUValue inline by validating precision and adjusted exponent against
// the encoded mantissa/exponent.
func decodeIOUValue(data []byte) (string, error) {
	b1 := data[0]
	b2 := data[1]
	positive := b1&0x40 != 0
	rawExp := int(b1&0x3F)<<2 | int(b2>>6)
	rawExp -= 97

	mantissa := uint64(b2&0x3F)<<48 |
		uint64(data[2])<<40 |
		uint64(data[3])<<32 |
		uint64(data[4])<<24 |
		uint64(data[5])<<16 |
		uint64(data[6])<<8 |
		uint64(data[7])
	if mantissa == 0 {
		return "", errIOUInvalidZero
	}

	mantStr := strconv.FormatUint(mantissa, 10)
	origLen := len(mantStr)
	trimLen := origLen
	for trimLen > 0 && mantStr[trimLen-1] == '0' {
		trimLen--
	}
	mTrimmed := mantStr[:trimLen]

	// verifyIOUValue: bigdecimal sees Scale = rawExp + (origLen - trimLen),
	// Precision = trimLen. adjustedExp = Scale + Precision - 16 collapses to
	// rawExp + origLen - 16, independent of how many trailing zeros the
	// mantissa carried.
	precision := trimLen
	if precision > maxIOUPrecisionInline {
		return "", errIOUPrecision
	}
	adjustedExp := rawExp + origLen - 16
	if adjustedExp < minIOUExponentInline || adjustedExp > maxIOUExponentInline {
		return "", errIOUExponent
	}

	scale := rawExp + (origLen - trimLen)

	if scale >= 0 {
		buf := make([]byte, 0, boolToInt(!positive)+trimLen+scale)
		if !positive {
			buf = append(buf, '-')
		}
		buf = append(buf, mTrimmed...)
		for i := 0; i < scale; i++ {
			buf = append(buf, '0')
		}
		return string(buf), nil
	}

	s := -scale
	if s >= trimLen {
		// "0." + (s-trimLen) zeros + mTrimmed
		buf := make([]byte, 0, boolToInt(!positive)+2+(s-trimLen)+trimLen)
		if !positive {
			buf = append(buf, '-')
		}
		buf = append(buf, '0', '.')
		for i := 0; i < s-trimLen; i++ {
			buf = append(buf, '0')
		}
		buf = append(buf, mTrimmed...)
		return string(buf), nil
	}
	// mTrimmed[:trimLen-s] + "." + mTrimmed[trimLen-s:]
	buf := make([]byte, 0, boolToInt(!positive)+trimLen+1)
	if !positive {
		buf = append(buf, '-')
	}
	buf = append(buf, mTrimmed[:trimLen-s]...)
	buf = append(buf, '.')
	buf = append(buf, mTrimmed[trimLen-s:]...)
	return string(buf), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// decodeCurrencyCode mirrors codec/binarycodec/types/amount.go's
// deserializeCurrencyCode without the strings.ToUpper(hex.EncodeToString(...))
// double allocation. Returns "XRP" for the all-zero sentinel, the uppercased
// 3-char ISO code when the 12/3/5-byte standard layout matches the IOU
// charset, and the upper-hex 40-char form for any non-standard 20-byte code.
func decodeCurrencyCode(data []byte) (string, error) {
	if len(data) != 20 {
		return "", errors.New("ledgerfields: currency code must be 20 bytes")
	}

	allZero := true
	for _, b := range data {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "XRP", nil
	}

	standardLayout := true
	for i := 0; i < 12; i++ {
		if data[i] != 0 {
			standardLayout = false
			break
		}
	}
	if standardLayout {
		for i := 15; i < 20; i++ {
			if data[i] != 0 {
				standardLayout = false
				break
			}
		}
	}

	if standardLayout {
		// 12 zero bytes + "XRP" + 5 zero bytes is reserved.
		if data[12] == 'X' && data[13] == 'R' && data[14] == 'P' {
			return "", errors.New("ledgerfields: invalid currency code")
		}
		var iso [3]byte
		ok := true
		for i := 0; i < 3; i++ {
			b := data[12+i]
			if b >= 'a' && b <= 'z' {
				b -= 'a' - 'A'
			}
			iso[i] = b
			if !isValidIOUCodeByte(b) {
				ok = false
			}
		}
		if ok {
			return string(iso[:]), nil
		}
	}

	return upperHex(data), nil
}

func isValidIOUCodeByte(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	}
	switch b {
	case '?', '!', '@', '#', '$', '%', '^', '&', '*',
		'<', '>', '(', ')', '{', '}', '[', ']', '|':
		return true
	}
	return false
}

// readMPTAmount decodes a 33-byte MPToken Amount inline. verifyMPTValue
// constrains |value| ≤ 2^63-1, so the 8-byte mantissa fits in a uint64 and
// no big.Int math is required. The returned map shape matches the codec's
// deserializeMPTAmount: {value, mpt_issuance_id}.
func (r *streamReader) readMPTAmount() (map[string]any, error) {
	if r.pos+33 > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading MPT Amount")
	}
	data := r.data[r.pos : r.pos+33]
	r.pos += 33

	positive := data[0]&0x40 != 0
	mant := binary.BigEndian.Uint64(data[1:9])

	var value string
	if positive {
		value = strconv.FormatUint(mant, 10)
	} else {
		value = "-" + strconv.FormatUint(mant, 10)
	}

	return map[string]any{
		"value":           value,
		"mpt_issuance_id": hex.EncodeToString(data[9:33]),
	}, nil
}

// readVector256 reads a VL-prefixed array of 32-byte hashes and returns
// the canonical decoded form ([]string of uppercase hex hashes) that
// binarycodec.Decode would produce. Used by ledger entries that carry
// Vector256 with sMD_default (LedgerHashes.Hashes, Amendments.Amendments).
func (r *streamReader) readVector256() ([]string, error) {
	n, err := r.readVariableLength()
	if err != nil {
		return nil, err
	}
	if n%32 != 0 {
		return nil, errors.New("ledgerfields: Vector256 length not a multiple of 32")
	}
	if r.pos+n > len(r.data) {
		return nil, errors.New("ledgerfields: out of bounds reading Vector256")
	}
	out := make([]string, 0, n/32)
	end := r.pos + n
	for r.pos < end {
		out = append(out, upperHex(r.data[r.pos:r.pos+32]))
		r.pos += 32
	}
	return out, nil
}

// readSTObject and readSTArray drive a sub-parser over the underlying byte
// slice via binarycodec.types and resync r.pos by how many bytes the
// sub-parser consumed. The compound types' decoders contain rippled-faithful
// recursive logic (nested STObjects, ObjectEndMarker handling, enum string
// substitution) that is not worth re-implementing inline; the cost is one
// BinaryParser allocation per compound field, paid only on ledger entries
// that carry one (AMM, SignerList, NFTokenPage, Vault, …).
func (r *streamReader) readSTObject() (map[string]any, error) {
	v, err := r.decodeViaCodec(&types.STObject{}, -1)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("ledgerfields: STObject decode returned wrong type")
	}
	return m, nil
}

func (r *streamReader) readSTArray() ([]any, error) {
	v, err := r.decodeViaCodec(&types.STArray{}, -1)
	if err != nil {
		return nil, err
	}
	a, ok := v.([]any)
	if !ok {
		return nil, errors.New("ledgerfields: STArray decode returned wrong type")
	}
	return a, nil
}

// readIssue reads an Issue (XRP / IOU / MPT shape, 20 / 40 / 44 bytes).
func (r *streamReader) readIssue() (any, error) {
	return r.decodeViaCodec(&types.Issue{}, -1)
}

// readXChainBridge reads a fixed-size 80-byte XChainBridge.
func (r *streamReader) readXChainBridge() (any, error) {
	const xChainBridgeLength = 80
	return r.decodeViaCodec(&types.XChainBridge{}, xChainBridgeLength)
}

// readNumber reads a 12-byte Number.
func (r *streamReader) readNumber() (any, error) {
	return r.decodeViaCodec(&types.Number{}, -1)
}

// readPathSet reads a PathSet. Currently no ledger entry type carries one;
// this exists so the generator's type dispatch is complete.
func (r *streamReader) readPathSet() (any, error) {
	return r.decodeViaCodec(&types.PathSet{}, -1)
}

// decodeViaCodec builds a sub-parser positioned at r.pos, hands it to the
// supplied SerializedType to decode, then advances r.pos by the number of
// bytes the sub-parser consumed. vlen < 0 means "no explicit length"; the
// decoder figures out where the value ends from its own grammar (Issue uses
// internal markers, STObject/STArray scan for end markers).
func (r *streamReader) decodeViaCodec(st types.SerializedType, vlen int) (any, error) {
	sub := serdes.NewBinaryParser(r.data[r.pos:], definitions.Get())
	startRem := sub.Remaining()
	var v any
	var err error
	if vlen < 0 {
		v, err = st.ToJSON(sub)
	} else {
		v, err = st.ToJSON(sub, vlen)
	}
	if err != nil {
		return nil, err
	}
	consumed := startRem - sub.Remaining()
	r.pos += consumed
	return v, nil
}

// upperHex is hex.EncodeToString uppercased without the intermediate
// allocation produced by strings.ToUpper.
func upperHex(b []byte) string {
	const hextable = "0123456789ABCDEF"
	buf := make([]byte, len(b)*2)
	for i, v := range b {
		buf[i*2] = hextable[v>>4]
		buf[i*2+1] = hextable[v&0x0F]
	}
	return string(buf)
}

// skipField advances past the value of a field of the given type/code without
// decoding it — used for unknown or sMD_Never fields. Falls back via err if
// the type is one we don't know how to size.
func (r *streamReader) skipField(typeCode int) error {
	switch typeCode {
	case 1: // UInt16
		r.pos += 2
	case 2: // UInt32
		r.pos += 4
	case 3: // UInt64
		r.pos += 8
	case 4: // Hash128
		r.pos += 16
	case 5: // Hash256
		r.pos += 32
	case 6: // Amount — same order as types/amount.go ToJSON.
		if r.pos >= len(r.data) {
			return errors.New("ledgerfields: out of bounds skipping Amount")
		}
		switch {
		case r.data[r.pos]&0x80 != 0:
			r.pos += 48 // IOU
		case r.data[r.pos]&0x20 != 0:
			r.pos += 33 // MPT
		default:
			r.pos += 8 // XRP
		}
	case 7, 8, 19: // Blob, AccountID, Vector256 (VL-prefixed)
		n, err := r.readVariableLength()
		if err != nil {
			return err
		}
		r.pos += n
	case 16: // UInt8
		r.pos++
	case 17: // Hash160
		r.pos += 20
	default:
		return errors.New("ledgerfields: cannot skip unknown type code")
	}
	if r.pos > len(r.data) {
		return errors.New("ledgerfields: skip overran buffer")
	}
	return nil
}

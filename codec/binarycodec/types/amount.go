package types

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/internal/decimal"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
)

const (
	// MinIOUExponent is the minimum allowed exponent for IOU amounts.
	MinIOUExponent = -96
	// MaxIOUExponent is the maximum allowed exponent for IOU amounts.
	MaxIOUExponent = 80
	// MaxIOUPrecision is the maximum precision (significant digits) for IOU amounts.
	MaxIOUPrecision = 16
	// MinIOUMantissa is the minimum mantissa value for IOU amounts.
	MinIOUMantissa = 1000000000000000
	// MaxIOUMantissa is the maximum mantissa value for IOU amounts.
	MaxIOUMantissa = 9999999999999999

	// NotXRPBitMask is the bit mask used to indicate non-XRP currency amounts.
	NotXRPBitMask = 0x80
	// PosSignBitMask is the bit mask used to indicate positive amounts.
	PosSignBitMask = 0x4000000000000000
	// ZeroCurrencyAmountHex is the hex representation of a zero currency amount.
	ZeroCurrencyAmountHex = 0x8000000000000000
	// NativeAmountByteLength is the byte length for native XRP amounts.
	NativeAmountByteLength = 8
	// CurrencyAmountByteLength is the byte length for currency amounts.
	CurrencyAmountByteLength = 48

	// MPTAmountByteLength is the byte length for MPToken amounts.
	MPTAmountByteLength = 33
	// MPTMarkerByte is the marker byte used to identify MPToken amounts.
	MPTMarkerByte = 0x60
	// MPTIssuanceIDByteLength is the byte length for MPToken issuance IDs.
	MPTIssuanceIDByteLength = 24
	// MPTValueByteLength is the byte length for MPToken values.
	MPTValueByteLength = 8
	// MPTValueWithHeaderLength is the byte length for MPToken values including header.
	MPTValueWithHeaderLength = 9
	// MPTSignBitMask is the bit mask for MPToken sign bits.
	MPTSignBitMask = 0x40
	// MPTHighBitMask is the high bit mask for MPToken amounts.
	MPTHighBitMask = 0x80
	// MPTAmountFlag is the flag used to identify MPToken amounts.
	MPTAmountFlag = 0x20

	// MinXRP is the minimum XRP amount in XRP units.
	MinXRP = 1e-6
	// MaxDrops is the maximum number of drops (100 billion XRP in drops). It is
	// an untyped integer constant so drop counts never enter float arithmetic.
	MaxDrops = 100_000_000_000_000_000 // 100 billion XRP in drops aka 10^17

	// IOUCodeRegex is the regular expression pattern for IOU currency codes.
	IOUCodeRegex = `[0-9A-Za-z?!@#$%^&*<>(){}\[\]|]{3}`
)

var (
	errInvalidXRPValue          = errors.New("invalid XRP value")
	errInvalidCurrencyCode      = errors.New("invalid currency code")
	errNonCanonicalNegativeZero = errors.New("negative zero is not canonical")

	errInvalidMPTLength     = fmt.Errorf("MPT slice must be exactly %d bytes", MPTAmountByteLength)
	errInsufficientMPTBytes = fmt.Errorf("not enough bytes for MPT issuance ID, need %d bytes", MPTIssuanceIDByteLength)
	errInvalidIssuanceIDLen = fmt.Errorf("mpt_issuance_id must be exactly %d bytes", MPTIssuanceIDByteLength)

	zeroByteArray = make([]byte, 20)

	errAmountMissingValue            = errors.New("amount missing value field")
	errInvalidAmountValue            = errors.New("invalid amount value")
	errInvalidCurrencyValue          = errors.New("invalid currency value")
	errInvalidMPTIssuanceID          = errors.New("invalid mpt_issuance_id")
	errIssuedCurrencyMissingCurrency = errors.New("issued currency missing currency field")
	errIssuedCurrencyMissingIssuer   = errors.New("issued currency missing issuer field")
	errInvalidCurrencyFormat         = errors.New("invalid currency")
	errInvalidIssuerFormat           = errors.New("invalid issuer")
	errInvalidAmountType             = errors.New("invalid amount type")
)

// IsScientificOffset reports whether an IOU mantissa/exponent combination
// renders in scientific notation, per rippled STAmount::getText
// (STAmount.cpp:706-732).
func IsScientificOffset(exponent int) bool {
	return exponent != 0 && (exponent < -25 || exponent > -5)
}

// InvalidAmountError is a custom error type for invalid amounts.
type InvalidAmountError struct {
	Amount string
}

func (e *InvalidAmountError) Error() string {
	return fmt.Sprintf("value '%s' is an invalid amount", e.Amount)
}

// OutOfRangeError is a custom error type for out-of-range values.
type OutOfRangeError struct {
	Type string
}

func (e *OutOfRangeError) Error() string {
	return fmt.Sprintf("%s is out of range", e.Type)
}

// InvalidCodeError is a custom error type for invalid currency codes.
type InvalidCodeError struct {
	Disallowed string
}

func (e *InvalidCodeError) Error() string {
	return fmt.Sprintf("'%s' is/are disallowed or invalid", e.Disallowed)
}

// Amount is a struct that represents an XRPL Amount.
type Amount struct{}

// RawXRPAmount preserves native ledger amounts above the supply cap so
// invariant checks can detect corrupt state. Ordinary JSON amounts remain capped.
type RawXRPAmount struct {
	Drops    uint64
	Negative bool
}

// MarshalJSON renders a raw native amount in the same signed decimal form as a
// normal XRP amount. The value remains a RawXRPAmount in maps passed directly to
// the binary codec, so canonical wire bytes retain its original magnitude.
func (a RawXRPAmount) MarshalJSON() ([]byte, error) {
	value := strconv.FormatUint(a.Drops, 10)
	if a.Negative && a.Drops != 0 {
		value = "-" + value
	}
	return json.Marshal(value)
}

// RawMPTValue preserves a raw MPT mantissa and sign when a transaction wire
// value cannot be represented by the canonical JSON amount string. The raw
// value remains typed in decoded field maps so binary re-encoding can retain
// non-canonical wire state.
type RawMPTValue struct {
	Magnitude uint64
	Negative  bool
}

// String renders a raw MPT value in rippled's signed decimal form. A zero
// magnitude is signless in JSON even when its wire sign is negative.
func (a RawMPTValue) String() string {
	value := strconv.FormatUint(a.Magnitude, 10)
	if a.Negative && a.Magnitude != 0 {
		value = "-" + value
	}
	return value
}

// MarshalJSON renders a raw MPT value in rippled's signed decimal form.
func (a RawMPTValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(a.String())
}

// FromJSON serializes an issued currency amount to its bytes representation from JSON.
func (a *Amount) FromJSON(value any) ([]byte, error) {
	return a.fromFields(value, jsonFields)
}

func (a *Amount) fromFields(value any, source fieldSource) ([]byte, error) {
	switch v := value.(type) {
	case RawXRPAmount:
		return serializeRawXrpAmount(v)
	case string:
		return serializeXrpAmount(v)
	case map[string]any:
		// Extract and normalize the "value" field
		rawVal, ok := v["value"]
		if !ok {
			return nil, errAmountMissingValue
		}
		rawMPT, isRawMPT := rawVal.(RawMPTValue)
		if isRawMPT && source != decodedFields {
			return nil, errInvalidAmountType
		}
		var val string
		if !isRawMPT {
			var err error
			val, err = valueToString(rawVal)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errInvalidAmountValue, err)
			}
		}
		// If there's an mpt_issuance_id key → MPT currency
		if rawID, ok := v["mpt_issuance_id"]; ok {
			id, err := valueToString(rawID)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errInvalidMPTIssuanceID, err)
			}
			if isRawMPT {
				return serializeRawMPTCurrencyAmount(rawMPT, id)
			}
			return serializeMPTCurrencyAmount(val, id)
		}

		// Otherwise, assume issued‐currency → must have both currency & issuer
		rawCurr, ok := v["currency"]
		if !ok {
			return nil, errIssuedCurrencyMissingCurrency
		}
		rawIss, ok := v["issuer"]
		if !ok {
			return nil, errIssuedCurrencyMissingIssuer
		}
		curr, ok := rawCurr.(string)
		if !ok {
			return nil, errInvalidCurrencyFormat
		}
		iss, err := valueToString(rawIss)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errInvalidIssuerFormat, err)
		}
		return serializeIssuedCurrencyAmount(val, source.currencyText(curr), iss)

	default:
		return nil, errInvalidAmountType
	}
}

// ToJSON deserializes a binary-encoded Amount object from a BinaryParser into a JSON representation.
// The order of checks is important:
// 1. If bit 0x80 is set → IOU (48 bytes)
// 2. If bit 0x20 is set → MPT (33 bytes)
// 3. Otherwise → XRP (8 bytes)
func (a *Amount) ToJSON(p *serdes.BinaryParser, _ ...int) (any, error) {
	return a.toJSON(p, p.AllowsOverCap(), p.AllowsOverCap())
}

// ToJSONAllowOverCap decodes an amount without enforcing the XRP supply cap.
// Transaction wire decoding uses ToJSON with a parser configured for the
// transaction ingress path; generic ledger decoding remains capped.
func (a *Amount) ToJSONAllowOverCap(p *serdes.BinaryParser) (any, error) {
	return a.toJSON(p, true, false)
}

func (a *Amount) toJSON(p *serdes.BinaryParser, allowOverCap, preserveRaw bool) (any, error) {
	b, err := p.Peek()
	if err != nil {
		return nil, err
	}
	// Check IOU first (bit 0x80 set) - IOU amounts are 48 bytes
	if !isNative(b) {
		token, err := p.ReadBytes(48)
		if err != nil {
			return nil, err
		}
		m, err := deserializeToken(token)
		if err != nil {
			return nil, err
		}
		return m, nil
	}

	// Check MPT next (bit 0x20 set) - MPT amounts are 33 bytes
	if b&MPTAmountFlag != 0 {
		token, err := p.ReadBytes(MPTAmountByteLength)
		if err != nil {
			return nil, err
		}
		m, err := deserializeMPTAmount(token, preserveRaw)
		if err != nil {
			return nil, err
		}
		return m, nil
	}

	// Otherwise it's native XRP (8 bytes)
	xrp, err := p.ReadBytes(8)
	if err != nil {
		return nil, err
	}
	xrpVal := binary.BigEndian.Uint64(xrp)
	xrpVal &= 0x3FFFFFFFFFFFFFFF
	if xrpVal == 0 {
		if !isPositive(b) {
			return nil, errNonCanonicalNegativeZero
		}
		return "0", nil
	}
	var sign string
	if !isPositive(b) {
		sign = "-"
	}
	// rippled's native deserialize skips canonicalize and so skips the max-drops
	// cap, but its JSON re-entry path applies it (STAmount.cpp:937-938,
	// cMaxNativeN = 10^17). This codec round-trips through JSON, so enforce the
	// cap on decode — otherwise an over-cap blob decodes to a value Encode rejects.
	if xrpVal > uint64(MaxDrops) && !allowOverCap {
		return nil, &InvalidAmountError{Amount: sign + strconv.FormatUint(xrpVal, 10)}
	}
	if xrpVal > uint64(MaxDrops) && preserveRaw {
		return RawXRPAmount{Drops: xrpVal, Negative: !isPositive(b)}, nil
	}
	return sign + strconv.FormatUint(xrpVal, 10), nil
}

func deserializeToken(data []byte) (map[string]any, error) {
	var value string
	var err error
	if bytes.Equal(data[0:8], []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}) {
		value = "0"
	} else {
		value, err = deserializeValue(data[:8])
		if err != nil {
			return nil, err
		}
	}
	// rippled rejects an issued-currency amount whose currency is the native XRP
	// code (all-zero 160 bits) — "invalid native currency" (STAmount.cpp:183-184).
	// deserializeCurrencyCode renders all-zero as "XRP", which Encode then refuses
	// as an IOU currency, so reject it here to keep decode and encode consistent.
	if bytes.Equal(data[8:28], zeroByteArray) {
		return nil, errInvalidCurrencyCode
	}
	issuer, err := deserializeIssuer(data[28:])
	if err != nil {
		return nil, err
	}
	curr, err := deserializeCurrencyCode(data[8:28])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"value":    value,
		"currency": curr,
		"issuer":   issuer,
	}, nil
}

func deserializeValue(data []byte) (string, error) {
	sign := ""
	if !isPositive(data[0]) {
		sign = "-"
	}
	valueBytes := data[:8]
	b1 := valueBytes[0]
	b2 := valueBytes[1]
	e1 := int((b1 & 0x3F) << 2)
	e2 := int(b2 >> 6)
	exponent := e1 + e2 - 97
	sigFigs := append([]byte{0, (b2 & 0x3F)}, valueBytes[2:]...)
	sigFigsInt := binary.BigEndian.Uint64(sigFigs)

	// rippled validates the raw mantissa and centered exponent on the IOU decode
	// path and throws "invalid currency value" for a non-canonical value
	// (STAmount.cpp:201-216). Without this the codec accepts an out-of-range
	// mantissa and silently re-normalizes it on the next Encode.
	if sigFigsInt != 0 {
		if sigFigsInt < MinIOUMantissa || sigFigsInt > MaxIOUMantissa ||
			exponent < MinIOUExponent || exponent > MaxIOUExponent {
			return "", errInvalidCurrencyValue
		}
	} else {
		// The only canonical zero is 0x8000000000000000 (the not-native bit alone,
		// rippled's offset == 512 at STAmount.cpp:215), which deserializeToken
		// short-circuits before reaching here — so any zero mantissa at this point
		// is non-canonical, exactly as rippled rejects it.
		return "", errInvalidCurrencyValue
	}

	if IsScientificOffset(exponent) {
		return sign + strconv.FormatUint(sigFigsInt, 10) + "e" + strconv.Itoa(exponent), nil
	}
	return decimal.Format(sigFigsInt, exponent, sign == "-"), nil
}

func deserializeCurrencyCode(data []byte) (string, error) {
	return decodeCurrencyCode(data)
}

func deserializeIssuer(data []byte) (string, error) {
	return addresscodec.Encode(data, []byte{addresscodec.AccountAddressPrefix}, addresscodec.AccountAddressLength)
}

// deserializeMPTValue extracts and formats the value component from an MPT amount binary representation.
// It handles sign bit and converts the 64-bit mantissa (split into MSB and LSB) to a string representation.
func deserializeMPTValue(data []byte) (string, error) {
	num, negative, err := mptValueParts(data)
	if err != nil {
		return "", err
	}
	return formatMPTValue(num, negative)
}

func mptValueParts(data []byte) (magnitude uint64, negative bool, err error) {
	if len(data) < MPTValueWithHeaderLength {
		return 0, false, errInvalidMPTLength
	}
	return binary.BigEndian.Uint64(data[1:MPTValueWithHeaderLength]), !isPositive(data[0]), nil
}

func formatMPTValue(magnitude uint64, negative bool) (string, error) {
	if magnitude == 0 {
		return "0", nil
	}
	sign := ""
	if negative {
		sign = "-"
	}
	// rippled's MPT deserialize skips canonicalize and so skips the max-value
	// cap, but its JSON re-entry path applies it (STAmount.cpp:939-940,
	// maxMPTokenAmount = 2^63-1). Enforce it on decode so the value re-encodes;
	// mirrors parseMPTValue's range check on the encode side.
	if magnitude > math.MaxInt64 {
		return "", &InvalidAmountError{Amount: sign + strconv.FormatUint(magnitude, 10)}
	}
	return sign + strconv.FormatUint(magnitude, 10), nil
}

// deserializeMPTIssuanceID extracts the issuance ID from an MPT amount binary representation
// and converts it to a hexadecimal string.
func deserializeMPTIssuanceID(data []byte) (string, error) {
	if len(data) < MPTIssuanceIDByteLength {
		return "", errInsufficientMPTBytes
	}
	idBytes := data[:MPTIssuanceIDByteLength]
	return strings.ToUpper(hex.EncodeToString(idBytes)), nil
}

// deserializeMPTAmount deserializes a complete MPT amount binary representation into its
// value and issuance ID components and returns them as a map.
// MPT amounts must be exactly 33 bytes in length.
func deserializeMPTAmount(data []byte, preserveRaw bool) (map[string]any, error) {
	if len(data) != MPTAmountByteLength {
		return nil, errInvalidMPTLength
	}
	val, err := deserializeMPTValueForDecode(data[:MPTValueWithHeaderLength], preserveRaw)
	if err != nil {
		return nil, err
	}
	id, err := deserializeMPTIssuanceID(data[MPTValueWithHeaderLength:])
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"value":           val,
		"mpt_issuance_id": id,
	}, nil
}

func deserializeMPTValueForDecode(data []byte, preserveRaw bool) (any, error) {
	magnitude, negative, err := mptValueParts(data)
	if err != nil {
		return nil, err
	}
	if preserveRaw && (magnitude > math.MaxInt64 || (magnitude == 0 && negative)) {
		return RawMPTValue{Magnitude: magnitude, Negative: negative}, nil
	}
	return formatMPTValue(magnitude, negative)
}

// splitIntegralAmount accepts an optional '+' or '-' followed by "0" or ASCII
// decimal digits without a leading zero, and returns the digits and sign.
func splitIntegralAmount(value string) (digits string, negative, ok bool) {
	digits = value
	if digits != "" && (digits[0] == '+' || digits[0] == '-') {
		negative = digits[0] == '-'
		digits = digits[1:]
	}
	if digits == "" || (digits[0] == '0' && len(digits) > 1) {
		return "", false, false
	}
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return "", false, false
		}
	}
	return digits, negative, true
}

// parseMagnitude returns the value of digits from splitIntegralAmount, or false
// when it exceeds limit. Without leading zeros, more than 19 digits is at least
// 10^19, above every limit used here; 19 digits always fit in a uint64.
func parseMagnitude(digits string, limit uint64) (uint64, bool) {
	if len(digits) > 19 {
		return 0, false
	}
	var n uint64
	for i := range len(digits) {
		n = n*10 + uint64(digits[i]-'0')
	}
	return n, n <= limit
}

// parseXrpValue parses a signed integer number of drops of magnitude at most
// MaxDrops. Negative values are valid in ledger objects (e.g., NFToken sell
// offers with negative prices).
func parseXrpValue(value string) (drops uint64, negative bool, err error) {
	digits, negative, ok := splitIntegralAmount(value)
	if !ok {
		return 0, false, errInvalidXRPValue
	}
	if drops, ok = parseMagnitude(digits, MaxDrops); !ok {
		return 0, false, &InvalidAmountError{Amount: value}
	}
	return drops, negative, nil
}

type iouValue struct {
	mantissa uint64
	exponent int32
	negative bool
}

func parseIOUValue(value string) (iouValue, error) {
	parts, err := decimal.Parse(value)
	if err != nil {
		return iouValue{}, errInvalidCurrencyValue
	}
	if parts.Mantissa == 0 {
		return iouValue{}, nil
	}

	// Apply the textual sign after the raw coefficient's signed conversion.
	signedMantissa := int64(parts.RawMantissa)
	if parts.Negative {
		signedMantissa = -signedMantissa
	}
	negative := signedMantissa < 0
	mantissa := uint64(signedMantissa)
	if negative {
		mantissa = -mantissa
	}
	exponent := int64(parts.RawExponent)
	for mantissa < MinIOUMantissa && exponent > MinIOUExponent {
		mantissa *= 10
		exponent--
	}

	var discarded, scale uint64 = 0, 1
	for mantissa > MaxIOUMantissa {
		discarded += mantissa % 10 * scale
		scale *= 10
		mantissa /= 10
		exponent++
	}
	if scale > 1 {
		half := scale / 2
		if discarded > half || discarded == half && mantissa&1 != 0 {
			mantissa++
			if mantissa > MaxIOUMantissa {
				mantissa /= 10
				exponent++
			}
		}
	}

	if exponent < MinIOUExponent || mantissa < MinIOUMantissa {
		return iouValue{}, nil
	}
	if exponent > MaxIOUExponent {
		return iouValue{}, &OutOfRangeError{Type: "Exponent"}
	}

	return iouValue{mantissa: mantissa, exponent: int32(exponent), negative: negative}, nil
}

// parseMPTValue parses a signed integer MPT amount whose magnitude fits in an
// int64. Negative values are valid for signing and serialization.
func parseMPTValue(value string) (magnitude uint64, negative bool, err error) {
	digits, negative, ok := splitIntegralAmount(value)
	if ok {
		magnitude, ok = parseMagnitude(digits, math.MaxInt64)
	}
	if !ok {
		return 0, false, &InvalidAmountError{Amount: value}
	}
	return magnitude, negative, nil
}

// serializeXrpAmount serializes an XRP amount value.
// Negative values are supported: the absolute value is stored without the positive sign bit.
// Reference: rippled STAmount::add — positive sets cPositive bit, negative does not.
func serializeXrpAmount(value string) ([]byte, error) {
	drops, negative, err := parseXrpValue(value)
	if err != nil {
		return nil, err
	}

	serialized := drops
	if !negative || drops == 0 {
		serialized |= PosSignBitMask
	}

	valBytes := make([]byte, NativeAmountByteLength)
	binary.BigEndian.PutUint64(valBytes, serialized)

	return valBytes, nil
}

func serializeRawXrpAmount(value RawXRPAmount) ([]byte, error) {
	// Bit 62 is the native positive marker and bit 61 identifies MPT amounts;
	// neither belongs to the native magnitude.
	if value.Drops > 0x1FFFFFFFFFFFFFFF {
		return nil, &InvalidAmountError{Amount: strconv.FormatUint(value.Drops, 10)}
	}
	serialized := value.Drops
	if !value.Negative || value.Drops == 0 {
		serialized |= PosSignBitMask
	}
	amount := make([]byte, NativeAmountByteLength)
	binary.BigEndian.PutUint64(amount, serialized)
	return amount, nil
}

// XRPL definition of precision is number of significant digits:
// Tokens can represent a wide variety of assets, including those typically measured in very small or very large denominations.
// This format uses significant digits and a power-of-ten exponent in a similar way to scientific notation.
// The format supports positive and negative significant digits and exponents within the specified range.
// Unlike typical floating-point representations of non-whole numbers, this format uses integer math for all calculations,
// so it always maintains 15 decimal digits of precision. Multiplication and division have adjustments to compensate for
// over-rounding in the least significant digits.

// SerializeIssuedCurrencyValue serializes the value field of an issued currency amount to its bytes representation.
func SerializeIssuedCurrencyValue(value string) ([]byte, error) {
	parsed, err := parseIOUValue(value)
	if err != nil {
		return nil, err
	}
	if parsed.mantissa == 0 {
		zeroAmount := make([]byte, 8)
		binary.BigEndian.PutUint64(zeroAmount, uint64(ZeroCurrencyAmountHex))
		return zeroAmount, nil
	}

	serial := uint64(ZeroCurrencyAmountHex)
	if !parsed.negative {
		serial |= PosSignBitMask
	}
	serial |= uint64(parsed.exponent+97) << 54
	serial |= parsed.mantissa

	serialReturn := make([]byte, 8)
	binary.BigEndian.PutUint64(serialReturn, serial)

	return serialReturn, nil
}

// serializeIssuedCurrencyCode serializes an issued currency code to its bytes representation.
// The currency code can be 3 allowed string characters, or 20 bytes of hex.
// The native code is disallowed in its ISO spelling.
func serializeIssuedCurrencyCode(currency string) ([]byte, error) {
	if currency == "XRP" {
		return nil, &InvalidCodeError{Disallowed: "XRP uppercase"}
	}

	return encodeCurrencyCode(currency)
}

// SerializeIssuedCurrencyAmount serializes the currency field of an issued currency amount to its bytes representation
// from value, currency code, and issuer address in string form (e.g. "USD", "r123456789").
// The currency code can be 3 allowed string characters, or 20 bytes of hex in standard currency format (e.g. with "00" prefix)
// or non-standard currency format (e.g. without "00" prefix)
func serializeIssuedCurrencyAmount(value, currency, issuer string) ([]byte, error) {
	valBytes, err := SerializeIssuedCurrencyValue(value) // serialize the value (zero included)
	if err != nil {
		return nil, err
	}
	currencyBytes, err := serializeIssuedCurrencyCode(currency) // serialize the currency code

	if err != nil {
		return nil, err
	}
	if bytes.Equal(currencyBytes, zeroByteArray) {
		return nil, errInvalidCurrencyCode
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer) // decode the issuer address
	if err != nil {
		return nil, err
	}

	// AccountIDs that appear as children of special fields (Amount issuer and PathSet account) are not length-prefixed.
	// So in Amount and PathSet fields, don't use the length indicator 0x14. This is in contrast to the AccountID fields where the length indicator prefix 0x14 is added.

	out := make([]byte, 0, len(valBytes)+len(currencyBytes)+len(issuerBytes))
	out = append(out, valBytes...)
	out = append(out, currencyBytes...)
	out = append(out, issuerBytes...)
	return out, nil
}

// serializeMPTCurrencyIssuanceID converts a hexadecimal issuance ID string to its binary representation.
// The issuance ID must be exactly 24 bytes when decoded.
func serializeMPTCurrencyIssuanceID(issuanceHex string) ([]byte, error) {
	idBytes, err := hex.DecodeString(issuanceHex)
	if err != nil {
		return nil, err
	}
	if len(idBytes) != MPTIssuanceIDByteLength {
		return nil, errInvalidIssuanceIDLen
	}
	return idBytes, nil
}

// serializeMPTCurrencyAmount serializes a complete MPT amount by combining the value and issuance ID.
// It adds the MPT marker byte and arranges the components into a 33-byte sequence.
func serializeMPTCurrencyAmount(valueStr, issuanceHex string) ([]byte, error) {
	magnitude, negative, err := parseMPTValue(valueStr)
	if err != nil {
		return nil, err
	}
	return serializeMPTCurrencyAmountParts(magnitude, magnitude == 0 || !negative, issuanceHex)
}

func serializeMPTCurrencyAmountParts(magnitude uint64, positive bool, issuanceHex string) ([]byte, error) {
	idBytes, err := serializeMPTCurrencyIssuanceID(issuanceHex)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, MPTAmountByteLength)
	// MPT header: bit 0x20 (MPT flag) + bit 0x40 (positive sign).
	// Negative amounts omit the positive bit.
	header := byte(MPTAmountFlag) // 0x20
	if positive {
		header |= MPTSignBitMask // 0x40
	}
	buf[0] = header
	binary.BigEndian.PutUint64(buf[1:MPTValueWithHeaderLength], magnitude)
	copy(buf[MPTValueWithHeaderLength:], idBytes)
	return buf, nil
}

func serializeRawMPTCurrencyAmount(value RawMPTValue, issuanceHex string) ([]byte, error) {
	return serializeMPTCurrencyAmountParts(value.Magnitude, !value.Negative, issuanceHex)
}

// Returns true if this amount is a "native" XRP amount - first bit in first byte set to 0 for native XRP
func isNative(value byte) bool {
	x := value&NotXRPBitMask == 0 // & bitwise operator returns 1 if both first bits are 1, otherwise 0
	return x
}

// Determines if this AmountType is positive - 2nd bit in 1st byte is set to 1 for positive amounts
func isPositive(value byte) bool {
	x := value&0x40 > 0
	return x
}

// valueToString converts various JSON‐style value types into their string form.
func valueToString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case float64:
		if x == math.Trunc(x) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported type %T for amount value", x)
	}
}

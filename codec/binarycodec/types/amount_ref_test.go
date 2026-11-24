package types

import (
	"encoding/binary"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Frozen copies of the original regexp and math/big amount validators and the
// serializers built on them. They are the oracle for the differential tests and
// fuzz targets; do not modify them.

var (
	refIntegralAmountRegex              = regexp.MustCompile(`^[+-]?(?:0|[1-9]\d*)$`)
	refErrFailedConvertStringToBigFloat = errors.New("failed to convert string to big.Float")
)

func refVerifyXrpValue(value string) error {
	if !refIntegralAmountRegex.MatchString(value) {
		return errInvalidXRPValue
	}

	// Strip the optional leading sign for validation.
	checkValue := value
	if strings.HasPrefix(checkValue, "-") || strings.HasPrefix(checkValue, "+") {
		checkValue = checkValue[1:]
	}

	decimal := new(big.Float)
	decimal, ok := decimal.SetString(checkValue) // bigFloat for precision (use absolute value)

	if !ok {
		return refErrFailedConvertStringToBigFloat
	}

	if decimal.Sign() == 0 {
		return nil
	}

	if decimal.Cmp(big.NewFloat(MinXRP)) == -1 || decimal.Cmp(big.NewFloat(MaxDrops)) == 1 {
		return &InvalidAmountError{value}
	}

	return nil
}

func refSerializeXrpAmount(value string) ([]byte, error) {
	if err := refVerifyXrpValue(value); err != nil {
		return nil, err
	}

	isNegative := strings.HasPrefix(value, "-")
	absValue := value
	if isNegative || strings.HasPrefix(value, "+") {
		absValue = value[1:]
	}

	val, err := strconv.ParseUint(absValue, 10, 64)
	if err != nil {
		return nil, err
	}

	var serialized uint64
	if isNegative && val != 0 {
		// Negative: store absolute value without positive sign bit
		serialized = val
	} else {
		// Positive: set the positive sign bit
		serialized = val | PosSignBitMask
	}

	valBytes := make([]byte, NativeAmountByteLength)
	binary.BigEndian.PutUint64(valBytes, serialized)

	return valBytes, nil
}

func refVerifyMPTValue(value string) error {
	if !refIntegralAmountRegex.MatchString(value) {
		return &InvalidAmountError{Amount: value}
	}

	bi := new(big.Int)
	if _, ok := bi.SetString(value, 10); !ok {
		return &InvalidAmountError{Amount: value}
	}

	// Negative values are valid for signing/serialization (sign bit used).
	// Use absolute value for magnitude checks.
	abs := new(big.Int).Abs(bi)

	// reject any absolute value ≥ 1<<63 so it fits in int64
	if abs.BitLen() > 63 {
		return &InvalidAmountError{Amount: value}
	}

	if abs.Sign() != 0 {
		mask := new(big.Int).SetUint64(ZeroCurrencyAmountHex)
		if new(big.Int).And(abs, mask).Sign() != 0 {
			return &InvalidAmountError{Amount: value}
		}
	}

	return nil
}

func refSerializeMPTCurrencyValue(value string) ([]byte, error) {
	if err := refVerifyMPTValue(value); err != nil {
		return nil, err
	}

	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, &InvalidAmountError{Amount: value}
	}

	// Use absolute value for encoding (sign is handled separately in the
	// header byte by serializeMPTCurrencyAmount).
	abs := new(big.Int).Abs(v)

	// verifyMPTValue ensures |v| ≤ 2^63-1, so Uint64() is safe
	buf := make([]byte, NativeAmountByteLength)
	binary.BigEndian.PutUint64(buf, abs.Uint64())
	return buf, nil
}

func refSerializeMPTCurrencyAmount(valueStr, issuanceHex string) ([]byte, error) {
	if err := refVerifyMPTValue(valueStr); err != nil {
		return nil, err
	}

	valBytes, err := refSerializeMPTCurrencyValue(valueStr)
	if err != nil {
		return nil, err
	}

	idBytes, err := serializeMPTCurrencyIssuanceID(issuanceHex)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, MPTAmountByteLength)
	// MPT header: bit 0x20 (MPT flag) + bit 0x40 (positive sign).
	// Negative amounts omit the positive bit.
	header := byte(MPTAmountFlag) // 0x20
	if binary.BigEndian.Uint64(valBytes) == 0 || !strings.HasPrefix(valueStr, "-") {
		header |= MPTSignBitMask // 0x40
	}
	buf[0] = header
	copy(buf[1:MPTValueWithHeaderLength], valBytes)
	copy(buf[MPTValueWithHeaderLength:], idBytes)
	return buf, nil
}

func refDeserializeMPTValue(data []byte) (string, error) {
	if len(data) < MPTValueWithHeaderLength {
		return "", errInvalidMPTLength
	}

	mant := data[1:MPTValueWithHeaderLength]
	msb := binary.BigEndian.Uint32(mant[0:4])
	lsb := binary.BigEndian.Uint32(mant[4:8])

	msbBig := new(big.Int).SetUint64(uint64(msb))
	lsbBig := new(big.Int).SetUint64(uint64(lsb))

	shifted := new(big.Int).Lsh(msbBig, 32)

	num := new(big.Int).Or(shifted, lsbBig)
	if num.Sign() == 0 {
		return "0", nil
	}

	sign := ""
	if !isPositive(data[0]) {
		sign = "-"
	}

	// rippled's MPT deserialize skips canonicalize and so skips the max-value
	// cap, but its JSON re-entry path applies it (STAmount.cpp:939-940,
	// maxMPTokenAmount = 2^63-1). Enforce it on decode so the value re-encodes;
	// mirrors verifyMPTValue's BitLen > 63 check on the encode side.
	if num.BitLen() > 63 {
		return "", &InvalidAmountError{Amount: sign + num.String()}
	}

	return sign + num.String(), nil
}

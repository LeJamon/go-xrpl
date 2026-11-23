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

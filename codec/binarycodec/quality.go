package binarycodec

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/internal/decimal"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

const (
	// zeroQualityHex is the hex representation of the zero quality.
	zeroQualityHex = 0x5500000000000000
)

var (
	// ErrInvalidQuality is returned when the quality is invalid.
	ErrInvalidQuality = errors.New("invalid quality")
)

// EncodeQuality encodes a quality amount to a hex string.
func EncodeQuality(quality string) (string, error) {
	if len(quality) == 0 {
		return "", ErrInvalidQuality
	}
	if len(strings.Trim(strings.Trim(quality, "0"), ".")) == 0 {
		zeroAmount := make([]byte, 8)
		binary.BigEndian.PutUint64(zeroAmount, uint64(zeroQualityHex))
		return hex.EncodeToString(zeroAmount), nil
	}

	parts, err := decimal.Parse(quality)
	if err != nil {
		return "", ErrInvalidQuality
	}

	if parts.Precision > types.MaxIOUPrecision ||
		parts.Exponent < types.MinIOUExponent || parts.Exponent > types.MaxIOUExponent {
		return "", ErrInvalidQuality
	}

	if parts.Mantissa == 0 {
		zeroAmount := make([]byte, 8)
		binary.BigEndian.PutUint64(zeroAmount, uint64(zeroQualityHex))
		return hex.EncodeToString(zeroAmount), nil
	}

	serialized := make([]byte, 8)
	binary.BigEndian.PutUint64(serialized, uint64(parts.Exponent+100)<<56|parts.Mantissa)
	return strings.ToUpper(hex.EncodeToString(serialized)), nil
}

// DecodeQuality decodes a quality amount from a hex string to a string.
func DecodeQuality(quality string) (string, error) {
	if quality == "" {
		return "", ErrInvalidQuality
	}

	decoded, err := hex.DecodeString(quality)
	if err != nil {
		return "", err
	}

	if len(decoded) < 8 {
		return "", ErrInvalidQuality
	}

	bytes := decoded[len(decoded)-8:]
	exp := int(bytes[0]) - 100
	mantissaBytes := append([]byte{0}, bytes[1:]...)
	mantissa := binary.BigEndian.Uint64(mantissaBytes)

	return decimal.Format(mantissa, exp, false), nil
}

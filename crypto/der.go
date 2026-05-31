package crypto

import (
	"encoding/hex"
	"errors"
	"math/big"
)

var (
	// ErrInvalidHexString is returned when the hex string is invalid.
	ErrInvalidHexString = errors.New("invalid hex string")

	// ErrInvalidDERNotEnoughData is returned when the DER data is not enough.
	ErrInvalidDERNotEnoughData = errors.New("invalid DER: not enough data")
	// ErrInvalidDERIntegerTag is returned when the DER integer tag is invalid.
	ErrInvalidDERIntegerTag = errors.New("invalid DER: expected integer tag")
	// ErrInvalidDERSignature is returned when the DER signature is invalid.
	ErrInvalidDERSignature = errors.New("invalid signature: incorrect length")
	// ErrInvalidDERSignatureValue is returned when r or s is not strictly
	// positive. A valid ECDSA signature has r and s in [1, n-1]; a zero scalar
	// is non-canonical (its DER integer carries no content bytes) and can never
	// verify.
	ErrInvalidDERSignatureValue = errors.New("invalid signature: r and s must be positive")
	// ErrLeftoverBytes is returned when there are leftover bytes after parsing the DER signature.
	ErrLeftoverBytes = errors.New("invalid signature: left bytes after parsing")
)

// EncodeDERSignature builds the canonical DER encoding of an ECDSA signature
// directly into a byte slice. The big.Int input form matches the existing
// signing code paths; callers holding raw r/s byte slices can pass
// new(big.Int).SetBytes(rBytes).
func EncodeDERSignature(r, s *big.Int) []byte {
	return encodeDERSignature(r, s)
}

// DERSigToRS decodes a DER-encoded ECDSA signature into the underlying
// r and s big-endian byte slices. Unlike [DERHexToSig], this avoids the
// hex round-trip required for callers that already hold raw bytes.
func DERSigToRS(data []byte) ([]byte, []byte, error) {
	if len(data) < 2 || data[0] != 0x30 {
		return nil, nil, ErrInvalidDERSignature
	}
	if int(data[1]) != len(data)-2 {
		return nil, nil, ErrInvalidDERSignature
	}

	r, sBytes, err := parseInt(data[2:])
	if err != nil {
		return nil, nil, ErrInvalidDERSignature
	}

	s, leftover, err := parseInt(sBytes)
	if err != nil {
		return nil, nil, ErrInvalidDERSignature
	}

	if len(leftover) > 0 {
		return nil, nil, ErrLeftoverBytes
	}

	if r.Sign() <= 0 || s.Sign() <= 0 {
		return nil, nil, ErrInvalidDERSignatureValue
	}

	return r.Bytes(), s.Bytes(), nil
}

// DERHexFromSig converts r and s hex strings to a DER-encoded signature hex string.
//
// Deprecated: callers that already hold r/s as *big.Int or []byte should use
// [EncodeDERSignature] directly to skip the hex round-trip.
func DERHexFromSig(rHex, sHex string) (string, error) {
	r, ok := new(big.Int).SetString(rHex, 16)
	if !ok {
		return "", ErrInvalidHexString
	}
	s, ok := new(big.Int).SetString(sHex, 16)
	if !ok {
		return "", ErrInvalidHexString
	}
	return hex.EncodeToString(encodeDERSignature(r, s)), nil
}

// parseInt parses an integer from DER-encoded data.
// It returns a *big.Int representing the parsed integer, a byte slice containing the remaining data after parsing,
// and an error if any occurred during parsing.
func parseInt(data []byte) (*big.Int, []byte, error) {
	if len(data) < 2 {
		return nil, nil, ErrInvalidDERNotEnoughData
	}
	if data[0] != 0x02 {
		return nil, nil, ErrInvalidDERIntegerTag
	}
	length := int(data[1])
	if len(data) < 2+length {
		return nil, nil, ErrInvalidDERNotEnoughData
	}
	number := new(big.Int).SetBytes(data[2 : 2+length])
	return number, data[2+length:], nil
}

// DERHexToSig converts a DER-encoded signature hex string to r and s byte slices.
//
// Deprecated: prefer [DERSigToRS] when the signature is already in byte form
// to avoid the hex round-trip.
func DERHexToSig(hexSignature string) ([]byte, []byte, error) {
	data, err := hex.DecodeString(hexSignature)
	if err != nil {
		return nil, nil, ErrInvalidHexString
	}
	return DERSigToRS(data)
}

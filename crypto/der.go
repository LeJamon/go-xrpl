package crypto

import (
	"errors"
	"math/big"
)

var (
	// ErrInvalidDERSignature is returned when the DER signature is invalid.
	ErrInvalidDERSignature = errors.New("invalid signature: incorrect length")
	// ErrInvalidDERSignatureValue is returned when r or s falls outside the
	// valid ECDSA scalar range [1, n-1], where n is the secp256k1 group order.
	// A zero scalar is non-canonical (its DER integer carries no content bytes)
	// and a scalar >= n is a reduced duplicate; neither can belong to a
	// signature that verifies.
	ErrInvalidDERSignatureValue = errors.New("invalid signature: r and s must be in [1, n-1]")
	// ErrLeftoverBytes is returned when there are leftover bytes after parsing the DER signature.
	ErrLeftoverBytes = errors.New("invalid signature: left bytes after parsing")
)

// EncodeDERSignature builds the minimal DER encoding of an ECDSA signature.
// Both scalars must be in [1, n-1]. The function does not normalize s to the
// low half of the curve order; use [ECDSACanonicality] when that distinction
// matters.
func EncodeDERSignature(r, s *big.Int) ([]byte, error) {
	if !validSecp256k1Scalar(r) || !validSecp256k1Scalar(s) {
		return nil, ErrInvalidDERSignatureValue
	}
	return encodeDERSignature(r, s), nil
}

// DERSigToRS decodes a DER-encoded ECDSA signature into the underlying
// r and s big-endian byte slices. It uses the strict integer parser shared
// with [ECDSACanonicality] (minimal encoding, non-negative, length-capped),
// and additionally requires r and s to lie in [1, n-1].
func DERSigToRS(data []byte) ([]byte, []byte, error) {
	if len(data) < 2 || data[0] != 0x30 {
		return nil, nil, ErrInvalidDERSignature
	}
	if int(data[1]) != len(data)-2 {
		return nil, nil, ErrInvalidDERSignature
	}

	rSlice, rest, ok := parseDERInteger(data[2:])
	if !ok {
		return nil, nil, ErrInvalidDERSignature
	}

	sSlice, leftover, ok := parseDERInteger(rest)
	if !ok {
		return nil, nil, ErrInvalidDERSignature
	}

	if len(leftover) > 0 {
		return nil, nil, ErrLeftoverBytes
	}

	r := new(big.Int).SetBytes(rSlice)
	s := new(big.Int).SetBytes(sSlice)
	if !validSecp256k1Scalar(r) || !validSecp256k1Scalar(s) {
		return nil, nil, ErrInvalidDERSignatureValue
	}

	return r.Bytes(), s.Bytes(), nil
}

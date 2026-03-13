package crypto

import (
	"math/big"
)

// Canonicality represents the canonicality status of an ECDSA signature.
type Canonicality int

const (
	// CanonicityNone indicates the signature is not canonical (invalid format or out of range).
	CanonicityNone Canonicality = iota
	// CanonicityCanonical indicates the signature is canonical but not fully canonical.
	// Both (R, S) and (R, G-S) are valid signatures for the same message.
	CanonicityCanonical
	// CanonicityFullyCanonical indicates the signature is fully canonical.
	// This means S <= G/2, which prevents signature malleability.
	CanonicityFullyCanonical
)

var (
	// secp256k1Order is the order of the secp256k1 curve group.
	// G = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
	secp256k1Order = func() *big.Int {
		n, _ := new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
		return n
	}()

	// secp256k1HalfOrder is G/2, used to determine full canonicality.
	secp256k1HalfOrder = new(big.Int).Rsh(secp256k1Order, 1)

	// ed25519Order is the order of the Ed25519 subgroup (L).
	// This is the big-endian representation used for signature validation.
	ed25519Order = []byte{
		0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x14, 0xDE, 0xF9, 0xDE, 0xA2, 0xF7, 0x9C, 0xD6,
		0x58, 0x12, 0x63, 0x1A, 0x5C, 0xF5, 0xD3, 0xED,
	}
)

// ECDSACanonicality checks if a DER-encoded ECDSA signature is canonical.
// It returns the canonicality status of the signature.
//
// A signature is canonical if:
//   - The DER encoding is valid
//   - R < curve order (G)
//   - S < curve order (G)
//
// A signature is fully canonical if additionally:
//   - S <= G/2 (low S value)
//
// Fully canonical signatures prevent signature malleability attacks.
// See: https://xrpl.org/transaction-malleability.html
func ECDSACanonicality(sig []byte) Canonicality {
	// DER signature format:
	// 0x30 <total-len> 0x02 <r-len> <r> 0x02 <s-len> <s>

	// Minimum: 8 bytes (0x30 len 0x02 1 R 0x02 1 S)
	// Maximum: 72 bytes (0x30 len 0x02 33 R 0x02 33 S)
	if len(sig) < 8 || len(sig) > 72 {
		return CanonicityNone
	}

	// Check sequence tag and length
	if sig[0] != 0x30 {
		return CanonicityNone
	}
	if int(sig[1]) != len(sig)-2 {
		return CanonicityNone
	}

	// Parse R
	rSlice, remaining, ok := parseDERInteger(sig[2:])
	if !ok {
		return CanonicityNone
	}

	// Parse S
	sSlice, remaining, ok := parseDERInteger(remaining)
	if !ok {
		return CanonicityNone
	}

	// No leftover bytes allowed
	if len(remaining) != 0 {
		return CanonicityNone
	}

	// Convert to big.Int
	r := new(big.Int).SetBytes(rSlice)
	s := new(big.Int).SetBytes(sSlice)

	// R must be in range [1, G-1]
	if r.Sign() <= 0 || r.Cmp(secp256k1Order) >= 0 {
		return CanonicityNone
	}

	// S must be in range [1, G-1]
	if s.Sign() <= 0 || s.Cmp(secp256k1Order) >= 0 {
		return CanonicityNone
	}

	// Check if fully canonical: S <= G/2
	if s.Cmp(secp256k1HalfOrder) <= 0 {
		return CanonicityFullyCanonical
	}

	return CanonicityCanonical
}

// parseDERInteger parses a DER-encoded integer from the data.
// Format: 0x02 <length> <integer-bytes>
// Returns the integer bytes, remaining data, and success status.
func parseDERInteger(data []byte) ([]byte, []byte, bool) {
	if len(data) < 2 {
		return nil, nil, false
	}

	// Check integer tag
	if data[0] != 0x02 {
		return nil, nil, false
	}

	length := int(data[1])
	if length < 1 || length > 33 {
		return nil, nil, false
	}

	if len(data) < 2+length {
		return nil, nil, false
	}

	intBytes := data[2 : 2+length]

	// Integer cannot be negative (high bit set without leading zero)
	if (intBytes[0] & 0x80) != 0 {
		return nil, nil, false
	}

	// Check for proper minimal encoding
	if intBytes[0] == 0 {
		// Single zero byte is invalid
		if length == 1 {
			return nil, nil, false
		}
		// Leading zero is only allowed if next byte has high bit set
		if (intBytes[1] & 0x80) == 0 {
			return nil, nil, false
		}
	}

	return intBytes, data[2+length:], true
}

// Ed25519Canonical checks if an Ed25519 signature has the correct format.
// It verifies that the S component is less than the Ed25519 subgroup order.
//
// Ed25519 signatures are 64 bytes: 32 bytes for R, 32 bytes for S (little-endian).
// The S component must be less than the curve order L to prevent malleability.
func Ed25519Canonical(sig []byte) bool {
	if len(sig) != 64 {
		return false
	}

	// The S component is in the second half of the signature (bytes 32-63).
	// It's stored in little-endian format, so we need to reverse it for comparison.
	sLE := sig[32:64]

	// Convert from little-endian to big-endian for comparison
	sBE := make([]byte, 32)
	for i := 0; i < 32; i++ {
		sBE[i] = sLE[31-i]
	}

	// S must be less than the Ed25519 order
	return bytesLessThan(sBE, ed25519Order)
}

// bytesLessThan compares two big-endian byte slices.
// Returns true if a < b.
func bytesLessThan(a, b []byte) bool {
	// Ensure equal length by padding
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	// Compare from most significant byte
	for i := 0; i < maxLen; i++ {
		var aByte, bByte byte
		if i < len(a) {
			aByte = a[i]
		}
		if i < len(b) {
			bByte = b[i]
		}
		if aByte < bByte {
			return true
		}
		if aByte > bByte {
			return false
		}
	}
	return false
}

// MakeSignatureCanonical takes a DER-encoded ECDSA signature and returns
// a fully canonical version by replacing S with G-S if S > G/2.
// Returns nil if the signature is invalid.
func MakeSignatureCanonical(sig []byte) []byte {
	canonicality := ECDSACanonicality(sig)
	if canonicality == CanonicityNone {
		return nil
	}
	if canonicality == CanonicityFullyCanonical {
		// Already fully canonical, return a copy
		result := make([]byte, len(sig))
		copy(result, sig)
		return result
	}

	// Need to replace S with G-S
	// Parse the signature
	if len(sig) < 8 || sig[0] != 0x30 {
		return nil
	}

	rSlice, remaining, ok := parseDERInteger(sig[2:])
	if !ok {
		return nil
	}
	sSlice, _, ok := parseDERInteger(remaining)
	if !ok {
		return nil
	}

	s := new(big.Int).SetBytes(sSlice)

	// Compute G - S
	newS := new(big.Int).Sub(secp256k1Order, s)

	// Re-encode the signature
	return encodeDERSignature(new(big.Int).SetBytes(rSlice), newS)
}

// encodeDERSignature creates a DER-encoded signature from R and S values.
func encodeDERSignature(r, s *big.Int) []byte {
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	// Add leading zero if high bit is set (would be interpreted as negative)
	if len(rBytes) > 0 && (rBytes[0]&0x80) != 0 {
		rBytes = append([]byte{0x00}, rBytes...)
	}
	if len(sBytes) > 0 && (sBytes[0]&0x80) != 0 {
		sBytes = append([]byte{0x00}, sBytes...)
	}

	// Handle zero case
	if len(rBytes) == 0 {
		rBytes = []byte{0x00}
	}
	if len(sBytes) == 0 {
		sBytes = []byte{0x00}
	}

	// Total length: 2 (R header) + R length + 2 (S header) + S length
	totalLen := 2 + len(rBytes) + 2 + len(sBytes)

	result := make([]byte, 2+totalLen)
	result[0] = 0x30           // Sequence tag
	result[1] = byte(totalLen) // Total length

	offset := 2
	result[offset] = 0x02 // Integer tag for R
	result[offset+1] = byte(len(rBytes))
	copy(result[offset+2:], rBytes)

	offset += 2 + len(rBytes)
	result[offset] = 0x02 // Integer tag for S
	result[offset+1] = byte(len(sBytes))
	copy(result[offset+2:], sBytes)

	return result
}

package addresscodec

import (
	"bytes"
	"crypto/subtle"
	"math/big"
)

// Frozen copies of the original math/big base58 codec and the check-decode
// helpers built on it. They are the oracle for the differential tests and
// fuzz targets; do not modify them.

var refBigRadix = [...]*big.Int{
	big.NewInt(0),
	big.NewInt(58),
	big.NewInt(58 * 58),
	big.NewInt(58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
	big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58),
}

func refDecodeBase58(b string) []byte {
	answer := big.NewInt(0)
	scratch := new(big.Int)

	for t := b; len(t) > 0; {
		n := min(len(t), 10)

		total := uint64(0)
		for i := range n {
			tmp := b58[t[i]]
			if tmp == 255 {
				return []byte("")
			}
			total = total*58 + uint64(tmp)
		}

		answer.Mul(answer, refBigRadix[n])
		scratch.SetUint64(total)
		answer.Add(answer, scratch)

		t = t[n:]
	}

	tmpval := answer.Bytes()

	var numZeros int
	for numZeros = 0; numZeros < len(b); numZeros++ {
		if b[numZeros] != alphabetIdx0 {
			break
		}
	}
	flen := numZeros + len(tmpval)
	val := make([]byte, flen)
	copy(val[numZeros:], tmpval)

	return val
}

func refBase58CheckDecode(input string) (result []byte, err error) {
	decoded := refDecodeBase58(input)
	if len(decoded) < 5 {
		return nil, ErrInvalidFormat
	}

	expected := checksum(decoded[:len(decoded)-4])
	if subtle.ConstantTimeCompare(expected[:], decoded[len(decoded)-4:]) != 1 {
		return nil, ErrChecksum
	}

	result = decoded[:len(decoded)-4]
	return
}

func refDecodeClassicAddressToAccountID(cAddress string) (typePrefix, accountID []byte, err error) {
	decoded, err := refBase58CheckDecode(cAddress)
	if err != nil {
		return nil, nil, ErrInvalidClassicAddress
	}
	if len(decoded) != 21 {
		return nil, nil, ErrInvalidClassicAddress
	}
	if decoded[0] != AccountAddressPrefix {
		return nil, nil, ErrInvalidClassicAddress
	}
	return decoded[:1], decoded[1:21], nil
}

func refDecode(b58string string, typePrefix []byte) ([]byte, error) {
	decoded, err := refBase58CheckDecode(b58string)
	if err != nil {
		return nil, err
	}
	prefixLength := len(typePrefix)
	if len(decoded) < prefixLength || !bytes.Equal(decoded[:prefixLength], typePrefix) {
		return nil, ErrPrefixMismatch
	}
	return decoded[prefixLength:], nil
}

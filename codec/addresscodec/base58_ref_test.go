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

func refEncodeBase58(b []byte) string {
	x := new(big.Int)
	x.SetBytes(b)

	maxlen := int(float64(len(b))*1.365658237309761) + 1
	answer := make([]byte, 0, maxlen)
	mod := new(big.Int)

	for x.Sign() > 0 {
		x.DivMod(x, refBigRadix[10], mod)

		if x.Sign() == 0 {
			m := mod.Int64()
			for m > 0 {
				answer = append(answer, xrpAlphabet[m%58])
				m /= 58
			}
		} else {
			m := mod.Int64()
			for range 10 {
				answer = append(answer, xrpAlphabet[m%58])
				m /= 58
			}
		}
	}
	for _, i := range b {
		if i != 0 {
			break
		}
		answer = append(answer, alphabetIdx0)
	}

	alen := len(answer)
	for i := 0; i < alen/2; i++ {
		answer[i], answer[alen-1-i] = answer[alen-1-i], answer[i]
	}

	return string(answer)
}

func refBase58CheckEncode(input []byte, prefix ...byte) string {
	b := make([]byte, 0, 1+len(input)+4)
	b = append(b, prefix...)
	b = append(b, input...)

	cksum := checksum(b)
	b = append(b, cksum[:]...)
	return refEncodeBase58(b)
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

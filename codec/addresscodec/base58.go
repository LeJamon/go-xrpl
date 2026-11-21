package addresscodec

import (
	"math/big"
	"math/bits"
)

var bigRadix10 = big.NewInt(58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58 * 58)

// pow58[n] is 58^n; 58^10 is the largest power of 58 that fits in a uint64,
// so decoding folds ten digits at a time into little-endian uint64 limbs.
var pow58 = func() (p [11]uint64) {
	p[0] = 1
	for i := 1; i < len(p); i++ {
		p[i] = p[i-1] * 58
	}
	return p
}()

// EncodeBase58 encodes a byte slice to a base58 string using the XRP alphabet.
func EncodeBase58(b []byte) string {
	x := new(big.Int)
	x.SetBytes(b)

	// Maximum length of output is log58(2^(8*len(b))) == len(b) * 8 / log(58)
	maxlen := int(float64(len(b))*1.365658237309761) + 1
	answer := make([]byte, 0, maxlen)
	mod := new(big.Int)

	for x.Sign() > 0 {
		// Calculating with big.Int is slow for each iteration.
		//    x, mod = x / 58, x % 58
		//
		// Instead we can try to do as much calculations on int64.
		//    x, mod = x / 58^10, x % 58^10
		//
		// Which will give us mod, which is 10 digit base58 number.
		// We'll loop that 10 times to convert to the answer.

		x.DivMod(x, bigRadix10, mod)

		if x.Sign() == 0 {
			// When x = 0, we need to ensure we don't add any extra zeros.
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
	// leading zero bytes
	for _, i := range b {
		if i != 0 {
			break
		}
		answer = append(answer, alphabetIdx0)
	}

	// reverse
	alen := len(answer)
	for i := 0; i < alen/2; i++ {
		answer[i], answer[alen-1-i] = answer[alen-1-i], answer[i]
	}

	return string(answer)
}

// DecodeBase58 decodes a modified base58 string to a byte slice. A string
// containing any character outside the XRP alphabet decodes to an empty slice.
func DecodeBase58(b string) []byte {
	var limbStack [8]uint64
	limbs := limbStack[:0]
	for t := b; len(t) > 0; {
		n := min(len(t), 10)
		var carry uint64
		// Index by byte, not by rune: bytes >= 0x80 map to the 255 sentinel.
		for i := range n {
			d := b58[t[i]]
			if d == 255 {
				return []byte("")
			}
			carry = carry*58 + uint64(d)
		}
		mul := pow58[n]
		i := 0
		// Two limbs per iteration keep the carry chain in the flags register.
		for ; i+1 < len(limbs); i += 2 {
			hi0, lo0 := bits.Mul64(limbs[i], mul)
			hi1, lo1 := bits.Mul64(limbs[i+1], mul)
			var c uint64
			limbs[i], c = bits.Add64(lo0, carry, 0)
			limbs[i+1], c = bits.Add64(lo1, hi0, c)
			carry, _ = bits.Add64(hi1, 0, c)
		}
		if i < len(limbs) {
			hi, lo := bits.Mul64(limbs[i], mul)
			var c uint64
			limbs[i], c = bits.Add64(lo, carry, 0)
			carry, _ = bits.Add64(hi, 0, c)
		}
		if carry != 0 {
			limbs = append(limbs, carry)
		}
		t = t[n:]
	}

	var numZeros int
	for numZeros = 0; numZeros < len(b); numZeros++ {
		if b[numZeros] != alphabetIdx0 {
			break
		}
	}
	// The most significant limb is never zero, so its bit length fixes the
	// minimal big-endian width of the value.
	valueLen := 0
	if len(limbs) > 0 {
		valueLen = (len(limbs)-1)*8 + (bits.Len64(limbs[len(limbs)-1])+7)/8
	}
	val := make([]byte, numZeros+valueLen)
	i := len(val)
	for _, limb := range limbs {
		for range 8 {
			if i == numZeros {
				break
			}
			i--
			val[i] = byte(limb)
			limb >>= 8
		}
	}

	return val
}

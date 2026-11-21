package addresscodec

import "math/bits"

// pow58[n] is 58^n; 58^10 is the largest power of 58 that fits in a uint64,
// so decoding folds ten digits at a time into little-endian uint64 limbs.
var pow58 = func() (p [11]uint64) {
	p[0] = 1
	for i := 1; i < len(p); i++ {
		p[i] = p[i-1] * 58
	}
	return p
}()

// radix58Pow5 is the largest power of 58 below 2^32, so dividing a uint32 limb
// and the carried remainder by it is a 64-bit division by a constant.
const radix58Pow5 = 58 * 58 * 58 * 58 * 58

// EncodeBase58 encodes a byte slice to a base58 string using the XRP alphabet.
func EncodeBase58(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}

	var limbStack [16]uint32
	limbs := limbStack[:0]
	for i := len(b); i > zeros; i -= 4 {
		var limb uint32
		for _, c := range b[max(i-4, zeros):i] {
			limb = limb<<8 | uint32(c)
		}
		limbs = append(limbs, limb)
	}

	// Each leading zero byte encodes as one 'r'; the remaining value needs at
	// most ceil(n*log(256)/log(58)) digits, and log(256)/log(58) < 1.366.
	var outStack [96]byte
	out := outStack[:]
	if size := zeros + (len(b)-zeros)*1366/1000 + 1; size > len(out) {
		out = make([]byte, size)
	}
	pos := len(out)

	for len(limbs) > 0 {
		// Each pass divides by 58^10 as two chained divisions by 58^5 whose
		// dependency chains overlap: lo is the first remainder, hi the second.
		var lo, hi uint64
		for i := len(limbs) - 1; i >= 0; i-- {
			cur := lo<<32 | uint64(limbs[i])
			q := cur / radix58Pow5
			lo = cur % radix58Pow5
			cur = hi<<32 | q
			limbs[i] = uint32(cur / radix58Pow5)
			hi = cur % radix58Pow5
		}
		for len(limbs) > 0 && limbs[len(limbs)-1] == 0 {
			limbs = limbs[:len(limbs)-1]
		}
		// Emit all ten digits of a low-order group, but no leading zeros for
		// the most significant one.
		rem := hi*radix58Pow5 + lo
		for range 10 {
			if rem == 0 && len(limbs) == 0 {
				break
			}
			pos--
			out[pos] = xrpAlphabet[rem%58]
			rem /= 58
		}
	}
	for range zeros {
		pos--
		out[pos] = alphabetIdx0
	}

	return string(out[pos:])
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

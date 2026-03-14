// Package rfc1751 implements RFC 1751 (S/KEY) encoding/decoding of binary
// data as human-readable English words. This is a faithful port of rippled's
// RFC1751.cpp implementation.
//
// RFC 1751 encodes 8 bytes of binary data as 6 English words.
// A 16-byte (128-bit) key produces 12 words (two groups of 6).
package rfc1751

import (
	"fmt"
	"strings"
)

// extract extracts 'length' bits from the byte slice 's' starting at bit 'start'.
func extract(s []byte, start, length int) uint32 {
	var cl, cc, cr byte

	shiftR := 24 - (length + (start % 8))
	cl = s[start/8]
	if shiftR < 16 {
		cc = s[start/8+1]
	}
	if shiftR < 8 {
		cr = s[start/8+2]
	}

	x := uint32(cl)<<16 | uint32(cc)<<8 | uint32(cr)
	x = x >> uint(shiftR)
	x = x & (0xffff >> uint(16-length))

	return x
}

// insert inserts 'length' bits of 'x' into byte slice 's' starting at bit 'start'.
func insert(s []byte, x int, start, length int) {
	shift := (8 - ((start + length) % 8)) % 8
	y := uint32(x) << uint(shift)
	cl := byte((y >> 16) & 0xff)
	cc := byte((y >> 8) & 0xff)
	cr := byte(y & 0xff)

	if shift+length > 16 {
		s[start/8] |= cl
		s[start/8+1] |= cc
		s[start/8+2] |= cr
	} else if shift+length > 8 {
		s[start/8] |= cc
		s[start/8+1] |= cr
	} else {
		s[start/8] |= cr
	}
}

// standard normalizes a word for dictionary lookup: uppercase, replace common
// digit-letter confusions.
func standard(word string) string {
	result := make([]byte, len(word))
	for i := 0; i < len(word); i++ {
		c := word[i]
		if c >= 'a' && c <= 'z' {
			result[i] = c - 32 // toupper
		} else if c == '1' {
			result[i] = 'L'
		} else if c == '0' {
			result[i] = 'O'
		} else if c == '5' {
			result[i] = 'S'
		} else {
			result[i] = c
		}
	}
	return string(result)
}

// wsrch performs a binary search of the dictionary for the given word.
// Returns the index or -1 if not found.
func wsrch(word string, iMin, iMax int) int {
	for iMin != iMax {
		iMid := iMin + (iMax-iMin)/2
		cmp := strings.Compare(word, dictionary[iMid])
		if cmp == 0 {
			return iMid
		} else if cmp < 0 {
			iMax = iMid
		} else {
			iMin = iMid + 1
		}
	}
	return -1
}

// btoe encodes 8 bytes of binary data as 6 English words.
func btoe(data []byte) string {
	buf := make([]byte, 9)
	copy(buf, data[:8])

	// Compute parity
	var p uint32
	for i := 0; i < 64; i += 2 {
		p += extract(buf, i, 2)
	}
	buf[8] = byte(p) << 6

	return dictionary[extract(buf, 0, 11)] + " " +
		dictionary[extract(buf, 11, 11)] + " " +
		dictionary[extract(buf, 22, 11)] + " " +
		dictionary[extract(buf, 33, 11)] + " " +
		dictionary[extract(buf, 44, 11)] + " " +
		dictionary[extract(buf, 55, 11)]
}

// etob converts 6 English words to 8 bytes of binary data.
// Returns the data and an error code:
//
//	1 = OK
//	0 = word not in dictionary
//	-1 = badly formed input
//	-2 = parity error
func etob(words []string) ([]byte, int) {
	if len(words) != 6 {
		return nil, -1
	}

	b := make([]byte, 9)
	p := 0

	for _, word := range words {
		l := len(word)
		if l > 4 || l < 1 {
			return nil, -1
		}

		w := standard(word)

		var minIdx, maxIdx int
		if l < 4 {
			minIdx, maxIdx = 0, 570
		} else {
			minIdx, maxIdx = 571, 2048
		}

		v := wsrch(w, minIdx, maxIdx)
		if v < 0 {
			return nil, 0
		}

		insert(b, v, p, 11)
		p += 11
	}

	// Check parity
	var parity uint32
	for i := 0; i < 64; i += 2 {
		parity += extract(b, i, 2)
	}

	if (parity & 3) != extract(b, 64, 2) {
		return nil, -2
	}

	return b[:8], 1
}

// KeyToEnglish converts a 16-byte (128-bit) key to 12 English words.
// The key bytes are in big-endian format.
func KeyToEnglish(key []byte) (string, error) {
	if len(key) != 16 {
		return "", fmt.Errorf("key must be exactly 16 bytes, got %d", len(key))
	}

	first := btoe(key[:8])
	second := btoe(key[8:16])

	return first + " " + second, nil
}

// EnglishToKey converts 12 English words to a 16-byte (128-bit) key.
// Returns the key in big-endian format.
func EnglishToKey(english string) ([]byte, error) {
	trimmed := strings.TrimSpace(english)
	words := strings.Fields(trimmed)

	if len(words) != 12 {
		return nil, fmt.Errorf("expected 12 words, got %d", len(words))
	}

	first, rc := etob(words[:6])
	if rc != 1 {
		return nil, fmt.Errorf("failed to decode first half (error code %d)", rc)
	}

	second, rc := etob(words[6:12])
	if rc != 1 {
		return nil, fmt.Errorf("failed to decode second half (error code %d)", rc)
	}

	key := make([]byte, 16)
	copy(key[:8], first)
	copy(key[8:], second)

	return key, nil
}

// SeedToEnglish converts a 16-byte seed to RFC1751 English words,
// matching rippled's seedAs1751 function which reverses the bytes before encoding.
func SeedToEnglish(seed []byte) (string, error) {
	if len(seed) != 16 {
		return "", fmt.Errorf("seed must be exactly 16 bytes, got %d", len(seed))
	}

	// rippled's seedAs1751 does: std::reverse_copy(seed.data(), seed.data() + 16, ...)
	reversed := make([]byte, 16)
	for i := 0; i < 16; i++ {
		reversed[i] = seed[15-i]
	}

	return KeyToEnglish(reversed)
}

// EnglishToSeed converts RFC1751 English words back to a 16-byte seed,
// reversing the byte order (inverse of SeedToEnglish).
func EnglishToSeed(english string) ([]byte, error) {
	key, err := EnglishToKey(english)
	if err != nil {
		return nil, err
	}

	// Reverse back
	seed := make([]byte, 16)
	for i := 0; i < 16; i++ {
		seed[i] = key[15-i]
	}

	return seed, nil
}

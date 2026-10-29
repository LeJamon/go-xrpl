package escrow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
)

func TestCryptoConditionLengthEncodings(t *testing.T) {
	field := func(tag byte, data []byte, n int) []byte {
		out := []byte{tag}
		if n == 0 {
			out = append(out, byte(len(data)))
		} else {
			out = append(out, 0x80|byte(n))
			out = append(out, make([]byte, n)...)
			out[len(out)-1] = byte(len(data))
		}
		return append(out, data...)
	}
	fingerprint := sha256.Sum256([]byte("a"))
	for _, target := range []string{"condition", "fingerprint", "cost", "fulfillment", "preimage"} {
		for n := 1; n <= 9; n++ {
			t.Run(fmt.Sprintf("%s/%d", target, n), func(t *testing.T) {
				width := func(name string) int {
					if target == name {
						return n
					}
					return 0
				}
				body := field(0x80, fingerprint[:], width("fingerprint"))
				body = append(body, field(0x81, []byte{1}, width("cost"))...)
				condition := field(0xa0, body, width("condition"))
				fulfillment := field(0xa0, field(0x80, []byte("a"), width("preimage")), width("fulfillment"))
				err := checkCondition(fulfillment, condition)
				if (err != nil) != (n == 9) {
					t.Fatalf("checkCondition error=%v; want error=%v", err, n == 9)
				}
				if target == "condition" || target == "fingerprint" || target == "cost" {
					err = ValidateConditionFormat(hex.EncodeToString(condition))
					if (err != nil) != (n == 9) {
						t.Fatalf("ValidateConditionFormat error=%v; want error=%v", err, n == 9)
					}
				}
			})
		}
	}
}

func TestASN1LengthBounds(t *testing.T) {
	for _, encoded := range []string{
		"", "80", "8100", "850000000000", "880000000000000000",
		"85", "8500000000", "8800000000000000",
		"850000000001", "880000000000000001", "88ffffffffffffffff",
		"888000000000000000", "887fffffffffffffff", "89000000000000000001ff",
	} {
		t.Run(encoded, func(t *testing.T) {
			data, err := hex.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseASN1Length(data); err == nil {
				t.Fatal("malformed or out-of-bounds length was accepted")
			}
		})
	}
}

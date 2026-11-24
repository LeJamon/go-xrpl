package types

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

func sameCodecErr(got, want error) bool {
	if got == nil || want == nil {
		return got == want
	}
	return reflect.TypeOf(got) == reflect.TypeOf(want) &&
		got.Error() == want.Error() &&
		errors.Is(got, errInvalidCurrencyCode) == errors.Is(want, errInvalidCurrencyCode)
}

func checkEncodeCurrencyMatchesReference(t testing.TB, code string) {
	t.Helper()
	got, gotErr := encodeCurrencyCode(code)
	want, wantErr := refEncodeCurrencyCode(code)
	if !sameCodecErr(gotErr, wantErr) || !bytes.Equal(got, want) || (got == nil) != (want == nil) {
		t.Fatalf("encodeCurrencyCode(%q) = %x, %v; want %x, %v", code, got, gotErr, want, wantErr)
	}
}

func checkDecodeCurrencyMatchesReference(t testing.TB, data []byte) {
	t.Helper()
	got, gotErr := decodeCurrencyCode(data)
	want, wantErr := refDecodeCurrencyCode(data)
	if !sameCodecErr(gotErr, wantErr) || got != want {
		t.Fatalf("decodeCurrencyCode(%x) = %q, %v; want %q, %v", data, got, gotErr, want, wantErr)
	}
}

func standardCurrency(code string) []byte {
	b := make([]byte, 20)
	copy(b[12:], code)
	return b
}

func TestCurrencyCodeMatchesReference(t *testing.T) {
	var compared int
	code := make([]byte, 3)
	for a := byte(0x20); a < 0x7f; a++ {
		for b := byte(0x20); b < 0x7f; b++ {
			for c := byte(0x20); c < 0x7f; c++ {
				code[0], code[1], code[2] = a, b, c
				checkEncodeCurrencyMatchesReference(t, string(code))
				checkDecodeCurrencyMatchesReference(t, standardCurrency(string(code)))
				compared++
			}
		}
	}

	neighbours := []byte("aZ0*| \x00\x7f\x80\xff")
	for pos := range 3 {
		for v := range 256 {
			for _, n1 := range neighbours {
				for _, n2 := range neighbours {
					code := []byte{n1, n2, n2}
					code[pos] = byte(v)
					checkEncodeCurrencyMatchesReference(t, string(code))
					checkEncodeCurrencyMatchesReference(t, "0x"+string(code))
					checkDecodeCurrencyMatchesReference(t, standardCurrency(string(code)))
					compared += 3
				}
			}
		}
	}

	rng := rand.New(rand.NewPCG(2192, 3))
	hexDigits := "0123456789abcdefABCDEF"
	randomHex := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = hexDigits[rng.IntN(len(hexDigits))]
		}
		return string(b)
	}
	codes := make([]string, 0, 8032)
	codes = append(codes,
		"", "0", "1", "XRP", "xrp", "Xrp", "xRP", "0xXRP", "0x", "0x1", "0x0x", "0xUSD", "USD", "usd",
		"US", "USDT", "U", "é", "éU", "Ué", "€", "\u0000\u0000\u0000",
		strings.Repeat("0", 40), "0x"+strings.Repeat("0", 40), strings.Repeat("0", 39)+"1",
		strings.Repeat("g", 40), strings.Repeat("0", 38)+"zz", strings.Repeat("0", 39), strings.Repeat("0", 41),
		"0158415500000000C1F76FF6ECB0BAC600000000", "0000000000000000000000005553440000000000",
		"0000000000000000000000005852500000000000", "0000000000000000000000000000000000000001",
	)
	for range 2000 {
		codes = append(codes, randomHex(40), "0x"+randomHex(40), randomHex(rng.IntN(45)))
		corrupt := []byte(randomHex(40))
		corrupt[rng.IntN(40)] = byte(rng.Uint32())
		codes = append(codes, string(corrupt))
	}
	for _, code := range codes {
		checkEncodeCurrencyMatchesReference(t, code)
		compared++
	}

	datas := make([][]byte, 0, 4007)
	datas = append(datas, nil, []byte{}, make([]byte, 19), make([]byte, 21), make([]byte, 20), noCurrencyBytes, standardCurrency("XRP"))
	for range 2000 {
		d := make([]byte, 20)
		for i := range d {
			d[i] = byte(rng.Uint32())
		}
		datas = append(datas, d)
		s := standardCurrency("USD")
		s[rng.IntN(20)] = byte(rng.Uint32())
		datas = append(datas, s)
	}
	for _, d := range datas {
		checkDecodeCurrencyMatchesReference(t, d)
		compared++
	}
	t.Logf("compared %d inputs", compared)
}

func FuzzEncodeCurrencyCodeDifferential(f *testing.F) {
	for _, s := range []string{"", "0", "1", "XRP", "USD", "usd", "0xA", "0x1", "0xUSD", "U$D", "U D", "é", "US\xff",
		"0158415500000000C1F76FF6ECB0BAC600000000", "0x0000000000000000000000005553440000000000",
		strings.Repeat("g", 40)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		checkEncodeCurrencyMatchesReference(t, code)
	})
}

func FuzzDecodeCurrencyCodeDifferential(f *testing.F) {
	for _, b := range [][]byte{{}, []byte("USD"), []byte("XRP"), []byte("U\x00D"), []byte("\xff\x80!"),
		standardCurrency("USD"), noCurrencyBytes, make([]byte, 20)} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkDecodeCurrencyMatchesReference(t, data)
		if len(data) >= 3 {
			checkDecodeCurrencyMatchesReference(t, standardCurrency(string(data[:3])))
		}
	})
}

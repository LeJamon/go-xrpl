package addresscodec

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
)

// sameSlice also compares nil-ness and capacity: callers receive sub-slices of
// the decoded buffer, so the shape of the result is part of the contract.
func sameSlice(a, b []byte) bool {
	return bytes.Equal(a, b) && (a == nil) == (b == nil) && cap(a) == cap(b)
}

func sameErr(a, b error) bool {
	if a == nil || b == nil {
		return a == b
	}
	return errors.Is(a, b) && errors.Is(b, a) && a.Error() == b.Error()
}

func checkDecodeMatchesReference(t testing.TB, s string) {
	t.Helper()

	if got, want := DecodeBase58(s), refDecodeBase58(s); !sameSlice(got, want) {
		t.Fatalf("DecodeBase58(%q) = %x (cap %d, nil %t), want %x (cap %d, nil %t)",
			s, got, cap(got), got == nil, want, cap(want), want == nil)
	}

	got, gotErr := Base58CheckDecode(s)
	want, wantErr := refBase58CheckDecode(s)
	if !sameErr(gotErr, wantErr) || !sameSlice(got, want) {
		t.Fatalf("Base58CheckDecode(%q) = %x, %v; want %x, %v", s, got, gotErr, want, wantErr)
	}

	gotPrefix, gotID, gotErr := DecodeClassicAddressToAccountID(s)
	wantPrefix, wantID, wantErr := refDecodeClassicAddressToAccountID(s)
	if !sameErr(gotErr, wantErr) || !sameSlice(gotPrefix, wantPrefix) || !sameSlice(gotID, wantID) {
		t.Fatalf("DecodeClassicAddressToAccountID(%q) = %x, %x, %v; want %x, %x, %v",
			s, gotPrefix, gotID, gotErr, wantPrefix, wantID, wantErr)
	}

	for _, prefix := range [][]byte{{NodePublicKeyPrefix}, {0x01, 0xE1, 0x4B}} {
		got, gotErr := Decode(s, prefix)
		want, wantErr := refDecode(s, prefix)
		if !sameErr(gotErr, wantErr) || !sameSlice(got, want) {
			t.Fatalf("Decode(%q, %x) = %x, %v; want %x, %v", s, prefix, got, gotErr, want, wantErr)
		}
	}
}

func randomAlphabetString(rng *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = xrpAlphabet[rng.IntN(len(xrpAlphabet))]
	}
	return string(b)
}

func randomBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

// base58DiffInputs generates valid encodings of every XRPL key class together
// with the malformed inputs derived from them.
func base58DiffInputs(t testing.TB) []string {
	rng := rand.New(rand.NewPCG(2192, 58))
	var inputs []string

	var ids [][]byte
	ids = append(ids, make([]byte, 20), bytes.Repeat([]byte{0xFF}, 20))
	for k := 1; k < 20; k++ {
		id := randomBytes(rng, 20)
		clear(id[:k])
		ids = append(ids, id)
	}
	for range 2000 {
		ids = append(ids, randomBytes(rng, 20))
	}

	addresses := make([]string, 0, len(ids))
	for _, id := range ids {
		addr, err := EncodeAccountIDToClassicAddress(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, got, err := DecodeClassicAddressToAccountID(addr); err != nil || !bytes.Equal(got, id) {
			t.Fatalf("valid address %q does not decode to %x: %x, %v", addr, id, got, err)
		}
		addresses = append(addresses, addr)
	}
	inputs = append(inputs, addresses...)

	const foreign = "0OIl+/ \x00\x7f\x80\xff"
	for _, addr := range addresses[:40] {
		for i := range len(addr) {
			for _, c := range []byte(xrpAlphabet + foreign) {
				if c != addr[i] {
					inputs = append(inputs, addr[:i]+string(c)+addr[i+1:])
				}
			}
			inputs = append(inputs, addr[:i], addr[i:], addr[:i]+addr[i+1:])
		}
		inputs = append(inputs, addr+"r", addr+"p", "r"+addr, addr+"é")
	}
	for k := range 41 {
		inputs = append(inputs, strings.Repeat("r", k), strings.Repeat("r", k)+addresses[0],
			strings.Repeat("r", k)+"p", strings.Repeat("r", k)+addresses[2][1:])
	}

	for range 300 {
		payload := randomBytes(rng, rng.IntN(48))
		prefix := randomBytes(rng, rng.IntN(4))
		inputs = append(inputs, Base58CheckEncode(payload, prefix...))
	}
	for range 200 {
		npk, err := EncodeNodePublicKey(randomBytes(rng, NodePublicKeyLength))
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, npk)
		var tag *uint32
		if rng.IntN(2) == 0 {
			v := rng.Uint32()
			tag = &v
		}
		x, err := EncodeXAddress(randomBytes(rng, AccountAddressLength), tag, Network(rng.IntN(2)))
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, x)
	}

	for n := range 121 {
		for range 20 {
			inputs = append(inputs, randomAlphabetString(rng, n))
		}
	}
	for n := range 61 {
		for range 20 {
			inputs = append(inputs, string(randomBytes(rng, n)))
		}
	}
	inputs = append(inputs,
		randomAlphabetString(rng, 1000),
		randomAlphabetString(rng, 5000),
		strings.Repeat("r", 100)+addresses[3],
		strings.Repeat("z", 300),
		strings.Repeat("r", 5000),
	)
	return inputs
}

func TestDecodeBase58MatchesReference(t *testing.T) {
	inputs := base58DiffInputs(t)
	for _, s := range inputs {
		checkDecodeMatchesReference(t, s)
	}
	t.Logf("compared %d inputs", len(inputs))
}

func checkEncodeMatchesReference(t testing.TB, b []byte) {
	t.Helper()

	if got, want := EncodeBase58(b), refEncodeBase58(b); got != want {
		t.Fatalf("EncodeBase58(%x) = %q, want %q", b, got, want)
	}
	for _, prefix := range [][]byte{nil, {AccountAddressPrefix}, {0x01, 0xE1, 0x4B}} {
		if got, want := Base58CheckEncode(b, prefix...), refBase58CheckEncode(b, prefix...); got != want {
			t.Fatalf("Base58CheckEncode(%x, %x) = %q, want %q", b, prefix, got, want)
		}
	}
}

func TestEncodeBase58MatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(2192, 85))
	var inputs [][]byte

	inputs = append(inputs, nil, []byte{})
	for v := range 1 << 16 {
		inputs = append(inputs, []byte{byte(v)}, []byte{byte(v >> 8), byte(v)})
	}
	for n := range 65 {
		inputs = append(inputs, make([]byte, n), bytes.Repeat([]byte{0xFF}, n))
		for k := 0; k <= n; k++ {
			b := randomBytes(rng, n)
			clear(b[:k])
			inputs = append(inputs, b)
		}
		for range 50 {
			inputs = append(inputs, randomBytes(rng, n))
		}
	}
	for _, n := range []int{20, 21, 25, 33, 35, 38} {
		for range 2000 {
			inputs = append(inputs, randomBytes(rng, n))
		}
	}
	for _, n := range []int{100, 255, 1000, 4096} {
		b := randomBytes(rng, n)
		inputs = append(inputs, b, append(make([]byte, 17), b...))
	}

	for _, b := range inputs {
		checkEncodeMatchesReference(t, b)
	}
	t.Logf("compared %d inputs", len(inputs))
}

func FuzzEncodeBase58Differential(f *testing.F) {
	for _, b := range [][]byte{
		{},
		{0},
		{0, 0, 0},
		{0, 0, 1},
		{0xFF, 0xFF, 0xFF},
		bytes.Repeat([]byte{0xFF}, 25),
		{0x88, 0xa5, 0xa5, 0x7c, 0x82, 0x9f, 0x40, 0xf2, 0x5e, 0xa8,
			0x33, 0x85, 0xbb, 0xde, 0x6c, 0x3d, 0x8b, 0x4c, 0xa0, 0x82},
	} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 256 {
			t.Skip()
		}
		checkEncodeMatchesReference(t, b)
	})
}

func FuzzDecodeBase58Differential(f *testing.F) {
	for _, s := range []string{
		"",
		"r",
		"rrrr",
		"rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmN",
		"rrrrrrrrrrrrrrrrrrrrrhoLvTp",
		"rQLbzfJH5BT1FS9apRLKV3G8dWEA5njaQi",
		"n9MDGCfimuyCmKXUAMcR12rv39PE6PY5YfFpNs75ZjtY3UWt31td",
		"snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
		"sEdTzRkEgPoxDG1mJ6WkSucHWnMkm1H",
		"X7AcgcsBL6XDcUb289X4mJ8djcdyKaB5hJDWMArnXr61cqZ",
		"rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhm0",
		"rDTXLQ7ZKZVKz33zJbHjgVShjsBnqMBhmNr",
		"\xff\x00r",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Both codecs are quadratic; long inputs are covered deterministically.
		if len(s) > 512 {
			t.Skip()
		}
		checkDecodeMatchesReference(t, s)
	})
}

package types

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/internal/testutil/rippled"
)

var amountErrSentinels = []error{
	errInvalidXRPValue, refErrFailedConvertStringToBigFloat,
	errInvalidMPTLength, errInvalidIssuanceIDLen, hex.ErrLength,
}

// sameAmountErr compares type, message and value, and the identity of both
// errors against every sentinel the amount serializers can return.
func sameAmountErr(got, want error) bool {
	if got == nil || want == nil {
		return got == want
	}
	if reflect.TypeOf(got) != reflect.TypeOf(want) || got.Error() != want.Error() || !reflect.DeepEqual(got, want) {
		return false
	}
	for _, sentinel := range amountErrSentinels {
		if errors.Is(got, sentinel) != errors.Is(want, sentinel) {
			return false
		}
	}
	return errors.Is(got, want) == errors.Is(want, got)
}

func sameAmountBytes(got, want []byte) bool {
	return bytes.Equal(got, want) && (got == nil) == (want == nil)
}

func checkXrpMatchesReference(t testing.TB, value string) {
	t.Helper()

	want, wantErr := refSerializeXrpAmount(value)
	got, gotErr := serializeXrpAmount(value)
	if !sameAmountErr(gotErr, wantErr) || !sameAmountBytes(got, want) {
		t.Fatalf("serializeXrpAmount(%q) = %x, %v; want %x, %v", value, got, gotErr, want, wantErr)
	}
	got, gotErr = (&Amount{}).FromJSON(value)
	if !sameAmountErr(gotErr, wantErr) || !sameAmountBytes(got, want) {
		t.Fatalf("Amount.FromJSON(%q) = %x, %v; want %x, %v", value, got, gotErr, want, wantErr)
	}

	wantErr = refVerifyXrpValue(value)
	drops, negative, gotErr := parseXrpValue(value)
	if !sameAmountErr(gotErr, wantErr) {
		t.Fatalf("parseXrpValue(%q) error = %v; want %v", value, gotErr, wantErr)
	}
	if gotErr != nil && (drops != 0 || negative) {
		t.Fatalf("parseXrpValue(%q) = %d, %t on error", value, drops, negative)
	}
}

// integralAmountInputs covers the grammar and range of signed integer amounts:
// every short string over digits, signs, decimal and exponent markers,
// whitespace, non-ASCII digits and invalid UTF-8; the neighbourhood of every
// limit; very long digit strings; random and mutated digit strings; and every
// amount value in the pinned conformance corpus.
func integralAmountInputs(t testing.TB) []string {
	t.Helper()
	rng := rand.New(rand.NewPCG(2192, 10))
	inputs := make([]string, 0, 1<<18)

	symbols := []string{
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"+", "-", ".", "e", "E", " ", "\t", "\n", "_", "x",
		"\x00", "\xff", "٣", "０", "५", "¹",
	}
	var allStrings func(prefix string, n int, alphabet []string)
	allStrings = func(prefix string, n int, alphabet []string) {
		inputs = append(inputs, prefix)
		if n == 0 {
			return
		}
		for _, s := range alphabet {
			allStrings(prefix+s, n-1, alphabet)
		}
	}
	allStrings("", 3, symbols)
	allStrings("", 7, []string{"0", "1", "9", "+", "-"})

	limits := []*big.Int{
		big.NewInt(0), big.NewInt(9), big.NewInt(10), big.NewInt(MaxDrops / 10), big.NewInt(MaxDrops),
		new(big.Int).Lsh(big.NewInt(1), 31), new(big.Int).Lsh(big.NewInt(1), 32),
		new(big.Int).Lsh(big.NewInt(1), 53), new(big.Int).Lsh(big.NewInt(1), 62),
		new(big.Int).Lsh(big.NewInt(1), 63), new(big.Int).Lsh(big.NewInt(1), 64),
		new(big.Int).Lsh(big.NewInt(1), 128),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil),
	}
	prefixes := []string{"", "+", "-", "--", "+-", "-+", "0", "-0", "+0", " ", "\t", "٣"}
	suffixes := []string{"", " ", "\n", ".", ".0", "e0", "E1", "0", "x", "\x00"}
	for _, limit := range limits {
		for delta := int64(-3); delta <= 3; delta++ {
			v := new(big.Int).Add(limit, big.NewInt(delta))
			if v.Sign() < 0 {
				continue
			}
			for _, p := range prefixes {
				for _, s := range suffixes {
					inputs = append(inputs, p+v.String()+s)
				}
			}
		}
	}

	for n := 1; n <= 45; n++ {
		for _, sign := range []string{"", "+", "-"} {
			inputs = append(inputs,
				sign+"1"+strings.Repeat("0", n-1),
				sign+strings.Repeat("9", n),
				sign+"1"+strings.Repeat("0", n)+"1",
				sign+"0"+strings.Repeat("1", n),
			)
		}
	}

	for _, n := range []int{100, 1000, 10000} {
		nines := strings.Repeat("9", n)
		zeros := strings.Repeat("0", n)
		inputs = append(inputs,
			nines, "-"+nines, "+"+nines, "1"+zeros, "-1"+zeros, zeros, "0"+nines,
			nines+"x", nines+".", nines+" ", "x"+nines, "-"+nines+"-",
		)
	}

	for range 20000 {
		b := make([]byte, 1+rng.IntN(25))
		for i := range b {
			b[i] = byte('0' + rng.IntN(10))
		}
		s := []string{"", "+", "-"}[rng.IntN(3)] + string(b)
		inputs = append(inputs, s)

		m := []byte(s)
		switch i := rng.IntN(len(m)); rng.IntN(3) {
		case 0:
			m[i] = byte(rng.Uint32())
		case 1:
			m = append(m[:i], append([]byte(symbols[rng.IntN(len(symbols))]), m[i:]...)...)
		default:
			m = append(m[:i], m[i+1:]...)
		}
		inputs = append(inputs, string(m))
	}

	inputs = append(inputs,
		"1.0", "0.000000007", "00", "125000708", "-125000708", "125000708.0", "524801", "+1", "+0", "-0",
		"7696581656832", "10000000", "99999999999999999", "10000000000000000", "100000000000000001",
		"1000000000000000000000000000000000000000", "100.50", "100abc", "9223372036854775807",
		"9223372036854775808", "-9223372036854775808", "18446744073709551615", "18446744073709551616",
		"1e6", "1E6", "0x10", "0b1", "0o7", "1_000", "Inf", "inf", "+Inf", "-inf", "NaN", "１", "1​",
	)
	inputs = append(inputs, conformanceAmountValues(t)...)
	for _, bc := range amountBenchCases {
		switch v := bc.value.(type) {
		case string:
			inputs = append(inputs, v)
		case map[string]any:
			inputs = append(inputs, v["value"].(string))
		}
	}
	return inputs
}

var conformanceCorpusDir = filepath.Join("..", "..", "..", filepath.FromSlash(rippled.Pin.Corpus))

// conformanceAmountValues returns the distinct value strings of every Amount
// field decoded from the transactions, metadata and ledger entries of the
// pinned conformance corpus.
func conformanceAmountValues(t testing.TB) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(conformanceCorpusDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Logf("no conformance corpus at %s", conformanceCorpusDir)
		return nil
	}

	seen := make(map[string]struct{})
	var blobs int
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture any
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		forEachCorpusBlob(fixture, func(blob string) {
			data, err := hex.DecodeString(blob)
			if err != nil {
				return
			}
			decoded, err := (&STObject{}).ToJSON(serdes.NewBinaryParser(data, definitions.Get()))
			if err != nil {
				return
			}
			blobs++
			collectAmountValues(decoded, "", seen)
		})
	}

	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	t.Logf("conformance corpus: %d blobs, %d distinct amount values", blobs, len(values))
	return values
}

func forEachCorpusBlob(v any, fn func(string)) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			switch k {
			case "tx_blob", "meta_blob", "data":
				if s, ok := child.(string); ok {
					fn(s)
					continue
				}
			case "tx_blobs":
				if list, ok := child.([]any); ok {
					for _, item := range list {
						if s, ok := item.(string); ok {
							fn(s)
						}
					}
					continue
				}
			}
			forEachCorpusBlob(child, fn)
		}
	case []any:
		for _, child := range v {
			forEachCorpusBlob(child, fn)
		}
	}
}

func collectAmountValues(v any, field string, seen map[string]struct{}) {
	isAmount := false
	if fi, err := definitions.Get().FieldInstanceByName(field); err == nil {
		isAmount = fi.Type == "Amount"
	}
	switch v := v.(type) {
	case string:
		if isAmount {
			seen[v] = struct{}{}
		}
	case map[string]any:
		if s, ok := v["value"].(string); ok && isAmount {
			seen[s] = struct{}{}
		}
		for k, child := range v {
			collectAmountValues(child, k, seen)
		}
	case []any:
		for _, child := range v {
			collectAmountValues(child, field, seen)
		}
	}
}

func TestXrpAmountMatchesReference(t *testing.T) {
	inputs := integralAmountInputs(t)
	for _, value := range inputs {
		checkXrpMatchesReference(t, value)
	}
	t.Logf("compared %d inputs", len(inputs))
}

const diffIssuanceID = "00000002430427B80BD2D09D36B70B969E12801065F22308"

var invalidIssuanceIDs = []string{
	"", "0", "00", diffIssuanceID[:46], diffIssuanceID[:47], diffIssuanceID + "0", diffIssuanceID + "00",
	diffIssuanceID[:47] + "G", "G" + diffIssuanceID[1:], strings.Repeat("Z", 49),
}

func checkMPTAmountMatchesReference(t testing.TB, value, issuanceID string) {
	t.Helper()

	want, wantErr := refSerializeMPTCurrencyAmount(value, issuanceID)
	got, gotErr := serializeMPTCurrencyAmount(value, issuanceID)
	if !sameAmountErr(gotErr, wantErr) || !sameAmountBytes(got, want) {
		t.Fatalf("serializeMPTCurrencyAmount(%q, %q) = %x, %v; want %x, %v",
			value, issuanceID, got, gotErr, want, wantErr)
	}
	got, gotErr = (&Amount{}).FromJSON(map[string]any{"value": value, "mpt_issuance_id": issuanceID})
	if !sameAmountErr(gotErr, wantErr) || !sameAmountBytes(got, want) {
		t.Fatalf("Amount.FromJSON(MPT %q, %q) = %x, %v; want %x, %v",
			value, issuanceID, got, gotErr, want, wantErr)
	}
	if wantErr == nil {
		checkMPTDecodeMatchesReference(t, want)
	}
}

func checkMPTValueMatchesReference(t testing.TB, value string) {
	t.Helper()

	wantErr := refVerifyMPTValue(value)
	magnitude, negative, gotErr := parseMPTValue(value)
	if !sameAmountErr(gotErr, wantErr) {
		t.Fatalf("parseMPTValue(%q) error = %v; want %v", value, gotErr, wantErr)
	}
	if gotErr != nil {
		if magnitude != 0 || negative {
			t.Fatalf("parseMPTValue(%q) = %d, %t on error", value, magnitude, negative)
		}
	} else {
		want, err := refSerializeMPTCurrencyValue(value)
		if err != nil {
			t.Fatalf("refSerializeMPTCurrencyValue(%q) = %v after a passing verify", value, err)
		}
		if magnitude != binary.BigEndian.Uint64(want) || negative != strings.HasPrefix(value, "-") {
			t.Fatalf("parseMPTValue(%q) = %d, %t; want %x", value, magnitude, negative, want)
		}
	}
	checkMPTAmountMatchesReference(t, value, diffIssuanceID)
}

// checkMPTDecodeMatchesReference decodes data as the leading value bytes of an
// MPT amount, directly and through Amount.ToJSON.
func checkMPTDecodeMatchesReference(t testing.TB, data []byte) {
	t.Helper()

	want, wantErr := refDeserializeMPTValue(data)
	got, gotErr := deserializeMPTValue(data)
	if !sameAmountErr(gotErr, wantErr) || got != want {
		t.Fatalf("deserializeMPTValue(%x) = %q, %v; want %q, %v", data, got, gotErr, want, wantErr)
	}
	if len(data) < MPTValueWithHeaderLength {
		return
	}

	wire := make([]byte, 0, MPTAmountByteLength)
	wire = append(wire, data[0]&^NotXRPBitMask|MPTAmountFlag)
	wire = append(wire, data[1:MPTValueWithHeaderLength]...)
	wire = append(wire, mustDecodeHex(diffIssuanceID)...)
	want, wantErr = refDeserializeMPTValue(wire)
	decoded, gotErr := (&Amount{}).ToJSON(serdes.NewBinaryParser(wire, nil))
	if !sameAmountErr(gotErr, wantErr) {
		t.Fatalf("Amount.ToJSON(%x) error = %v; want %v", wire, gotErr, wantErr)
	}
	if wantErr == nil {
		m, ok := decoded.(map[string]any)
		if !ok || m["value"] != want || m["mpt_issuance_id"] != diffIssuanceID {
			t.Fatalf("Amount.ToJSON(%x) = %v; want value %q", wire, decoded, want)
		}
	}
}

func TestMPTAmountMatchesReference(t *testing.T) {
	inputs := integralAmountInputs(t)
	for i, value := range inputs {
		checkMPTValueMatchesReference(t, value)
		checkMPTAmountMatchesReference(t, value, invalidIssuanceIDs[i%len(invalidIssuanceIDs)])
	}
	t.Logf("compared %d inputs", len(inputs))
}

func TestMPTAmountDecodeMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(2192, 33))
	magnitudes := make([]uint64, 0, 3016)
	magnitudes = append(magnitudes,
		0, 1, 2, 9, 10, 99, 100, 1<<32-1, 1<<32, MaxDrops, math.MaxInt64-1, math.MaxInt64,
		1<<63, 1<<63+1, math.MaxUint64-1, math.MaxUint64,
	)
	for range 1000 {
		magnitudes = append(magnitudes, rng.Uint64(), rng.Uint64()>>1, rng.Uint64()>>rng.IntN(64))
	}

	var compared int
	data := make([]byte, MPTValueWithHeaderLength)
	for header := range 256 {
		for _, m := range magnitudes {
			data[0] = byte(header)
			binary.BigEndian.PutUint64(data[1:], m)
			checkMPTDecodeMatchesReference(t, data)
			compared++
		}
	}
	for n := range MPTAmountByteLength + 2 {
		for range 200 {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte(rng.Uint32())
			}
			checkMPTDecodeMatchesReference(t, b)
			compared++
		}
	}
	checkMPTDecodeMatchesReference(t, nil)
	t.Logf("compared %d inputs", compared+1)
}

func FuzzMPTAmountDifferential(f *testing.F) {
	for _, s := range []string{
		"", "0", "-0", "+0", "100", "-1", "00", "01", "1.0", "1e3", " 1", "٣",
		"9223372036854775807", "-9223372036854775807", "9223372036854775808", "18446744073709551616",
	} {
		f.Add(s, diffIssuanceID)
		f.Add(s, diffIssuanceID[:47]+"G")
	}
	f.Fuzz(func(t *testing.T, value, issuanceID string) {
		checkMPTValueMatchesReference(t, value)
		checkMPTAmountMatchesReference(t, value, issuanceID)
	})
}

func FuzzMPTAmountDecodeDifferential(f *testing.F) {
	for _, b := range [][]byte{
		{}, {0x60}, mustDecodeHex("600000000000000064"), mustDecodeHex("207FFFFFFFFFFFFFFF"),
		mustDecodeHex("608000000000000000"), mustDecodeHex("20FFFFFFFFFFFFFFFF"), mustDecodeHex("200000000000000000"),
	} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkMPTDecodeMatchesReference(t, data)
	})
}

func FuzzXrpAmountDifferential(f *testing.F) {
	for _, s := range []string{
		"", "0", "-0", "+0", "10", "-1", "00", "01", "1.0", "1e3", " 1", "1 ", "٣",
		"100000000000000000", "100000000000000001", "99999999999999999999", "18446744073709551616",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, value string) {
		checkXrpMatchesReference(t, value)
	})
}

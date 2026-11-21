package entry

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/testutil/rippled"
	"github.com/LeJamon/go-xrpl/ledger/entry/schema"
)

// The differential harness below proves that every encoder an entry exposes
// produces exactly the bytes, and accepts exactly the inputs, of encodeViaMap:
// the original ToMap-through-binarycodec path, kept as the oracle.

type namedEncoder struct {
	name   string
	encode func() ([]byte, error)
}

func parityCandidates(e Entry) []namedEncoder {
	return []namedEncoder{{name: "Encode", encode: e.(encodeIface).Encode}}
}

type parityResult struct {
	encoded  bool
	textDiff string
}

// compareEncoders reports the first candidate whose acceptance or output
// differs from encodeViaMap. Error messages are compared separately: when an
// entry has several faults the oracle names one in map-iteration order, so a
// message difference is recorded only against an oracle message that is
// stable across runs.
func compareEncoders(e Entry) (parityResult, error) {
	oracle := e.(viaMapEncoder).encodeViaMap
	want, wantErr := oracle()
	result := parityResult{encoded: wantErr == nil}
	for _, candidate := range parityCandidates(e) {
		got, gotErr := candidate.encode()
		switch {
		case (gotErr == nil) != (wantErr == nil):
			return result, fmt.Errorf("%s %s: acceptance differs: candidate=%v map=%v", e.Type(), candidate.name, gotErr, wantErr)
		case gotErr != nil:
			if gotErr.Error() != wantErr.Error() && result.textDiff == "" && stableError(oracle, wantErr) {
				result.textDiff = fmt.Sprintf("%s %s: %q, map %q", e.Type(), candidate.name, gotErr, wantErr)
			}
		case !bytes.Equal(got, want):
			return result, fmt.Errorf("%s %s: bytes differ:\n got  %X\n want %X", e.Type(), candidate.name, got, want)
		}
	}
	return result, nil
}

func stableError(encode func() ([]byte, error), first error) bool {
	for range 8 {
		if _, err := encode(); err == nil || err.Error() != first.Error() {
			return false
		}
	}
	return true
}

func requireEncodeParity(t testing.TB, e Entry) parityResult {
	t.Helper()
	result, err := compareEncoders(e)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// requireDecodedParity re-decodes an oracle encoding and compares the encoders
// again on the decoded value. It reports whether the bytes decoded.
func requireDecodedParity(t testing.TB, typ Type, data []byte) bool {
	t.Helper()
	decoded := New(typ)
	if err := DecodeLegacy(decoded, data); err != nil {
		return false
	}
	requireEncodeParity(t, decoded)
	return true
}

func TestEncodeParitySLECorpus(t *testing.T) {
	manifest := loadSLECorpus(t)
	for _, vector := range manifest.Vectors {
		t.Run(vector.ID, func(t *testing.T) {
			corpusCase := decodeSLECorpusCase(t, vector)
			requireEncodeParity(t, corpusCase.Entry)
		})
	}
	t.Logf("compared %d corpus vectors", len(manifest.Vectors))
}

func TestEncodeParityFixtures(t *testing.T) {
	for _, tc := range roundTripCases {
		t.Run("roundtrip/"+tc.name, func(t *testing.T) {
			canonical, err := binarycodec.EncodeBytes(tc.json)
			if err != nil {
				t.Fatal(err)
			}
			e := NewByName(tc.json["LedgerEntryType"].(string))
			if err := e.Decode(canonical); err != nil {
				t.Fatal(err)
			}
			requireEncodeParity(t, e)
		})
	}
	for _, spec := range schema.Specs {
		t.Run("coverage/"+spec.Name, func(t *testing.T) {
			requireEncodeParity(t, fixtureEntry(t, spec.Name))
		})
	}
	t.Run("constructed", func(t *testing.T) {
		requireEncodeParity(t, completeWriterCheck(true))
		requireEncodeParity(t, completeWriterCheck(false))
		var amm AMM
		amm.SetTradingFee(500)
		requireEncodeParity(t, &amm)
		var vault Vault
		vault.SetAssetsTotal("0")
		requireEncodeParity(t, &vault)
		requireEncodeParity(t, &Sponsorship{})
		for _, count := range []int{0, 1, 32, 512, 513} {
			for _, uri := range []int{-1, 0, 192, 193, 12481} {
				requireEncodeParity(t, nftCodecPage(t, count, uri))
			}
		}
		page := nftCodecPage(t, 1, -1)
		page.SetNFTokens([]NFTokenValue{{}})
		requireEncodeParity(t, page)
	})
}

// conformanceCorpusDir is the pinned rippled corpus, relative to this package.
var conformanceCorpusDir = filepath.Join("..", "..", filepath.FromSlash(rippled.Pin.Corpus))

// TestEncodeParityConformanceCorpus decodes every ledger-entry blob recorded
// by the pinned rippled conformance corpus (pre-state, post-state and
// post-submit snapshots) and requires both the encoders' agreement and an
// exact reproduction of rippled's bytes.
func TestEncodeParityConformanceCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(conformanceCorpusDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no conformance fixtures under %s", conformanceCorpusDir)
	}
	blobs := make(map[string]struct{})
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var fixture any
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		collectLedgerEntryBlobs(fixture, blobs)
	}
	perType := make(map[Type]int)
	var unencodable []string
	for blob := range blobs {
		data, err := hex.DecodeString(blob)
		if err != nil {
			t.Fatalf("corpus blob is not hex: %v", err)
		}
		if len(data) < 3 || data[0] != 0x11 {
			t.Fatalf("corpus blob has no LedgerEntryType: %s", blob)
		}
		typ := Type(uint16(data[1])<<8 | uint16(data[2]))
		e := New(typ)
		if e == nil {
			t.Fatalf("corpus blob has unmodelled type %s", typ)
		}
		if err := e.Decode(data); err != nil {
			t.Fatalf("%s: Decode: %v", typ, err)
		}
		requireEncodeParity(t, e)
		perType[typ]++
		encoded, err := e.(encodeIface).Encode()
		if err != nil {
			// The JSON amount path caps native amounts at 10^17 drops; a
			// recorded over-cap balance decodes but is rejected by every encoder.
			unencodable = append(unencodable, fmt.Sprintf("%s: %v", typ, err))
			continue
		}
		if !bytes.Equal(encoded, data) {
			t.Fatalf("%s: Encode does not reproduce rippled's blob:\n got  %X\n want %X", typ, encoded, data)
		}
	}
	t.Logf("compared %d distinct ledger entries from %d fixtures: %v", len(blobs), len(files), perType)
	t.Logf("%d entries rejected by every encoder: %v", len(unencodable), unencodable)
}

func collectLedgerEntryBlobs(node any, out map[string]struct{}) {
	switch v := node.(type) {
	case map[string]any:
		if data, ok := v["data"].(string); ok {
			if _, ok := v["index"]; ok {
				out[data] = struct{}{}
			}
		}
		for _, child := range v {
			collectLedgerEntryBlobs(child, out)
		}
	case []any:
		for _, child := range v {
			collectLedgerEntryBlobs(child, out)
		}
	}
}

// fixtureBlob is the canonical encoding of coverageFixtures[name] with the
// common Sponsor field set, so every field of the entry is present.
func fixtureBlob(t testing.TB, name string) []byte {
	t.Helper()
	input := make(map[string]any, len(coverageFixtures[name])+2)
	for k, v := range coverageFixtures[name] {
		input[k] = v
	}
	if _, ok := input["Sponsor"]; !ok {
		input["Sponsor"] = fxAccount
	}
	input["LedgerEntryType"] = name
	data, err := binarycodec.EncodeBytes(input)
	if err != nil {
		t.Fatalf("%s: encode fixture: %v", name, err)
	}
	return data
}

func fixtureEntry(t testing.TB, name string) Entry {
	t.Helper()
	e := NewByName(name)
	if err := e.Decode(fixtureBlob(t, name)); err != nil {
		t.Fatalf("%s: decode fixture: %v", name, err)
	}
	return e
}

// parityField is one serialized field of an entry, with the candidate values
// the property test assigns to it.
type parityField struct {
	name      string
	xrplType  string
	clearable bool
	values    []any
}

func parityFields(t testing.TB, spec schema.Entry, base Entry) []parityField {
	t.Helper()
	defs := definitions.Get()
	elem := reflect.ValueOf(base).Elem()
	fields := make([]parityField, 0, len(spec.AllFields()))
	for _, f := range spec.AllFields() {
		if f.DecodeOnly {
			continue
		}
		fi, err := defs.FieldInstanceByName(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		slot := elem.FieldByName(f.Name)
		if !slot.IsValid() {
			t.Fatalf("%s has no struct field %s", spec.Name, f.Name)
		}
		pf := parityField{
			name:      f.Name,
			xrplType:  fi.Type,
			clearable: reflect.ValueOf(base).MethodByName("Clear" + f.Name).IsValid(),
		}
		for _, v := range parityValues(fi.Type, f.Name, slot.Interface(), slot.Kind()) {
			if assignable(slot.Type(), v) {
				pf.values = append(pf.values, v)
			}
		}
		fields = append(fields, pf)
	}
	return fields
}

func assignable(slot reflect.Type, v any) bool {
	if v == nil {
		switch slot.Kind() {
		case reflect.Interface, reflect.Slice, reflect.Map:
			return true
		}
		return false
	}
	return reflect.TypeOf(v).AssignableTo(slot)
}

func setRawField(e Entry, name string, v any) {
	slot := reflect.ValueOf(e).Elem().FieldByName(name)
	if v == nil {
		slot.Set(reflect.Zero(slot.Type()))
		return
	}
	slot.Set(reflect.ValueOf(v))
}

func clearField(e Entry, name string) {
	reflect.ValueOf(e).MethodByName("Clear" + name).Call(nil)
}

var (
	parityZeroAccount   = mustEncodeAccount([20]byte{})
	parityXAddress      = mustXAddress(nil, addresscodec.Mainnet)
	parityXAddressTag   = mustXAddress(ptrUint32(7), addresscodec.Mainnet)
	parityXAddressZero  = mustXAddress(ptrUint32(0), addresscodec.Testnet)
	parityTestXAddress  = mustXAddress(nil, addresscodec.Testnet)
	parityBadChecksum   = fxAccount[:len(fxAccount)-1] + "j"
	parityXAddressNoise = "X" + strings.Repeat("7", 46)
)

func mustEncodeAccount(id [20]byte) string {
	s, err := addresscodec.EncodeAccountIDToClassicAddress(id[:])
	if err != nil {
		panic(err)
	}
	return s
}

func mustXAddress(tag *uint32, network addresscodec.Network) string {
	_, id, err := addresscodec.DecodeClassicAddressToAccountID(fxIssuer)
	if err != nil {
		panic(err)
	}
	s, err := addresscodec.EncodeXAddress(id, tag, network)
	if err != nil {
		panic(err)
	}
	return s
}

func ptrUint32(v uint32) *uint32 { return &v }

var parityAddressStrings = []any{
	parityXAddress, parityXAddressTag, parityXAddressZero, parityTestXAddress, parityXAddressNoise,
}

func hexOfLen(n int, digit string) string { return strings.Repeat(digit, n) }

// parityValues returns valid, boundary, non-canonical and invalid values for
// one XRPL field type; current is the decoded fixture value and slotKind the
// kind of the struct field holding it.
func parityValues(xrplType, field string, current any, slotKind reflect.Kind) []any {
	values := []any{current}
	switch xrplType {
	case "UInt8":
		values = append(values, 0, 1, math.MaxUint8, math.MaxUint8+1, -1, math.MinInt)
	case "UInt16":
		values = append(values, 0, 1, math.MaxUint16, math.MaxUint16+1, -1)
	case "UInt32":
		values = append(values, uint32(0), uint32(1), uint32(math.MaxUint32))
	case "Int32":
		values = append(values, 0, -1, math.MinInt32, math.MaxInt32, math.MaxInt32+1, math.MinInt32-1)
	case "UInt64":
		if definitions.IsBaseTenUInt64FieldName(field) {
			values = append(values, "0", "1", "18446744073709551615", "18446744073709551616", "", "abc", "-1", "+1", "1e3", "ff", "0001", " 1")
		} else {
			values = append(values, "0", "1", "ffffffffffffffff", "FFFFFFFFFFFFFFFF", "10000000000000000", "", "xyz", "0x10", "+1", "-1", " 1", "0000000000000000001", "7fffffffffffffff")
		}
	case "Hash128", "Hash160", "Hash192", "Hash256", "Currency":
		size := map[string]int{"Hash128": 16, "Hash160": 20, "Hash192": 24, "Hash256": 32, "Currency": 20}[xrplType]
		values = append(values,
			"0", "", "00", hexOfLen(2*size, "0"), hexOfLen(2*size, "a"), hexOfLen(2*size, "F"),
			hexOfLen(2*size-1, "1"), hexOfLen(2*size+1, "1"), hexOfLen(2*size-2, "1"), hexOfLen(2*size+2, "1"),
			"G"+hexOfLen(2*size-1, "1"), hexOfLen(2*size-1, "1")+"g", hexOfLen(2*size-3, "1")+"Z", "USD", "XRP")
		if s, ok := current.(string); ok {
			values = append(values, strings.ToLower(s))
		}
	case "AccountID":
		values = append(values, fxAccount, fxIssuer, "", parityZeroAccount, "rInvalid", parityBadChecksum, "x"+fxAccount[1:], "1abc", "r")
	case "Blob":
		values = append(values, "", "0", "A", "ABC", "abcd", "DEADBEEF", "ZZ", "0G", "G", "AG",
			strings.Repeat("AB", 192), strings.Repeat("AB", 193), strings.Repeat("ab", 12480), strings.Repeat("AB", 12481))
	case "Amount":
		values = append(values, parityAmountValues()...)
	case "Number":
		values = append(values, "0", "1", "-1", "1e10", "123.456", "-0.0001", "9999999999999999999", "1e-200", "1e200",
			"", "abc", nil, 5, json.Number("12"), " 1", "1.0", "-0", "+7", "1E3")
	case "Issue":
		values = append(values,
			map[string]any{"currency": "XRP"},
			map[string]any{"currency": "USD", "issuer": fxIssuer},
			map[string]any{"currency": fxCurrEUR, "issuer": fxAccount},
			map[string]any{"mpt_issuance_id": fxHash192},
			map[string]any{"mpt_issuance_id": strings.ToLower(fxHash192)},
			map[string]any{"mpt_issuance_id": "00"},
			map[string]any{"currency": "XRP", "issuer": fxIssuer},
			map[string]any{"currency": "USD"},
			map[string]any{},
			map[string]any(nil),
			"XRP", nil, 5)
	case "XChainBridge":
		values = append(values, parityBridgeValues(current)...)
	case "Vector256":
		values = append(values, []string(nil), []string{}, []string{fxHash256}, []string{fxHash256, fxHashB},
			[]string{strings.ToLower(fxHashB)}, []string{"0"}, []string{"zz"}, []string{fxHash256[:62]},
			slices.Repeat([]string{fxHash256}, types.MaxJSONArrayElements),
			slices.Repeat([]string{fxHash256}, types.MaxJSONArrayElements+1))
	case "STArray":
		values = append(values, parityArrayValues(current)...)
	case "STObject":
		values = append(values, map[string]any(nil), map[string]any{})
		if object, ok := current.(map[string]any); ok {
			withDiscardable := make(map[string]any, len(object)+1)
			for k, v := range object {
				withDiscardable[k] = v
			}
			withDiscardable["Generic"] = 0
			values = append(values, withDiscardable)
		}
	}
	switch slotKind {
	case reflect.String, reflect.Interface:
		values = append(values, parityAddressStrings...)
	}
	return values
}

func parityAmountValues() []any {
	values := make([]any, 0, 100)
	values = append(values,
		"0", "1", "-1", "100000000000000000", "-100000000000000000", "100000000000000001", "99999999999999999",
		"+5", "-0", "+0", "00", "01", "-01", "1.5", "", "-", "abc", "99999999999999999999", "123456789012345678",
		"1e6", " 5", "5 ", "18446744073709551616", "١",
		nil, 5, 1.5, json.Number("5"), []any{},
		types.RawXRPAmount{Drops: 5}, types.RawXRPAmount{Drops: 7, Negative: true},
		types.RawXRPAmount{Drops: 1 << 61}, types.RawXRPAmount{Drops: 1<<61 - 1}, types.RawXRPAmount{Negative: true},
		map[string]any{"currency": "USD", "issuer": fxIssuer},
		map[string]any{"value": "1", "issuer": fxIssuer},
		map[string]any{"value": "1", "currency": "USD"},
		map[string]any{"value": 1.5, "currency": "USD", "issuer": fxIssuer},
		map[string]any{"value": json.Number("2.5"), "currency": "USD", "issuer": fxIssuer},
		map[string]any{"value": 7, "currency": "USD", "issuer": fxIssuer},
		map[string]any{"value": "1", "currency": 5, "issuer": fxIssuer},
		map[string]any{"value": "1", "currency": "USD", "issuer": 5},
		map[string]any{"value": "1", "mpt_issuance_id": 5},
		map[string]any(nil),
	)
	for _, v := range []string{
		"0", "1", "-1", "100", "1.5", "-1.5", "0.000001", "1e-81", "1e-96", "1e-97", "1e-100", "9999999999999999e80",
		"1e96", "1e97", "12345678901234567", "1.2345678901234565", "1.2345678901234575", "-0", "", "abc", "1e",
		"1000000000000000e-96", "9999999999999999e-96", "0.0", "+3", "1E2",
	} {
		values = append(values, map[string]any{"value": v, "currency": "USD", "issuer": fxIssuer})
	}
	for _, c := range []string{"XRP", fxCurrUSD, "0158415500000000C1F76FF6ECB0BAC600000000", "usd", "US", "", "0x" + fxCurrUSD, hexOfLen(40, "0"), "ABCD", "+++"} {
		values = append(values, map[string]any{"value": "10", "currency": c, "issuer": fxIssuer})
	}
	for _, i := range []string{fxAccount, "", parityXAddress, parityBadChecksum, parityZeroAccount} {
		values = append(values, map[string]any{"value": "10", "currency": "EUR", "issuer": i})
	}
	for _, v := range []string{"0", "1", "-1", "9223372036854775807", "9223372036854775808", "-9223372036854775807", "1.5", "", "01", "1e3"} {
		values = append(values, map[string]any{"value": v, "mpt_issuance_id": fxHash192})
	}
	for _, id := range []string{strings.ToLower(fxHash192), "00", "", "ZZ" + fxHash192[2:]} {
		values = append(values, map[string]any{"value": "5", "mpt_issuance_id": id})
	}
	values = append(values, map[string]any{"value": "5", "mpt_issuance_id": fxHash192, "currency": "USD"})
	return values
}

func parityBridgeValues(current any) []any {
	values := make([]any, 0, 10)
	values = append(values, nil, "bridge", map[string]any(nil), map[string]any{})
	bridge, ok := current.(map[string]any)
	if !ok {
		return values
	}
	with := func(key string, value any) map[string]any {
		out := make(map[string]any, len(bridge)+1)
		for k, v := range bridge {
			out[k] = v
		}
		if value == nil {
			delete(out, key)
		} else {
			out[key] = value
		}
		return out
	}
	return append(values,
		with("LockingChainDoor", parityXAddress),
		with("LockingChainDoor", ""),
		with("IssuingChainDoor", parityZeroAccount),
		with("IssuingChainIssue", nil),
		with("LockingChainIssue", map[string]any{"currency": "USD", "issuer": fxIssuer}),
		with("Extra", "x"),
	)
}

func parityArrayValues(current any) []any {
	values := []any{nil, []any(nil), []any{}, []any{"x"}, []any{map[string]any{}}}
	switch array := current.(type) {
	case []any:
		if len(array) > 0 {
			values = append(values,
				slices.Concat(array, array),
				slices.Repeat(array[:1], types.MaxJSONArrayElements+1))
		}
	case []NFTokenValue:
		var token, withURI, emptyURI NFTokenValue
		token.SetNFTokenID([32]byte{1})
		withURI.SetNFTokenID([32]byte{2})
		withURI.SetURI(bytes.Repeat([]byte{0xAB}, 193))
		emptyURI.SetNFTokenID([32]byte{3})
		emptyURI.SetURI([]byte{})
		values = append(values,
			[]NFTokenValue(nil), []NFTokenValue{}, []NFTokenValue{token}, []NFTokenValue{token, withURI, emptyURI},
			[]NFTokenValue{{}}, slices.Repeat([]NFTokenValue{token}, types.MaxJSONArrayElements+1))
	}
	return values
}

type parityStats struct {
	cases, encoded, reencoded int
	textDiffs                 []string
}

func (s *parityStats) check(t testing.TB, e Entry) {
	t.Helper()
	s.cases++
	result := requireEncodeParity(t, e)
	if result.textDiff != "" {
		s.textDiffs = append(s.textDiffs, result.textDiff)
	}
	if !result.encoded {
		return
	}
	s.encoded++
	data, err := e.(viaMapEncoder).encodeViaMap()
	if err != nil {
		t.Fatal(err)
	}
	if requireDecodedParity(t, e.Type(), data) {
		s.reencoded++
	}
}

// TestEncodeParityProperties drives every entry type through schema-derived
// field combinations: the fully populated fixture, each field set to each
// candidate value, each optional field cleared or set alone, every required
// field missing from a legacy blob, and seeded random mixtures.
func TestEncodeParityProperties(t *testing.T) {
	var total parityStats
	for _, spec := range schema.Specs {
		t.Run(spec.Name, func(t *testing.T) {
			var stats parityStats
			blob := fixtureBlob(t, spec.Name)
			fresh := func() Entry {
				e := NewByName(spec.Name)
				if err := e.Decode(blob); err != nil {
					t.Fatal(err)
				}
				return e
			}
			fields := parityFields(t, spec, fresh())

			stats.check(t, fresh())
			for _, f := range fields {
				for _, v := range f.values {
					e := fresh()
					setRawField(e, f.name, v)
					stats.check(t, e)
				}
				if f.clearable {
					e := fresh()
					clearField(e, f.name)
					stats.check(t, e)
				}
			}

			allCleared := fresh()
			for _, f := range fields {
				if f.clearable {
					clearField(allCleared, f.name)
				}
			}
			stats.check(t, allCleared)
			for _, keep := range fields {
				if !keep.clearable {
					continue
				}
				e := fresh()
				for _, f := range fields {
					if f.clearable && f.name != keep.name {
						clearField(e, f.name)
					}
				}
				stats.check(t, e)
			}

			for _, data := range sparseLegacyBlobs(t, spec) {
				e := NewByName(spec.Name)
				if err := DecodeLegacy(e, data); err != nil {
					t.Fatalf("decode sparse blob: %v", err)
				}
				stats.check(t, e)
			}

			rng := rand.New(rand.NewPCG(uint64(len(spec.Name)), 2192))
			for range 300 {
				e := fresh()
				for _, f := range fields {
					switch r := rng.IntN(4); {
					case r == 0 && f.clearable:
						clearField(e, f.name)
					case r == 1 && len(f.values) > 0:
						setRawField(e, f.name, f.values[rng.IntN(len(f.values))])
					}
				}
				stats.check(t, e)
			}

			t.Logf("%d cases, %d encoded, %d re-encoded after decode, %d message differences", stats.cases, stats.encoded, stats.reencoded, len(stats.textDiffs))
			for _, diff := range stats.textDiffs {
				t.Log(diff)
			}
			total.cases += stats.cases
			total.encoded += stats.encoded
			total.reencoded += stats.reencoded
			total.textDiffs = append(total.textDiffs, stats.textDiffs...)
		})
	}
	t.Logf("total: %d cases, %d encoded, %d re-encoded after decode, %d message differences", total.cases, total.encoded, total.reencoded, len(total.textDiffs))
}

// sparseLegacyBlobs returns, for one entry type, a blob holding only
// LedgerEntryType and, for each field, a blob holding just that field. Legacy
// decoding skips the template check, so required fields can be absent.
func sparseLegacyBlobs(t testing.TB, spec schema.Entry) [][]byte {
	t.Helper()
	encode := func(fields map[string]any) []byte {
		fields["LedgerEntryType"] = spec.Name
		data, err := binarycodec.EncodeBytes(fields)
		if err != nil {
			t.Fatalf("%s: encode sparse blob: %v", spec.Name, err)
		}
		return data
	}
	blobs := [][]byte{encode(map[string]any{})}
	fixture := coverageFixtures[spec.Name]
	for _, f := range spec.AllFields() {
		value, ok := fixture[f.Name]
		if f.DecodeOnly || !ok {
			continue
		}
		blobs = append(blobs, encode(map[string]any{f.Name: value}))
	}
	return blobs
}

func FuzzEncodeParity(f *testing.F) {
	specIndex := make(map[string]uint8, len(schema.Specs))
	for i, spec := range schema.Specs {
		specIndex[spec.Name] = uint8(i)
		f.Add(uint8(i), fixtureBlob(f, spec.Name))
	}
	data, err := os.ReadFile("testdata/sle-corpus.json")
	if err != nil {
		f.Fatal(err)
	}
	var manifest sleCorpusManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		f.Fatal(err)
	}
	for _, vector := range manifest.Vectors {
		raw, err := hex.DecodeString(vector.Hex)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(specIndex[vector.LedgerEntryType], raw)
	}
	f.Fuzz(func(t *testing.T, typeIndex uint8, data []byte) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		name := schema.Specs[int(typeIndex)%len(schema.Specs)].Name
		if e := NewByName(name); e.Decode(data) == nil {
			requireEncodeParity(t, e)
		}
		if e := NewByName(name); DecodeLegacy(e, data) == nil {
			requireEncodeParity(t, e)
		}
	})
}

// FuzzEncodeParityStringField assigns an arbitrary string to one string- or
// Amount-valued field of a fully populated entry, either directly or as one
// component of an issued or MPT amount, and compares the encoders.
func FuzzEncodeParityStringField(f *testing.F) {
	for i := range schema.Specs {
		f.Add(uint8(i), uint8(0), uint8(0), "0")
		f.Add(uint8(i), uint8(1), uint8(0), parityXAddressTag)
	}
	f.Add(uint8(0), uint8(1), uint8(1), "1.5e-3")
	f.Add(uint8(0), uint8(1), uint8(2), fxCurrUSD)
	f.Add(uint8(0), uint8(1), uint8(3), fxIssuer)
	f.Add(uint8(0), uint8(1), uint8(4), "-9223372036854775807")
	f.Add(uint8(0), uint8(1), uint8(5), fxHash192)

	blobs := make([][]byte, len(schema.Specs))
	targets := make([][]string, len(schema.Specs))
	for i, spec := range schema.Specs {
		blobs[i] = fixtureBlob(f, spec.Name)
		elem := reflect.ValueOf(NewByName(spec.Name)).Elem()
		for _, field := range spec.AllFields() {
			if field.DecodeOnly {
				continue
			}
			switch elem.FieldByName(field.Name).Kind() {
			case reflect.String, reflect.Interface:
				targets[i] = append(targets[i], field.Name)
			}
		}
	}
	f.Fuzz(func(t *testing.T, typeIndex, fieldIndex, mode uint8, value string) {
		i := int(typeIndex) % len(schema.Specs)
		if len(targets[i]) == 0 || len(value) > 4096 {
			t.Skip()
		}
		e := NewByName(schema.Specs[i].Name)
		if err := e.Decode(blobs[i]); err != nil {
			t.Fatal(err)
		}
		name := targets[i][int(fieldIndex)%len(targets[i])]
		var v any = value
		if reflect.ValueOf(e).Elem().FieldByName(name).Kind() == reflect.Interface {
			switch mode % 6 {
			case 1:
				v = map[string]any{"value": value, "currency": "USD", "issuer": fxIssuer}
			case 2:
				v = map[string]any{"value": "10", "currency": value, "issuer": fxIssuer}
			case 3:
				v = map[string]any{"value": "10", "currency": "USD", "issuer": value}
			case 4:
				v = map[string]any{"value": value, "mpt_issuance_id": fxHash192}
			case 5:
				v = map[string]any{"value": "5", "mpt_issuance_id": value}
			}
		}
		setRawField(e, name, v)
		requireEncodeParity(t, e)
	})
}

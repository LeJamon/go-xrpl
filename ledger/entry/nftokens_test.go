package entry

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
)

func nftCodecPage(t testing.TB, count, uriLength int) *NFTokenPage {
	t.Helper()
	page := &NFTokenPage{}
	page.SetFlags(0)
	page.SetPreviousTxnIDValue([32]byte{1})
	page.SetPreviousTxnLgrSeq(12)
	page.SetPreviousPageMinValue([32]byte{2})
	page.SetNextPageMinValue([32]byte{3})
	if err := page.SetSponsorValue([20]byte{4}); err != nil {
		t.Fatal(err)
	}
	tokens := make([]NFTokenValue, count)
	for i := range tokens {
		tokens[i].SetNFTokenID([32]byte{byte(i), byte(i >> 8)})
		if uriLength >= 0 {
			tokens[i].SetURI(bytes.Repeat([]byte{byte(i + 1)}, uriLength))
		}
	}
	if err := page.SetNFTokensValue(tokens); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNFTokenPageTypedCodec(t *testing.T) {
	for _, count := range []int{0, 1, 16, 32, 512} {
		for _, size := range []int{-1, 0, 64, 192, 193, 256} {
			t.Run(fmt.Sprintf("tokens=%d/uri=%d", count, size), func(t *testing.T) {
				page := nftCodecPage(t, count, size)
				want, err := binarycodec.EncodeBytes(page.ToMap())
				if err != nil {
					t.Fatal(err)
				}
				got, err := page.Encode()
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatal("typed encoding differs from generic codec")
				}
				var decoded NFTokenPage
				if err := decoded.Decode(want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(decoded.ToMap(), page.ToMap()) {
					t.Fatal("typed decoding changed values or field presence")
				}
				fields := make(map[string]any)
				decoded.EmitNewFields(fields)
				_, present := fields["NFTokens"]
				if present != (count != 0) {
					t.Fatal("NewFields did not omit the empty array")
				}
			})
		}
	}
}

func TestNFTokenPageTypedCodecBounds(t *testing.T) {
	for _, size := range []int{12480, 12481, 918744, 918745} {
		page := nftCodecPage(t, 1, size)
		want, genericErr := binarycodec.EncodeBytes(page.ToMap())
		got, typedErr := page.Encode()
		if (genericErr == nil) != (typedErr == nil) || !bytes.Equal(got, want) {
			t.Fatalf("URI length %d: typed error %v, generic error %v", size, typedErr, genericErr)
		}
		if typedErr == nil {
			var decoded NFTokenPage
			if err := decoded.Decode(got); err != nil || !reflect.DeepEqual(page.ToMap(), decoded.ToMap()) {
				t.Fatalf("URI length %d: decode %v", size, err)
			}
		}
	}
	page := nftCodecPage(t, 513, -1)
	_, err := page.Encode()
	if _, ok := binarycodec.AsArrayTooLargeError(err); !ok {
		t.Fatalf("missing array-size error: %v", err)
	}
	data, err := binarycodec.EncodeBytesTrusted(page.ToMap())
	if err != nil {
		t.Fatal(err)
	}
	var decoded NFTokenPage
	if err := decoded.Decode(data); err != nil {
		t.Fatalf("binary decoder applied JSON array cap: %v", err)
	}
	if err := page.SetNFTokensValue([]NFTokenValue{{}}); err == nil {
		t.Fatal("setter accepted missing NFTokenID")
	}
	page.SetNFTokens([]NFTokenValue{{}})
	if _, err := page.Encode(); err == nil {
		t.Fatal("encoder accepted missing NFTokenID")
	}
}

func TestNFTokenPageTypedPresenceAndOwnership(t *testing.T) {
	absent := nftCodecPage(t, 1, -1)
	empty := nftCodecPage(t, 1, 0)
	absentObject := absent.ToMap()["NFTokens"].([]any)[0].(map[string]any)["NFToken"].(map[string]any)
	if _, exists := absentObject["URI"]; exists {
		t.Fatal("absent URI appeared in JSON")
	}
	emptyObject := empty.ToMap()["NFTokens"].([]any)[0].(map[string]any)["NFToken"].(map[string]any)
	if uri, exists := emptyObject["URI"]; !exists || uri != "" {
		t.Fatal("explicit empty URI disappeared from JSON")
	}
	previous := make(map[string]any)
	empty.EmitPreviousFields(absent, previous)
	if !reflect.DeepEqual(previous["NFTokens"], absent.ToMap()["NFTokens"]) {
		t.Fatal("absent to empty URI did not emit previous value")
	}
	previous = make(map[string]any)
	absent.EmitPreviousFields(empty, previous)
	if !reflect.DeepEqual(previous["NFTokens"], empty.ToMap()["NFTokens"]) {
		t.Fatal("empty to absent URI did not emit previous value")
	}
	page := nftCodecPage(t, 1, 64)
	want := page.ToMap()
	values, err := page.GetNFTokens()
	if err != nil {
		t.Fatal(err)
	}
	values[0].URI[0]++
	if !reflect.DeepEqual(page.ToMap(), want) {
		t.Fatal("getter aliases page bytes")
	}
	if err := page.SetNFTokensValue(values); err != nil {
		t.Fatal(err)
	}
	want = page.ToMap()
	values[0].URI[0]++
	if !reflect.DeepEqual(page.ToMap(), want) {
		t.Fatal("setter aliases caller bytes")
	}
	data, err := page.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded NFTokenPage
	if err := decoded.Decode(data); err != nil {
		t.Fatal(err)
	}
	clear(data)
	if !reflect.DeepEqual(decoded.ToMap(), want) {
		t.Fatal("decoded URI aliases input buffer")
	}
	projection := decoded.ToMap()["NFTokens"].([]any)
	projection[0].(map[string]any)["NFToken"].(map[string]any)["URI"] = "FF"
	if !reflect.DeepEqual(decoded.ToMap(), want) {
		t.Fatal("JSON projection aliases page")
	}
}

func compareNFTokensDecoders(t testing.TB, data []byte) {
	t.Helper()
	genericReader := newStreamReader(data)
	want, genericErr := genericReader.readSTArray("NFTokens")
	typedReader := newStreamReader(data)
	got, typedErr := typedReader.readNFTokens()
	if (genericErr == nil) != (typedErr == nil) {
		t.Fatalf("acceptance differs for %X: typed=%v generic=%v", data, typedErr, genericErr)
	}
	if typedErr != nil {
		return
	}
	if typedReader.pos != genericReader.pos || !reflect.DeepEqual(nftokensToAny(got), want) {
		t.Fatalf("decode differs for %X: consumed typed=%d generic=%d", data, typedReader.pos, genericReader.pos)
	}
}

func TestNFTokensDecodeMalformed(t *testing.T) {
	page := nftCodecPage(t, 2, 64)
	valid, err := (&types.STArray{}).FromJSON(page.ToMap()["NFTokens"])
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= len(valid); i++ {
		compareNFTokensDecoders(t, valid[:i])
	}
	id := append([]byte{0x5A}, make([]byte, 32)...)
	cases := [][]byte{
		{0xE1}, {0xEC, 0xE1, 0xF1}, {0xEC, 0x75, 0x00, 0xE1, 0xF1},
		{0xEB, 0xE1, 0xF1}, {0xFA, 0xF1}, {0xEC, 0xFA, 0xF1},
		{0xEC, 0x75, 0xFF}, {0xE0, 0x0C}, {0x00, 0x0E, 0x0C},
		append(append([]byte{0xEC}, id...), 0xF1),
		append(append(append([]byte{0xEC}, id...), id...), 0xE1, 0xF1),
		append(append([]byte{0xEC, 0x75, 0x00}, id...), 0xE1, 0xF1),
		append(append([]byte{0xEC}, id...), 0x75, 0x00, 0x75, 0x00, 0xE1, 0xF1),
		append(append([]byte{0xEC}, id...), 0x22, 0, 0, 0, 0, 0xE1, 0xF1),
	}
	for _, data := range cases {
		compareNFTokensDecoders(t, data)
	}
}

func FuzzNFTokensCodec(f *testing.F) {
	for _, size := range []int{-1, 0, 64, 256} {
		page := nftCodecPage(f, 2, size)
		data, err := (&types.STArray{}).FromJSON(page.ToMap()["NFTokens"])
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		f.Add(data[:len(data)-1])
	}
	f.Add([]byte{})
	f.Add([]byte{0xF1})
	f.Add([]byte{0xEC, 0x75, 0x00, 0xE1, 0xF1})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		compareNFTokensDecoders(t, data)
	})
}

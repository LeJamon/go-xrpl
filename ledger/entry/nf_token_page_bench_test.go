package entry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// BenchmarkNFTokenPageDecodeAndGetTokens and BenchmarkNFTokenPageEncode use the canonical
// binarycodec as the fixture oracle. Keeping the fixture independent of the
// typed entry implementation makes the benchmark useful when the generated
// representation changes.
func BenchmarkNFTokenPageDecodeAndGetTokens(b *testing.B) {
	for _, tokens := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			name := fmt.Sprintf("tokens=%d/uri=%d", tokens, uriBytes)
			b.Run(name, func(b *testing.B) {
				blob := benchmarkNFTokenPageCanonical(b, tokens, uriBytes)
				b.SetBytes(int64(len(blob)))
				b.ReportAllocs()
				b.Logf("fixture sha256=%x bytes=%d", sha256.Sum256(blob), len(blob))
				b.ResetTimer()
				for range b.N {
					page := new(NFTokenPage)
					if err := page.Decode(blob); err != nil {
						b.Fatal(err)
					}
					if got, err := page.GetNFTokens(); err != nil || len(got) != tokens {
						b.Fatalf("decoded NFTokens = %d, %v; want %d", len(got), err, tokens)
					}
				}
			})
		}
	}
}

func BenchmarkNFTokenPageEncode(b *testing.B) {
	for _, tokens := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			name := fmt.Sprintf("tokens=%d/uri=%d", tokens, uriBytes)
			b.Run(name, func(b *testing.B) {
				want := benchmarkNFTokenPageCanonical(b, tokens, uriBytes)
				page := benchmarkNFTokenPageTyped(b, tokens, uriBytes)
				b.SetBytes(int64(len(want)))
				b.ReportAllocs()
				b.Logf("fixture sha256=%x bytes=%d", sha256.Sum256(want), len(want))
				b.ResetTimer()
				for range b.N {
					got, err := page.Encode()
					if err != nil {
						b.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						b.Fatalf("encoded NFTokenPage changed: got %x, want %x", got, want)
					}
				}
			})
		}
	}
}

func benchmarkNFTokenPageCanonical(b testing.TB, tokenCount, uriBytes int) []byte {
	b.Helper()
	objects := make([]any, tokenCount)
	for i := range tokenCount {
		id := benchmarkNFTokenID(i + 1)
		object := map[string]any{
			"NFToken": map[string]any{
				"NFTokenID": hex.EncodeToString(id[:]),
			},
		}
		if uriBytes > 0 {
			object["NFToken"].(map[string]any)["URI"] = hex.EncodeToString(benchmarkNFTokenURI(uriBytes))
		}
		objects[i] = object
	}
	previousTxnID := make([]byte, 32)
	for i := range previousTxnID {
		previousTxnID[i] = byte(i + 1)
	}
	fields := map[string]any{
		"LedgerEntryType":   "NFTokenPage",
		"NFTokens":          objects,
		"Flags":             uint32(0),
		"PreviousTxnID":     hex.EncodeToString(previousTxnID),
		"PreviousTxnLgrSeq": uint32(123),
	}
	blob, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		b.Fatalf("encode canonical NFTokenPage fixture: %v", err)
	}
	return blob
}

func benchmarkNFTokenPageTyped(b testing.TB, tokenCount, uriBytes int) *NFTokenPage {
	b.Helper()
	page := &NFTokenPage{}
	values := make([]NFTokenValue, tokenCount)
	for i := range values {
		values[i].SetNFTokenID(benchmarkNFTokenID(i + 1))
		if uriBytes > 0 {
			values[i].SetURI(benchmarkNFTokenURI(uriBytes))
		}
	}
	if err := page.SetNFTokensValue(values); err != nil {
		b.Fatalf("set typed NFTokens: %v", err)
	}
	page.SetFlagsValue(0)
	var previousTxnID [32]byte
	for i := range previousTxnID {
		previousTxnID[i] = byte(i + 1)
	}
	page.SetPreviousTxnIDValue(previousTxnID)
	page.SetPreviousTxnLgrSeqValue(123)
	return page
}

func benchmarkNFTokenID(n int) [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = byte(0xa0 + (i+n)%31)
	}
	id[28] = byte(n >> 24)
	id[29] = byte(n >> 16)
	id[30] = byte(n >> 8)
	id[31] = byte(n)
	return id
}

func benchmarkNFTokenURI(size int) []byte {
	const seed = "https://example.invalid/xrpl/nft/issue-2082/"
	uri := make([]byte, size)
	for i := range uri {
		uri[i] = seed[i%len(seed)]
	}
	return uri
}

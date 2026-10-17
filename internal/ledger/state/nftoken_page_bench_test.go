package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func BenchmarkParseNFTokenPage(b *testing.B) {
	for _, tokens := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			b.Run(fmt.Sprintf("tokens=%d/uri=%d", tokens, uriBytes), func(b *testing.B) {
				blob := benchmarkStateNFTokenPageCanonical(b, tokens, uriBytes)
				b.SetBytes(int64(len(blob)))
				b.ReportAllocs()
				b.Logf("fixture sha256=%x bytes=%d", sha256.Sum256(blob), len(blob))
				b.ResetTimer()
				for range b.N {
					page, err := ParseNFTokenPage(blob)
					if err != nil {
						b.Fatal(err)
					}
					if len(page.NFTokens) != tokens {
						b.Fatalf("parsed NFTokens = %d, want %d", len(page.NFTokens), tokens)
					}
				}
			})
		}
	}
}

func BenchmarkSerializeNFTokenPage(b *testing.B) {
	for _, tokens := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			b.Run(fmt.Sprintf("tokens=%d/uri=%d", tokens, uriBytes), func(b *testing.B) {
				want := benchmarkStateNFTokenPageCanonical(b, tokens, uriBytes)
				page := benchmarkStateNFTokenPageTyped(b, tokens, uriBytes)
				b.SetBytes(int64(len(want)))
				b.ReportAllocs()
				b.Logf("fixture sha256=%x bytes=%d", sha256.Sum256(want), len(want))
				b.ResetTimer()
				for range b.N {
					got, err := SerializeNFTokenPage(page)
					if err != nil {
						b.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						b.Fatalf("serialized NFTokenPage changed: got %x, want %x", got, want)
					}
				}
			})
		}
	}
}

func benchmarkStateNFTokenPageCanonical(b testing.TB, tokenCount, uriBytes int) []byte {
	b.Helper()
	objects := make([]any, tokenCount)
	for i := range tokenCount {
		id := benchmarkStateNFTokenID(i + 1)
		value := map[string]any{"NFTokenID": hex.EncodeToString(id[:])}
		if uriBytes > 0 {
			value["URI"] = hex.EncodeToString(benchmarkStateNFTokenURI(uriBytes))
		}
		objects[i] = map[string]any{"NFToken": value}
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

func benchmarkStateNFTokenPageTyped(b testing.TB, tokenCount, uriBytes int) *NFTokenPageData {
	b.Helper()
	page := &NFTokenPageData{
		NFTokens:          make([]NFTokenData, tokenCount),
		PreviousTxnLgrSeq: 123,
	}
	for i := range page.NFTokens {
		page.NFTokens[i] = NFTokenData{NFTokenID: benchmarkStateNFTokenID(i + 1)}
		if uriBytes > 0 {
			page.NFTokens[i].URI = hex.EncodeToString(benchmarkStateNFTokenURI(uriBytes))
		}
	}
	for i := range page.PreviousTxnID {
		page.PreviousTxnID[i] = byte(i + 1)
	}
	return page
}

func benchmarkStateNFTokenID(n int) [32]byte {
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

func benchmarkStateNFTokenURI(size int) []byte {
	const seed = "https://example.invalid/xrpl/nft/issue-2082/"
	uri := make([]byte, size)
	for i := range uri {
		uri[i] = seed[i%len(seed)]
	}
	return uri
}

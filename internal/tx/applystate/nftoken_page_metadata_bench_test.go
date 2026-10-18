package applystate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// BenchmarkBuildModifiedNode_NFTokenPage measures the apply-state metadata
// projection for the page shapes produced when NFT ownership is concentrated
// on one account. The source and destination blobs are prepared once so the
// timed path is limited to decoding the two entries and building the node.
func BenchmarkBuildModifiedNode_NFTokenPage(b *testing.B) {
	for _, tokenCount := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			b.Run(fmt.Sprintf("tokens=%d/uri=%d", tokenCount, uriBytes), func(b *testing.B) {
				original, current, key := benchmarkApplyNFTokenPagePair(b, tokenCount, uriBytes)
				want := benchmarkApplyNFTokenPageMetadata(b, key, original, current)
				b.SetBytes(int64(len(want)))
				b.ReportAllocs()
				b.Logf("metadata sha256=%x bytes=%d", sha256.Sum256(want), len(want))
				b.ResetTimer()
				table := &ApplyStateTable{}
				for range b.N {
					if _, err := table.buildModifiedNode(key, original, current); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkSerializeNFTokenPageMetadata includes the final binary metadata
// encoding used in the transaction tree. It also checks every iteration
// against a deterministic byte fixture, catching changes that preserve the
// in-memory AffectedNode shape but alter wire bytes.
func BenchmarkSerializeNFTokenPageMetadata(b *testing.B) {
	for _, tokenCount := range []int{1, 16, 32} {
		for _, uriBytes := range []int{0, 64, 256} {
			b.Run(fmt.Sprintf("tokens=%d/uri=%d", tokenCount, uriBytes), func(b *testing.B) {
				original, current, key := benchmarkApplyNFTokenPagePair(b, tokenCount, uriBytes)
				want := benchmarkApplyNFTokenPageMetadata(b, key, original, current)
				b.SetBytes(int64(len(want)))
				b.ReportAllocs()
				b.Logf("metadata sha256=%x bytes=%d", sha256.Sum256(want), len(want))
				b.ResetTimer()
				table := &ApplyStateTable{}
				for range b.N {
					node, err := table.buildModifiedNode(key, original, current)
					if err != nil {
						b.Fatal(err)
					}
					got, err := tx.SerializeMetadata(&tx.Metadata{
						AffectedNodes:     []tx.AffectedNode{node},
						TransactionResult: ter.TesSUCCESS,
					})
					if err != nil {
						b.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						b.Fatalf("metadata bytes changed: got %x, want %x", got, want)
					}
				}
			})
		}
	}
}

func benchmarkApplyNFTokenPagePair(b testing.TB, tokenCount, uriBytes int) (original, current []byte, key [32]byte) {
	b.Helper()
	originalPage := benchmarkApplyNFTokenPage(b, tokenCount, uriBytes, 123)
	currentPage := benchmarkApplyNFTokenPage(b, tokenCount, uriBytes, 124)
	currentPage.NFTokens[len(currentPage.NFTokens)-1].NFTokenID = benchmarkApplyNFTokenID(tokenCount + 1000)
	var err error
	original, err = state.SerializeNFTokenPage(originalPage)
	if err != nil {
		b.Fatalf("serialize original NFTokenPage: %v", err)
	}
	current, err = state.SerializeNFTokenPage(currentPage)
	if err != nil {
		b.Fatalf("serialize current NFTokenPage: %v", err)
	}
	for i := range key {
		key[i] = byte(0xD0 + i)
	}
	return original, current, key
}

func benchmarkApplyNFTokenPageMetadata(b testing.TB, key [32]byte, original, current []byte) []byte {
	b.Helper()
	table := &ApplyStateTable{}
	node, err := table.buildModifiedNode(key, original, current)
	if err != nil {
		b.Fatalf("build NFTokenPage metadata fixture: %v", err)
	}
	metadata, err := tx.SerializeMetadata(&tx.Metadata{
		AffectedNodes:     []tx.AffectedNode{node},
		TransactionResult: ter.TesSUCCESS,
	})
	if err != nil {
		b.Fatalf("serialize NFTokenPage metadata fixture: %v", err)
	}
	return metadata
}

func benchmarkApplyNFTokenPage(b testing.TB, tokenCount, uriBytes int, previousSequence uint32) *state.NFTokenPageData {
	b.Helper()
	page := &state.NFTokenPageData{
		NFTokens:          make([]state.NFTokenData, tokenCount),
		PreviousTxnLgrSeq: previousSequence,
	}
	for i := range page.PreviousTxnID {
		page.PreviousTxnID[i] = byte(i + 1)
	}
	for i := range page.NFTokens {
		page.NFTokens[i].NFTokenID = benchmarkApplyNFTokenID(i + 1)
		if uriBytes > 0 {
			page.NFTokens[i].URI = hex.EncodeToString(benchmarkApplyNFTokenURI(uriBytes))
		}
	}
	return page
}

func benchmarkApplyNFTokenID(n int) [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = byte(0x40 + (i+n)%47)
	}
	id[28] = byte(n >> 24)
	id[29] = byte(n >> 16)
	id[30] = byte(n >> 8)
	id[31] = byte(n)
	return id
}

func benchmarkApplyNFTokenURI(size int) []byte {
	const seed = "https://example.invalid/xrpl/nft/issue-2082/"
	uri := make([]byte, size)
	for i := range uri {
		uri[i] = seed[i%len(seed)]
	}
	return uri
}

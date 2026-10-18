package invariants

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
)

func BenchmarkValidNFTokenPage(b *testing.B) {
	scenarios := []string{"unchanged", "modified", "inserted", "deleted"}
	tokenCounts := []int{1, 16, 32}
	uriLengths := []int{0, 32, 256}

	for _, scenario := range scenarios {
		for _, tokenCount := range tokenCounts {
			for _, uriLength := range uriLengths {
				name := fmt.Sprintf("%s/tokens_%d/uri_%d", scenario, tokenCount, uriLength)
				b.Run(name, func(b *testing.B) {
					ids := make([][32]byte, tokenCount)
					for i := range ids {
						ids[i] = nftToken(byte(i + 1))
					}
					includeURI := uriLength > 0
					beforeURI := strings.Repeat("ab", uriLength)
					afterURI := strings.Repeat("cd", uriLength)
					pageKey := nftInvariantPageKey(0xaa, 0x40)
					beforeNext := nftInvariantPageKey(0xaa, 0x80)
					afterNext := nftInvariantPageKey(0xaa, 0x90)

					var entries []InvariantEntry
					switch scenario {
					case "unchanged":
						page := nftInvariantPageData(b, ids, beforeURI, includeURI, nil, nil)
						entries = []InvariantEntry{nftInvariantEntry(pageKey, page, page, false)}
					case "modified":
						before := nftInvariantPageData(b, ids, beforeURI, includeURI, nil, &beforeNext)
						after := nftInvariantPageData(b, ids, afterURI, includeURI, nil, &afterNext)
						entries = []InvariantEntry{nftInvariantEntry(pageKey, before, after, false)}
					case "inserted":
						after := nftInvariantPageData(b, ids, beforeURI, includeURI, nil, nil)
						entries = []InvariantEntry{nftInvariantEntry(pageKey, nil, after, false)}
					case "deleted":
						before := nftInvariantPageData(b, ids, beforeURI, includeURI, nil, nil)
						entries = []InvariantEntry{nftInvariantEntry(pageKey, before, nil, true)}
					}

					rules := amendment.EmptyRules()
					view := stubView{}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if violation := checkValidNFTokenPage(entries, view, rules); violation != nil {
							b.Fatal(violation)
						}
					}
				})
			}
		}
	}
}

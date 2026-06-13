package subscription

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
)

// TestBookKeyFoldsCurrencySpellings pins M7: a market subscribed with a 3-char
// ISO currency and the same market spelled as its 40-hex form share one
// bookKey, so they dedup on subscribe and match on unsubscribe — mirroring
// rippled's parsed Book{in,out} identity (Book.h:79-84) rather than a raw-byte
// comparison. A genuinely different currency must keep a distinct key.
func TestBookKeyFoldsCurrencySpellings(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	// "USD" (0x55 0x53 0x44) packed at bytes 12-14 of the 160-bit currency.
	const hexUSD = "0000000000000000000000005553440000000000"

	iso := types.BookRequest{
		TakerPays: []byte(`{"currency":"USD","issuer":"` + issuer + `"}`),
		TakerGets: []byte(`{"currency":"XRP"}`),
	}
	hex := types.BookRequest{
		TakerPays: []byte(`{"currency":"` + hexUSD + `","issuer":"` + issuer + `"}`),
		TakerGets: []byte(`{"currency":"XRP"}`),
	}
	eur := types.BookRequest{
		TakerPays: []byte(`{"currency":"EUR","issuer":"` + issuer + `"}`),
		TakerGets: []byte(`{"currency":"XRP"}`),
	}

	assert.Equal(t, bookKey(iso), bookKey(hex),
		"USD and its 40-hex encoding must collapse to one bookKey")
	assert.NotEqual(t, bookKey(iso), bookKey(eur),
		"distinct currencies must keep distinct bookKeys")

	// The fold must carry through dedup and per-book removal.
	merged := mergeBooks([]types.BookRequest{iso}, []types.BookRequest{hex})
	assert.Len(t, merged, 1, "re-subscribing the same market spelled differently must not duplicate")

	remaining := removeBooks([]types.BookRequest{iso}, []types.BookRequest{hex})
	assert.Empty(t, remaining, "unsubscribing the same market spelled differently must remove it")
}

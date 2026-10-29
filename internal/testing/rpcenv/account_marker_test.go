package rpcenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
)

// Based on rippled AccountLines_test.cpp and PayChan_test.cpp
// (XRPLF/rippled#8561, #8573): a marker's hint may carry a leading '+', and
// anything after a second comma is ignored, so both spellings resume on the
// same page.
func TestAccountMarkerSignedHintResumesSamePage(t *testing.T) {
	env := New(t)
	alice := jtx.NewAccount("alice")
	gateways := []*jtx.Account{jtx.NewAccount("gw1"), jtx.NewAccount("gw2"), jtx.NewAccount("gw3")}
	env.Fund(append([]*jtx.Account{alice}, gateways...)...)
	env.Close()
	for _, gw := range gateways {
		env.Trust(alice, jtx.USD(gw, 1000))
	}
	for i, gw := range gateways {
		jtx.RequireTxSuccess(t, env.Submit(offer.OfferCreateXRP(alice, uint64(jtx.XRP(int64(10+i))), jtx.USD(gw, 1), false).Build()))
	}
	env.Close()

	for _, tc := range []struct {
		method string
		list   string
	}{
		{"account_lines", "lines"},
		{"account_offers", "offers"},
	} {
		t.Run(tc.method, func(t *testing.T) {
			first, rpcErr := env.RPC(tc.method, map[string]any{"account": alice.Address, "limit": 1, "ledger_index": "validated"})
			require.Nil(t, rpcErr)
			marker, ok := didJSONMap(t, first)["marker"].(string)
			require.True(t, ok, "first page has a marker")
			key, hint, found := strings.Cut(marker, ",")
			require.True(t, found)

			page := func(marker string) []any {
				t.Helper()
				result, rpcErr := env.RPC(tc.method, map[string]any{"account": alice.Address, "marker": marker, "ledger_index": "validated"})
				require.Nil(t, rpcErr, marker)
				return didJSONMap(t, result)[tc.list].([]any)
			}
			// The owner directory mixes lines and offers, so the first page's
			// single entry may be either kind; only equality is pinned.
			plain := page(marker)
			require.NotEmpty(t, plain)
			require.Equal(t, plain, page(key+",+"+hint+",ignored"))
		})
	}
}

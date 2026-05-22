package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetCurrencyBytes_XRP_AllZero pins the canonical XRP-encoding contract.
// AMMCreate (internal/tx/amm/keylet.go) and every AMM-side lookup path
// (internal/rpc/handlers/amm_info.go, ledger_entry.go, internal/testing/amm/
// helpers.go) all route through GetCurrencyBytes; if XRP ever stopped
// round-tripping to the all-zero currency here, every XRP-paired AMM lookup
// by asset would silently mis-key and surface as actNotFound.
func TestGetCurrencyBytes_XRP_AllZero(t *testing.T) {
	t.Parallel()

	assert.Equal(t, [20]byte{}, GetCurrencyBytes("XRP"),
		"XRP must encode to the all-zero currency to match AMMCreate's keylet")
	assert.Equal(t, [20]byte{}, GetCurrencyBytes(""),
		"empty currency must also encode to all-zero (defensive)")

	var usd [20]byte
	usd[12], usd[13], usd[14] = 'U', 'S', 'D'
	assert.Equal(t, usd, GetCurrencyBytes("USD"),
		"3-letter ISO codes encode as ASCII at bytes 12-14")
}

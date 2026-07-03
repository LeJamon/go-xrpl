package offer

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestTickSize1224 reproduces ledger 99478516 idx46: a tfSell OfferCreate
// TakerGets 900000 drops XRP, TakerPays 5000000 HADA (issuer TickSize 15).
// Mainnet places TakerPays = 5000000.000000004; goXRPL currently places 5000000.
func TestTickSize1224(t *testing.T) {
	const hada = "4841444100000000000000000000000000000000"
	const issuer = "rsR5JSisuXsbipP6sGdKdz5agjxn8BhHUC"

	takerGets := tx.NewXRPAmount(900000)                                // XRP drops
	takerPays := tx.NewIssuedAmount(5000000000000000, -9, hada, issuer) // 5000000

	quality := state.CalculateQuality(takerGets, takerPays)
	t.Logf("quality  mantissa=%d exp=%d", quality&0x00ffffffffffffff, int(quality>>56)-100)

	rounded := roundToTickSize(quality, 15)
	t.Logf("rounded  mantissa=%d exp=%d", rounded&0x00ffffffffffffff, int(rounded>>56)-100)

	res := multiplyByQuality(takerGets, rounded, hada, issuer)
	t.Logf("result   mantissa=%d exp=%d", res.Mantissa(), res.Exponent())

	if res.Mantissa() != 5000000000000004 || res.Exponent() != -9 {
		t.Errorf("TakerPays got mantissa=%d exp=%d, want 5000000000000004 exp=-9 (5000000.000000004)",
			res.Mantissa(), res.Exponent())
	}
}

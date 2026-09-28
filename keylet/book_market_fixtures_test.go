package keylet

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/testing/marketfixtures"
)

func TestMPTMarketBookFixtures(t *testing.T) {
	side := func(asset marketfixtures.Asset) BookSide {
		if asset.MPTID != ([24]byte{}) {
			return MPTSide(asset.MPTID)
		}
		return IssueSide(asset.Currency, asset.Issuer)
	}
	for _, fixture := range marketfixtures.Books() {
		t.Run(fixture.Name, func(t *testing.T) {
			got := BookBase(side(fixture.Pays), side(fixture.Gets), fixture.Domain).Key
			if hex.EncodeToString(got[:]) != fixture.Base {
				t.Fatalf("book base = %x, want %s", got, fixture.Base)
			}
		})
	}
}

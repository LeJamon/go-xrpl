package oracle

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// pairKey returns the deduplication key for a base/quote token pair.
func pairKey(base, quote string) string {
	return base + "/" + quote
}

// currencyOrderKey returns a sort key that orders a base/quote token pair by its
// canonical 20-byte Currency codes (base first, then quote), matching rippled's
// std::map<std::pair<Currency, Currency>>. XRP is the all-zero currency, so
// XRP-based pairs sort before any token pair; sorting the asset strings directly
// would instead place XRP ("XRP" = 0x585250…) after the tokens.
func currencyOrderKey(base, quote string) string {
	b := currencyCode(base)
	q := currencyCode(quote)
	return string(b[:]) + string(q[:])
}

// currencyCode converts an oracle asset string — as produced by the binary codec
// and the SLE parser ("XRP", a 3-letter ISO code, or a 40-char hex code) — into
// its canonical 20-byte Currency. XRP is the all-zero code; a 3-letter code fills
// bytes 12-14; a 40-char hex code is decoded verbatim.
func currencyCode(asset string) [20]byte {
	var c [20]byte
	switch {
	case asset == "XRP":
		// all-zero code
	case len(asset) == 40:
		if b, err := hex.DecodeString(asset); err == nil {
			copy(c[:], b)
		}
	case len(asset) == 3:
		copy(c[12:], asset)
	}
	return c
}

// validateHexFieldLen checks that a hex-encoded field value is non-empty and
// decodes to at most maxBytes bytes. Odd-length hex is rounded up to the next
// whole byte. Returns a temMALFORMED error on violation.
func validateHexFieldLen(name, value string, maxBytes int) error {
	if len(value) == 0 {
		return ter.Errorf(ter.TemMALFORMED, "%s cannot be empty", name)
	}
	byteLen := (len(value) + 1) / 2
	if byteLen > maxBytes {
		return ter.Errorf(ter.TemMALFORMED, "%s length must be between 1 and %d bytes", name, maxBytes)
	}
	return nil
}

// TokenPairKey returns a unique key for this token pair (for deduplication)
func (p *PriceDataEntry) TokenPairKey() string {
	return pairKey(p.BaseAsset, p.QuoteAsset)
}

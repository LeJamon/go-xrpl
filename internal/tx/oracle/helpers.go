package oracle

import "github.com/LeJamon/go-xrpl/internal/tx"

// pairKey returns the deduplication key for a base/quote token pair.
func pairKey(base, quote string) string {
	return base + "/" + quote
}

// validateHexFieldLen checks that a hex-encoded field value is non-empty and
// decodes to at most maxBytes bytes. Odd-length hex is rounded up to the next
// whole byte. Returns a temMALFORMED error on violation.
func validateHexFieldLen(name, value string, maxBytes int) error {
	if len(value) == 0 {
		return tx.Errorf(tx.TemMALFORMED, "%s cannot be empty", name)
	}
	byteLen := (len(value) + 1) / 2
	if byteLen > maxBytes {
		return tx.Errorf(tx.TemMALFORMED, "%s length must be between 1 and %d bytes", name, maxBytes)
	}
	return nil
}

// TokenPairKey returns a unique key for this token pair (for deduplication)
func (p *PriceDataEntry) TokenPairKey() string {
	return pairKey(p.BaseAsset, p.QuoteAsset)
}

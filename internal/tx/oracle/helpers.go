package oracle

// TokenPairKey returns a unique key for this token pair (for deduplication)
func (p *PriceDataEntry) TokenPairKey() string {
	return p.BaseAsset + "/" + p.QuoteAsset
}

// IsDeleteRequest returns true if this entry represents a delete request
// (AssetPrice is not present)
func (p *PriceDataEntry) IsDeleteRequest() bool {
	return p.AssetPrice == nil
}

// CalculateOwnerCountAdjustment calculates the owner count adjustment based on
// the number of price data entries. Oracle entries with >5 pairs count as 2,
// otherwise count as 1.
func CalculateOwnerCountAdjustment(priceDataCount int) int {
	if priceDataCount > 5 {
		return 2
	}
	return 1
}

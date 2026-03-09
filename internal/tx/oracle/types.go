package oracle

// PriceData represents a price data point wrapper
type PriceData struct {
	PriceData PriceDataEntry `json:"PriceData"`
}

// PriceDataEntry contains the price data fields
type PriceDataEntry struct {
	BaseAsset  string  `json:"BaseAsset"`
	QuoteAsset string  `json:"QuoteAsset"`
	AssetPrice *uint64 `json:"AssetPrice,omitempty"` // Omitted = delete this pair
	Scale      *uint8  `json:"Scale,omitempty"`
}

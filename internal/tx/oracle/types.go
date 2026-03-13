package oracle

import (
	"encoding/json"
	"strconv"
	"strings"
)

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

// UnmarshalJSON handles AssetPrice as either a number or a hex string
// (the binary codec outputs UInt64 fields as 16-char hex strings).
func (p *PriceDataEntry) UnmarshalJSON(data []byte) error {
	type Alias PriceDataEntry
	aux := &struct {
		AssetPrice *json.RawMessage `json:"AssetPrice,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if aux.AssetPrice != nil {
		raw := *aux.AssetPrice
		// Try as number first
		var n uint64
		if err := json.Unmarshal(raw, &n); err == nil {
			p.AssetPrice = &n
			return nil
		}
		// Try as hex string (binary codec format)
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			n, err := strconv.ParseUint(strings.TrimLeft(s, "0"), 16, 64)
			if err != nil && s != "" {
				// Try full string
				n, err = strconv.ParseUint(s, 16, 64)
				if err != nil {
					return err
				}
			}
			p.AssetPrice = &n
		}
	}

	return nil
}

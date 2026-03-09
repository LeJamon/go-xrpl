package entry

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// PriceData represents a single price data point in an oracle
type PriceData struct {
	BaseAsset       string // Base asset symbol (e.g., "XRP")
	QuoteAsset      string // Quote asset symbol (e.g., "USD")
	AssetPrice      uint64 // Price scaled by 10^Scale
	Scale           uint8  // Decimal scale for the price
}

// Oracle represents a price oracle ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltORACLE
type Oracle struct {
	BaseEntry

	// Required fields
	Owner           [20]byte    // Account that owns this oracle
	Provider        []byte      // Provider identifier (up to 256 bytes)
	PriceDataSeries []PriceData // Array of price data points
	AssetClass      []byte      // Asset class identifier
	LastUpdateTime  uint32      // Unix timestamp of last update
	OwnerNode       uint64      // Directory node hint

	// Optional fields
	URI *string // URI for additional oracle information
}

func (o *Oracle) Type() entry.Type {
	return entry.TypeOracle
}

func (o *Oracle) Validate() error {
	if o.Owner == [20]byte{} {
		return errors.New("owner is required")
	}
	if len(o.Provider) == 0 {
		return errors.New("provider is required")
	}
	if len(o.Provider) > 256 {
		return errors.New("provider cannot exceed 256 bytes")
	}
	if len(o.PriceDataSeries) == 0 {
		return errors.New("price data series is required")
	}
	if len(o.PriceDataSeries) > 10 {
		return errors.New("price data series cannot exceed 10 entries")
	}
	if len(o.AssetClass) == 0 {
		return errors.New("asset class is required")
	}
	return nil
}

func (o *Oracle) Hash() ([32]byte, error) {
	hash := o.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= o.Owner[i]
	}
	return hash, nil
}

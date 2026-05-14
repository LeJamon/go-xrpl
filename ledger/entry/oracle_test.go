package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOracle_Type verifies Oracle returns correct type
func TestOracle_Type(t *testing.T) {
	oracle := &Oracle{}
	assert.Equal(t, TypeOracle, oracle.Type())
	assert.Equal(t, "Oracle", oracle.Type().String())
}

// TestOracle_Validate tests Oracle validation logic
// Reference: rippled/src/test/app/Oracle_test.cpp
func TestOracle_Validate(t *testing.T) {
	validOwner := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validProvider := []byte("chainlink")
	validAssetClass := []byte("currency")
	validPriceData := []PriceData{
		{BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 74000, Scale: 2},
	}

	t.Run("Valid Oracle with minimum fields", func(t *testing.T) {
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        validProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: validPriceData,
			LastUpdateTime:  1000000,
		}
		err := oracle.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Oracle with URI", func(t *testing.T) {
		uri := "https://oracle.example.com"
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        validProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: validPriceData,
			LastUpdateTime:  1000000,
			URI:             &uri,
		}
		err := oracle.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Oracle with multiple price data points", func(t *testing.T) {
		// Reference: Oracle_test.cpp - up to 10 price data points allowed
		oracle := &Oracle{
			Owner:      validOwner,
			Provider:   validProvider,
			AssetClass: validAssetClass,
			PriceDataSeries: []PriceData{
				{BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 74000, Scale: 2},
				{BaseAsset: "XRP", QuoteAsset: "EUR", AssetPrice: 68000, Scale: 2},
				{BaseAsset: "XRP", QuoteAsset: "GBP", AssetPrice: 58000, Scale: 2},
			},
			LastUpdateTime: 1000000,
		}
		err := oracle.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid with empty owner", func(t *testing.T) {
		oracle := &Oracle{
			Owner:           [20]byte{},
			Provider:        validProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: validPriceData,
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "owner")
	})

	t.Run("Invalid with empty provider", func(t *testing.T) {
		// Reference: Oracle_test.cpp testInvalidSet - "provider not included"
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        []byte{},
			AssetClass:      validAssetClass,
			PriceDataSeries: validPriceData,
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "provider")
	})

	t.Run("Invalid with provider too long", func(t *testing.T) {
		// Provider cannot exceed 256 bytes
		longProvider := make([]byte, 257)
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        longProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: validPriceData,
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "provider cannot exceed 256")
	})

	t.Run("Invalid with empty price data series", func(t *testing.T) {
		// Reference: Oracle_test.cpp testInvalidSet - temARRAY_EMPTY
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        validProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: []PriceData{},
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "price data series is required")
	})

	t.Run("Invalid with too many price data points", func(t *testing.T) {
		// Reference: Oracle_test.cpp testInvalidSet - temARRAY_TOO_LARGE (>10)
		priceData := make([]PriceData, 11)
		for i := range priceData {
			priceData[i] = PriceData{BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 74000}
		}
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        validProvider,
			AssetClass:      validAssetClass,
			PriceDataSeries: priceData,
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot exceed 10")
	})

	t.Run("Invalid with empty asset class", func(t *testing.T) {
		// Reference: Oracle_test.cpp testInvalidSet - "asset class not included"
		oracle := &Oracle{
			Owner:           validOwner,
			Provider:        validProvider,
			AssetClass:      []byte{},
			PriceDataSeries: validPriceData,
		}
		err := oracle.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "asset class")
	})
}

// TestPriceData tests the PriceData struct
func TestPriceData(t *testing.T) {
	pd := PriceData{
		BaseAsset:  "XRP",
		QuoteAsset: "USD",
		AssetPrice: 74000,
		Scale:      2,
	}

	assert.Equal(t, "XRP", pd.BaseAsset)
	assert.Equal(t, "USD", pd.QuoteAsset)
	assert.Equal(t, uint64(74000), pd.AssetPrice)
	assert.Equal(t, uint8(2), pd.Scale)
}

package oracle

// Oracle constants matching rippled Protocol.h
const (
	// MaxOracleURI is the maximum length of the URI field (in bytes)
	MaxOracleURI = 256

	// MaxOracleProvider is the maximum length of the Provider field (in bytes)
	MaxOracleProvider = 256

	// MaxOracleDataSeries is the maximum number of price data entries
	MaxOracleDataSeries = 10

	// MaxOracleSymbolClass is the maximum length of the AssetClass field (in bytes)
	MaxOracleSymbolClass = 16

	// MaxLastUpdateTimeDelta is the maximum allowed delta between LastUpdateTime
	// and the ledger close time (in seconds)
	MaxLastUpdateTimeDelta = 300

	// MaxPriceScale is the maximum allowed scale value for price data
	// Reference: rippled Oracle_test.cpp line 354 tests maxPriceScale + 1 = 9 fails
	MaxPriceScale = 8
)

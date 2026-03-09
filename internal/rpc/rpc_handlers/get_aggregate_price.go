package rpc_handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binary-codec"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// GetAggregatePriceMethod handles the get_aggregate_price RPC method
type GetAggregatePriceMethod struct{}

// PriceDataPoint represents a single price data point with its update time
type PriceDataPoint struct {
	Price          float64
	LastUpdateTime uint32
}

func (m *GetAggregatePriceMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		rpc_types.LedgerSpecifier
		Oracles       []map[string]interface{} `json:"oracles"`
		BaseAsset     string                   `json:"base_asset"`
		QuoteAsset    string                   `json:"quote_asset"`
		Trim          uint32                   `json:"trim,omitempty"`
		TimeThreshold uint32                   `json:"time_threshold,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Validate required parameters
	if len(request.Oracles) == 0 {
		return nil, rpc_types.RpcErrorInvalidParams("Missing required parameter: oracles")
	}
	if request.BaseAsset == "" {
		return nil, rpc_types.RpcErrorInvalidParams("Missing required parameter: base_asset")
	}
	if request.QuoteAsset == "" {
		return nil, rpc_types.RpcErrorInvalidParams("Missing required parameter: quote_asset")
	}

	// Validate constraints
	const maxOracles = 200
	const maxTrim = 25

	if len(request.Oracles) > maxOracles {
		return nil, rpc_types.RpcErrorInvalidParams("oracles array exceeds maximum size of 200")
	}
	if request.Trim > maxTrim {
		return nil, rpc_types.RpcErrorInvalidParams("trim must be between 1 and 25")
	}

	if rpc_types.Services == nil || rpc_types.Services.Ledger == nil {
		return nil, rpc_types.RpcErrorInternal("Ledger service not available")
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Collect prices from all oracles
	var prices []PriceDataPoint

	for _, oracleSpec := range request.Oracles {
		accountStr, ok := oracleSpec["account"].(string)
		if !ok {
			return nil, rpc_types.RpcErrorInvalidParams("Each oracle must have an account field")
		}

		documentIDRaw, ok := oracleSpec["oracle_document_id"]
		if !ok {
			return nil, rpc_types.RpcErrorInvalidParams("Each oracle must have an oracle_document_id field")
		}

		var documentID uint32
		switch v := documentIDRaw.(type) {
		case float64:
			documentID = uint32(v)
		case int:
			documentID = uint32(v)
		case uint32:
			documentID = v
		default:
			return nil, rpc_types.RpcErrorInvalidParams("oracle_document_id must be a number")
		}

		// Decode account address
		_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(accountStr)
		if err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid account in oracle: " + err.Error())
		}
		var accountID [20]byte
		copy(accountID[:], accountBytes)

		// Get oracle keylet
		oracleKeylet := keylet.Oracle(accountID, documentID)

		// Get the Oracle entry
		oracleEntry, lookupErr := rpc_types.Services.Ledger.GetLedgerEntry(oracleKeylet.Key, ledgerIndex)
		if lookupErr != nil {
			// Skip oracles that don't exist
			continue
		}

		// Decode the Oracle entry
		oracleDecoded, decodeErr := binarycodec.Decode(hex.EncodeToString(oracleEntry.Node))
		if decodeErr != nil {
			continue
		}

		// Get the last update time
		var lastUpdateTime uint32
		if lut, ok := oracleDecoded["LastUpdateTime"]; ok {
			switch v := lut.(type) {
			case float64:
				lastUpdateTime = uint32(v)
			case int:
				lastUpdateTime = uint32(v)
			case uint32:
				lastUpdateTime = v
			}
		}

		// Find matching price data
		priceDataSeries, ok := oracleDecoded["PriceDataSeries"].([]interface{})
		if !ok {
			continue
		}

		for _, pd := range priceDataSeries {
			priceData, ok := pd.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this matches the requested asset pair
			baseAsset, _ := priceData["BaseAsset"].(string)
			quoteAsset, _ := priceData["QuoteAsset"].(string)

			if baseAsset != request.BaseAsset || quoteAsset != request.QuoteAsset {
				continue
			}

			// Get the asset price
			assetPrice, ok := priceData["AssetPrice"]
			if !ok {
				continue
			}

			var priceValue float64
			switch v := assetPrice.(type) {
			case float64:
				priceValue = v
			case int:
				priceValue = float64(v)
			case uint64:
				priceValue = float64(v)
			case string:
				parsed, parseErr := strconv.ParseUint(v, 10, 64)
				if parseErr != nil {
					continue
				}
				priceValue = float64(parsed)
			default:
				continue
			}

			// Apply scale if present
			if scale, ok := priceData["Scale"]; ok {
				var scaleValue int
				switch v := scale.(type) {
				case float64:
					scaleValue = int(v)
				case int:
					scaleValue = v
				case uint8:
					scaleValue = int(v)
				}
				priceValue = priceValue / math.Pow(10, float64(scaleValue))
			}

			prices = append(prices, PriceDataPoint{
				Price:          priceValue,
				LastUpdateTime: lastUpdateTime,
			})
		}
	}

	if len(prices) == 0 {
		return nil, &rpc_types.RpcError{
			Code:    21,
			Message: "No matching price data found",
		}
	}

	// Find the latest update time
	var latestTime uint32
	for _, p := range prices {
		if p.LastUpdateTime > latestTime {
			latestTime = p.LastUpdateTime
		}
	}

	// Filter by time threshold if specified
	if request.TimeThreshold > 0 {
		var filtered []PriceDataPoint
		threshold := latestTime - request.TimeThreshold
		for _, p := range prices {
			if p.LastUpdateTime >= threshold {
				filtered = append(filtered, p)
			}
		}
		if len(filtered) == 0 {
			return nil, &rpc_types.RpcError{
				Code:    21,
				Message: "No price data within time threshold",
			}
		}
		prices = filtered
	}

	// Sort prices by value for statistics
	sort.Slice(prices, func(i, j int) bool {
		return prices[i].Price < prices[j].Price
	})

	// Calculate statistics for entire set
	entireMean, entireSD := calculateStats(prices)
	entireSize := len(prices)

	// Calculate median
	median := calculateMedian(prices)

	// Build response
	response := map[string]interface{}{
		"time": latestTime,
		"entire_set": map[string]interface{}{
			"mean":               fmt.Sprintf("%g", entireMean),
			"size":               entireSize,
			"standard_deviation": fmt.Sprintf("%g", entireSD),
		},
		"median": fmt.Sprintf("%g", median),
	}

	// Calculate trimmed set if requested
	if request.Trim > 0 && len(prices) > 2 {
		trimCount := len(prices) * int(request.Trim) / 100
		if trimCount > 0 && len(prices) > 2*trimCount {
			trimmedPrices := prices[trimCount : len(prices)-trimCount]
			trimmedMean, trimmedSD := calculateStats(trimmedPrices)
			response["trimmed_set"] = map[string]interface{}{
				"mean":               fmt.Sprintf("%g", trimmedMean),
				"size":               len(trimmedPrices),
				"standard_deviation": fmt.Sprintf("%g", trimmedSD),
			}
		}
	}

	return response, nil
}

// calculateStats calculates mean and standard deviation for a set of prices
func calculateStats(prices []PriceDataPoint) (mean, sd float64) {
	if len(prices) == 0 {
		return 0, 0
	}

	// Calculate mean
	var sum float64
	for _, p := range prices {
		sum += p.Price
	}
	mean = sum / float64(len(prices))

	// Calculate standard deviation
	if len(prices) > 1 {
		var variance float64
		for _, p := range prices {
			diff := p.Price - mean
			variance += diff * diff
		}
		variance /= float64(len(prices) - 1)
		sd = math.Sqrt(variance)
	}

	return mean, sd
}

// calculateMedian calculates the median of sorted prices
func calculateMedian(prices []PriceDataPoint) float64 {
	n := len(prices)
	if n == 0 {
		return 0
	}

	if n%2 == 0 {
		return (prices[n/2-1].Price + prices[n/2].Price) / 2
	}
	return prices[n/2].Price
}

func (m *GetAggregatePriceMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *GetAggregatePriceMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}

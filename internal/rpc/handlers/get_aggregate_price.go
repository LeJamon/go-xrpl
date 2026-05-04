package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/keylet"
)

// GetAggregatePriceMethod handles the get_aggregate_price RPC method
type GetAggregatePriceMethod struct{}

// PriceDataPoint represents a single price data point with its update time
type PriceDataPoint struct {
	Price          float64
	LastUpdateTime uint32
}

func (m *GetAggregatePriceMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse into raw map first for field-presence detection (matching rippled's
	// isMember() checks which distinguish "absent" from "present but invalid").
	var raw map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	// rippled: !params.isMember(jss::oracles) -> missing_field_error
	oraclesRaw, hasOracles := raw["oracles"]
	if !hasOracles {
		return nil, types.RpcErrorMissingField("oracles")
	}
	// rippled: !isArray() || size()==0 || size()>200 -> rpcORACLE_MALFORMED
	var oracles []json.RawMessage
	if err := json.Unmarshal(oraclesRaw, &oracles); err != nil {
		// Not an array
		return nil, types.RpcErrorOracleMalformed()
	}
	const maxOracles = 200
	if len(oracles) == 0 || len(oracles) > maxOracles {
		return nil, types.RpcErrorOracleMalformed()
	}

	// rippled: !params.isMember(jss::base_asset) -> missing_field_error
	baseAssetRaw, hasBaseAsset := raw["base_asset"]
	if !hasBaseAsset {
		return nil, types.RpcErrorMissingField("base_asset")
	}
	// rippled: !params.isMember(jss::quote_asset) -> missing_field_error
	quoteAssetRaw, hasQuoteAsset := raw["quote_asset"]
	if !hasQuoteAsset {
		return nil, types.RpcErrorMissingField("quote_asset")
	}

	// rippled: if present, must be valid uint; then if present, must be 1..25
	const maxTrim = 25
	var trimValue uint32
	hasTrim := false
	if trimRaw, ok := raw["trim"]; ok {
		hasTrim = true
		v, err := parseUintParam(trimRaw)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		trimValue = v
		if trimValue == 0 || trimValue > maxTrim {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}

	// rippled: if present, must be valid uint
	var timeThreshold uint32
	if ttRaw, ok := raw["time_threshold"]; ok {
		v, err := parseUintParam(ttRaw)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		timeThreshold = v
	}

	// rippled: empty or invalid currency -> rpcINVALID_PARAMS
	baseAsset, err := parseCurrencyParam(baseAssetRaw)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	quoteAsset, err := parseCurrencyParam(quoteAssetRaw)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Parse the ledger specifier from the raw params
	var ledgerSpec struct {
		types.LedgerSpecifier
	}
	if params != nil {
		_ = json.Unmarshal(params, &ledgerSpec)
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if ledgerSpec.LedgerIndex != "" {
		ledgerIndex = ledgerSpec.LedgerIndex.String()
	}

	// Collect prices from all oracles
	var prices []PriceDataPoint

	for _, oracleRaw := range oracles {
		var oracleSpec map[string]interface{}
		if err := json.Unmarshal(oracleRaw, &oracleSpec); err != nil {
			return nil, types.RpcErrorOracleMalformed()
		}

		// rippled: missing account or oracle_document_id -> rpcORACLE_MALFORMED
		accountRaw, hasAccount := oracleSpec["account"]
		docIDRaw, hasDocID := oracleSpec["oracle_document_id"]

		if !hasAccount || !hasDocID {
			return nil, types.RpcErrorOracleMalformed()
		}

		// rippled: invalid oracle_document_id (not uint) -> rpcINVALID_PARAMS
		documentID, ok := parseOracleDocumentID(docIDRaw)
		if !ok {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}

		// rippled: invalid account (not valid base58) -> rpcINVALID_PARAMS
		accountStr, ok := accountRaw.(string)
		if !ok {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		_, accountBytes, decodeErr := addresscodec.DecodeClassicAddressToAccountID(accountStr)
		if decodeErr != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		var accountID [20]byte
		copy(accountID[:], accountBytes)

		// Check for zero account (rippled rejects account->isZero())
		allZero := true
		for _, b := range accountID {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}

		oracleKeylet := keylet.Oracle(accountID, documentID)
		oracleEntry, lookupErr := ctx.Services.Ledger.GetLedgerEntry(oracleKeylet.Key, ledgerIndex)
		if lookupErr != nil {
			continue
		}

		oracleDecoded, decodeErr2 := binarycodec.Decode(hex.EncodeToString(oracleEntry.Node))
		if decodeErr2 != nil {
			continue
		}

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
		priceDataSeries, ok2 := oracleDecoded["PriceDataSeries"].([]interface{})
		if !ok2 {
			continue
		}

		for _, pd := range priceDataSeries {
			priceData, ok := pd.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this matches the requested asset pair
			ba, _ := priceData["BaseAsset"].(string)
			qa, _ := priceData["QuoteAsset"].(string)

			if ba != baseAsset || qa != quoteAsset {
				continue
			}

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

	// rippled: prices.empty() -> rpcOBJECT_NOT_FOUND
	if len(prices) == 0 {
		return nil, types.RpcErrorObjectNotFound("Object not found.")
	}

	// Find the latest update time
	var latestTime uint32
	for _, p := range prices {
		if p.LastUpdateTime > latestTime {
			latestTime = p.LastUpdateTime
		}
	}

	// Filter by time threshold if specified
	if timeThreshold > 0 {
		var filtered []PriceDataPoint
		// rippled: upperBound = latestTime > threshold ? (latestTime - threshold) : oldestTime
		var oldestTime uint32 = math.MaxUint32
		for _, p := range prices {
			if p.LastUpdateTime < oldestTime {
				oldestTime = p.LastUpdateTime
			}
		}
		var upperBound uint32
		if latestTime > timeThreshold {
			upperBound = latestTime - timeThreshold
		} else {
			upperBound = oldestTime
		}
		if upperBound > oldestTime {
			// Erase entries with LastUpdateTime <= upperBound (rippled erases
			// from upper_bound(upperBound) to end() in the descending-sorted
			// left map, which removes all entries with time < upperBound.
			// Since we're using "strictly less than", we keep entries where
			// time > upperBound — matching rippled's upper_bound semantics).
			for _, p := range prices {
				if p.LastUpdateTime > upperBound {
					filtered = append(filtered, p)
				}
			}
		} else {
			filtered = prices
		}
		if len(filtered) == 0 {
			return nil, types.RpcErrorInternal("Internal error.")
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
			"size":               uint16(entireSize),
			"standard_deviation": fmt.Sprintf("%g", entireSD),
		},
		"median": fmt.Sprintf("%g", median),
	}

	// Calculate trimmed set if requested
	if hasTrim && trimValue > 0 {
		trimCount := len(prices) * int(trimValue) / 100
		trimmedPrices := prices[trimCount : len(prices)-trimCount]
		trimmedMean, trimmedSD := calculateStats(trimmedPrices)
		response["trimmed_set"] = map[string]interface{}{
			"mean":               fmt.Sprintf("%g", trimmedMean),
			"size":               uint16(len(trimmedPrices)),
			"standard_deviation": fmt.Sprintf("%g", trimmedSD),
		}
	}

	return response, nil
}

// parseUintParam parses a JSON value as a non-negative uint32.
// Supports positive int, uint, and numeric string representations.
// Matches rippled's validUInt lambda in GetAggregatePrice.cpp.
func parseUintParam(raw json.RawMessage) (uint32, error) {
	// Try as number first
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		// Reject negative, non-integer, or NaN values
		if f < 0 || f != math.Floor(f) || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("invalid uint")
		}
		return uint32(f), nil
	}
	// Try as string containing a number
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid uint string")
		}
		return uint32(v), nil
	}
	return 0, fmt.Errorf("invalid uint type")
}

// parseCurrencyParam parses and validates a currency code from a JSON raw value.
// Returns the currency string or an error if invalid.
// Matches rippled's getCurrency lambda in GetAggregatePrice.cpp.
func parseCurrencyParam(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("not a string")
	}
	if s == "" {
		return "", fmt.Errorf("empty currency")
	}
	if !isValidCurrency(s) {
		return "", fmt.Errorf("invalid currency")
	}
	return s, nil
}

// isValidCurrency validates a currency code matching rippled's to_currency().
// Accepts:
//   - "XRP" (system currency code)
//   - 3-character ISO-like codes using alphanumeric + special chars
//   - 40-character hex strings (160-bit currency)
func isValidCurrency(code string) bool {
	if code == "XRP" || code == "xrp" {
		return true
	}
	if len(code) == 3 {
		for _, c := range code {
			if !isIsoCurrencyChar(c) {
				return false
			}
		}
		return true
	}
	// 40-character hex representation of 160-bit currency
	if len(code) == 40 {
		for _, c := range code {
			if !isHexChar(c) {
				return false
			}
		}
		return true
	}
	return false
}

// isIsoCurrencyChar matches rippled's isoCharSet:
// abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789<>(){}[]|?!@#$%^&*
func isIsoCurrencyChar(c rune) bool {
	if c >= 'a' && c <= 'z' {
		return true
	}
	if c >= 'A' && c <= 'Z' {
		return true
	}
	if c >= '0' && c <= '9' {
		return true
	}
	switch c {
	case '<', '>', '(', ')', '{', '}', '[', ']', '|', '?', '!', '@', '#', '$', '%', '^', '&', '*':
		return true
	}
	return false
}

func isHexChar(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// parseOracleDocumentID parses an oracle_document_id from a JSON-decoded interface value.
// Returns (documentID, true) on success or (0, false) if the value is not a valid uint.
func parseOracleDocumentID(v interface{}) (uint32, bool) {
	switch val := v.(type) {
	case float64:
		// Reject negative, non-integer, NaN
		if val < 0 || val != math.Floor(val) || math.IsNaN(val) || math.IsInf(val, 0) {
			return 0, false
		}
		return uint32(val), true
	case int:
		if val < 0 {
			return 0, false
		}
		return uint32(val), true
	case uint32:
		return val, true
	case string:
		// rippled: validUInt supports string representation
		uv, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(uv), true
	default:
		return 0, false
	}
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

func (m *GetAggregatePriceMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *GetAggregatePriceMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *GetAggregatePriceMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

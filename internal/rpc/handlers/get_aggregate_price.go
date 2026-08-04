package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

type GetAggregatePriceMethod struct{ baseHandler }

type aggregatePriceAmount struct {
	number state.XRPLNumber
}

type aggregatePricePoint struct {
	price          aggregatePriceAmount
	lastUpdateTime uint32
}

func (m *GetAggregatePriceMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return nil, rpcErr
	}

	var raw map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	oraclesRaw, hasOracles := raw["oracles"]
	if !hasOracles {
		return nil, types.RpcErrorMissingField("oracles")
	}
	var oracles []json.RawMessage
	if err := json.Unmarshal(oraclesRaw, &oracles); err != nil {
		return nil, types.RpcErrorOracleMalformed()
	}
	if len(oracles) == 0 || len(oracles) > 200 {
		return nil, types.RpcErrorOracleMalformed()
	}

	baseAssetRaw, hasBaseAsset := raw["base_asset"]
	if !hasBaseAsset {
		return nil, types.RpcErrorMissingField("base_asset")
	}
	quoteAssetRaw, hasQuoteAsset := raw["quote_asset"]
	if !hasQuoteAsset {
		return nil, types.RpcErrorMissingField("quote_asset")
	}

	var trimValue uint32
	hasTrim := false
	if trimRaw, ok := raw["trim"]; ok {
		hasTrim = true
		value, err := parseUintParam(trimRaw)
		if err != nil || value == 0 || value > 25 {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		trimValue = value
	}

	var timeThreshold uint32
	if thresholdRaw, ok := raw["time_threshold"]; ok {
		value, err := parseUintParam(thresholdRaw)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		timeThreshold = value
	}

	baseAsset, err := parseCurrencyParam(baseAssetRaw)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	quoteAsset, err := parseCurrencyParam(quoteAssetRaw)
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selectorErr := resolveLedgerSelector(ledgerSpec)
	if selectorErr != nil {
		return nil, selectorErr
	}
	targetLedger, lookupValidated, lookupErr := lookupLedger(ctx, ledgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	lookupFields := ledgerEntryResponseFields(targetLedger, lookupValidated)

	prices := make([]aggregatePricePoint, 0, len(oracles))
	for _, oracleRaw := range oracles {
		var oracleSpec map[string]json.RawMessage
		if err := json.Unmarshal(oracleRaw, &oracleSpec); err != nil {
			return nil, types.RpcErrorOracleMalformed().WithExtra(lookupFields)
		}
		accountRaw, hasAccount := oracleSpec["account"]
		documentIDRaw, hasDocumentID := oracleSpec["oracle_document_id"]
		if !hasAccount || !hasDocumentID {
			return nil, types.RpcErrorOracleMalformed().WithExtra(lookupFields)
		}

		documentID, err := parseUintParam(documentIDRaw)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.").WithExtra(lookupFields)
		}
		var account string
		if err := json.Unmarshal(accountRaw, &account); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.").WithExtra(lookupFields)
		}
		_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.").WithExtra(lookupFields)
		}
		var accountID [20]byte
		copy(accountID[:], accountBytes)
		if accountID == ([20]byte{}) {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.").WithExtra(lookupFields)
		}

		entry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Oracle(accountID, documentID).Key, ledgerIndex)
		if err != nil {
			if errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
				continue
			}
			return nil, rpcInternalError("get_aggregate_price: oracle lookup failed", err).WithExtra(lookupFields)
		}
		if entry == nil {
			return nil, rpcInternalError("get_aggregate_price: oracle lookup returned no result", nil).WithExtra(lookupFields)
		}
		if _, err := state.ParseOracle(entry.Node); err != nil {
			return nil, rpcInternalError("get_aggregate_price: oracle decoding failed", err).WithExtra(lookupFields)
		}
		decoded, err := binarycodec.Decode(hex.EncodeToString(entry.Node))
		if err != nil {
			return nil, rpcInternalError("get_aggregate_price: oracle decoding failed", err).WithExtra(lookupFields)
		}

		if err := iterateAggregatePriceData(ctx, decoded, func(node map[string]any) bool {
			point, found := aggregatePriceFromNode(node, baseAsset, quoteAsset)
			if found {
				prices = append(prices, point)
			}
			return found
		}); err != nil {
			return nil, rpcInternalError("get_aggregate_price: transaction decoding failed", err).WithExtra(lookupFields)
		}
	}

	if len(prices) == 0 {
		return nil, types.RpcErrorObjectNotFound("The requested object was not found.").WithExtra(lookupFields)
	}

	latestTime := prices[0].lastUpdateTime
	oldestTime := latestTime
	for _, point := range prices[1:] {
		if point.lastUpdateTime > latestTime {
			latestTime = point.lastUpdateTime
		}
		if point.lastUpdateTime < oldestTime {
			oldestTime = point.lastUpdateTime
		}
	}
	if timeThreshold != 0 {
		upperBound := oldestTime
		if latestTime > timeThreshold {
			upperBound = latestTime - timeThreshold
		}
		if upperBound > oldestTime {
			filtered := prices[:0]
			for _, point := range prices {
				if point.lastUpdateTime >= upperBound {
					filtered = append(filtered, point)
				}
			}
			prices = filtered
		}
	}

	sort.Slice(prices, func(i, j int) bool {
		return prices[i].price.compare(prices[j].price) < 0
	})

	mean, standardDeviation := aggregatePriceStats(prices)
	response := lookupFields
	response["time"] = latestTime
	response["entire_set"] = map[string]any{
		"mean":               mean.text(),
		"size":               uint16(len(prices)),
		"standard_deviation": standardDeviation.String(),
	}
	response["median"] = aggregatePriceMedian(prices).text()

	if hasTrim {
		trimCount := len(prices) * int(trimValue) / 100
		trimmed := prices[trimCount : len(prices)-trimCount]
		trimmedMean, trimmedStandardDeviation := aggregatePriceStats(trimmed)
		response["trimmed_set"] = map[string]any{
			"mean":               trimmedMean.text(),
			"size":               uint16(len(trimmed)),
			"standard_deviation": trimmedStandardDeviation.String(),
		}
	}

	return response, nil
}

func parseUintParam(raw json.RawMessage) (uint32, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if len(text) > 0 && text[0] == '+' {
			text = text[1:]
		}
		value, err := strconv.ParseUint(text, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid uint string")
		}
		return uint32(value), nil
	}

	token := string(bytes.TrimSpace(raw))
	negative := false
	if len(token) > 0 && token[0] == '-' {
		negative = true
		token = token[1:]
	}
	if token == "" {
		return 0, fmt.Errorf("invalid uint type")
	}
	for _, digit := range token {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid uint type")
		}
	}
	value, err := strconv.ParseUint(token, 10, 32)
	if err != nil || negative && value != 0 {
		return 0, fmt.Errorf("invalid uint")
	}
	return uint32(value), nil
}

func parseCurrencyParam(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" || !keylet.IsValidCurrencyCode(value) {
		return "", fmt.Errorf("invalid currency")
	}
	return value, nil
}

func iterateAggregatePriceData(ctx *types.RpcContext, initial map[string]any, visit func(map[string]any) bool) error {
	oracle := initial
	chain := initial
	isNew := false
	for history := uint8(0); ; {
		if oracle == nil || visit(oracle) || isNew {
			return nil
		}
		history++
		if history > 3 {
			return nil
		}

		previousID, previousSequence, ok := aggregatePreviousTransaction(chain)
		if !ok {
			return nil
		}
		transaction, err := ctx.Services.Ledger.GetTransaction(previousID)
		if err != nil {
			if errors.Is(err, svcerr.ErrTxnNotFound) || errors.Is(err, svcerr.ErrLedgerNotFound) {
				return nil
			}
			return err
		}
		if transaction == nil || transaction.LedgerIndex != previousSequence {
			return nil
		}
		stored, err := decodeTxBlob(transaction.TxData)
		if err != nil {
			return err
		}

		found := false
		for _, affected := range affectedNodes(stored.Meta) {
			_, inner := nodeParts(affected)
			if nodeType(inner) != "Oracle" {
				continue
			}
			chain = inner
			oracle, isNew = inner["NewFields"].(map[string]any)
			if isNew && history == 1 {
				return nil
			}
			if !isNew {
				oracle, _ = inner["FinalFields"].(map[string]any)
			}
			found = true
			break
		}
		if !found {
			return nil
		}
	}
}

func aggregatePreviousTransaction(node map[string]any) ([32]byte, uint32, bool) {
	var hash [32]byte
	hashText, ok := node["PreviousTxnID"].(string)
	if !ok || len(hashText) != 64 {
		return hash, 0, false
	}
	decoded, err := hex.DecodeString(hashText)
	if err != nil {
		return hash, 0, false
	}
	copy(hash[:], decoded)
	sequence, ok := aggregateUint32(node["PreviousTxnLgrSeq"])
	return hash, sequence, ok
}

func aggregatePriceFromNode(node map[string]any, baseAsset, quoteAsset string) (aggregatePricePoint, bool) {
	lastUpdateTime, ok := aggregateUint32(node["LastUpdateTime"])
	if !ok {
		return aggregatePricePoint{}, false
	}
	series, ok := node["PriceDataSeries"].([]any)
	if !ok {
		return aggregatePricePoint{}, false
	}
	for _, rawPrice := range series {
		priceData, ok := rawPrice.(map[string]any)
		if !ok {
			continue
		}
		if nested, nestedOK := priceData["PriceData"].(map[string]any); nestedOK {
			priceData = nested
		}
		base, _ := priceData["BaseAsset"].(string)
		quote, _ := priceData["QuoteAsset"].(string)
		if base != baseAsset || quote != quoteAsset {
			continue
		}
		assetPrice, ok := aggregateAssetPrice(priceData["AssetPrice"])
		if !ok {
			continue
		}
		var scale uint32
		if rawScale, present := priceData["Scale"]; present {
			scale, ok = aggregateUint32(rawScale)
			if !ok || scale > 255 {
				continue
			}
		}
		return aggregatePricePoint{
			price:          newAggregatePriceAmountUnsigned(assetPrice, -int(scale)),
			lastUpdateTime: lastUpdateTime,
		}, true
	}
	return aggregatePricePoint{}, false
}

func aggregateAssetPrice(value any) (uint64, bool) {
	switch typed := value.(type) {
	case string:
		price, err := strconv.ParseUint(typed, 16, 64)
		return price, err == nil
	case uint64:
		return typed, true
	case uint32:
		return uint64(typed), true
	case int:
		return uint64(typed), typed >= 0
	case float64:
		if typed < 0 || typed > float64(^uint32(0)) || typed != float64(uint64(typed)) {
			return 0, false
		}
		return uint64(typed), true
	default:
		return 0, false
	}
}

func aggregateUint32(value any) (uint32, bool) {
	switch typed := value.(type) {
	case uint32:
		return typed, true
	case uint8:
		return uint32(typed), true
	case int:
		if typed < 0 || uint64(typed) > uint64(^uint32(0)) {
			return 0, false
		}
		return uint32(typed), true
	case float64:
		if typed < 0 || typed > float64(^uint32(0)) || typed != float64(uint32(typed)) {
			return 0, false
		}
		return uint32(typed), true
	default:
		return 0, false
	}
}

func newAggregatePriceAmount(mantissa int64, exponent int) aggregatePriceAmount {
	return aggregatePriceAmountFromNumber(state.NewXRPLNumber(mantissa, exponent))
}

func newAggregatePriceAmountUnsigned(mantissa uint64, exponent int) aggregatePriceAmount {
	return aggregatePriceAmountFromNumber(state.NewXRPLNumberFromUint(mantissa, exponent))
}

func aggregatePriceAmountFromNumber(number state.XRPLNumber) aggregatePriceAmount {
	if number.IsZero() || number.Exponent() < state.MinExponent {
		return aggregatePriceAmount{number: state.NewXRPLNumber(0, 0)}
	}
	if number.Exponent() > state.MaxExponent {
		panic("aggregate price STAmount overflow")
	}
	return aggregatePriceAmount{number: number}
}

func (amount aggregatePriceAmount) add(other aggregatePriceAmount) aggregatePriceAmount {
	return aggregatePriceAmountFromNumber(amount.number.Add(other.number))
}

func (amount aggregatePriceAmount) subtract(other aggregatePriceAmount) aggregatePriceAmount {
	return aggregatePriceAmountFromNumber(amount.number.Sub(other.number))
}

func (amount aggregatePriceAmount) divide(other aggregatePriceAmount) aggregatePriceAmount {
	if other.number.IsZero() {
		panic("aggregate price STAmount division by zero")
	}
	if amount.number.IsZero() {
		return amount
	}

	numerator := new(big.Int).SetInt64(amount.number.Mantissa())
	denominator := new(big.Int).SetInt64(other.number.Mantissa())
	negative := numerator.Sign() != denominator.Sign()
	numerator.Abs(numerator)
	denominator.Abs(denominator)
	numerator.Mul(numerator, big.NewInt(100_000_000_000_000_000))
	numerator.Div(numerator, denominator)
	numerator.Add(numerator, big.NewInt(5))
	if negative {
		numerator.Neg(numerator)
	}
	return newAggregatePriceAmount(
		numerator.Int64(),
		amount.number.Exponent()-other.number.Exponent()-17,
	)
}

func (amount aggregatePriceAmount) compare(other aggregatePriceAmount) int {
	leftSign := amount.number.Signum()
	rightSign := other.number.Signum()
	if leftSign != rightSign {
		if leftSign < rightSign {
			return -1
		}
		return 1
	}
	if leftSign == 0 {
		return 0
	}
	if amount.number.Exponent() != other.number.Exponent() {
		if amount.number.Exponent() < other.number.Exponent() {
			return -leftSign
		}
		return leftSign
	}
	leftMantissa := amount.number.Mantissa()
	rightMantissa := other.number.Mantissa()
	if leftMantissa < rightMantissa {
		return -1
	}
	if leftMantissa > rightMantissa {
		return 1
	}
	return 0
}

func (amount aggregatePriceAmount) text() string {
	return amount.number.String()
}

func aggregatePriceStats(prices []aggregatePricePoint) (aggregatePriceAmount, state.XRPLNumber) {
	mean := newAggregatePriceAmount(0, 0)
	for _, point := range prices {
		mean = mean.add(point.price)
	}
	mean = mean.divide(newAggregatePriceAmount(int64(len(prices)), 0))

	standardDeviation := state.NewXRPLNumberScaled(0, 0, state.MantissaScaleLarge, state.RoundToNearest)
	if len(prices) > 1 {
		for _, point := range prices {
			amountDifference := point.price.subtract(mean).number
			difference := state.NewXRPLNumberScaled(
				amountDifference.Mantissa(),
				amountDifference.Exponent(),
				state.MantissaScaleLarge,
				state.RoundToNearest,
			)
			standardDeviation = standardDeviation.Add(difference.Mul(difference))
		}
		divisor := state.NewXRPLNumberScaled(
			int64(len(prices)-1),
			0,
			state.MantissaScaleLarge,
			state.RoundToNearest,
		)
		standardDeviation = standardDeviation.Div(divisor).Root2()
	}
	return mean, standardDeviation
}

func aggregatePriceMedian(prices []aggregatePricePoint) aggregatePriceAmount {
	middle := len(prices) / 2
	if len(prices)%2 != 0 {
		return prices[middle].price
	}
	return prices[middle-1].price.add(prices[middle].price).divide(newAggregatePriceAmount(2, 0))
}

func (m *GetAggregatePriceMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

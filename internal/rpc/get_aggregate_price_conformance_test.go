package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type aggregatePriceLedgerService struct {
	*mockLedgerService
	entries        map[[32]byte]*types.LedgerEntryResult
	transactions   map[[32]byte]*types.TransactionInfo
	entryErr       error
	transactionErr error
}

func newAggregatePriceLedgerService() *aggregatePriceLedgerService {
	return &aggregatePriceLedgerService{
		mockLedgerService: newMockLedgerService(),
		entries:           make(map[[32]byte]*types.LedgerEntryResult),
		transactions:      make(map[[32]byte]*types.TransactionInfo),
	}
}

func (m *aggregatePriceLedgerService) GetLedgerEntry(_ context.Context, entryKey [32]byte, _ string) (*types.LedgerEntryResult, error) {
	if m.entryErr != nil {
		return nil, m.entryErr
	}
	entry, ok := m.entries[entryKey]
	if !ok {
		return nil, svcerr.ErrLedgerEntryNotFound
	}
	return entry, nil
}

func (m *aggregatePriceLedgerService) GetTransaction(hash [32]byte) (*types.TransactionInfo, error) {
	if m.transactionErr != nil {
		return nil, m.transactionErr
	}
	transaction, ok := m.transactions[hash]
	if !ok {
		return nil, svcerr.ErrTxnNotFound
	}
	return transaction, nil
}

func TestGetAggregatePriceProjectionAndXRPLArithmetic(t *testing.T) {
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	service := newAggregatePriceLedgerService()
	oracles := make([]map[string]any, 0, 10)
	for documentID := uint32(1); documentID <= 10; documentID++ {
		key, node := aggregateOracleNode(t, owner, documentID, 946694900, "XRP", "USD", uint64(739+documentID), 1, "", 0)
		service.entries[key] = &types.LedgerEntryResult{Node: node}
		oracles = append(oracles, map[string]any{"account": owner, "oracle_document_id": documentID})
	}

	request := map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles":     oracles,
	}
	result := callAggregatePrice(t, service, request)
	assert.Equal(t, uint32(3), result["ledger_current_index"])
	assert.Equal(t, false, result["validated"])
	assert.NotContains(t, result, "ledger_hash")
	assert.NotContains(t, result, "ledger_index")
	assert.Equal(t, "74.45", result["median"])
	entire := result["entire_set"].(map[string]any)
	assert.Equal(t, "74.45", entire["mean"])
	assert.Equal(t, uint16(10), entire["size"])
	assert.Equal(t, "0.3027650354097491666", entire["standard_deviation"])

	request["ledger_index"] = "validated"
	result = callAggregatePrice(t, service, request)
	assert.Equal(t, uint32(2), result["ledger_index"])
	assert.Equal(t, strings.Repeat("0", 64), result["ledger_hash"])
	assert.Equal(t, true, result["validated"])
	assert.NotContains(t, result, "ledger_current_index")
}

func TestGetAggregatePricePreservesUnsignedAssetPrices(t *testing.T) {
	tests := []struct {
		name  string
		price uint64
		scale uint8
		want  string
	}{
		{name: "max int64", price: math.MaxInt64, want: "9223372036854776e3"},
		{name: "max int64 plus one", price: uint64(math.MaxInt64) + 1, want: "9223372036854776e3"},
		{name: "max uint64", price: math.MaxUint64, want: "1844674407370955e4"},
		{name: "minimum STAmount exponent", price: math.MaxUint64, scale: 100, want: "1844674407370955e-96"},
		{name: "below minimum STAmount exponent", price: math.MaxUint64, scale: 101, want: "0"},
		{name: "max scale", price: math.MaxUint64, scale: math.MaxUint8, want: "0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newAggregatePriceLedgerService()
			key, node := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "USD", test.price, test.scale, "", 0)
			service.entries[key] = &types.LedgerEntryResult{Node: node}

			result := callAggregatePrice(t, service, map[string]any{
				"base_asset":  "XRP",
				"quote_asset": "USD",
				"oracles": []map[string]any{{
					"account":            ownerForAggregatePriceTest,
					"oracle_document_id": 1,
				}},
			})
			assert.Equal(t, test.want, result["median"])
			assert.Equal(t, test.want, result["entire_set"].(map[string]any)["mean"])
		})
	}
}

func TestGetAggregatePriceSkipsMissingOracleEntries(t *testing.T) {
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	service := newAggregatePriceLedgerService()
	key, node := aggregateOracleNode(t, owner, 1, 100, "XRP", "USD", 740, 0, "", 0)
	service.entries[key] = &types.LedgerEntryResult{Node: node}

	result := callAggregatePrice(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{
			{"account": owner, "oracle_document_id": 999},
			{"account": owner, "oracle_document_id": 1},
		},
	})
	assert.Equal(t, uint16(1), result["entire_set"].(map[string]any)["size"])
}

func TestGetAggregatePriceNilOracleResultReturnsInternal(t *testing.T) {
	service := newAggregatePriceLedgerService()
	key, _ := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "USD", 740, 0, "", 0)
	service.entries[key] = nil
	result, rpcErr := callAggregatePriceError(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{{
			"account":            ownerForAggregatePriceTest,
			"oracle_document_id": 1,
		}},
	})
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestGetAggregatePriceLookupErrorReturnsInternal(t *testing.T) {
	service := newAggregatePriceLedgerService()
	service.entryErr = errors.New("node store unavailable")
	result, rpcErr := callAggregatePriceError(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{{
			"account":            ownerForAggregatePriceTest,
			"oracle_document_id": 1,
		}},
	})
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestGetAggregatePriceMalformedOracleBytesReturnsInternal(t *testing.T) {
	accountRoot, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  ownerForAggregatePriceTest,
		Balance:  1,
		Sequence: 1,
	})
	require.NoError(t, err)

	for name, node := range map[string][]byte{
		"invalid binary": {0xff},
		"empty object":   {},
		"wrong type":     accountRoot,
	} {
		t.Run(name, func(t *testing.T) {
			service := newAggregatePriceLedgerService()
			key, _ := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "USD", 740, 0, "", 0)
			service.entries[key] = &types.LedgerEntryResult{Node: node}

			result, rpcErr := callAggregatePriceError(t, service, map[string]any{
				"base_asset":  "XRP",
				"quote_asset": "USD",
				"oracles": []map[string]any{{
					"account":            ownerForAggregatePriceTest,
					"oracle_document_id": 1,
				}},
			})
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		})
	}
}

func TestGetAggregatePriceHistoricalLookupErrorReturnsInternal(t *testing.T) {
	service := newAggregatePriceLedgerService()
	service.transactionErr = errors.New("transaction store unavailable")
	key, node := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "EUR", 740, 0, strings.Repeat("A", 64), 5)
	service.entries[key] = &types.LedgerEntryResult{Node: node}

	result, rpcErr := callAggregatePriceError(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{{
			"account":            ownerForAggregatePriceTest,
			"oracle_document_id": 1,
		}},
	})
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

func TestGetAggregatePriceMissingHistoricalTransactionReturnsObjectNotFound(t *testing.T) {
	service := newAggregatePriceLedgerService()
	key, node := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "EUR", 740, 0, strings.Repeat("A", 64), 5)
	service.entries[key] = &types.LedgerEntryResult{Node: node}

	result, rpcErr := callAggregatePriceError(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{{
			"account":            ownerForAggregatePriceTest,
			"oracle_document_id": 1,
		}},
	})
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcOBJECT_NOT_FOUND, rpcErr.Code)
}

func TestGetAggregatePriceMalformedHistoricalTransactionReturnsInternal(t *testing.T) {
	service := newAggregatePriceLedgerService()
	key, node := aggregateOracleNode(t, ownerForAggregatePriceTest, 1, 100, "XRP", "EUR", 740, 0, strings.Repeat("A", 64), 5)
	service.entries[key] = &types.LedgerEntryResult{Node: node}
	service.transactions[aggregateHash(t, strings.Repeat("A", 64))] = &types.TransactionInfo{
		TxData:      []byte{0xff},
		LedgerIndex: 5,
	}

	result, rpcErr := callAggregatePriceError(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles": []map[string]any{{
			"account":            ownerForAggregatePriceTest,
			"oracle_document_id": 1,
		}},
	})
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
}

const ownerForAggregatePriceTest = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func callAggregatePriceError(t *testing.T, service types.LedgerService, request map[string]any) (any, *types.RpcError) {
	t.Helper()
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}
	return (&handlers.GetAggregatePriceMethod{}).Handle(ctx, encoded)
}

func TestGetAggregatePriceThresholdIncludesBoundary(t *testing.T) {
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	service := newAggregatePriceLedgerService()
	oracles := make([]map[string]any, 0, 3)
	for i, point := range []struct {
		price uint64
		time  uint32
	}{{100, 100}, {200, 90}, {300, 80}} {
		documentID := uint32(i + 1)
		key, node := aggregateOracleNode(t, owner, documentID, point.time, "XRP", "USD", point.price, 0, "", 0)
		service.entries[key] = &types.LedgerEntryResult{Node: node}
		oracles = append(oracles, map[string]any{"account": owner, "oracle_document_id": documentID})
	}

	result := callAggregatePrice(t, service, map[string]any{
		"base_asset":     "XRP",
		"quote_asset":    "USD",
		"oracles":        oracles,
		"time_threshold": 10,
	})
	entire := result["entire_set"].(map[string]any)
	assert.Equal(t, uint16(2), entire["size"])
	assert.Equal(t, "150", entire["mean"])
	assert.Equal(t, uint32(100), result["time"])
}

func TestGetAggregatePriceWalksExactlyThreePriorOracleVersions(t *testing.T) {
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, test := range []struct {
		name       string
		matchDepth int
		wantFound  bool
	}{
		{name: "third prior version is included", matchDepth: 3, wantFound: true},
		{name: "fourth prior version is excluded", matchDepth: 4, wantFound: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newAggregatePriceLedgerService()
			service.currentLedgerIndex = 6
			service.closedLedgerIndex = 5
			service.validatedLedgerIndex = 5

			hashes := []string{
				strings.Repeat("A", 64),
				strings.Repeat("B", 64),
				strings.Repeat("C", 64),
				strings.Repeat("D", 64),
			}
			key, node := aggregateOracleNode(t, owner, 1, 100, "XRP", "EUR", 740, 1, hashes[0], 5)
			service.entries[key] = &types.LedgerEntryResult{Node: node}

			for depth := 1; depth <= test.matchDepth; depth++ {
				base, quote := "XRP", "EUR"
				if depth == test.matchDepth {
					quote = "USD"
				}
				fields := aggregateHistoricalOracleFields(100-uint32(depth), base, quote, 740, 1)
				nextHash := ""
				nextSequence := uint32(0)
				if depth < test.matchDepth {
					nextHash = hashes[depth]
					nextSequence = uint32(5 - depth)
				}
				hash := aggregateHash(t, hashes[depth-1])
				service.transactions[hash] = &types.TransactionInfo{
					TxData:      aggregateOracleMetadata(t, fields, nextHash, nextSequence),
					LedgerIndex: uint32(6 - depth),
				}
			}

			params := map[string]any{
				"base_asset":  "XRP",
				"quote_asset": "USD",
				"oracles": []map[string]any{{
					"account": owner, "oracle_document_id": 1,
				}},
			}
			encoded, err := json.Marshal(params)
			require.NoError(t, err)
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion1,
				Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
			}
			result, rpcErr := (&handlers.GetAggregatePriceMethod{}).Handle(ctx, encoded)
			if !test.wantFound {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr)
				assert.Equal(t, types.RpcOBJECT_NOT_FOUND, rpcErr.Code)
				return
			}
			require.Nil(t, rpcErr)
			response := result.(map[string]any)
			assert.Equal(t, uint16(1), response["entire_set"].(map[string]any)["size"])
			assert.Equal(t, "74", response["median"])
		})
	}
}

func callAggregatePrice(t *testing.T, service types.LedgerService, request map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}
	result, rpcErr := (&handlers.GetAggregatePriceMethod{}).Handle(ctx, encoded)
	require.Nil(t, rpcErr)
	response, ok := result.(map[string]any)
	require.True(t, ok)
	return response
}

func aggregateOracleNode(
	t *testing.T,
	owner string,
	documentID uint32,
	lastUpdateTime uint32,
	baseAsset string,
	quoteAsset string,
	assetPrice uint64,
	scale uint8,
	previousHash string,
	previousSequence uint32,
) ([32]byte, []byte) {
	t.Helper()
	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(owner)
	require.NoError(t, err)
	var accountID [20]byte
	copy(accountID[:], accountBytes)
	oracle := &state.OracleData{
		Owner:               accountID,
		Provider:            "00",
		AssetClass:          "00",
		LastUpdateTime:      lastUpdateTime,
		OracleDocumentID:    documentID,
		HasOracleDocumentID: true,
		PriceDataSeries: []state.OraclePriceData{{
			BaseAsset:  baseAsset,
			QuoteAsset: quoteAsset,
			AssetPrice: assetPrice,
			Scale:      scale,
			HasPrice:   true,
			HasScale:   scale != 0,
		}},
		PreviousTxnLgrSeq: previousSequence,
	}
	if previousHash != "" {
		oracle.PreviousTxnID = aggregateHash(t, previousHash)
	}
	node, err := state.SerializeOracle(oracle)
	require.NoError(t, err)
	return keylet.Oracle(accountID, documentID).Key, node
}

func aggregateHistoricalOracleFields(lastUpdateTime uint32, baseAsset, quoteAsset string, assetPrice uint64, scale uint8) map[string]any {
	return map[string]any{
		"LastUpdateTime": lastUpdateTime,
		"PriceDataSeries": []any{map[string]any{
			"PriceData": map[string]any{
				"BaseAsset":  baseAsset,
				"QuoteAsset": quoteAsset,
				"AssetPrice": strings.ToUpper(strconv.FormatUint(assetPrice, 16)),
				"Scale":      scale,
			},
		}},
	}
}

func aggregateOracleMetadata(t *testing.T, fields map[string]any, previousHash string, previousSequence uint32) []byte {
	t.Helper()
	inner := map[string]any{
		"LedgerEntryType": "Oracle",
		"FinalFields":     fields,
	}
	if previousHash != "" {
		inner["PreviousTxnID"] = previousHash
		inner["PreviousTxnLgrSeq"] = previousSequence
	}
	blob, err := json.Marshal(map[string]any{
		"tx_json": map[string]any{
			"TransactionType": "AccountSet",
			"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"Fee":             "10",
			"Sequence":        1,
			"SigningPubKey":   "",
		},
		"meta": map[string]any{
			"AffectedNodes": []any{map[string]any{"ModifiedNode": inner}},
		},
	})
	require.NoError(t, err)
	return blob
}

func aggregateHash(t *testing.T, value string) [32]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	var hash [32]byte
	copy(hash[:], decoded)
	return hash
}

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssue1902GatewayBalancesAccountAndIdentTypes(t *testing.T) {
	invalidValues := []struct {
		name  string
		value any
	}{
		{name: "number", value: 42},
		{name: "fraction", value: 1.5},
		{name: "boolean", value: true},
		{name: "null", value: nil},
		{name: "object", value: map[string]any{}},
		{name: "array", value: []any{}},
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3} {
		for _, field := range []string{"account", "ident"} {
			for _, test := range invalidValues {
				t.Run(strings.Join([]string{field, test.name, "api", strconv.Itoa(apiVersion)}, "/"), func(t *testing.T) {
					mock := newMockGatewayBalancesLedgerService()
					ctx := &types.RpcContext{
						Context:    context.Background(),
						Role:       types.RoleGuest,
						ApiVersion: apiVersion,
						Services:   newGatewayBalancesTestServices(mock),
					}
					params, err := json.Marshal(map[string]any{field: test.value})
					require.NoError(t, err)

					result, rpcErr := (&handlers.GatewayBalancesMethod{}).Handle(ctx, params)
					require.Nil(t, result)
					require.NotNil(t, rpcErr)
					assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
					assert.Equal(t, "invalidParams", rpcErr.ErrorString)
					assert.Equal(t, "Invalid field '"+field+"'.", rpcErr.Message)
				})
			}
		}
	}

	t.Run("account and ident are independently validated", func(t *testing.T) {
		mock := newMockGatewayBalancesLedgerService()
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   newGatewayBalancesTestServices(mock),
		}
		params, err := json.Marshal(map[string]any{
			"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"ident":   42,
		})
		require.NoError(t, err)

		result, rpcErr := (&handlers.GatewayBalancesMethod{}).Handle(ctx, params)
		require.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "Invalid field 'ident'.", rpcErr.Message)
	})
}

func TestIssue1902AccountLinesPeerTypes(t *testing.T) {
	invalidValues := []struct {
		name  string
		value any
	}{
		{name: "number", value: 42},
		{name: "fraction", value: 1.5},
		{name: "boolean", value: true},
		{name: "null", value: nil},
		{name: "object", value: map[string]any{}},
		{name: "array", value: []any{}},
	}

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3} {
		for _, test := range invalidValues {
			t.Run(strings.Join([]string{test.name, "api", strconv.Itoa(apiVersion)}, "/"), func(t *testing.T) {
				mock := newMockAccountLinesLedgerService()
				ctx := &types.RpcContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   newAccountLinesTestServices(mock),
				}
				params, err := json.Marshal(map[string]any{
					"account": ownerForAggregatePriceTest,
					"peer":    test.value,
				})
				require.NoError(t, err)

				result, rpcErr := (&handlers.AccountLinesMethod{}).Handle(ctx, params)
				require.Nil(t, result)
				require.NotNil(t, rpcErr)
				assert.Equal(t, rpcerrors.RpcINVALID_PARAMS, rpcErr.Code)
				assert.Equal(t, "invalidParams", rpcErr.ErrorString)
				assert.Equal(t, "Invalid field 'peer'.", rpcErr.Message)
			})
		}
	}
}

type countedAggregatePriceLedgerService struct {
	*aggregatePriceLedgerService
	lookups map[[32]byte]int
}

func (m *countedAggregatePriceLedgerService) GetLedgerEntry(ctx context.Context, key [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	m.lookups[key]++
	return m.aggregatePriceLedgerService.GetLedgerEntry(ctx, key, ledgerIndex)
}

func TestIssue1902AggregatePriceDeduplicatesPairsAndPreservesLimit(t *testing.T) {
	const secondOwner = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	base := newAggregatePriceLedgerService()
	service := &countedAggregatePriceLedgerService{
		aggregatePriceLedgerService: base,
		lookups:                     make(map[[32]byte]int),
	}
	oracles := make([]map[string]any, 0, 4)
	for _, test := range []struct {
		owner      string
		documentID uint32
		price      uint64
	}{
		{ownerForAggregatePriceTest, 1, 100},
		{ownerForAggregatePriceTest, 2, 200},
		{secondOwner, 1, 300},
	} {
		key, node := aggregateOracleNode(t, test.owner, test.documentID, 100, "XRP", "USD", test.price, 0, "", 0)
		base.entries[key] = &types.LedgerEntryResult{Node: node}
		oracles = append(oracles, map[string]any{
			"account":            test.owner,
			"oracle_document_id": test.documentID,
		})
	}
	// Repeat the first pair after all fields have been validated. The lookup and
	// resulting price must be emitted once, while the two distinct pairs remain.
	oracles = append(oracles, oracles[0])
	result := callAggregatePrice(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles":     oracles,
	})
	entire := result["entire_set"].(map[string]any)
	assert.Equal(t, uint16(3), entire["size"])
	assert.Equal(t, "200", entire["mean"])
	assert.Equal(t, "200", result["median"])
	assert.Len(t, service.lookups, 3)
	for key, count := range service.lookups {
		assert.Equal(t, 1, count, "oracle %X was looked up more than once", key)
	}

	duplicateOracles := make([]map[string]any, 200)
	for i := range duplicateOracles {
		duplicateOracles[i] = oracles[0]
	}
	result = callAggregatePrice(t, service, map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles":     duplicateOracles,
	})
	assert.Equal(t, uint16(1), result["entire_set"].(map[string]any)["size"])

	tooMany := append(duplicateOracles, oracles[0])
	encoded, err := json.Marshal(map[string]any{
		"base_asset":  "XRP",
		"quote_asset": "USD",
		"oracles":     tooMany,
	})
	require.NoError(t, err)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: service}),
	}
	resultAny, rpcErr := (&handlers.GetAggregatePriceMethod{}).Handle(ctx, encoded)
	assert.Nil(t, resultAny)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcORACLE_MALFORMED, rpcErr.Code)
}

func TestIssue1902NFTOfferMarkerLookupErrorsFallThroughToFetcher(t *testing.T) {
	service := newMockNFTOffersLedgerService()
	service.nftBuyOffersErr = errors.New("ledger unavailable")
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   newNFTOffersTestServices(service),
	}
	params, err := json.Marshal(map[string]any{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		"marker": strings.Repeat("A", 64),
	})
	require.NoError(t, err)
	result, rpcErr := (&handlers.NftBuyOffersMethod{}).Handle(ctx, params)
	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
}

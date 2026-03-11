package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerDataMock wraps mockLedgerService and overrides GetLedgerData
type ledgerDataMock struct {
	*mockLedgerService
	getLedgerDataFn func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error)
}

func (m *ledgerDataMock) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	if m.getLedgerDataFn != nil {
		return m.getLedgerDataFn(ledgerIndex, limit, marker)
	}
	return m.mockLedgerService.GetLedgerData(ledgerIndex, limit, marker)
}

// newDefaultLedgerDataResult creates a default LedgerDataResult for testing
func newDefaultLedgerDataResult(numItems int, withMarker bool) *types.LedgerDataResult {
	var ledgerHash [32]byte
	ledgerHash[0] = 0xAB
	ledgerHash[31] = 0xCD

	items := make([]types.LedgerDataItem, numItems)
	for i := 0; i < numItems; i++ {
		var indexHash [32]byte
		indexHash[0] = byte(i)
		items[i] = types.LedgerDataItem{
			Index: hex.EncodeToString(indexHash[:]),
			Data:  []byte{0x11, 0x00, byte(i)}, // minimal data
		}
	}

	result := &types.LedgerDataResult{
		LedgerIndex: 2,
		LedgerHash:  ledgerHash,
		State:       items,
		Validated:   true,
	}

	if withMarker {
		result.Marker = "0000000000000000000000000000000000000000000000000000000000000010"
	}

	return result
}

// TestLedgerDataLimitClamping tests that the limit is properly clamped
// Based on rippled LedgerData_test.cpp testCurrentLedgerToLimits()
func TestLedgerDataLimitClamping(t *testing.T) {
	var capturedLimit uint32

	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		capturedLimit = limit
		return newDefaultLedgerDataResult(int(limit), false), nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Default limit is 256", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(256), capturedLimit, "Default limit should be 256")
	})

	t.Run("Limit below max passes through", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        100,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(100), capturedLimit, "Limit 100 should pass through")
	})

	t.Run("Limit at max 2048 passes through", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        2048,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(2048), capturedLimit, "Limit 2048 should pass through")
	})

	t.Run("Limit above max 2048 is clamped", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        5000,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(2048), capturedLimit, "Limit above 2048 should be clamped to 2048")
	})

	t.Run("Limit 255 passes through", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        255,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(255), capturedLimit, "Limit 255 should pass through")
	})

	t.Run("Limit 257 passes through", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        257,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(257), capturedLimit, "Limit 257 should pass through")
	})
}

// TestLedgerDataBinaryMode tests binary vs JSON response format
// Based on rippled LedgerData_test.cpp testCurrentLedgerBinary()
func TestLedgerDataBinaryMode(t *testing.T) {
	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		return newDefaultLedgerDataResult(3, false), nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Binary false returns JSON objects", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"binary":       false,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]interface{})
		assert.Equal(t, 3, len(state))

		// Each item should have an index field
		for _, item := range state {
			itemMap := item.(map[string]interface{})
			assert.Contains(t, itemMap, "index")
		}
	})

	t.Run("Binary true returns hex data", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"binary":       true,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]interface{})
		assert.Equal(t, 3, len(state))

		// Each item should have data and index
		for _, item := range state {
			itemMap := item.(map[string]interface{})
			assert.Contains(t, itemMap, "data")
			assert.Contains(t, itemMap, "index")
			// data should be a hex string
			dataStr, ok := itemMap["data"].(string)
			assert.True(t, ok, "data should be a string")
			_, err := hex.DecodeString(dataStr)
			assert.NoError(t, err, "data should be valid hex")
		}
	})
}

// TestLedgerDataTypeFilter tests the type filter parameter
// Based on rippled LedgerData_test.cpp testLedgerType()
func TestLedgerDataTypeFilter(t *testing.T) {
	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		return newDefaultLedgerDataResult(5, false), nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// The type parameter is passed through to the service layer.
	// The handler itself should not error for valid types.
	t.Run("Type parameter accepted", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "current",
			"type":         "account",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for valid type, got: %v", rpcErr)
		require.NotNil(t, result)
	})
}

// TestLedgerDataMarkerPagination tests marker-based pagination
// Based on rippled LedgerData_test.cpp testMarkerFollow()
func TestLedgerDataMarkerPagination(t *testing.T) {
	callCount := 0

	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		callCount++
		if marker == "" {
			// First call: return items with marker
			return newDefaultLedgerDataResult(5, true), nil
		}
		// Second call: return remaining items without marker
		return newDefaultLedgerDataResult(3, false), nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("First page has marker", func(t *testing.T) {
		callCount = 0
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        5,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]interface{})
		assert.Equal(t, 5, len(state))
		assert.Contains(t, resp, "marker")
		markerStr, ok := resp["marker"].(string)
		assert.True(t, ok, "marker should be a string")
		assert.NotEmpty(t, markerStr, "marker should not be empty")
	})

	t.Run("Second page with marker has no marker", func(t *testing.T) {
		callCount = 0
		params := map[string]interface{}{
			"ledger_index": "current",
			"limit":        5,
			"marker":       "0000000000000000000000000000000000000000000000000000000000000010",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]interface{})
		assert.Equal(t, 3, len(state))
		// No marker when all data returned
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "Last page should not have a marker")
	})
}

// TestLedgerDataResponseStructure tests that the response has the correct structure
// Based on rippled LedgerData_test.cpp response field checks
func TestLedgerDataResponseStructure(t *testing.T) {
	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}

	var ledgerHash [32]byte
	ledgerHash[0] = 0xAB
	ledgerHash[31] = 0xCD

	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		return &types.LedgerDataResult{
			LedgerIndex: 2,
			LedgerHash:  ledgerHash,
			State: []types.LedgerDataItem{
				{
					Index: "0000000000000000000000000000000000000000000000000000000000000001",
					Data:  []byte{0x11, 0x00, 0x01},
				},
			},
			Validated: true,
		}, nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "current",
		"binary":       true,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapData(t, result)

	// Check required top-level fields
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "state")
	assert.Contains(t, resp, "validated")

	// ledger_hash should be a hex string
	hashStr, ok := resp["ledger_hash"].(string)
	assert.True(t, ok, "ledger_hash should be a string")
	assert.Equal(t, 64, len(hashStr), "ledger_hash should be 64 hex chars")

	// ledger_index should be a number
	switch v := resp["ledger_index"].(type) {
	case float64:
		assert.Equal(t, float64(2), v)
	default:
		t.Errorf("unexpected ledger_index type: %T", v)
	}

	// state should be an array
	state, ok := resp["state"].([]interface{})
	assert.True(t, ok, "state should be an array")
	assert.Equal(t, 1, len(state))

	// validated should be bool
	assert.Equal(t, true, resp["validated"])
}

// TestLedgerDataServiceUnavailable tests behavior when ledger service is not available
func TestLedgerDataServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Nil services", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Nil ledger in services", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Service returns error", func(t *testing.T) {
		mock := &ledgerDataMock{
			mockLedgerService: newMockLedgerService(),
		}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			return nil, errors.New("storage unavailable")
		}
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: mock}
		defer func() { types.Services = oldServices }()

		params := map[string]interface{}{
			"ledger_index": "current",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	})
}

// TestLedgerDataMethodMetadata tests the method's metadata
func TestLedgerDataMethodMetadata(t *testing.T) {
	method := &handlers.LedgerDataMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"ledger_data should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestLedgerDataLedgerHeader tests that ledger header info is included
// Based on rippled LedgerData_test.cpp testLedgerHeader()
func TestLedgerDataLedgerHeader(t *testing.T) {
	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}

	var ledgerHash [32]byte
	ledgerHash[0] = 0xE8
	ledgerHash[1] = 0x6D

	var accountHash, parentHash, txHash [32]byte
	accountHash[0] = 0x01
	parentHash[0] = 0x02
	txHash[0] = 0x03

	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		result := newDefaultLedgerDataResult(2, false)
		if marker == "" {
			result.LedgerHeader = &types.LedgerHeaderInfo{
				AccountHash:         accountHash,
				CloseFlags:          0,
				CloseTime:           776000030,
				CloseTimeHuman:      "2024-Aug-01 12:00:30.000000000 UTC",
				CloseTimeISO:        "2024-08-01T12:00:30Z",
				CloseTimeResolution: 10,
				Closed:              true,
				LedgerHash:          ledgerHash,
				LedgerIndex:         3,
				ParentCloseTime:     776000020,
				ParentHash:          parentHash,
				TotalCoins:          99999999999999980,
				TransactionHash:     txHash,
			}
		}
		return result, nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("First query includes ledger header JSON", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "closed",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		assert.Contains(t, resp, "ledger")

		ledger := resp["ledger"].(map[string]interface{})
		assert.Contains(t, ledger, "ledger_hash")
		assert.Contains(t, ledger, "account_hash")
		assert.Contains(t, ledger, "parent_hash")
		assert.Contains(t, ledger, "transaction_hash")
		assert.Contains(t, ledger, "close_time")
		assert.Contains(t, ledger, "close_time_human")
		assert.Contains(t, ledger, "close_time_iso")
		assert.Contains(t, ledger, "close_time_resolution")
		assert.Contains(t, ledger, "closed")
		assert.Contains(t, ledger, "total_coins")
	})

	t.Run("First query includes ledger header binary", func(t *testing.T) {
		params := map[string]interface{}{
			"ledger_index": "closed",
			"binary":       true,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		assert.Contains(t, resp, "ledger")

		ledger := resp["ledger"].(map[string]interface{})
		assert.Contains(t, ledger, "ledger_data")
		assert.Contains(t, ledger, "closed")

		// ledger_data should be a hex string
		dataStr, ok := ledger["ledger_data"].(string)
		assert.True(t, ok, "ledger_data should be a string in binary mode")
		_, err := hex.DecodeString(dataStr)
		assert.NoError(t, err, "ledger_data should be valid hex")
	})
}

// TestLedgerDataEmptyState tests response when state is empty
func TestLedgerDataEmptyState(t *testing.T) {
	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		return newDefaultLedgerDataResult(0, false), nil
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"ledger_index": "current",
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapData(t, result)
	state := resp["state"].([]interface{})
	assert.Equal(t, 0, len(state), "state should be an empty array")
}

// resultToMapData is a test helper for ledger_data tests
func resultToMapData(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}

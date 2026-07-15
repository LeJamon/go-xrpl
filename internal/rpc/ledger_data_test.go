package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ledgerDataMock wraps mockLedgerService and overrides GetLedgerData
type ledgerDataMock struct {
	*mockLedgerService
	getLedgerBySequenceFn func(seq uint32) (types.LedgerReader, error)
	getLedgerDataFn       func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error)
}

func (m *ledgerDataMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

func (m *ledgerDataMock) GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	if m.getLedgerDataFn != nil {
		return m.getLedgerDataFn(ledgerIndex, limit, marker)
	}
	return m.mockLedgerService.GetLedgerData(ctx, ledgerIndex, limit, marker)
}

// newDefaultLedgerDataResult creates a default LedgerDataResult for testing
func newDefaultLedgerDataResult(numItems int, withMarker bool) *types.LedgerDataResult {
	var ledgerHash [32]byte
	ledgerHash[0] = 0xAB
	ledgerHash[31] = 0xCD

	items := make([]types.LedgerDataItem, numItems)
	for i := range numItems {
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

func TestLedgerDataCurrentResponseFields(t *testing.T) {
	mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		assert.Equal(t, "current", ledgerIndex)
		result := newDefaultLedgerDataResult(0, false)
		result.LedgerIndex = mock.currentLedgerIndex
		result.Validated = false
		return result, nil
	}
	ctx := &types.RPCContext{
		Context:  context.Background(),
		Services: &types.ServiceContainer{Ledger: mock},
	}

	result, rpcErr := (&handlers.LedgerDataMethod{}).Handle(ctx, json.RawMessage(`{"ledger_index":"current"}`))
	require.Nil(t, rpcErr)
	response := resultToMapData(t, result)
	assert.Equal(t, float64(mock.currentLedgerIndex), response["ledger_current_index"])
	assert.Equal(t, float64(mock.currentLedgerIndex), response["ledger_index"])
	assert.Equal(t, handlers.FormatLedgerHash(newDefaultLedgerDataResult(0, false).LedgerHash), response["ledger_hash"])
}

func TestLedgerDataLimitClamping(t *testing.T) {
	var capturedLimit uint32

	mock := &ledgerDataMock{
		mockLedgerService: newMockLedgerService(),
	}
	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		capturedLimit = limit
		result := newDefaultLedgerDataResult(int(limit), false)
		if limit == 0 {
			result.Marker = strings.Repeat("0", 64)
		}
		return result, nil
	}

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("JSON mode default limit is 256", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(256), capturedLimit, "JSON default limit should be 256")
	})

	t.Run("JSON mode limit 100 passes through", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"limit":        100,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(100), capturedLimit, "Limit 100 should pass through")
	})

	t.Run("JSON mode limit above 256 is clamped to 256", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"limit":        2048,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(256), capturedLimit, "JSON limit above 256 should be clamped to 256")
	})

	t.Run("JSON mode limit 257 is clamped to 256", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"limit":        257,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(256), capturedLimit, "JSON limit 257 should be clamped to 256")
	})

	t.Run("JSON mode positive limit below 16 passes through", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"limit":        5,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(5), capturedLimit, "Positive JSON limit should pass through")
	})

	t.Run("JSON mode limit 255 passes through", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"limit":        255,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(255), capturedLimit, "Limit 255 should pass through")
	})

	t.Run("Binary mode default limit is 2048", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(2048), capturedLimit, "Binary default limit should be 2048")
	})

	t.Run("Binary mode limit 500 passes through", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
			"limit":        500,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(500), capturedLimit, "Binary limit 500 should pass through")
		response := resultToMapData(t, result)
		assert.Len(t, response["state"], 500, "binary pages must not be re-capped to the JSON maximum")
	})

	t.Run("Binary mode limit above 2048 is clamped", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
			"limit":        5000,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(2048), capturedLimit, "Binary limit above 2048 should be clamped to 2048")
	})

	t.Run("Binary mode positive limit below 16 passes through", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
			"limit":        3,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(3), capturedLimit, "Positive binary limit should pass through")
	})

	t.Run("Explicit zero returns an empty page with marker", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","limit":0}`))
		require.Nil(t, rpcErr)
		assert.Equal(t, uint32(0), capturedLimit)
		response := resultToMapData(t, result)
		assert.Empty(t, response["state"])
		assert.Equal(t, strings.Repeat("0", 64), response["marker"])
		assert.NotContains(t, response, "limit")
	})

	t.Run("Negative integral limit uses the mode maximum", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","limit":-1}`))
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		assert.Equal(t, uint32(256), capturedLimit)
	})

	for _, input := range []string{
		`{"ledger_index":"current","limit":null}`,
		`{"ledger_index":"current","limit":1.5}`,
		`{"ledger_index":"current","limit":"1"}`,
	} {
		t.Run("Non-integral limit is rejected "+input, func(t *testing.T) {
			result, rpcErr := method.Handle(ctx, json.RawMessage(input))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, "Invalid field 'limit', not integer.", rpcErr.Message)
		})
	}

	t.Run("Limit above signed 32-bit range is rejected", func(t *testing.T) {
		ctx.Unlimited = true
		t.Cleanup(func() { ctx.Unlimited = false })
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","limit":2147483648}`))
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
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

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Binary false returns JSON objects", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       false,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]any)
		assert.Equal(t, 3, len(state))

		// Each item should have an index field with uppercase hex
		for _, item := range state {
			itemMap := item.(map[string]any)
			assert.Contains(t, itemMap, "index")
			indexStr := itemMap["index"].(string)
			assert.Equal(t, strings.ToUpper(indexStr), indexStr, "index should be uppercase hex")
		}
	})

	t.Run("Binary true returns hex data", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]any)
		assert.Equal(t, 3, len(state))

		// Each item should have data and index, both uppercase hex
		for _, item := range state {
			itemMap := item.(map[string]any)
			assert.Contains(t, itemMap, "data")
			assert.Contains(t, itemMap, "index")
			// data should be an uppercase hex string
			dataStr, ok := itemMap["data"].(string)
			assert.True(t, ok, "data should be a string")
			_, err := hex.DecodeString(dataStr)
			assert.NoError(t, err, "data should be valid hex")
			assert.Equal(t, strings.ToUpper(dataStr), dataStr, "data should be uppercase hex")
			// index should be uppercase hex
			indexStr := itemMap["index"].(string)
			assert.Equal(t, strings.ToUpper(indexStr), indexStr, "index should be uppercase hex")
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
		result := newDefaultLedgerDataResult(0, false)
		result.State = []types.LedgerDataItem{
			{Index: strings.Repeat("1", 64), Data: []byte{0x11, 0x00, 0x61}},
			{Index: strings.Repeat("2", 64), Data: []byte{0x11, 0x00, 0x6f}},
			{Index: strings.Repeat("3", 64), Data: []byte{0x11, 0x00, 0x61}},
		}
		result.Marker = strings.Repeat("F", 64)
		return result, nil
	}

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("RPC type filters without changing page marker", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "current",
			"binary":       true,
			"type":         "offer",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for valid type, got: %v", rpcErr)
		response := resultToMapData(t, result)
		state := response["state"].([]any)
		require.Len(t, state, 1)
		assert.Equal(t, strings.Repeat("2", 64), state[0].(map[string]any)["index"])
		assert.Equal(t, strings.Repeat("F", 64), response["marker"])
	})

	t.Run("Canonical type is case insensitive", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"ledger_index":"current","binary":true,"type":"aCcOuNtRoOt"}`))
		require.Nil(t, rpcErr)
		assert.Len(t, resultToMapData(t, result)["state"], 2)
	})

	for _, input := range []string{
		`{"ledger_index":"current","type":"MPT_Issuance"}`,
		`{"ledger_index":"current","type":"account_root"}`,
		`{"ledger_index":"current","type":"ripple_state"}`,
		`{"ledger_index":"current","type":"unknown"}`,
		`{"ledger_index":"current","type":123}`,
	} {
		t.Run("Invalid type "+input, func(t *testing.T) {
			result, rpcErr := method.Handle(ctx, json.RawMessage(input))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		})
	}
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

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("First page has marker and no limit field", func(t *testing.T) {
		callCount = 0
		params := map[string]any{
			"ledger_index": "current",
			"limit":        50,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]any)
		assert.Equal(t, 5, len(state))
		assert.Contains(t, resp, "marker")
		markerStr, ok := resp["marker"].(string)
		assert.True(t, ok, "marker should be a string")
		assert.NotEmpty(t, markerStr, "marker should not be empty")
		assert.NotContains(t, resp, "limit")
	})

	t.Run("Second page with marker has no marker and no limit", func(t *testing.T) {
		callCount = 0
		params := map[string]any{
			"ledger_index": "current",
			"limit":        50,
			"marker":       "0000000000000000000000000000000000000000000000000000000000000010",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		state := resp["state"].([]any)
		assert.Equal(t, 3, len(state))
		// No marker when all data returned
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "Last page should not have a marker")
		// No limit when no marker
		_, hasLimit := resp["limit"]
		assert.False(t, hasLimit, "limit should not be present when no marker")
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
	currentLedger := newDefaultLedgerReader(3, false)
	currentLedger.hash = ledgerHash
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		require.Equal(t, uint32(3), seq)
		return currentLedger, nil
	}

	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		return &types.LedgerDataResult{
			LedgerIndex: 3,
			LedgerHash:  ledgerHash,
			State: []types.LedgerDataItem{
				{
					Index: "0000000000000000000000000000000000000000000000000000000000000001",
					Data:  []byte{0x11, 0x00, 0x01},
				},
			},
			Validated: false,
		}, nil
	}

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params := map[string]any{
		"ledger_index": "current",
		"binary":       true,
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapData(t, result)

	// Check required top-level fields
	assert.Contains(t, resp, "ledger_current_index")
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "state")
	assert.Contains(t, resp, "validated")

	switch v := resp["ledger_current_index"].(type) {
	case float64:
		assert.Equal(t, float64(3), v)
	default:
		t.Errorf("unexpected ledger_current_index type: %T", v)
	}

	// state should be an array
	state, ok := resp["state"].([]any)
	assert.True(t, ok, "state should be an array")
	assert.Equal(t, 1, len(state))

	// State entries should have uppercase index
	entry := state[0].(map[string]any)
	indexStr := entry["index"].(string)
	assert.Equal(t, strings.ToUpper(indexStr), indexStr, "state entry index should be uppercase hex")

	// validated should be bool
	assert.Equal(t, false, resp["validated"])

	// No marker means no limit in response
	_, hasLimit := resp["limit"]
	assert.False(t, hasLimit, "limit should not be present when no marker")
}

// TestLedgerDataServiceUnavailable tests behavior when ledger service is not available
func TestLedgerDataServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerDataMethod{}

	t.Run("Nil services", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.LogDetail(), "Ledger service not available")
	})

	t.Run("Nil ledger in services", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.LogDetail(), "Ledger service not available")
	})

	t.Run("Service returns error", func(t *testing.T) {
		mock := &ledgerDataMock{
			mockLedgerService: newMockLedgerService(),
		}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			return nil, errors.New("storage unavailable")
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}

		params := map[string]any{
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
	closedLedger := &mockLedgerReader{
		seq:                 2,
		hash:                ledgerHash,
		parentHash:          parentHash,
		txMapHash:           txHash,
		stateMapHash:        accountHash,
		closed:              true,
		validated:           true,
		totalDrops:          99999999999999980,
		closeTime:           776000030,
		closeTimeResolution: 10,
		parentCloseTime:     776000020,
	}
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		require.Equal(t, uint32(2), seq)
		return closedLedger, nil
	}

	mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
		result := newDefaultLedgerDataResult(2, false)
		result.LedgerHash = ledgerHash
		return result, nil
	}

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("First query includes ledger header JSON with uppercase hashes", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "closed",
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		assert.Contains(t, resp, "ledger")

		// Top-level ledger_hash should be uppercase
		topHash := resp["ledger_hash"].(string)
		assert.Equal(t, strings.ToUpper(topHash), topHash, "top-level ledger_hash should be uppercase")

		ledger := resp["ledger"].(map[string]any)
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

		// All hashes in ledger header should be uppercase
		for _, field := range []string{"ledger_hash", "account_hash", "parent_hash", "transaction_hash"} {
			hashVal := ledger[field].(string)
			assert.Equal(t, strings.ToUpper(hashVal), hashVal, field+" should be uppercase hex")
		}
	})

	t.Run("First query includes ledger header binary with uppercase hex", func(t *testing.T) {
		params := map[string]any{
			"ledger_index": "closed",
			"binary":       true,
		}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapData(t, result)
		assert.Contains(t, resp, "ledger")

		ledger := resp["ledger"].(map[string]any)
		assert.Contains(t, ledger, "ledger_data")
		assert.Contains(t, ledger, "closed")

		// ledger_data should be an uppercase hex string
		dataStr, ok := ledger["ledger_data"].(string)
		assert.True(t, ok, "ledger_data should be a string in binary mode")
		const expectedLedgerData = "00000002016345785D89FFEC0200000000000000000000000000000000000000000000000000000000000000030000000000000000000000000000000000000000000000000000000000000001000000000000000000000000000000000000000000000000000000000000002E40D2142E40D21E0A00"
		assert.Equal(t, expectedLedgerData, dataStr)
		_, err := hex.DecodeString(dataStr)
		assert.NoError(t, err, "ledger_data should be valid hex")
		assert.Equal(t, strings.ToUpper(dataStr), dataStr, "ledger_data should be uppercase hex")
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

	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerDataMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params := map[string]any{
		"ledger_index": "current",
	}
	paramsJSON, _ := json.Marshal(params)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapData(t, result)
	state := resp["state"].([]any)
	assert.Equal(t, 0, len(state), "state should be an empty array")
}

// TestLedgerDataMarkerValidation pins rippled's doLedgerData marker rejection
// (LedgerData.cpp:57-62): a present non-string marker, or a present string that
// is not valid hex-256, returns invalidParams "Invalid field 'marker', not
// valid." instead of silently restarting from the first page.
func TestLedgerDataMarkerValidation(t *testing.T) {
	method := &handlers.LedgerDataMethod{}

	t.Run("Malformed marker string is rejected", func(t *testing.T) {
		mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			return nil, svcerr.ErrInvalidMarker
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
		params := map[string]any{"ledger_index": "current", "marker": "not-a-valid-hash"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'marker', not valid.", rpcErr.Message)
	})

	t.Run("Non-string marker is rejected before the service", func(t *testing.T) {
		called := false
		mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			called = true
			return newDefaultLedgerDataResult(1, false), nil
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
		params := map[string]any{"ledger_index": "current", "marker": 12345}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'marker', not valid.", rpcErr.Message)
		assert.False(t, called, "service must not be called for a non-string marker")
	})

	// A present JSON null is isMember==true but isString()==false in rippled, so
	// it is rejected — not conflated with an absent marker (a fresh first page).
	t.Run("Present null marker is rejected before the service", func(t *testing.T) {
		called := false
		mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			called = true
			return newDefaultLedgerDataResult(1, false), nil
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
		params := map[string]any{"ledger_index": "current", "marker": nil}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'marker', not valid.", rpcErr.Message)
		assert.False(t, called, "service must not be called for a present null marker")
	})

	// A present empty-string marker is parseHex("") → badLength in rippled, not
	// the absent-marker case. It must be rejected, not silently restart paging.
	t.Run("Empty-string marker is rejected before the service", func(t *testing.T) {
		called := false
		mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			called = true
			return newDefaultLedgerDataResult(1, false), nil
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
		params := map[string]any{"ledger_index": "current", "marker": ""}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'marker', not valid.", rpcErr.Message)
		assert.False(t, called, "service must not be called for a present empty marker")
	})

	// rippled's parseHex special-cases "0" as the all-zero key: paging starts at
	// the first entry, but because a marker is present the base-ledger header is
	// omitted. The handler normalizes "0" to its canonical 64-char form.
	t.Run("Marker '0' is the all-zero key and omits the ledger header", func(t *testing.T) {
		var forwarded string
		called := false
		mock := &ledgerDataMock{mockLedgerService: newMockLedgerService()}
		mock.getLedgerDataFn = func(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
			called = true
			forwarded = marker
			// A present marker → service returns no ledger header.
			return newDefaultLedgerDataResult(2, false), nil
		}
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: mock},
		}
		params := map[string]any{"ledger_index": "current", "marker": "0"}
		paramsJSON, _ := json.Marshal(params)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "marker '0' must be accepted, got: %v", rpcErr)
		require.NotNil(t, result)
		assert.True(t, called, "service must be called for marker '0'")
		assert.Equal(t, strings.Repeat("0", 64), forwarded,
			"marker '0' must be normalized to the canonical all-zero key")

		resp := resultToMapData(t, result)
		_, hasHeader := resp["ledger"]
		assert.False(t, hasHeader, "a present marker must omit the base-ledger header")
		state := resp["state"].([]any)
		assert.Equal(t, 2, len(state))
	})
}

// resultToMapData is a test helper for ledger_data tests
func resultToMapData(t *testing.T, result any) map[string]any {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}

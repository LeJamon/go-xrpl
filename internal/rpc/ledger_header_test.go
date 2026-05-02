package rpc

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resultToMapHeader is a test helper that JSON-round-trips a result to a map.
func resultToMapHeader(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}

// TestLedgerHeaderBasicRequest tests basic ledger_header with default params.
// Reference: rippled LedgerHeader.cpp doLedgerHeader()
func TestLedgerHeaderBasicRequest(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Default params returns validated ledger with all fields", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapHeader(t, result)

		// Top-level fields from lookupLedger equivalent
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")
		assert.Equal(t, true, resp["validated"])

		// ledger_data binary hex must be present for closed ledgers
		assert.Contains(t, resp, "ledger_data")
		ledgerData, ok := resp["ledger_data"].(string)
		assert.True(t, ok, "ledger_data should be a string")
		assert.NotEmpty(t, ledgerData)
		// All hex should be uppercase
		assert.Equal(t, strings.ToUpper(ledgerData), ledgerData, "ledger_data should be uppercase hex")

		// Nested "ledger" JSON object
		assert.Contains(t, resp, "ledger")
		ledger, ok := resp["ledger"].(map[string]interface{})
		require.True(t, ok, "ledger should be an object")

		// Verify all expected fields in the ledger object
		assert.Contains(t, ledger, "ledger_index")
		assert.Contains(t, ledger, "ledger_hash")
		assert.Contains(t, ledger, "parent_hash")
		assert.Contains(t, ledger, "total_coins")
		assert.Contains(t, ledger, "close_time")
		assert.Contains(t, ledger, "close_time_resolution")
		assert.Contains(t, ledger, "close_flags")
		assert.Contains(t, ledger, "account_hash")
		assert.Contains(t, ledger, "transaction_hash")
		assert.Contains(t, ledger, "parent_close_time")
		assert.Contains(t, ledger, "closed")
		assert.Equal(t, true, ledger["closed"])

		// ledger_index in the nested object should be a string (API v1)
		assert.Equal(t, "2", ledger["ledger_index"])

		// total_coins should be a string (matching rippled to_string(info.drops))
		assert.IsType(t, "", ledger["total_coins"])

		// close_time_human should be present when closeTime > 0
		if reader.closeTime > 0 {
			assert.Contains(t, ledger, "close_time_human")
			assert.Contains(t, ledger, "close_time_iso")
		}
	})

	t.Run("Numeric ledger_index", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_index": 2}`)
		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapHeader(t, result)
		assert.Contains(t, resp, "ledger_data")
		assert.Contains(t, resp, "ledger")
	})

	t.Run("String validated", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_index": "validated"}`)
		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapHeader(t, result)
		assert.Equal(t, true, resp["validated"])
	})

	t.Run("String closed", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_index": "closed"}`)
		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapHeader(t, result)
		assert.Contains(t, resp, "ledger_data")
	})

	t.Run("String current", func(t *testing.T) {
		// Current ledger (seq=3) is not in the mock, so it will fail
		params := json.RawMessage(`{"ledger_index": "current"}`)
		_, rpcErr := method.Handle(ctx, params)
		// Should return lgrNotFound since seq=3 is not in the mock
		require.NotNil(t, rpcErr)
	})

	t.Run("Lookup by hash", func(t *testing.T) {
		hash := reader.Hash()
		hashHex := strings.ToUpper(hex.EncodeToString(hash[:]))
		params, _ := json.Marshal(map[string]interface{}{
			"ledger_hash": hashHex,
		})

		mock.getLedgerByHashFn = func(h [32]byte) (types.LedgerReader, error) {
			if h == hash {
				return reader, nil
			}
			return nil, errors.New("not found")
		}

		result, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resp := resultToMapHeader(t, result)
		assert.Contains(t, resp, "ledger_data")
		assert.Equal(t, hashHex, resp["ledger_hash"])
	})
}

// TestLedgerHeaderBinaryFormat validates that ledger_data matches rippled's
// addRaw() serialization format.
func TestLedgerHeaderBinaryFormat(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}

	// Create a reader with known values
	var hash [32]byte
	hash[0] = 0x02
	hash[31] = 0xAA
	var parentHash [32]byte
	parentHash[0] = 0x01
	parentHash[31] = 0xAA
	var txMapHash [32]byte
	txMapHash[0] = 0x10
	var stateMapHash [32]byte
	stateMapHash[0] = 0x20

	reader := &mockLedgerReader{
		seq:                 5,
		hash:                hash,
		parentHash:          parentHash,
		txMapHash:           txMapHash,
		stateMapHash:        stateMapHash,
		closed:              true,
		validated:           true,
		totalDrops:          99999999999999980,
		closeTime:           776000030,
		closeTimeResolution: 10,
		closeFlags:          0,
		parentCloseTime:     776000020,
	}

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		return reader, nil
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapHeader(t, result)
	ledgerDataHex, ok := resp["ledger_data"].(string)
	require.True(t, ok)

	data, err := hex.DecodeString(ledgerDataHex)
	require.NoError(t, err)

	// rippled addRaw format: 4+8+32+32+32+4+4+1+1 = 118 bytes
	assert.Equal(t, 118, len(data), "ledger_data should be 118 bytes (rippled addRaw format)")

	// Parse and verify each field
	offset := 0

	// seq (uint32, 4 bytes)
	seq := binary.BigEndian.Uint32(data[offset : offset+4])
	assert.Equal(t, uint32(5), seq)
	offset += 4

	// drops (uint64, 8 bytes)
	drops := binary.BigEndian.Uint64(data[offset : offset+8])
	assert.Equal(t, uint64(99999999999999980), drops)
	offset += 8

	// parentHash (32 bytes)
	var gotParentHash [32]byte
	copy(gotParentHash[:], data[offset:offset+32])
	assert.Equal(t, parentHash, gotParentHash)
	offset += 32

	// txHash (32 bytes)
	var gotTxHash [32]byte
	copy(gotTxHash[:], data[offset:offset+32])
	assert.Equal(t, txMapHash, gotTxHash)
	offset += 32

	// accountHash (32 bytes)
	var gotStateHash [32]byte
	copy(gotStateHash[:], data[offset:offset+32])
	assert.Equal(t, stateMapHash, gotStateHash)
	offset += 32

	// parentCloseTime (uint32, 4 bytes)
	pct := binary.BigEndian.Uint32(data[offset : offset+4])
	assert.Equal(t, uint32(776000020), pct)
	offset += 4

	// closeTime (uint32, 4 bytes)
	ct := binary.BigEndian.Uint32(data[offset : offset+4])
	assert.Equal(t, uint32(776000030), ct)
	offset += 4

	// closeTimeResolution (uint8, 1 byte)
	assert.Equal(t, uint8(10), data[offset])
	offset++

	// closeFlags (uint8, 1 byte)
	assert.Equal(t, uint8(0), data[offset])
}

// TestLedgerHeaderHashFormat verifies all hash fields are uppercase hex.
func TestLedgerHeaderHashFormat(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		return reader, nil
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)

	resp := resultToMapHeader(t, result)

	// Check top-level ledger_hash
	topHash, ok := resp["ledger_hash"].(string)
	assert.True(t, ok)
	assert.Equal(t, strings.ToUpper(topHash), topHash, "top-level ledger_hash should be uppercase")

	// Check nested ledger object hashes
	ledger := resp["ledger"].(map[string]interface{})
	for _, field := range []string{"ledger_hash", "parent_hash", "account_hash", "transaction_hash"} {
		v, ok := ledger[field].(string)
		if ok && v != "" {
			assert.Equal(t, strings.ToUpper(v), v, "%s should be uppercase hex", field)
			assert.Equal(t, 64, len(v), "%s should be 64 hex chars", field)
		}
	}
}

// TestLedgerHeaderBadInput tests error handling for invalid inputs.
func TestLedgerHeaderBadInput(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	reader := newDefaultLedgerReader(2, true)
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq <= 2 {
			return reader, nil
		}
		return nil, errors.New("ledger not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Invalid string ledger_index", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_index": "potato"}`)
		result, rpcErr := method.Handle(ctx, params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Non-existent ledger_index", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_index": 999}`)
		result, rpcErr := method.Handle(ctx, params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("Invalid ledger_hash - too short", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_hash": "ABCD"}`)
		result, rpcErr := method.Handle(ctx, params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Invalid ledger_hash - not hex", func(t *testing.T) {
		params := json.RawMessage(`{"ledger_hash": "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"}`)
		result, rpcErr := method.Handle(ctx, params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Invalid JSON params", func(t *testing.T) {
		params := json.RawMessage(`{invalid json}`)
		result, rpcErr := method.Handle(ctx, params)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})
}

// TestLedgerHeaderOpenLedger tests behavior when querying an open (non-closed) ledger.
func TestLedgerHeaderOpenLedger(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	// Open ledger: not closed, not validated
	openReader := &mockLedgerReader{
		seq:                 3,
		closed:              false,
		validated:           false,
		totalDrops:          99999999999999980,
		closeTimeResolution: 10,
	}
	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 3 {
			return openReader, nil
		}
		return nil, errors.New("not found")
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	params := json.RawMessage(`{"ledger_index": "current"}`)
	result, rpcErr := method.Handle(ctx, params)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapHeader(t, result)

	// Open ledger: lookupLedger sets ledger_current_index instead of ledger_hash/ledger_index
	assert.Contains(t, resp, "ledger_current_index")
	assert.NotContains(t, resp, "ledger_hash", "open ledger should not have ledger_hash at top level")

	// validated should be false
	assert.Equal(t, false, resp["validated"])

	// ledger_data is always present (rippled doLedgerHeader sets it unconditionally)
	assert.Contains(t, resp, "ledger_data")

	// Nested ledger object should have closed=false
	ledger := resp["ledger"].(map[string]interface{})
	assert.Equal(t, false, ledger["closed"])
	// Open ledger should only have parent_hash, ledger_index, closed
	assert.Contains(t, ledger, "parent_hash")
	assert.Contains(t, ledger, "ledger_index")
}

// TestLedgerHeaderCloseTimeEstimated tests the close_time_estimated field
// when the LCFNoConsensusTime flag is set.
func TestLedgerHeaderCloseTimeEstimated(t *testing.T) {
	mock := &ledgerMock{
		mockLedgerService: newMockLedgerService(),
	}
	// Set closeFlags with LCFNoConsensusTime (0x01)
	reader := newDefaultLedgerReader(2, true)
	reader.closeFlags = 0x01

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		return reader, nil
	}
	services := &types.ServiceContainer{Ledger: mock}

	method := &handlers.LedgerHeaderMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)

	resp := resultToMapHeader(t, result)
	ledger := resp["ledger"].(map[string]interface{})

	// When LCFNoConsensusTime is set, close_time_estimated should be true
	assert.Equal(t, true, ledger["close_time_estimated"])
}

// TestLedgerHeaderMethodMetadata tests RequiredRole, SupportedApiVersions, RequiredCondition.
func TestLedgerHeaderMethodMetadata(t *testing.T) {
	method := &handlers.LedgerHeaderMethod{}

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("Supports only API version 1", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.NotContains(t, versions, types.ApiVersion2)
		assert.NotContains(t, versions, types.ApiVersion3)
	})

	t.Run("No required condition", func(t *testing.T) {
		assert.Equal(t, types.NoCondition, method.RequiredCondition())
	})
}

// TestLedgerHeaderServiceUnavailable tests behavior when services are not available.
func TestLedgerHeaderServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerHeaderMethod{}

	t.Run("Nil services", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Nil ledger in services", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})
}

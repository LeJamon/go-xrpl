package rpc

import (
	"context"
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

// ledgerClosedMock wraps mockLedgerService and overrides GetLedgerBySequence
type ledgerClosedMock struct {
	*mockLedgerService
	getLedgerBySequenceFn func(seq uint32) (types.LedgerReader, error)
}

func (m *ledgerClosedMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

// TestLedgerClosedBasicSuccess tests the basic success case for ledger_closed
// Based on rippled LedgerClosed_test.cpp testMonitorRoot()
func TestLedgerClosedBasicSuccess(t *testing.T) {
	mock := &ledgerClosedMock{
		mockLedgerService: newMockLedgerService(),
	}

	// Create a mock closed ledger with a known hash
	var closedHash [32]byte
	closedHash[0] = 0xCC
	closedHash[1] = 0xC3
	closedHash[31] = 0xA5

	closedReader := &mockLedgerReader{
		seq:       2,
		hash:      closedHash,
		closed:    true,
		validated: true,
	}

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return closedReader, nil
		}
		return nil, errors.New("not found")
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerClosedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr, "Expected no error, got: %v", rpcErr)
	require.NotNil(t, result)

	resp := resultToMapClosed(t, result)

	// Should contain ledger_hash and ledger_index
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")

	// ledger_index should match the closed ledger sequence
	switch v := resp["ledger_index"].(type) {
	case float64:
		assert.Equal(t, float64(2), v)
	default:
		t.Errorf("unexpected ledger_index type: %T", v)
	}

	// ledger_hash should match the expected hash
	expectedHashStr := hex.EncodeToString(closedHash[:])
	assert.Equal(t, expectedHashStr, resp["ledger_hash"])
}

// TestLedgerClosedHashFormat tests that the hash is properly formatted
// Based on rippled LedgerClosed_test.cpp testMonitorRoot() verifying hash format
func TestLedgerClosedHashFormat(t *testing.T) {
	mock := &ledgerClosedMock{
		mockLedgerService: newMockLedgerService(),
	}

	var closedHash [32]byte
	closedHash[0] = 0xE8
	closedHash[1] = 0x6D
	closedHash[2] = 0xE7
	closedHash[31] = 0x4E

	closedReader := &mockLedgerReader{
		seq:       2,
		hash:      closedHash,
		closed:    true,
		validated: true,
	}

	mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
		if seq == 2 {
			return closedReader, nil
		}
		return nil, errors.New("not found")
	}

	cleanup := setupTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerClosedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resp := resultToMapClosed(t, result)
	hashStr, ok := resp["ledger_hash"].(string)
	require.True(t, ok, "ledger_hash should be a string")

	// Hash should be 64 characters (32 bytes hex-encoded)
	assert.Equal(t, 64, len(hashStr), "ledger_hash should be 64 hex characters")

	// Hash should be valid hex
	_, err := hex.DecodeString(hashStr)
	assert.NoError(t, err, "ledger_hash should be valid hex")

	// Verify we can round-trip decode the hash
	decoded, err := hex.DecodeString(hashStr)
	require.NoError(t, err)
	assert.Equal(t, 32, len(decoded), "Decoded hash should be 32 bytes")

	// Verify the hash is lowercase hex (the handler uses hex.EncodeToString which produces lowercase)
	assert.Equal(t, strings.ToLower(hashStr), hashStr,
		"ledger_hash should be lowercase hex from hex.EncodeToString")
}

// TestLedgerClosedServiceUnavailable tests behavior when ledger service is not available
func TestLedgerClosedServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerClosedMethod{}
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

	t.Run("Closed ledger index is zero", func(t *testing.T) {
		mock := &ledgerClosedMock{
			mockLedgerService: &mockLedgerService{
				closedLedgerIndex: 0,
			},
		}
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: mock}
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, -1, rpcErr.Code, "Should return lgrNotFound error code")
	})

	t.Run("GetLedgerBySequence returns error", func(t *testing.T) {
		mock := &ledgerClosedMock{
			mockLedgerService: newMockLedgerService(),
		}
		mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
			return nil, errors.New("storage error")
		}
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: mock}
		defer func() { types.Services = oldServices }()

		result, rpcErr := method.Handle(ctx, nil)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, -1, rpcErr.Code)
	})
}

// TestLedgerClosedMethodMetadata tests the method's metadata
func TestLedgerClosedMethodMetadata(t *testing.T) {
	method := &handlers.LedgerClosedMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"ledger_closed should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// resultToMapClosed is a test helper for ledger_closed tests
func resultToMapClosed(t *testing.T, result interface{}) map[string]interface{} {
	t.Helper()
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]interface{}
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)
	return resp
}

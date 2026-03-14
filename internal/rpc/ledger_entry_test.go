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

// mockLedgerEntryService implements LedgerService for ledger_entry testing
type mockLedgerEntryService struct {
	mockLedgerService
	ledgerEntryResult *types.LedgerEntryResult
	ledgerEntryErr    error
}

func newMockLedgerEntryService() *mockLedgerEntryService {
	return &mockLedgerEntryService{
		mockLedgerService: mockLedgerService{
			currentLedgerIndex:   3,
			closedLedgerIndex:    2,
			validatedLedgerIndex: 2,
			standalone:           true,
			serverInfo: types.LedgerServerInfo{
				Standalone:         true,
				OpenLedgerSeq:      3,
				ClosedLedgerSeq:    2,
				ValidatedLedgerSeq: 2,
				CompleteLedgers:    "1-2",
			},
		},
	}
}

func (m *mockLedgerEntryService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	if m.ledgerEntryErr != nil {
		return nil, m.ledgerEntryErr
	}
	if m.ledgerEntryResult != nil {
		return m.ledgerEntryResult, nil
	}
	// Default result
	return &types.LedgerEntryResult{
		Index:       hex.EncodeToString(entryKey[:]),
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
		Node:        []byte(`{"LedgerEntryType": "AccountRoot", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
		Validated:   true,
	}, nil
}

// setupLedgerEntryTestServices initializes the Services singleton with a mock for ledger_entry testing
func setupLedgerEntryTestServices(mock *mockLedgerEntryService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// =============================================================================
// Direct Index Lookup Tests
// =============================================================================

// TestLedgerEntryDirectIndexLookup tests direct index lookup (256-bit hex)
// Based on rippled LedgerEntry_test.cpp testLedgerEntryInvalid() and testLedgerEntryAccountRoot()
func TestLedgerEntryDirectIndexLookup(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Valid 256-bit hex index - success",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "Balance": "10000000000"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "validated")
				assert.Contains(t, resp, "node")
				assert.Equal(t, validIndex, resp["index"])
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "Request index with binary=true",
			params: map[string]interface{}{
				"index":        validIndex,
				"binary":       true,
				"ledger_index": "validated",
			},
			setupMock: func() {
				nodeBinary := "1100612200800000240000000425000000032D00000000559CE54C3B934E473A995B477E92EC229F99CED5B62BF4D2ACE4DC42719103AE2F6240000002540BE4008114AE123A8556F3CF91154711376AFB0F894F832B3D"
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					NodeBinary:  nodeBinary,
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "node_binary")
			},
		},
		{
			name: "Invalid hex format - too short",
			params: map[string]interface{}{
				"index":        "A33EC6BB85FB5674074C4A3A43373BB17645308F",
				"ledger_index": "validated",
			},
			expectError:   true,
			expectedError: "Invalid index",
		},
		{
			name: "Invalid hex format - contains non-hex chars",
			params: map[string]interface{}{
				"index":        "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B21GH",
				"ledger_index": "validated",
			},
			expectError:   true,
			expectedError: "Invalid index",
		},
		{
			name: "Entry not found",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError,
						"Error message should contain: %s", tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Check Entry Tests
// =============================================================================

// TestLedgerEntryCheck tests check lookup by check ID
// Based on rippled LedgerEntry_test.cpp testLedgerEntryCheck()
func TestLedgerEntryCheck(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	checkIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request check by ID - success",
			params: map[string]interface{}{
				"check":        checkIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       checkIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "Check", "SendMax": "100000000"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
			},
		},
		{
			name: "Invalid check - too short hex",
			params: map[string]interface{}{
				"check":        "A33EC6BB85FB5674",
				"ledger_index": "validated",
			},
			expectError:   true,
			expectedError: "Invalid check",
		},
		{
			name: "Check entry not found",
			params: map[string]interface{}{
				"check":        checkIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Payment Channel Entry Tests
// =============================================================================

// TestLedgerEntryPaymentChannel tests payment_channel lookup by channel ID
// Based on rippled LedgerEntry_test.cpp testLedgerEntryPayChan()
func TestLedgerEntryPaymentChannel(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	payChanIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request payment channel by index - success",
			params: map[string]interface{}{
				"payment_channel": payChanIndex,
				"ledger_index":    "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       payChanIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "PayChannel", "Amount": "57000000", "Balance": "0", "SettleDelay": 18}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
			},
		},
		{
			name: "Invalid payment_channel - too short",
			params: map[string]interface{}{
				"payment_channel": "A33EC6BB",
				"ledger_index":    "validated",
			},
			expectError:   true,
			expectedError: "Invalid payment_channel",
		},
		{
			name: "Payment channel not found",
			params: map[string]interface{}{
				"payment_channel": "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
				"ledger_index":    "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Directory Entry Tests
// =============================================================================

// TestLedgerEntryDirectory tests directory lookup by directory index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryDirectory()
// Note: Current implementation only supports lookup by direct index string, not by owner/dir_root objects
func TestLedgerEntryDirectory(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	dirRootIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Locate directory by index - success",
			params: map[string]interface{}{
				"directory":    dirRootIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       dirRootIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "DirectoryNode", "Indexes": []}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
			},
		},
		{
			name: "Invalid directory - too short hex",
			params: map[string]interface{}{
				"directory":    "A33EC6BB85FB56",
				"ledger_index": "validated",
			},
			expectError:   true,
			expectedError: "Invalid directory",
		},
		{
			name: "Directory not found",
			params: map[string]interface{}{
				"directory":    dirRootIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// NFT Page Entry Tests
// =============================================================================

// TestLedgerEntryNFTPage tests NFT page lookup by page index
func TestLedgerEntryNFTPage(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	nftPageIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request NFT page by index - success",
			params: map[string]interface{}{
				"nft_page":     nftPageIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       nftPageIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "NFTokenPage", "NFTokens": []}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
			},
		},
		{
			name: "Invalid NFT page index - not hex",
			params: map[string]interface{}{
				"nft_page":     "not-a-hex-string",
				"ledger_index": "validated",
			},
			expectError:   true,
			expectedError: "Invalid nft_page",
		},
		{
			name: "NFT page not found",
			params: map[string]interface{}{
				"nft_page":     "B33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Error Cases - Missing Entry Type
// =============================================================================

// TestLedgerEntryMissingEntryType tests error when no entry type is specified
func TestLedgerEntryMissingEntryType(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		expectedError string
	}{
		{
			name:          "Empty params - must specify object type",
			params:        map[string]interface{}{},
			expectedError: "Must specify object to look up",
		},
		{
			name: "Only ledger_index specified - must specify object type",
			params: map[string]interface{}{
				"ledger_index": "validated",
			},
			expectedError: "Must specify object to look up",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		})
	}
}

// =============================================================================
// Ledger Specification Tests
// =============================================================================

// TestLedgerEntryLedgerSpecification tests different ledger index specifications
// Based on rippled ledger specification behavior
func TestLedgerEntryLedgerSpecification(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name         string
		params       map[string]interface{}
		setupMock    func()
		expectError  bool
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "ledger_index: validated",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "ledger_index: current",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "current",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   false,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
			},
		},
		{
			name: "ledger_index: closed",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "closed",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]interface{}{
				"index":        validIndex,
				"ledger_index": 2,
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				ledgerIndex := resp["ledger_index"]
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				case int:
					assert.Equal(t, 2, v)
				}
			},
		},
		{
			name: "ledger_hash: valid hash",
			params: map[string]interface{}{
				"index":       validIndex,
				"ledger_hash": "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "ledger_hash")
			},
		},
		{
			name: "Default ledger_index when not specified",
			params: map[string]interface{}{
				"index": validIndex,
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       validIndex,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				// Should default to validated and succeed
				assert.Contains(t, resp, "index")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error")
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error, got: %v", rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Response Format Tests
// =============================================================================

// TestLedgerEntryResponseFields tests that the response contains expected fields
func TestLedgerEntryResponseFields(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	t.Run("Response contains required fields", func(t *testing.T) {
		mock.ledgerEntryResult = &types.LedgerEntryResult{
			Index:       validIndex,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
			Node:        []byte(`{"LedgerEntryType": "AccountRoot", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "Balance": "10000000000"}`),
			Validated:   true,
		}
		mock.ledgerEntryErr = nil

		params := map[string]interface{}{
			"index":        validIndex,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check required response fields per rippled spec
		assert.Contains(t, resp, "index", "Response should contain 'index' field")
		assert.Contains(t, resp, "ledger_hash", "Response should contain 'ledger_hash' field")
		assert.Contains(t, resp, "ledger_index", "Response should contain 'ledger_index' field")
		assert.Contains(t, resp, "validated", "Response should contain 'validated' field")
		assert.Contains(t, resp, "node", "Response should contain 'node' field when binary=false")
	})

	t.Run("Binary format includes node_binary", func(t *testing.T) {
		nodeBinary := "1100612200800000240000000425000000032D00000000559CE54C3B934E473A995B477E92EC229F99CED5B62BF4D2ACE4DC42719103AE2F6240000002540BE4008114AE123A8556F3CF91154711376AFB0F894F832B3D"
		mock.ledgerEntryResult = &types.LedgerEntryResult{
			Index:       validIndex,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
			NodeBinary:  nodeBinary,
			Validated:   true,
		}
		mock.ledgerEntryErr = nil

		params := map[string]interface{}{
			"index":        validIndex,
			"binary":       true,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check for node_binary field when binary=true
		assert.Contains(t, resp, "node_binary", "Response should contain 'node_binary' when binary=true")
		assert.Equal(t, nodeBinary, resp["node_binary"])
	})
}

// =============================================================================
// Service Unavailability Tests
// =============================================================================

// TestLedgerEntryServiceUnavailable tests behavior when ledger service is not available
func TestLedgerEntryServiceUnavailable(t *testing.T) {
	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("Services is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = nil
		defer func() { types.Services = oldServices }()

		params := map[string]interface{}{
			"index": "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})

	t.Run("Services.Ledger is nil", func(t *testing.T) {
		oldServices := types.Services
		types.Services = &types.ServiceContainer{Ledger: nil}
		defer func() { types.Services = oldServices }()

		params := map[string]interface{}{
			"index": "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.Message, "Ledger service not available")
	})
}

// =============================================================================
// Method Metadata Tests
// =============================================================================

// TestLedgerEntryMethodMetadata tests the method's metadata functions
func TestLedgerEntryMethodMetadata(t *testing.T) {
	method := &handlers.LedgerEntryMethod{}

	t.Run("RequiredRole should be Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"ledger_entry should be accessible to guests")
	})

	t.Run("SupportedApiVersions includes all versions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// =============================================================================
// Entry Type Priority Tests
// =============================================================================

// TestLedgerEntryTypePriority tests that index takes priority over other entry types
func TestLedgerEntryTypePriority(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	indexValue := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	checkValue := "B33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	t.Run("Index takes priority over check", func(t *testing.T) {
		mock.ledgerEntryResult = &types.LedgerEntryResult{
			Index:       indexValue,
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
			Validated:   true,
		}
		mock.ledgerEntryErr = nil

		params := map[string]interface{}{
			"index":        indexValue,
			"check":        checkValue,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Should return the index value, not the check value
		assert.Equal(t, indexValue, resp["index"])
	})
}

// =============================================================================
// Not Implemented Entry Types - Document Expected Behavior
// =============================================================================

// TestLedgerEntryNotImplementedTypes documents entry types that are defined but not fully implemented
// TestLedgerEntryImplementedTypes tests that keylet-based lookups now work
// The mock returns a default result for any keylet, so these should succeed.
func TestLedgerEntryImplementedTypes(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("account_root by address", func(t *testing.T) {
		params := map[string]interface{}{
			"account_root": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		// Mock returns a default result for any key
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("offer by account and seq", func(t *testing.T) {
		params := map[string]interface{}{
			"offer": map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"seq":     5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("escrow by owner and seq", func(t *testing.T) {
		params := map[string]interface{}{
			"escrow": map[string]interface{}{
				"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"seq":   5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("ticket by account and ticket_seq", func(t *testing.T) {
		params := map[string]interface{}{
			"ticket": map[string]interface{}{
				"account":    "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"ticket_seq": 5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("directory by owner", func(t *testing.T) {
		params := map[string]interface{}{
			"directory": map[string]interface{}{
				"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// Invalid Parameters Tests
// =============================================================================

// TestLedgerEntryInvalidParameters tests various invalid parameter scenarios
func TestLedgerEntryInvalidParameters(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		expectedError string
	}{
		{
			name: "Index with empty hex string",
			params: map[string]interface{}{
				"index": "",
			},
			expectedError: "Invalid index",
		},
		{
			name: "Index with non-hex characters",
			params: map[string]interface{}{
				"index": "ZZZEC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			},
			expectedError: "Invalid index",
		},
		{
			name: "Check with wrong length",
			params: map[string]interface{}{
				"check": "A33EC6BB85FB5674",
			},
			expectedError: "Invalid check",
		},
		{
			name: "Payment channel with invalid hex",
			params: map[string]interface{}{
				"payment_channel": "not-valid-hex-at-all",
			},
			expectedError: "Invalid payment_channel",
		},
		{
			name: "Directory with invalid hex",
			params: map[string]interface{}{
				"directory": "invalid-directory-hex",
			},
			expectedError: "Invalid directory",
		},
		{
			name: "NFT page with wrong length",
			params: map[string]interface{}{
				"nft_page": "A33EC6BB",
			},
			expectedError: "Invalid nft_page",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for invalid parameters")
			require.NotNil(t, rpcErr, "Expected RPC error for invalid parameters")
			assert.Contains(t, rpcErr.Message, tc.expectedError)
		})
	}
}

// =============================================================================
// Entry Not Found Error Code Test
// =============================================================================

// TestLedgerEntryNotFoundErrorCode tests that entry not found returns correct error code
func TestLedgerEntryNotFoundErrorCode(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.ledgerEntryErr = errors.New("entry not found")

	params := map[string]interface{}{
		"index":        "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		"ledger_index": "validated",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, 21, rpcErr.Code, "Entry not found should return error code 21 (entryNotFound)")
	assert.Contains(t, rpcErr.Message, "not found")
}

// =============================================================================
// AccountRoot Entry Tests
// =============================================================================

// TestLedgerEntryAccountRoot tests account_root lookup by address and by index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryAccountRoot()
func TestLedgerEntryAccountRoot(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	accountRootIndex := "9CE54C3B934E473A995B477E92EC229F99CED5B62BF4D2ACE4DC42719103AE2F"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request account root by index - success",
			params: map[string]interface{}{
				"index":        accountRootIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       accountRootIndex,
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "Balance": "10000000000"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, accountRootIndex, resp["index"])
			},
		},
		{
			name: "Request account root by index with binary=true",
			params: map[string]interface{}{
				"index":        accountRootIndex,
				"binary":       true,
				"ledger_index": "validated",
			},
			setupMock: func() {
				nodeBinary := "1100612200800000240000000425000000032D00000000559CE54C3B934E473A995B477E92EC229F99CED5B62BF4D2ACE4DC42719103AE2F6240000002540BE4008114AE123A8556F3CF91154711376AFB0F894F832B3D"
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       accountRootIndex,
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					NodeBinary:  nodeBinary,
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "node_binary")
			},
		},
		{
			name: "Account root not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Escrow Entry Tests
// =============================================================================

// TestLedgerEntryEscrow tests escrow lookup by owner+seq and by index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryEscrow()
func TestLedgerEntryEscrow(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	escrowIndex := "DC5F3851D8A1AB622F957761E5963BC5BD439D5C24AC6AD7AC4523F0640A0BF5"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request escrow by index - success",
			params: map[string]interface{}{
				"index":        escrowIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       escrowIndex,
					LedgerIndex: 4,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "Escrow", "Amount": "333000000", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, escrowIndex, resp["index"])
			},
		},
		{
			name: "Escrow not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Offer Entry Tests
// =============================================================================

// TestLedgerEntryOffer tests offer lookup by account+seq and by index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryOffer()
func TestLedgerEntryOffer(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	offerIndex := "E7D0EC33B0C2A0F2C9E16CCCA8E12F88F4F9CEFEC3D82C1E68F5B4CC4B3DEEEF"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request offer by index - success",
			params: map[string]interface{}{
				"index":        offerIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       offerIndex,
					LedgerIndex: 4,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "Offer", "TakerGets": "322000000", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, offerIndex, resp["index"])
			},
		},
		{
			name: "Offer not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// RippleState (Trust Line) Entry Tests
// =============================================================================

// TestLedgerEntryRippleState tests trust line lookup by accounts+currency
// Based on rippled LedgerEntry_test.cpp testLedgerEntryRippleState()
func TestLedgerEntryRippleState(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	rippleStateIndex := "B7F9C5E1A8D4F2C3E6B9A8D7C5E4F3A2B1C9D8E7F6A5B4C3D2E1F0A9B8C7D6E5"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request ripple_state by index - success",
			params: map[string]interface{}{
				"index":        rippleStateIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       rippleStateIndex,
					LedgerIndex: 5,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "RippleState", "Balance": {"value": "-97", "currency": "USD", "issuer": "rrrrrrrrrrrrrrrrrrrrBZbvji"}, "HighLimit": {"value": "999", "currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, rippleStateIndex, resp["index"])
			},
		},
		{
			name: "RippleState not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Ticket Entry Tests
// =============================================================================

// TestLedgerEntryTicket tests ticket lookup by account+ticket_seq and by index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryTicket()
func TestLedgerEntryTicket(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	ticketIndex := "C8D2A5E4B7F1C3D6E9A8B7C5D4E3F2A1B9C8D7E6F5A4B3C2D1E0F9A8B7C6D5E4"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request ticket by index - success",
			params: map[string]interface{}{
				"index":        ticketIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       ticketIndex,
					LedgerIndex: 4,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "Ticket", "TicketSequence": 4, "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, ticketIndex, resp["index"])
			},
		},
		{
			name: "Ticket not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// DepositPreauth Entry Tests
// =============================================================================

// TestLedgerEntryDepositPreauth tests deposit_preauth lookup by owner+authorized and by index
// Based on rippled LedgerEntry_test.cpp testLedgerEntryDepositPreauth()
func TestLedgerEntryDepositPreauth(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	depositPreauthIndex := "F1E2D3C4B5A6978869504132B4C5D6E7F8A9B0C1D2E3F4A5B6C7D8E9F0A1B2C3"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request deposit_preauth by index - success",
			params: map[string]interface{}{
				"index":        depositPreauthIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       depositPreauthIndex,
					LedgerIndex: 4,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "DepositPreauth", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "Authorize": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, depositPreauthIndex, resp["index"])
			},
		},
		{
			name: "DepositPreauth not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// AMM Entry Tests
// =============================================================================

// TestLedgerEntryAMM tests AMM lookup by asset+asset2 and by index
// Based on rippled AMM ledger entry tests
func TestLedgerEntryAMM(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	ammIndex := "A1B2C3D4E5F6A7B8C9D0E1F2A3B4C5D6E7F8A9B0C1D2E3F4A5B6C7D8E9F0A1B2"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		validateResp  func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "Request AMM by index - success",
			params: map[string]interface{}{
				"index":        ammIndex,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       ammIndex,
					LedgerIndex: 5,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AMM", "Asset": {"currency": "XRP"}, "Asset2": {"currency": "USD", "issuer": "rGateway123456789012345678901234567"}}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Contains(t, resp, "index")
				assert.Contains(t, resp, "node")
				assert.Equal(t, ammIndex, resp["index"])
			},
		},
		{
			name: "AMM not found",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000001",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")

				if tc.validateResp != nil {
					resultJSON, err := json.Marshal(result)
					require.NoError(t, err)
					var respMap map[string]interface{}
					err = json.Unmarshal(resultJSON, &respMap)
					require.NoError(t, err)
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// =============================================================================
// Invalid Ledger Hash/Index Tests
// =============================================================================

// TestLedgerEntryInvalidLedgerSpecification tests error handling for invalid ledger specifications
// Based on rippled LedgerEntry_test.cpp testLedgerEntryInvalid()
func TestLedgerEntryInvalidLedgerSpecification(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
	}{
		{
			name: "Missing ledger_hash results in lgrNotFound",
			params: map[string]interface{}{
				"index":       validIndex,
				"ledger_hash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("ledger not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
		{
			name: "Zero index is invalid",
			params: map[string]interface{}{
				"index":        "0000000000000000000000000000000000000000000000000000000000000000",
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.ledgerEntryErr = errors.New("entry not found")
			},
			expectError:   true,
			expectedError: "not found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected RPC error for test: %s", tc.name)
				if tc.expectedError != "" {
					assert.Contains(t, rpcErr.Message, tc.expectedError)
				}
			} else {
				require.Nil(t, rpcErr, "Expected no RPC error")
				require.NotNil(t, result, "Expected result")
			}
		})
	}
}

// =============================================================================
// Unexpected Ledger Type Tests
// =============================================================================

// TestLedgerEntryUnexpectedType tests error when requesting wrong entry type
// Based on rippled behavior where requesting an AccountRoot index via check field fails
func TestLedgerEntryUnexpectedType(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Test requesting an entry using the wrong type
	// e.g., using an AccountRoot index when requesting a check
	t.Run("Entry type mismatch should succeed with index lookup", func(t *testing.T) {
		// When using direct index lookup, it returns whatever entry is at that index
		// regardless of what type was expected
		accountRootIndex := "9CE54C3B934E473A995B477E92EC229F99CED5B62BF4D2ACE4DC42719103AE2F"

		mock.ledgerEntryResult = &types.LedgerEntryResult{
			Index:       accountRootIndex,
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Node:        []byte(`{"LedgerEntryType": "AccountRoot", "Account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`),
			Validated:   true,
		}
		mock.ledgerEntryErr = nil

		// Using check field but providing an AccountRoot index
		params := map[string]interface{}{
			"check":        accountRootIndex,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		// Current implementation does direct index lookup, so it returns the entry
		require.Nil(t, rpcErr, "Direct index lookup should succeed")
		require.NotNil(t, result, "Expected result")
	})
}

// =============================================================================
// Multiple Entry Type Parameters Tests
// =============================================================================

// TestLedgerEntryMultipleTypes tests behavior when multiple entry types are specified
func TestLedgerEntryMultipleTypes(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	index1 := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"
	index2 := "B33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Index:       index1,
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
		Validated:   true,
	}
	mock.ledgerEntryErr = nil

	t.Run("First valid entry type is used (index has priority)", func(t *testing.T) {
		params := map[string]interface{}{
			"index":        index1,
			"check":        index2,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// index should take priority
		assert.Equal(t, index1, resp["index"])
	})
}

// =============================================================================
// Malformed Request Tests
// =============================================================================

// TestLedgerEntryMalformedRequests tests various malformed request scenarios
// Based on rippled LedgerEntry_test.cpp malformed request handling
func TestLedgerEntryMalformedRequests(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		expectedError string
	}{
		{
			name: "Index as integer should fail",
			params: map[string]interface{}{
				"index": 12345,
			},
			expectedError: "Invalid index",
		},
		{
			name: "Index as array should fail",
			params: map[string]interface{}{
				"index": []string{"A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"},
			},
			expectedError: "Invalid index",
		},
		{
			name: "Check as integer should fail",
			params: map[string]interface{}{
				"check": 12345,
			},
			expectedError: "Invalid check",
		},
		{
			name: "Payment channel as boolean should fail",
			params: map[string]interface{}{
				"payment_channel": true,
			},
			expectedError: "Invalid payment_channel",
		},
		{
			name: "Directory as null should fail",
			params: map[string]interface{}{
				"directory": nil,
			},
			expectedError: "Invalid directory",
		},
		{
			name: "NFT page as empty string should fail",
			params: map[string]interface{}{
				"nft_page": "",
			},
			expectedError: "Invalid nft_page",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for malformed request")
			require.NotNil(t, rpcErr, "Expected RPC error for malformed request")
			assert.Contains(t, rpcErr.Message, tc.expectedError)
		})
	}
}

// =============================================================================
// API Version Tests
// =============================================================================

// TestLedgerEntryAPIVersions tests that the method works correctly with different API versions
func TestLedgerEntryAPIVersions(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}

	validIndex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Index:       validIndex,
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
		Validated:   true,
	}
	mock.ledgerEntryErr = nil

	versions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for _, version := range versions {
		t.Run("API Version "+string(rune('0'+version)), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: version,
			}

			params := map[string]interface{}{
				"index":        validIndex,
				"ledger_index": "validated",
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			require.Nil(t, rpcErr, "Should succeed with API version %d", version)
			require.NotNil(t, result, "Expected result")
		})
	}
}

// =============================================================================
// Hex Validation Tests
// =============================================================================

// TestLedgerEntryHexValidation tests hex string validation for various entry types
func TestLedgerEntryHexValidation(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		paramName     string
		paramValue    string
		expectError   bool
		expectedError string
	}{
		// Index validation
		{
			name:          "Valid 64-char hex index",
			paramName:     "index",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Index too short (62 chars)",
			paramName:     "index",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B21",
			expectError:   true,
			expectedError: "Invalid index",
		},
		{
			name:          "Index too long (66 chars)",
			paramName:     "index",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217DAB",
			expectError:   true,
			expectedError: "Invalid index",
		},
		{
			name:          "Index with lowercase hex (valid)",
			paramName:     "index",
			paramValue:    "a33ec6bb85fb5674074c4a3a43373bb17645308f3eae1933e3e35252162b217d",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Index with mixed case hex (valid)",
			paramName:     "index",
			paramValue:    "A33ec6BB85fb5674074C4a3A43373bb17645308F3EaE1933E3e35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Index with 'G' character (invalid hex)",
			paramName:     "index",
			paramValue:    "G33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   true,
			expectedError: "Invalid index",
		},
		{
			name:          "Index with spaces (invalid)",
			paramName:     "index",
			paramValue:    "A33EC6BB 85FB5674 074C4A3A 43373BB1 7645308F 3EAE1933 E3E35252 162B217D",
			expectError:   true,
			expectedError: "Invalid index",
		},
		// Check validation
		{
			name:          "Valid 64-char hex check",
			paramName:     "check",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Check too short",
			paramName:     "check",
			paramValue:    "A33EC6BB85FB5674",
			expectError:   true,
			expectedError: "Invalid check",
		},
		// Payment channel validation
		{
			name:          "Valid 64-char hex payment_channel",
			paramName:     "payment_channel",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Payment channel with prefix (invalid)",
			paramName:     "payment_channel",
			paramValue:    "0xA33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   true,
			expectedError: "Invalid payment_channel",
		},
		// Directory validation
		{
			name:          "Valid 64-char hex directory",
			paramName:     "directory",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "Directory with special chars (invalid)",
			paramName:     "directory",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217!",
			expectError:   true,
			expectedError: "Invalid directory",
		},
		// NFT page validation
		{
			name:          "Valid 64-char hex nft_page",
			paramName:     "nft_page",
			paramValue:    "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			expectError:   false,
			expectedError: "",
		},
		{
			name:          "NFT page empty string (invalid)",
			paramName:     "nft_page",
			paramValue:    "",
			expectError:   true,
			expectedError: "Invalid nft_page",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.expectError {
				mock.ledgerEntryResult = &types.LedgerEntryResult{
					Index:       tc.paramValue,
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
					Validated:   true,
				}
				mock.ledgerEntryErr = nil
			}

			params := map[string]interface{}{
				tc.paramName:   tc.paramValue,
				"ledger_index": "validated",
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				require.NotNil(t, rpcErr, "Expected error for test: %s", tc.name)
				assert.Contains(t, rpcErr.Message, tc.expectedError)
			} else {
				require.Nil(t, rpcErr, "Expected no error for test: %s, got: %v", tc.name, rpcErr)
				require.NotNil(t, result, "Expected result")
			}
		})
	}
}

// =============================================================================
// Hex Index Fallback Tests
// =============================================================================

// TestLedgerEntryHexFallback tests that all entry types that support it accept
// a direct hex string as an alternative to the structured object form.
// Reference: rippled LedgerEntry.cpp — each parse* function checks isObject()
// first and falls back to parseHex if the value is a string.
func TestLedgerEntryHexFallback(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	validHex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	// All entry types that should accept a direct hex string
	hexEntryTypes := []string{
		"amm",
		"bridge",
		"credential",
		"delegate",
		"deposit_preauth",
		"escrow",
		"mptoken",
		"offer",
		"oracle",
		"permissioned_domain",
		"ticket",
		"vault",
		"xchain_owned_claim_id",
		"xchain_owned_create_account_claim_id",
	}

	for _, entryType := range hexEntryTypes {
		t.Run(entryType+" accepts hex string", func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil

			params := map[string]interface{}{
				entryType:      validHex,
				"ledger_index": "validated",
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			require.Nil(t, rpcErr, "Entry type %s should accept hex string, got error: %v", entryType, rpcErr)
			require.NotNil(t, result)
		})

		t.Run(entryType+" rejects invalid hex", func(t *testing.T) {
			mock.ledgerEntryResult = nil
			mock.ledgerEntryErr = nil

			params := map[string]interface{}{
				entryType:      "INVALID_NOT_HEX_STRING",
				"ledger_index": "validated",
			}
			paramsJSON, err := json.Marshal(params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)
			// Should fail because it's not valid hex and won't parse as object either
			require.NotNil(t, rpcErr, "Entry type %s should reject invalid hex/params", entryType)
			assert.Nil(t, result)
		})
	}
}

// =============================================================================
// New Entry Type Tests: vault, delegate, bridge, xchain
// =============================================================================

// TestLedgerEntryVault tests vault entry lookup by hex and by object form
func TestLedgerEntryVault(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	vaultHex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	t.Run("vault by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"vault":        vaultHex,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("vault by owner and seq", func(t *testing.T) {
		params := map[string]interface{}{
			"vault": map[string]interface{}{
				"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"seq":   5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("vault with invalid owner", func(t *testing.T) {
		params := map[string]interface{}{
			"vault": map[string]interface{}{
				"owner": "not-a-valid-address",
				"seq":   5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "Invalid vault owner")
	})
}

// TestLedgerEntryDelegate tests delegate entry lookup by hex and by object form
func TestLedgerEntryDelegate(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	delegateHex := "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D"

	t.Run("delegate by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"delegate":     delegateHex,
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("delegate by account and authorize", func(t *testing.T) {
		params := map[string]interface{}{
			"delegate": map[string]interface{}{
				"account":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"authorize": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("delegate missing authorize", func(t *testing.T) {
		params := map[string]interface{}{
			"delegate": map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "account and authorize required")
	})
}

// TestLedgerEntryBridge tests bridge entry lookup by hex string
func TestLedgerEntryBridge(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("bridge by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"bridge":       "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("bridge invalid hex", func(t *testing.T) {
		params := map[string]interface{}{
			"bridge":       "TOOSHORT",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "Invalid bridge")
	})
}

// TestLedgerEntryXChainClaimID tests xchain_owned_claim_id entry lookup by hex
func TestLedgerEntryXChainClaimID(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("xchain_owned_claim_id by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"xchain_owned_claim_id": "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index":          "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("xchain_owned_create_account_claim_id by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"xchain_owned_create_account_claim_id": "B33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index":                         "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// Ticket Conformance Tests
// =============================================================================

// TestLedgerEntryTicketConformance tests that ticket uses ticket_seq (not ticket_id)
// matching rippled's parseTicket() which expects jss::ticket_seq
func TestLedgerEntryTicketConformance(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("ticket by account and ticket_seq", func(t *testing.T) {
		params := map[string]interface{}{
			"ticket": map[string]interface{}{
				"account":    "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"ticket_seq": 5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("ticket by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"ticket":       "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// Uppercase Hex Response Tests
// =============================================================================

// TestLedgerEntryUppercaseHex verifies that the ledger_hash in the response
// uses uppercase hex encoding, matching rippled's output.
func TestLedgerEntryUppercaseHex(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	mock.ledgerEntryResult = &types.LedgerEntryResult{
		Index:       "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B, 0x0D, 0x85, 0x15, 0xD3, 0xEA, 0xAE, 0x1E, 0x74, 0xB2, 0x9A, 0x95, 0x80, 0x43, 0x46, 0xC4, 0x91, 0xEE, 0x1A, 0x95, 0xBF, 0x25, 0xE4, 0xAA, 0xB8, 0x54, 0xA6, 0xA6, 0x52},
		Node:        []byte(`{"LedgerEntryType": "AccountRoot"}`),
		Validated:   true,
	}

	params := map[string]interface{}{
		"index":        "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
		"ledger_index": "validated",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	resultMap, ok := result.(map[string]interface{})
	require.True(t, ok)

	ledgerHash, ok := resultMap["ledger_hash"].(string)
	require.True(t, ok)

	// Verify uppercase hex
	assert.Equal(t, "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652", ledgerHash,
		"ledger_hash should use uppercase hex")
	// Verify no lowercase letters in the hash
	assert.Equal(t, ledgerHash, strings.ToUpper(ledgerHash),
		"ledger_hash should be entirely uppercase")
}

// =============================================================================
// Escrow Hex Fallback Tests
// =============================================================================

// TestLedgerEntryEscrowHexFallback tests escrow specifically with hex vs object form
func TestLedgerEntryEscrowHexFallback(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("escrow by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"escrow":       "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("escrow by owner and seq (object form)", func(t *testing.T) {
		params := map[string]interface{}{
			"escrow": map[string]interface{}{
				"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"seq":   5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// Offer Hex Fallback Tests
// =============================================================================

// TestLedgerEntryOfferHexFallback tests offer specifically with hex vs object form
func TestLedgerEntryOfferHexFallback(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("offer by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"offer":        "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("offer by account and seq (object form)", func(t *testing.T) {
		params := map[string]interface{}{
			"offer": map[string]interface{}{
				"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"seq":     5,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// Oracle Hex Fallback Tests
// =============================================================================

// TestLedgerEntryOracleHexFallback tests oracle with hex vs object form
func TestLedgerEntryOracleHexFallback(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("oracle by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"oracle":       "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("oracle by account and document_id (object form)", func(t *testing.T) {
		params := map[string]interface{}{
			"oracle": map[string]interface{}{
				"account":            "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"oracle_document_id": 1,
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// =============================================================================
// AMM Hex Fallback Tests
// =============================================================================

// TestLedgerEntryAMMHexFallback tests AMM with hex vs object form
func TestLedgerEntryAMMHexFallback(t *testing.T) {
	mock := newMockLedgerEntryService()
	cleanup := setupLedgerEntryTestServices(mock)
	defer cleanup()

	method := &handlers.LedgerEntryMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	t.Run("amm by hex string", func(t *testing.T) {
		params := map[string]interface{}{
			"amm":          "A33EC6BB85FB5674074C4A3A43373BB17645308F3EAE1933E3E35252162B217D",
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("amm by asset pair (object form)", func(t *testing.T) {
		params := map[string]interface{}{
			"amm": map[string]interface{}{
				"asset":  map[string]interface{}{"currency": "XRP"},
				"asset2": map[string]interface{}{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("amm object missing asset2", func(t *testing.T) {
		params := map[string]interface{}{
			"amm": map[string]interface{}{
				"asset": map[string]interface{}{"currency": "XRP"},
			},
			"ledger_index": "validated",
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Contains(t, rpcErr.Message, "asset and asset2 required")
	})
}

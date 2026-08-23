package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccountChannelsLedgerService implements LedgerService for account_channels testing
type mockAccountChannelsLedgerService struct {
	*mockLedgerService
	accountChannelsResult *types.AccountChannelsResult
	accountChannelsErr    error
}

func newMockAccountChannelsLedgerService() *mockAccountChannelsLedgerService {
	return &mockAccountChannelsLedgerService{mockLedgerService: newMockLedgerService()}
}

func (m *mockAccountChannelsLedgerService) GetAccountChannels(_ context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32, _ string) (*types.AccountChannelsResult, error) {
	if m.accountChannelsErr != nil {
		return nil, m.accountChannelsErr
	}
	if m.accountChannelsResult != nil {
		return m.accountChannelsResult, nil
	}
	return &types.AccountChannelsResult{
		Account:     account,
		Channels:    []types.AccountChannel{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}

// newAccountChannelsTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountChannelsTestServices(mock *mockAccountChannelsLedgerService) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{Ledger: mock})
}

// TestAccountChannelsErrorValidation tests error handling for invalid inputs
// Based on rippled PayChan_test.cpp testAccountChannels()
func TestAccountChannelsErrorValidation(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name          string
		params        any
		expectedError string
		expectedCode  int
		setupMock     func()
	}{
		{
			name:          "Missing account field - empty params",
			params:        map[string]any{},
			expectedError: "Missing field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing account field - nil params",
			params:        nil,
			expectedError: "Missing field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - integer",
			params: map[string]any{
				"account": 12345,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - boolean",
			params: map[string]any{
				"account": true,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  rpcerrors.RpcINVALID_PARAMS,
		},
		{
			// Test case from rippled: malformed account using node public key format
			name: "Malformed account address - node public key format (actMalformed)",
			params: map[string]any{
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Account malformed.",
			expectedCode:  rpcerrors.RpcACT_MALFORMED,
		},
		{
			// Test case from rippled: account not found (unfunded account)
			name: "Account not found - valid format but not in ledger (actNotFound)",
			params: map[string]any{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  rpcerrors.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountChannelsErr = svcerr.ErrAccountNotFound
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountChannelsResult = nil
			mock.accountChannelsErr = nil

			if tc.setupMock != nil {
				tc.setupMock()
			}

			// Marshal params to JSON
			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			// Verify error response
			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError,
				"Error message should contain expected text")
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

// TestAccountChannelsSimple tests basic channel retrieval
// Based on rippled PayChan_test.cpp testSimple()
func TestAccountChannelsSimple(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Account with no channels returns empty array", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account:     aliceAccount,
			Channels:    []types.AccountChannel{},
			LedgerIndex: 2,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		assert.Len(t, channels, 0, "Should have no channels")
	})

	t.Run("Account with one channel returns channel details", func(t *testing.T) {
		// Based on rippled's testSimple after creating a channel
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "5DB01B7FFED6B67E6B0414DED11E051D2EE2B7619CE0EAA6286D67A3A4D5BDB3",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000", // 1000 XRP in drops
					Balance:            "0",
					SettleDelay:        100,
					PublicKey:          "aB44YfzW24VDEJQ2UuLPV2PvqcPCSoLnL7y5M1EzhdW4LnK5xMS3",
					PublicKeyHex:       "023693F15967AE357D0327974AD46FE3C127113B1110D6044FD41E723689F81CC6",
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check top-level fields. A bare query targets the open ledger, so
		// rippled's lookupLedger emits only ledger_current_index.
		assert.Equal(t, aliceAccount, resp["account"])
		assert.Contains(t, resp, "ledger_current_index")
		assert.NotContains(t, resp, "ledger_hash")
		assert.NotContains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check channels array
		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]any)
		assert.Equal(t, "5DB01B7FFED6B67E6B0414DED11E051D2EE2B7619CE0EAA6286D67A3A4D5BDB3", channel["channel_id"])
		assert.Equal(t, aliceAccount, channel["account"])
		assert.Equal(t, bobAccount, channel["destination_account"])
		assert.Equal(t, "1000000000", channel["amount"])
		assert.Equal(t, "0", channel["balance"])
		assert.Equal(t, float64(100), channel["settle_delay"])
		assert.Contains(t, channel, "public_key")
		assert.Contains(t, channel, "public_key_hex")
	})

	t.Run("Channel after funding shows updated amount", func(t *testing.T) {
		// Based on rippled's testSimple after funding the channel
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "5DB01B7FFED6B67E6B0414DED11E051D2EE2B7619CE0EAA6286D67A3A4D5BDB3",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "2000000000", // 2000 XRP after funding
					Balance:            "0",
					SettleDelay:        100,
				},
			},
			LedgerIndex: 4,
			LedgerHash:  [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]any)
		assert.Equal(t, "2000000000", channel["amount"])
	})

	t.Run("Channel after claim shows updated balance", func(t *testing.T) {
		// Based on rippled's testSimple after claiming from channel
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "5DB01B7FFED6B67E6B0414DED11E051D2EE2B7619CE0EAA6286D67A3A4D5BDB3",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "2000000000",
					Balance:            "500000000", // 500 XRP claimed
					SettleDelay:        100,
				},
			},
			LedgerIndex: 5,
			LedgerHash:  [32]byte{0x6B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]any)
		assert.Equal(t, "500000000", channel["balance"])
	})
}

// TestAccountChannelsDestinationFilter tests filtering by destination account
// Based on rippled AccountChannels.cpp destination_account parameter handling
func TestAccountChannelsDestinationFilter(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	carolAccount := "rfAetmdrVkPrSdthFd8qTMqruNvWkKvw4e"

	t.Run("Filter by destination returns only matching channels", func(t *testing.T) {
		// Setup: alice has channels to both bob and carol
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "CHANNEL_TO_BOB",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "0",
					SettleDelay:        100,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account":             aliceAccount,
			"destination_account": bobAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		assert.Len(t, channels, 1)
		channel := channels[0].(map[string]any)
		assert.Equal(t, bobAccount, channel["destination_account"])
	})

	t.Run("Filter by non-existent destination returns empty", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account:     aliceAccount,
			Channels:    []types.AccountChannel{},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account":             aliceAccount,
			"destination_account": carolAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		assert.Len(t, channels, 0)
	})
}

// TestAccountChannelsOptionalFields tests that optional fields are properly included/excluded
// Based on rippled PayChan_test.cpp channel creation with various options
func TestAccountChannelsOptionalFields(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	t.Run("Channel with expiration shows expiration field", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "CHANNEL_WITH_EXPIRATION",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "0",
					SettleDelay:        100,
					Expiration:         12345678,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]any)
		assert.Contains(t, channel, "expiration")
		assert.Equal(t, float64(12345678), channel["expiration"])
	})

	t.Run("Channel with cancel_after shows cancel_after field", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "CHANNEL_WITH_CANCEL",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "0",
					SettleDelay:        100,
					CancelAfter:        98765432,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]any)
		assert.Contains(t, channel, "cancel_after")
		assert.Equal(t, float64(98765432), channel["cancel_after"])
	})

	t.Run("Channel with source_tag and destination_tag shows tag fields", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "CHANNEL_WITH_TAGS",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "0",
					SettleDelay:        100,
					SourceTag:          12345,
					DestinationTag:     67890,
					HasSourceTag:       true,
					HasDestTag:         true,
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]any)
		assert.Contains(t, channel, "source_tag")
		assert.Contains(t, channel, "destination_tag")
		assert.Equal(t, float64(12345), channel["source_tag"])
		assert.Equal(t, float64(67890), channel["destination_tag"])
	})

	t.Run("Channel without optional fields excludes them from response", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "BASIC_CHANNEL",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "0",
					SettleDelay:        100,
					// No optional fields set
				},
			},
			LedgerIndex: 3,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]any)

		// These optional fields should not be present
		assert.NotContains(t, channel, "expiration")
		assert.NotContains(t, channel, "cancel_after")
		assert.NotContains(t, channel, "source_tag")
		assert.NotContains(t, channel, "destination_tag")
		assert.NotContains(t, channel, "public_key")
		assert.NotContains(t, channel, "public_key_hex")
	})
}

// TestAccountChannelsLedgerSpecification tests different ledger index specifications
func TestAccountChannelsLedgerSpecification(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name         string
		params       map[string]any
		setupMock    func()
		expectError  bool
		expectedCode int
		validateResp func(t *testing.T, resp map[string]any)
	}{
		{
			name: "ledger_index: validated",
			params: map[string]any{
				"account":      validAccount,
				"ledger_index": "validated",
			},
			setupMock: func() {
				mock.accountChannelsResult = &types.AccountChannelsResult{
					Account:     validAccount,
					Channels:    []types.AccountChannel{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountChannelsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "ledger_index: current",
			params: map[string]any{
				"account":      validAccount,
				"ledger_index": "current",
			},
			setupMock: func() {
				mock.accountChannelsResult = &types.AccountChannelsResult{
					Account:     validAccount,
					Channels:    []types.AccountChannel{},
					LedgerIndex: 3,
					LedgerHash:  [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
					Validated:   false,
				}
				mock.accountChannelsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]any) {
				assert.Equal(t, validAccount, resp["account"])
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]any{
				"account":      validAccount,
				"ledger_index": 2,
			},
			setupMock: func() {
				mock.accountChannelsResult = &types.AccountChannelsResult{
					Account:     validAccount,
					Channels:    []types.AccountChannel{},
					LedgerIndex: 2,
					LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
					Validated:   true,
				}
				mock.accountChannelsErr = nil
			},
			expectError: false,
			validateResp: func(t *testing.T, resp map[string]any) {
				ledgerIndex := resp["ledger_index"]
				switch v := ledgerIndex.(type) {
				case float64:
					assert.Equal(t, float64(2), v)
				case uint32:
					assert.Equal(t, uint32(2), v)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.accountChannelsResult = nil
			mock.accountChannelsErr = nil
			if tc.setupMock != nil {
				tc.setupMock()
			}

			paramsJSON, err := json.Marshal(tc.params)
			require.NoError(t, err)

			result, rpcErr := method.Handle(ctx, paramsJSON)

			if tc.expectError {
				assert.Nil(t, result)
				require.NotNil(t, rpcErr)
				if tc.expectedCode != 0 {
					assert.Equal(t, tc.expectedCode, rpcErr.Code)
				}
			} else {
				require.Nil(t, rpcErr)
				require.NotNil(t, result)

				resultJSON, err := json.Marshal(result)
				require.NoError(t, err)
				var respMap map[string]any
				err = json.Unmarshal(resultJSON, &respMap)
				require.NoError(t, err)

				if tc.validateResp != nil {
					tc.validateResp(t, respMap)
				}
			}
		})
	}
}

// TestAccountChannelsServiceUnavailable tests behavior when ledger service is not available
func TestAccountChannelsServiceUnavailable(t *testing.T) {
	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, rpcerrors.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestAccountChannelsMethodMetadata tests the method's metadata functions
func TestAccountChannelsMethodMetadata(t *testing.T) {
	method := &handlers.AccountChannelsMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_channels should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestAccountChannelsMultipleChannels tests retrieval of multiple channels
func TestAccountChannelsMultipleChannels(t *testing.T) {
	mock := newMockAccountChannelsLedgerService()
	services := newAccountChannelsTestServices(mock)

	method := &handlers.AccountChannelsMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bobAccount := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	carolAccount := "rfAetmdrVkPrSdthFd8qTMqruNvWkKvw4e"

	t.Run("Account with multiple channels to different destinations", func(t *testing.T) {
		mock.accountChannelsResult = &types.AccountChannelsResult{
			Account: aliceAccount,
			Channels: []types.AccountChannel{
				{
					ChannelID:          "CHANNEL_1",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "1000000000",
					Balance:            "100000000",
					SettleDelay:        100,
				},
				{
					ChannelID:          "CHANNEL_2",
					Account:            aliceAccount,
					DestinationAccount: carolAccount,
					Amount:             "2000000000",
					Balance:            "500000000",
					SettleDelay:        200,
				},
				{
					ChannelID:          "CHANNEL_3",
					Account:            aliceAccount,
					DestinationAccount: bobAccount,
					Amount:             "500000000",
					Balance:            "0",
					SettleDelay:        50,
				},
			},
			LedgerIndex: 5,
			LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:   true,
		}
		mock.accountChannelsErr = nil

		params := map[string]any{
			"account": aliceAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]any)
		assert.Len(t, channels, 3, "Should have 3 channels")

		// Verify each channel has required fields
		for _, ch := range channels {
			channel := ch.(map[string]any)
			assert.Contains(t, channel, "channel_id")
			assert.Contains(t, channel, "account")
			assert.Contains(t, channel, "destination_account")
			assert.Contains(t, channel, "amount")
			assert.Contains(t, channel, "balance")
			assert.Contains(t, channel, "settle_delay")
		}
	})
}

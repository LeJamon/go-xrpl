package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccountChannelsLedgerService implements LedgerService for account_channels testing
type mockAccountChannelsLedgerService struct {
	accountChannelsResult *types.AccountChannelsResult
	accountChannelsErr    error
	accountInfo           *types.AccountInfo
	accountInfoErr        error
	currentLedgerIndex    uint32
	closedLedgerIndex     uint32
	validatedLedgerIndex  uint32
	standalone            bool
	serverInfo            types.LedgerServerInfo
}

func newMockAccountChannelsLedgerService() *mockAccountChannelsLedgerService {
	return &mockAccountChannelsLedgerService{
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
	}
}

func (m *mockAccountChannelsLedgerService) GetCurrentLedgerIndex() uint32 {
	return m.currentLedgerIndex
}
func (m *mockAccountChannelsLedgerService) GetClosedLedgerIndex() uint32 { return m.closedLedgerIndex }
func (m *mockAccountChannelsLedgerService) GetValidatedLedgerIndex() uint32 {
	return m.validatedLedgerIndex
}
func (m *mockAccountChannelsLedgerService) AcceptLedger(context.Context) (uint32, error) {
	return m.closedLedgerIndex + 1, nil
}
func (m *mockAccountChannelsLedgerService) IsStandalone() bool { return m.standalone }
func (m *mockAccountChannelsLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockAccountChannelsLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockAccountChannelsLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockAccountChannelsLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
	if m.accountInfoErr != nil {
		return nil, m.accountInfoErr
	}
	if m.accountInfo != nil {
		return m.accountInfo, nil
	}
	return &types.AccountInfo{
		Account:     account,
		Balance:     "100000000",
		Flags:       0,
		OwnerCount:  0,
		Sequence:    1,
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652",
		Validated:   true,
	}, nil
}
func (m *mockAccountChannelsLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetTransactionHistory(ctx context.Context, startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	if m.accountChannelsErr != nil {
		return nil, m.accountChannelsErr
	}
	if m.accountChannelsResult != nil {
		return m.accountChannelsResult, nil
	}
	// Return empty channels by default
	return &types.AccountChannelsResult{
		Account:     account,
		Channels:    []types.AccountChannel{},
		LedgerIndex: m.validatedLedgerIndex,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}, nil
}
func (m *mockAccountChannelsLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountChannelsLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockAccountChannelsLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// newAccountChannelsTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountChannelsTestServices(mock *mockAccountChannelsLedgerService) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
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
		params        interface{}
		expectedError string
		expectedCode  int
		setupMock     func()
	}{
		{
			name:          "Missing account field - empty params",
			params:        map[string]interface{}{},
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing account field - nil params",
			params:        nil,
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - integer",
			params: map[string]interface{}{
				"account": 12345,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - boolean",
			params: map[string]interface{}{
				"account": true,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// Test case from rippled: malformed account using node public key format
			name: "Malformed account address - node public key format (actMalformed)",
			params: map[string]interface{}{
				"account": "n9MJkEKHDhy5eTLuHUQeAAjo382frHNbFK4C8hcwN4nwM2SrLdBj",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			// Test case from rippled: account not found (unfunded account)
			name: "Account not found - valid format but not in ledger (actNotFound)",
			params: map[string]interface{}{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountChannelsErr = errors.New("account not found")
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		// Check top-level fields
		assert.Equal(t, aliceAccount, resp["account"])
		assert.Contains(t, resp, "ledger_hash")
		assert.Contains(t, resp, "ledger_index")
		assert.Contains(t, resp, "validated")

		// Check channels array
		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)

		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
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
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]interface{})
		assert.Len(t, channels, 1)
		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
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
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		channels := resp["channels"].([]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]interface{})
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		require.Len(t, channels, 1)
		channel := channels[0].(map[string]interface{})

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
		params       map[string]interface{}
		setupMock    func()
		expectError  bool
		expectedCode int
		validateResp func(t *testing.T, resp map[string]interface{})
	}{
		{
			name: "ledger_index: validated",
			params: map[string]interface{}{
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, true, resp["validated"])
			},
		},
		{
			name: "ledger_index: current",
			params: map[string]interface{}{
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
				assert.Equal(t, validAccount, resp["account"])
			},
		},
		{
			name: "ledger_index: integer sequence number",
			params: map[string]interface{}{
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
			validateResp: func(t *testing.T, resp map[string]interface{}) {
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
				var respMap map[string]interface{}
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

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
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

		params := map[string]interface{}{
			"account": aliceAccount,
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

		channels := resp["channels"].([]interface{})
		assert.Len(t, channels, 3, "Should have 3 channels")

		// Verify each channel has required fields
		for _, ch := range channels {
			channel := ch.(map[string]interface{})
			assert.Contains(t, channel, "channel_id")
			assert.Contains(t, channel, "account")
			assert.Contains(t, channel, "destination_account")
			assert.Contains(t, channel, "amount")
			assert.Contains(t, channel, "balance")
			assert.Contains(t, channel, "settle_delay")
		}
	})
}

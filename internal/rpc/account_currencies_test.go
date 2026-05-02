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

// mockAccountCurrenciesLedgerService implements LedgerService for account_currencies testing
type mockAccountCurrenciesLedgerService struct {
	accountCurrenciesResult *types.AccountCurrenciesResult
	accountCurrenciesErr    error
	accountInfo             *types.AccountInfo
	accountInfoErr          error
	currentLedgerIndex      uint32
	closedLedgerIndex       uint32
	validatedLedgerIndex    uint32
	standalone              bool
	serverInfo              types.LedgerServerInfo
}

func newMockAccountCurrenciesLedgerService() *mockAccountCurrenciesLedgerService {
	return &mockAccountCurrenciesLedgerService{
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

func (m *mockAccountCurrenciesLedgerService) GetCurrentLedgerIndex() uint32 {
	return m.currentLedgerIndex
}
func (m *mockAccountCurrenciesLedgerService) GetClosedLedgerIndex() uint32 {
	return m.closedLedgerIndex
}
func (m *mockAccountCurrenciesLedgerService) GetValidatedLedgerIndex() uint32 {
	return m.validatedLedgerIndex
}
func (m *mockAccountCurrenciesLedgerService) AcceptLedger() (uint32, error) {
	return m.closedLedgerIndex + 1, nil
}
func (m *mockAccountCurrenciesLedgerService) IsStandalone() bool { return m.standalone }
func (m *mockAccountCurrenciesLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockAccountCurrenciesLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockAccountCurrenciesLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockAccountCurrenciesLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
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
func (m *mockAccountCurrenciesLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	if m.accountCurrenciesErr != nil {
		return nil, m.accountCurrenciesErr
	}
	if m.accountCurrenciesResult != nil {
		return m.accountCurrenciesResult, nil
	}
	// Return empty currencies by default
	return &types.AccountCurrenciesResult{
		ReceiveCurrencies: []string{},
		SendCurrencies:    []string{},
		LedgerIndex:       m.validatedLedgerIndex,
		LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:         true,
	}, nil
}
func (m *mockAccountCurrenciesLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockAccountCurrenciesLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockAccountCurrenciesLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// newAccountCurrenciesTestServices builds a *types.ServiceContainer wrapping the mock.
func newAccountCurrenciesTestServices(mock *mockAccountCurrenciesLedgerService) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
}

// TestAccountCurrenciesBadInput tests error handling for invalid inputs
// Based on rippled AccountCurrencies_test.cpp testBadInput()
func TestAccountCurrenciesBadInput(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
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
			// missing account field
			name:          "Missing account field - empty params",
			params:        map[string]interface{}{},
			expectedError: "Missing required parameter: account",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (integer)
			name: "Invalid account type - integer",
			params: map[string]interface{}{
				"account": 1,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (float)
			name: "Invalid account type - float",
			params: map[string]interface{}{
				"account": 1.1,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// test account non-string (boolean)
			name: "Invalid account type - boolean",
			params: map[string]interface{}{
				"account": true,
			},
			expectedError: "Invalid parameters:",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			// invalid base58 characters (llIIOO)
			// rippled returns rpcACT_MALFORMED for malformed addresses
			name: "Malformed account - invalid base58 characters",
			params: map[string]interface{}{
				"account": "llIIOO",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			// Cannot use a seed as account
			// rippled returns rpcACT_MALFORMED
			name: "Malformed account - seed format (actMalformed)",
			params: map[string]interface{}{
				"account": "Bob",
			},
			expectedError: "Malformed account.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			// ask for nonexistent account (actNotFound)
			name: "Account not found - valid format but not in ledger",
			params: map[string]interface{}{
				"account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account not found.",
			expectedCode:  types.RpcACT_NOT_FOUND,
			setupMock: func() {
				mock.accountCurrenciesErr = errors.New("account not found")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock state
			mock.accountCurrenciesResult = nil
			mock.accountCurrenciesErr = nil

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

// TestAccountCurrenciesBasic tests basic functionality
// Based on rippled AccountCurrencies_test.cpp testBasic()
func TestAccountCurrenciesBasic(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	aliceAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Account with no trust lines returns empty arrays", func(t *testing.T) {
		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: []string{},
			SendCurrencies:    []string{},
			LedgerIndex:       2,
			LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

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

		receiveCurrencies := resp["receive_currencies"].([]interface{})
		sendCurrencies := resp["send_currencies"].([]interface{})
		assert.Len(t, receiveCurrencies, 0, "Should have no receive currencies")
		assert.Len(t, sendCurrencies, 0, "Should have no send currencies")
	})

	t.Run("Account with trust lines but no balance - can receive", func(t *testing.T) {
		// Based on rippled test: after setting up 26 trust lines (USA - USZ)
		// receive_currencies should contain all, send_currencies should be empty
		currencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: currencies,
			SendCurrencies:    []string{},
			LedgerIndex:       3,
			LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

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

		receiveCurrencies := resp["receive_currencies"].([]interface{})
		sendCurrencies := resp["send_currencies"].([]interface{})
		assert.Len(t, receiveCurrencies, 26, "Should have 26 receive currencies")
		assert.Len(t, sendCurrencies, 0, "Should have no send currencies (no balance)")
	})

	t.Run("Account with trust lines and balance - can send and receive", func(t *testing.T) {
		// After payment, alice has balance, so can both send and receive
		currencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: currencies,
			SendCurrencies:    currencies,
			LedgerIndex:       4,
			LedgerHash:        [32]byte{0x5B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

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

		receiveCurrencies := resp["receive_currencies"].([]interface{})
		sendCurrencies := resp["send_currencies"].([]interface{})
		assert.Len(t, receiveCurrencies, 26, "Should have 26 receive currencies")
		assert.Len(t, sendCurrencies, 26, "Should have 26 send currencies")
	})

	t.Run("Exhausted trust line removes from receive_currencies", func(t *testing.T) {
		// When balance == limit, cannot receive more
		receiveCurrencies := []string{"USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"} // USA missing
		sendCurrencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: receiveCurrencies,
			SendCurrencies:    sendCurrencies,
			LedgerIndex:       5,
			LedgerHash:        [32]byte{0x6B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

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

		rcvCurrencies := resp["receive_currencies"].([]interface{})
		sndCurrencies := resp["send_currencies"].([]interface{})
		assert.Len(t, rcvCurrencies, 25, "Should have 25 receive currencies (USA exhausted)")
		assert.Len(t, sndCurrencies, 26, "Should still have 26 send currencies")

		// Verify USA is not in receive_currencies
		for _, c := range rcvCurrencies {
			assert.NotEqual(t, "USA", c.(string))
		}
	})

	t.Run("Zero balance removes from send_currencies", func(t *testing.T) {
		// When balance == 0, cannot send
		receiveCurrencies := []string{"USA", "USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"}
		sendCurrencies := []string{"USB", "USC", "USD", "USE", "USF", "USG", "USH", "USI", "USJ",
			"USK", "USL", "USM", "USN", "USO", "USP", "USQ", "USR", "USS", "UST",
			"USU", "USV", "USW", "USX", "USY", "USZ"} // USA missing

		mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
			ReceiveCurrencies: receiveCurrencies,
			SendCurrencies:    sendCurrencies,
			LedgerIndex:       6,
			LedgerHash:        [32]byte{0x7B, 0xC5, 0x0C, 0x9B},
			Validated:         true,
		}
		mock.accountCurrenciesErr = nil

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

		rcvCurrencies := resp["receive_currencies"].([]interface{})
		sndCurrencies := resp["send_currencies"].([]interface{})
		assert.Len(t, rcvCurrencies, 26, "Should have all 26 receive currencies")
		assert.Len(t, sndCurrencies, 25, "Should have 25 send currencies (USA has zero balance)")

		// Verify USA is not in send_currencies
		for _, c := range sndCurrencies {
			assert.NotEqual(t, "USA", c.(string))
		}
	})
}

// TestAccountCurrenciesResponseFields tests that all required fields are present
func TestAccountCurrenciesResponseFields(t *testing.T) {
	mock := newMockAccountCurrenciesLedgerService()
	services := newAccountCurrenciesTestServices(mock)

	method := &handlers.AccountCurrenciesMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.accountCurrenciesResult = &types.AccountCurrenciesResult{
		ReceiveCurrencies: []string{"USD", "EUR"},
		SendCurrencies:    []string{"USD"},
		LedgerIndex:       2,
		LedgerHash:        [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:         true,
	}

	params := map[string]interface{}{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
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

	// Verify all required fields are present
	assert.Contains(t, resp, "ledger_hash")
	assert.Contains(t, resp, "ledger_index")
	assert.Contains(t, resp, "receive_currencies")
	assert.Contains(t, resp, "send_currencies")
	assert.Contains(t, resp, "validated")
}

// TestAccountCurrenciesServiceUnavailable tests behavior when ledger service is not available
func TestAccountCurrenciesServiceUnavailable(t *testing.T) {
	method := &handlers.AccountCurrenciesMethod{}
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

// TestAccountCurrenciesMethodMetadata tests the method's metadata functions
func TestAccountCurrenciesMethodMetadata(t *testing.T) {
	method := &handlers.AccountCurrenciesMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_currencies should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

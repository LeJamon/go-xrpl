package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDepositAuthorizedLedgerService implements LedgerService for deposit_authorized testing
type mockDepositAuthorizedLedgerService struct {
	depositAuthorizedResult *types.DepositAuthorizedResult
	depositAuthorizedErr    error
	accountInfo             *types.AccountInfo
	accountInfoErr          error
	currentLedgerIndex      uint32
	closedLedgerIndex       uint32
	validatedLedgerIndex    uint32
	standalone              bool
	serverInfo              types.LedgerServerInfo
}

func newMockDepositAuthorizedLedgerService() *mockDepositAuthorizedLedgerService {
	return &mockDepositAuthorizedLedgerService{
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

func (m *mockDepositAuthorizedLedgerService) GetCurrentLedgerIndex() uint32 {
	return m.currentLedgerIndex
}
func (m *mockDepositAuthorizedLedgerService) GetClosedLedgerIndex() uint32 {
	return m.closedLedgerIndex
}
func (m *mockDepositAuthorizedLedgerService) GetValidatedLedgerIndex() uint32 {
	return m.validatedLedgerIndex
}
func (m *mockDepositAuthorizedLedgerService) AcceptLedger(context.Context) (uint32, error) {
	return m.closedLedgerIndex + 1, nil
}
func (m *mockDepositAuthorizedLedgerService) AcceptLedgerAt(context.Context, time.Time) (uint32, error) {
	return m.closedLedgerIndex + 1, nil
}
func (m *mockDepositAuthorizedLedgerService) IsStandalone() bool { return m.standalone }
func (m *mockDepositAuthorizedLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockDepositAuthorizedLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockDepositAuthorizedLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) SubmitTransaction(txJSON []byte, txBlobHex ...string) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockDepositAuthorizedLedgerService) GetAccountInfo(_ context.Context, account string, ledgerIndex string) (*types.AccountInfo, error) {
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
func (m *mockDepositAuthorizedLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountLines(_ context.Context, account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountOffers(_ context.Context, account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetBookOffers(_ context.Context, takerGets, takerPays types.Amount, _, _ string, ledgerIndex string, limit uint32, _ string, _ bool) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetTransactionHistory(ctx context.Context, startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetLedgerEntry(_ context.Context, entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetLedgerData(_ context.Context, ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountObjects(_ context.Context, account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountChannels(_ context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountCurrencies(_ context.Context, account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAccountNFTs(_ context.Context, account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetGatewayBalances(_ context.Context, account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetNoRippleCheck(_ context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetDepositAuthorized(_ context.Context, sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	if m.depositAuthorizedErr != nil {
		return nil, m.depositAuthorizedErr
	}
	if m.depositAuthorizedResult != nil {
		return m.depositAuthorizedResult, nil
	}
	// Return authorized by default
	return &types.DepositAuthorizedResult{
		SourceAccount:      sourceAccount,
		DestinationAccount: destinationAccount,
		DepositAuthorized:  true,
		LedgerIndex:        m.validatedLedgerIndex,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}, nil
}
func (m *mockDepositAuthorizedLedgerService) GetNFTBuyOffers(_ context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetNFTSellOffers(_ context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAutofillFee(txJSON []byte, unlimited bool, mult, div int) (uint64, error) {
	return 0, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) GetAutofillSequence(account string, hasTicketSequence bool) (uint32, error) {
	return 0, errors.New("not implemented")
}
func (m *mockDepositAuthorizedLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockDepositAuthorizedLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// newDepositAuthorizedTestServices builds a per-test ServiceContainer wrapping mock.
func newDepositAuthorizedTestServices(mock *mockDepositAuthorizedLedgerService) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger: mock,
	}
}

// Error Validation Tests

// TestDepositAuthorizedErrorValidation tests error handling for invalid inputs
// Based on rippled DepositAuthorized_test.cpp testErrors()
func TestDepositAuthorizedErrorValidation(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name          string
		params        map[string]any
		setupMock     func()
		expectError   bool
		expectedError string
		expectedCode  int
	}{
		{
			name:          "Missing source_account field",
			params:        map[string]any{"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
			expectError:   true,
			expectedError: "Missing field 'source_account'.",
		},
		{
			name:          "Missing destination_account field",
			params:        map[string]any{"source_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			expectError:   true,
			expectedError: "Missing field 'destination_account'.",
		},
		{
			name: "Corrupt source_account field",
			params: map[string]any{
				"source_account":      "rG1QQv2nh2gr7RCZ!P8YYcBUKCCN633jCn",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			// No setupMock needed — handler-level ValidateAccount catches this
			// before the service is called.
			expectError:   true,
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "Corrupt destination_account field",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rP6P9ypfAmc!pw8SZHNwM4nvZHFXDraQas",
			},
			// No setupMock needed — handler-level ValidateAccount catches this
			// before the service is called.
			expectError:   true,
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "Source account not found",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			setupMock: func() {
				mock.depositAuthorizedErr = svcerr.ErrSrcAccountNotFound
			},
			expectError:   true,
			expectedError: "Source account not found.",
			expectedCode:  types.RpcSRC_ACT_NOT_FOUND,
		},
		{
			name: "Destination account not found",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			setupMock: func() {
				mock.depositAuthorizedErr = svcerr.ErrDstAccountNotFound
			},
			expectError:   true,
			expectedError: "Destination account not found.",
			expectedCode:  types.RpcDST_ACT_NOT_FOUND,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock state
			mock.depositAuthorizedErr = nil
			mock.depositAuthorizedResult = nil

			if tt.setupMock != nil {
				tt.setupMock()
			}

			paramsJSON, _ := json.Marshal(tt.params)
			resp, err := method.Handle(ctx, paramsJSON)

			if tt.expectError {
				require.NotNil(t, err, "Expected an error but got none")
				assert.Contains(t, err.Message, tt.expectedError)
				if tt.expectedCode != 0 {
					assert.Equal(t, tt.expectedCode, err.Code)
				}
				assert.Nil(t, resp)
			} else {
				require.Nil(t, err, "Unexpected error: %v", err)
				require.NotNil(t, resp)
			}
		})
	}
}

// Authorization Tests

// TestDepositAuthorizedBasicAuthorized tests when deposit is authorized (no DepositAuth flag)
// Based on rippled DepositAuthorized_test.cpp testValid()
func TestDepositAuthorizedBasicAuthorized(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Alice can deposit to Becky (no DepositAuth set)
	mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
		SourceAccount:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DepositAuthorized:  true,
		LedgerIndex:        2,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}

	params := map[string]any{
		"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"ledger_index":        "validated",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, respMap["deposit_authorized"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", respMap["source_account"])
	assert.Equal(t, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", respMap["destination_account"])
	// A "validated" query targets a closed ledger, so lookupLedger emits
	// ledger_hash + ledger_index and never ledger_current_index.
	assert.Contains(t, respMap, "ledger_index")
	assert.Contains(t, respMap, "ledger_hash")
	assert.NotContains(t, respMap, "ledger_current_index")
	assert.Contains(t, respMap, "validated")
}

// TestDepositAuthorizedLedgerShape verifies the lookupLedger response-shape
// contract: an open ("current") query emits only ledger_current_index, while a
// closed ("validated") query emits ledger_hash + ledger_index.
func TestDepositAuthorizedLedgerShape(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("current ledger emits ledger_current_index only", func(t *testing.T) {
		mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
			SourceAccount:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			DepositAuthorized:  true,
			LedgerIndex:        7,
			LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:          false,
		}

		params := map[string]any{
			"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"ledger_index":        "current",
		}
		paramsJSON, _ := json.Marshal(params)
		resp, err := method.Handle(ctx, paramsJSON)
		require.Nil(t, err)
		respMap := resp.(map[string]any)

		assert.Equal(t, uint32(7), respMap["ledger_current_index"])
		assert.NotContains(t, respMap, "ledger_hash")
		assert.NotContains(t, respMap, "ledger_index")
		assert.Equal(t, false, respMap["validated"])
	})

	t.Run("validated ledger emits ledger_hash and ledger_index", func(t *testing.T) {
		mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
			SourceAccount:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			DepositAuthorized:  true,
			LedgerIndex:        6,
			LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
			Validated:          true,
		}

		params := map[string]any{
			"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			"ledger_index":        "validated",
		}
		paramsJSON, _ := json.Marshal(params)
		resp, err := method.Handle(ctx, paramsJSON)
		require.Nil(t, err)
		respMap := resp.(map[string]any)

		assert.Contains(t, respMap, "ledger_hash")
		assert.Equal(t, uint32(6), respMap["ledger_index"])
		assert.NotContains(t, respMap, "ledger_current_index")
		assert.Equal(t, true, respMap["validated"])
	})
}

// TestDepositAuthorizedSelfDeposit tests that self-deposit is always authorized
// Based on rippled DepositAuthorized_test.cpp testValid() - becky can deposit to herself
func TestDepositAuthorizedSelfDeposit(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Becky can always deposit to herself, even with DepositAuth set
	mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
		SourceAccount:      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DepositAuthorized:  true,
		LedgerIndex:        2,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}

	params := map[string]any{
		"source_account":      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, respMap["deposit_authorized"])
}

// TestDepositAuthorizedNotAuthorized tests when deposit is NOT authorized (DepositAuth flag set, no preauth)
// Based on rippled DepositAuthorized_test.cpp testValid()
func TestDepositAuthorizedNotAuthorized(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Alice is NOT authorized to deposit to Becky (DepositAuth set, no preauth)
	mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
		SourceAccount:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DepositAuthorized:  false,
		LedgerIndex:        2,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}

	params := map[string]any{
		"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, false, respMap["deposit_authorized"])
}

// TestDepositAuthorizedWithPreauth tests when deposit IS authorized (DepositAuth flag set WITH preauth)
// Based on rippled DepositAuthorized_test.cpp testValid()
func TestDepositAuthorizedWithPreauth(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Alice is authorized to deposit to Becky (DepositAuth set, with preauth)
	mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
		SourceAccount:      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		DestinationAccount: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DepositAuthorized:  true,
		LedgerIndex:        2,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}

	params := map[string]any{
		"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, respMap["deposit_authorized"])
}

// TestDepositAuthorizedReciprocal tests that deposit authorization is not reciprocal
// Based on rippled DepositAuthorized_test.cpp testValid()
func TestDepositAuthorizedReciprocal(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Becky can deposit to Alice even though Alice can't deposit to Becky
	// (It's not reciprocal)
	mock.depositAuthorizedResult = &types.DepositAuthorizedResult{
		SourceAccount:      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		DestinationAccount: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		DepositAuthorized:  true,
		LedgerIndex:        2,
		LedgerHash:         [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:          true,
	}

	params := map[string]any{
		"source_account":      "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
		"destination_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]any)
	require.True(t, ok)

	assert.Equal(t, true, respMap["deposit_authorized"])
}

// Service Unavailable Tests

// TestDepositAuthorizedServiceUnavailable tests response when ledger service is unavailable
func TestDepositAuthorizedServiceUnavailable(t *testing.T) {
	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	params := map[string]any{
		"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Ledger service not available")
	assert.Nil(t, resp)
}

// Method Metadata Tests

// TestDepositAuthorizedMethodMetadata tests method metadata (role, API versions)
func TestDepositAuthorizedMethodMetadata(t *testing.T) {
	method := &handlers.DepositAuthorizedMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// Handler-Level Address Validation Tests

// TestDepositAuthorizedAddressValidation tests that handler-level Base58 address
// validation catches malformed addresses before the service layer is called.
// Reference: rippled DepositAuthorized.cpp — parseBase58 → rpcACT_MALFORMED
func TestDepositAuthorizedAddressValidation(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name          string
		params        map[string]any
		expectedError string
		expectedCode  int
	}{
		{
			name: "source_account with special characters",
			params: map[string]any{
				"source_account":      "rG1QQv2nh2gr7RCZ!P8YYcBUKCCN633jCn",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "destination_account with special characters",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rP6P9ypfAmc!pw8SZHNwM4nvZHFXDraQas",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "source_account too short",
			params: map[string]any{
				"source_account":      "rHb9",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "destination_account too short",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "r",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "source_account random string",
			params: map[string]any{
				"source_account":      "not_a_valid_address",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
		{
			name: "both accounts malformed — source caught first",
			params: map[string]any{
				"source_account":      "INVALID",
				"destination_account": "ALSO_INVALID",
			},
			expectedError: "Account malformed.",
			expectedCode:  types.RpcACT_MALFORMED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock — these errors should be caught before hitting the service
			mock.depositAuthorizedErr = nil
			mock.depositAuthorizedResult = nil

			paramsJSON, _ := json.Marshal(tt.params)
			resp, err := method.Handle(ctx, paramsJSON)

			require.NotNil(t, err, "Expected an error but got none")
			assert.Equal(t, tt.expectedError, err.Message)
			assert.Equal(t, tt.expectedCode, err.Code)
			assert.Nil(t, resp)
		})
	}
}

// Credential Validation Tests

// TestDepositAuthorizedCredentialValidation tests handler-level credential format
// checks and the mapping of service-level credential failures to rpcBAD_CREDENTIALS.
// Reference: rippled DepositAuthorized.cpp — credential parsing loop
func TestDepositAuthorizedCredentialValidation(t *testing.T) {
	mock := newMockDepositAuthorizedLedgerService()
	services := newDepositAuthorizedTestServices(mock)

	method := &handlers.DepositAuthorizedMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	validCred1 := "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
	validCred2 := "1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF"

	tests := []struct {
		name          string
		params        map[string]any
		serviceErr    error
		expectedError string
		expectedCode  int
	}{
		{
			name: "Credential too short",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{"ABCD"},
			},
			expectedError: "Invalid field 'credentials', not an array of CredentialID(hash256).",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Credential not valid hex",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{"ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"},
			},
			expectedError: "Invalid field 'credentials', not an array of CredentialID(hash256).",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Too many credentials",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials": []string{
					"0000000000000000000000000000000000000000000000000000000000000001",
					"0000000000000000000000000000000000000000000000000000000000000002",
					"0000000000000000000000000000000000000000000000000000000000000003",
					"0000000000000000000000000000000000000000000000000000000000000004",
					"0000000000000000000000000000000000000000000000000000000000000005",
					"0000000000000000000000000000000000000000000000000000000000000006",
					"0000000000000000000000000000000000000000000000000000000000000007",
					"0000000000000000000000000000000000000000000000000000000000000008",
					"0000000000000000000000000000000000000000000000000000000000000009",
				},
			},
			expectedError: "Invalid field 'credentials', not array too long.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Empty credentials array",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{},
			},
			expectedError: "Invalid field 'credentials', not is non-empty array of CredentialID(hash256).",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Non-array credentials",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         "not-an-array",
			},
			expectedError: "Invalid field 'credentials', not is non-empty array of CredentialID(hash256).",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Non-string credentials entry",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []any{1, 3},
			},
			expectedError: "Invalid field 'credentials', not an array of CredentialID(hash256).",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Valid credentials — no duplicates",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1, validCred2},
			},
			// No error expected — this case should pass through to the service
			expectedError: "",
			expectedCode:  0,
		},
		// Ledger-side credential failures surface from the service as
		// ErrBadCredentials wrappers; the handler maps each to
		// rpcBAD_CREDENTIALS with rippled's exact detail message.
		// Reference: rippled DepositAuthorized.cpp RPC::inject_error(rpcBAD_CREDENTIALS, ...)
		{
			name: "Credential does not exist on ledger",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1},
			},
			serviceErr:    fmt.Errorf("%w: credentials don't exist", svcerr.ErrBadCredentials),
			expectedError: "credentials don't exist",
			expectedCode:  types.RpcBAD_CREDENTIALS,
		},
		{
			name: "Credential not accepted",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1},
			},
			serviceErr:    fmt.Errorf("%w: credentials aren't accepted", svcerr.ErrBadCredentials),
			expectedError: "credentials aren't accepted",
			expectedCode:  types.RpcBAD_CREDENTIALS,
		},
		{
			name: "Credential expired",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1},
			},
			serviceErr:    fmt.Errorf("%w: credentials are expired", svcerr.ErrBadCredentials),
			expectedError: "credentials are expired",
			expectedCode:  types.RpcBAD_CREDENTIALS,
		},
		{
			name: "Credential belongs to another account",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1},
			},
			serviceErr:    fmt.Errorf("%w: credentials doesn't belong to the root account", svcerr.ErrBadCredentials),
			expectedError: "credentials doesn't belong to the root account",
			expectedCode:  types.RpcBAD_CREDENTIALS,
		},
		{
			name: "Duplicate credentials by issuer and type",
			params: map[string]any{
				"source_account":      "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				"destination_account": "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				"credentials":         []string{validCred1, validCred2},
			},
			serviceErr:    fmt.Errorf("%w: duplicates in credentials", svcerr.ErrBadCredentials),
			expectedError: "duplicates in credentials",
			expectedCode:  types.RpcBAD_CREDENTIALS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock.depositAuthorizedErr = tt.serviceErr
			mock.depositAuthorizedResult = nil

			paramsJSON, _ := json.Marshal(tt.params)
			resp, err := method.Handle(ctx, paramsJSON)

			if tt.expectedError != "" {
				require.NotNil(t, err, "Expected an error but got none")
				assert.Equal(t, tt.expectedError, err.Message)
				assert.Equal(t, tt.expectedCode, err.Code)
				assert.Nil(t, resp)
			} else {
				require.Nil(t, err, "Unexpected error: %v", err)
				require.NotNil(t, resp)
			}
		})
	}
}

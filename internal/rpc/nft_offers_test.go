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

// mockNFTOffersLedgerService implements LedgerService for nft_buy_offers/nft_sell_offers testing
type mockNFTOffersLedgerService struct {
	nftBuyOffersResult   *types.NFTOffersResult
	nftBuyOffersErr      error
	nftSellOffersResult  *types.NFTOffersResult
	nftSellOffersErr     error
	currentLedgerIndex   uint32
	closedLedgerIndex    uint32
	validatedLedgerIndex uint32
	standalone           bool
	serverInfo           types.LedgerServerInfo
}

func newMockNFTOffersLedgerService() *mockNFTOffersLedgerService {
	return &mockNFTOffersLedgerService{
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

func (m *mockNFTOffersLedgerService) GetCurrentLedgerIndex() uint32   { return m.currentLedgerIndex }
func (m *mockNFTOffersLedgerService) GetClosedLedgerIndex() uint32    { return m.closedLedgerIndex }
func (m *mockNFTOffersLedgerService) GetValidatedLedgerIndex() uint32 { return m.validatedLedgerIndex }
func (m *mockNFTOffersLedgerService) AcceptLedger() (uint32, error)   { return m.closedLedgerIndex + 1, nil }
func (m *mockNFTOffersLedgerService) IsStandalone() bool              { return m.standalone }
func (m *mockNFTOffersLedgerService) GetServerInfo() types.LedgerServerInfo {
	return m.serverInfo
}
func (m *mockNFTOffersLedgerService) GetGenesisAccount() (string, error) {
	return "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", nil
}
func (m *mockNFTOffersLedgerService) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) SubmitTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return 10, 10000000, 2000000
}
func (m *mockNFTOffersLedgerService) GetAccountInfo(account string, ledgerIndex string) (*types.AccountInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetTransaction(txHash [32]byte) (*types.TransactionInfo, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) StoreTransaction(txHash [32]byte, txData []byte) error {
	return errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*types.AccountLinesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountOffers(account string, ledgerIndex string, limit uint32) (*types.AccountOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetBookOffers(takerGets, takerPays types.Amount, ledgerIndex string, limit uint32) (*types.BookOffersResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetTransactionHistory(startIndex uint32) (*types.TxHistoryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetLedgerRange(minSeq, maxSeq uint32) (*types.LedgerRangeResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*types.LedgerEntryResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetLedgerData(ledgerIndex string, limit uint32, marker string) (*types.LedgerDataResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*types.AccountObjectsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*types.AccountChannelsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountCurrencies(account string, ledgerIndex string) (*types.AccountCurrenciesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*types.AccountNFTsResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*types.GatewayBalancesResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*types.NoRippleCheckResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*types.DepositAuthorizedResult, error) {
	return nil, errors.New("not implemented")
}

// NFT offer methods
func (m *mockNFTOffersLedgerService) GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	if m.nftBuyOffersErr != nil {
		return nil, m.nftBuyOffersErr
	}
	if m.nftBuyOffersResult != nil {
		return m.nftBuyOffersResult, nil
	}
	return nil, errors.New("object not found")
}

func (m *mockNFTOffersLedgerService) GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error) {
	if m.nftSellOffersErr != nil {
		return nil, m.nftSellOffersErr
	}
	if m.nftSellOffersResult != nil {
		return m.nftSellOffersResult, nil
	}
	return nil, errors.New("object not found")
}
func (m *mockNFTOffersLedgerService) SimulateTransaction(txJSON []byte) (*types.SubmitResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockNFTOffersLedgerService) IsAmendmentBlocked() bool { return false }
func (m *mockNFTOffersLedgerService) GetClosedLedgerView() (types.LedgerStateView, error) {
	return nil, errors.New("not implemented in mock")
}

// setupNFTOffersTestServices initializes the Services singleton with a mock for testing
func setupNFTOffersTestServices(mock *mockNFTOffersLedgerService) func() {
	oldServices := types.Services
	types.Services = &types.ServiceContainer{
		Ledger: mock,
	}
	return func() {
		types.Services = oldServices
	}
}

// =============================================================================
// nft_buy_offers Tests
// =============================================================================

// TestNftBuyOffersErrorValidation tests error handling for invalid inputs
// Reference: rippled NFTOffers_test.cpp testErrors()
func TestNftBuyOffersErrorValidation(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftBuyOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		expectedCode  int
	}{
		{
			name:          "Missing nft_id field",
			params:        map[string]interface{}{},
			expectError:   true,
			expectedError: "Missing field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Empty nft_id field",
			params:        map[string]interface{}{"nft_id": ""},
			expectError:   true,
			expectedError: "Missing field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Invalid nft_id - too short",
			params:        map[string]interface{}{"nft_id": "00081388DC1AB4E7C57F8067A3AB"},
			expectError:   true,
			expectedError: "Invalid field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Invalid nft_id - not hex",
			params:        map[string]interface{}{"nft_id": "00081388DC1AB4E7C57F8067A3ABGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG"},
			expectError:   true,
			expectedError: "Invalid field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "NFT not found",
			params: map[string]interface{}{
				"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
			},
			setupMock: func() {
				mock.nftBuyOffersErr = errors.New("object not found")
			},
			expectError:   true,
			expectedError: "The requested object was not found.",
			expectedCode:  types.RpcOBJECT_NOT_FOUND,
		},
		{
			name: "Invalid marker",
			params: map[string]interface{}{
				"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
				"marker": "invalid_marker",
			},
			expectError:   true,
			expectedError: "Invalid marker",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock state
			mock.nftBuyOffersErr = nil
			mock.nftBuyOffersResult = nil

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

// TestNftBuyOffersSuccess tests successful retrieval of NFT buy offers
func TestNftBuyOffersSuccess(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftBuyOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Setup mock to return some buy offers
	mock.nftBuyOffersResult = &types.NFTOffersResult{
		NFTID: "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		Offers: []types.NFTOfferInfo{
			{
				NFTOfferIndex: "AAA588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
				Flags:         0, // Buy offer has no sell flag
				Owner:         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				Amount:        "1000000", // 1 XRP
			},
			{
				NFTOfferIndex: "BBB588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
				Flags:         0,
				Owner:         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
				Amount:        "2000000", // 2 XRP
				Destination:   "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
				Expiration:    123456789,
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4", respMap["nft_id"])
	assert.Contains(t, respMap, "offers")
	assert.Contains(t, respMap, "ledger_index")
	assert.Contains(t, respMap, "ledger_hash")
	assert.Contains(t, respMap, "validated")

	offers, ok := respMap["offers"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, offers, 2)

	// Check first offer
	assert.Equal(t, "AAA588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400", offers[0]["nft_offer_index"])
	assert.Equal(t, uint32(0), offers[0]["flags"])
	assert.Equal(t, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", offers[0]["owner"])
	assert.Equal(t, "1000000", offers[0]["amount"])

	// Check second offer with optional fields
	assert.Equal(t, "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9", offers[1]["destination"])
	assert.Equal(t, uint32(123456789), offers[1]["expiration"])
}

// TestNftBuyOffersWithIOUAmount tests NFT buy offers with IOU amounts
func TestNftBuyOffersWithIOUAmount(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftBuyOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Setup mock to return a buy offer with IOU amount
	mock.nftBuyOffersResult = &types.NFTOffersResult{
		NFTID: "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		Offers: []types.NFTOfferInfo{
			{
				NFTOfferIndex: "AAA588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
				Flags:         0,
				Owner:         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				Amount: map[string]string{
					"currency": "USD",
					"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
					"value":    "100",
				},
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	offers, ok := respMap["offers"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, offers, 1)

	// Check IOU amount
	amount, ok := offers[0]["amount"].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, "USD", amount["currency"])
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", amount["issuer"])
	assert.Equal(t, "100", amount["value"])
}

// TestNftBuyOffersWithPagination tests NFT buy offers with pagination
func TestNftBuyOffersWithPagination(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftBuyOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Setup mock to return paginated results
	mock.nftBuyOffersResult = &types.NFTOffersResult{
		NFTID: "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		Offers: []types.NFTOfferInfo{
			{
				NFTOfferIndex: "AAA588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
				Flags:         0,
				Owner:         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				Amount:        "1000000",
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
		Limit:       50,
		Marker:      "BBB588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		"limit":  50,
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	assert.Contains(t, respMap, "marker")
	assert.Contains(t, respMap, "limit")
	assert.Equal(t, uint32(50), respMap["limit"])
	assert.Equal(t, "BBB588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400", respMap["marker"])
}

// =============================================================================
// nft_sell_offers Tests
// =============================================================================

// TestNftSellOffersErrorValidation tests error handling for invalid inputs
func TestNftSellOffersErrorValidation(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftSellOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	tests := []struct {
		name          string
		params        map[string]interface{}
		setupMock     func()
		expectError   bool
		expectedError string
		expectedCode  int
	}{
		{
			name:          "Missing nft_id field",
			params:        map[string]interface{}{},
			expectError:   true,
			expectedError: "Missing field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Empty nft_id field",
			params:        map[string]interface{}{"nft_id": ""},
			expectError:   true,
			expectedError: "Missing field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Invalid nft_id - too short",
			params:        map[string]interface{}{"nft_id": "00081388DC1AB4E7C57F8067A3AB"},
			expectError:   true,
			expectedError: "Invalid field 'nft_id'",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "NFT not found",
			params: map[string]interface{}{
				"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
			},
			setupMock: func() {
				mock.nftSellOffersErr = errors.New("object not found")
			},
			expectError:   true,
			expectedError: "The requested object was not found.",
			expectedCode:  types.RpcOBJECT_NOT_FOUND,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mock state
			mock.nftSellOffersErr = nil
			mock.nftSellOffersResult = nil

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

// TestNftSellOffersSuccess tests successful retrieval of NFT sell offers
func TestNftSellOffersSuccess(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftSellOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// lsfSellNFToken flag
	const lsfSellNFToken uint32 = 0x00000001

	// Setup mock to return some sell offers
	mock.nftSellOffersResult = &types.NFTOffersResult{
		NFTID: "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		Offers: []types.NFTOfferInfo{
			{
				NFTOfferIndex: "AAA588DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F400",
				Flags:         lsfSellNFToken, // Sell offer has the sell flag
				Owner:         "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
				Amount:        "5000000", // 5 XRP
			},
		},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	assert.Equal(t, "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4", respMap["nft_id"])

	offers, ok := respMap["offers"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, offers, 1)

	// Check sell offer has the lsfSellNFToken flag
	assert.Equal(t, lsfSellNFToken, offers[0]["flags"])
	assert.Equal(t, "5000000", offers[0]["amount"])
}

// TestNftSellOffersEmptyResult tests when no sell offers exist
func TestNftSellOffersEmptyResult(t *testing.T) {
	mock := newMockNFTOffersLedgerService()
	cleanup := setupNFTOffersTestServices(mock)
	defer cleanup()

	method := &handlers.NftSellOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	// Setup mock to return empty result
	mock.nftSellOffersResult = &types.NFTOffersResult{
		NFTID:       "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
		Offers:      []types.NFTOfferInfo{},
		LedgerIndex: 2,
		LedgerHash:  [32]byte{0x4B, 0xC5, 0x0C, 0x9B},
		Validated:   true,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.Nil(t, err, "Unexpected error: %v", err)
	require.NotNil(t, resp)

	respMap, ok := resp.(map[string]interface{})
	require.True(t, ok)

	offers, ok := respMap["offers"].([]map[string]interface{})
	require.True(t, ok)
	assert.Len(t, offers, 0)
}

// =============================================================================
// Service Unavailable Tests
// =============================================================================

// TestNftBuyOffersServiceUnavailable tests response when ledger service is unavailable
func TestNftBuyOffersServiceUnavailable(t *testing.T) {
	oldServices := types.Services
	types.Services = nil
	defer func() {
		types.Services = oldServices
	}()

	method := &handlers.NftBuyOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Ledger service not available")
	assert.Nil(t, resp)
}

// TestNftSellOffersServiceUnavailable tests response when ledger service is unavailable
func TestNftSellOffersServiceUnavailable(t *testing.T) {
	oldServices := types.Services
	types.Services = nil
	defer func() {
		types.Services = oldServices
	}()

	method := &handlers.NftSellOffersMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	params := map[string]interface{}{
		"nft_id": "00081388DC1AB4E7C57F8067A3AB15BEA8B0F1A0DE14678200000099000001F4",
	}

	paramsJSON, _ := json.Marshal(params)
	resp, err := method.Handle(ctx, paramsJSON)

	require.NotNil(t, err)
	assert.Contains(t, err.Message, "Ledger service not available")
	assert.Nil(t, resp)
}

// =============================================================================
// Method Metadata Tests
// =============================================================================

// TestNftBuyOffersMethodMetadata tests method metadata (role, API versions)
func TestNftBuyOffersMethodMetadata(t *testing.T) {
	method := &handlers.NftBuyOffersMethod{}

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

// TestNftSellOffersMethodMetadata tests method metadata (role, API versions)
func TestNftSellOffersMethodMetadata(t *testing.T) {
	method := &handlers.NftSellOffersMethod{}

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

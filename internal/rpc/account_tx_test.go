package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accountTxMock wraps mockLedgerService and overrides GetAccountTransactions
type accountTxMock struct {
	*mockLedgerService
	getAccountTransactionsFn func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error)
	getLedgerBySequenceFn    func(uint32) (types.LedgerReader, error)
	getLedgerByHashFn        func([32]byte) (types.LedgerReader, error)
}

func newAccountTxMock() *accountTxMock {
	return &accountTxMock{
		mockLedgerService: newMockLedgerService(),
	}
}

func (m *accountTxMock) GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
	if m.getAccountTransactionsFn != nil {
		return m.getAccountTransactionsFn(ctx, account, ledgerMin, ledgerMax, limit, marker, forward)
	}
	return nil, errors.New("not implemented")
}

func (m *accountTxMock) GetLedgerBySequence(seq uint32) (types.LedgerReader, error) {
	if m.getLedgerBySequenceFn != nil {
		return m.getLedgerBySequenceFn(seq)
	}
	return m.mockLedgerService.GetLedgerBySequence(seq)
}

func (m *accountTxMock) GetLedgerByHash(hash [32]byte) (types.LedgerReader, error) {
	if m.getLedgerByHashFn != nil {
		return m.getLedgerByHashFn(hash)
	}
	return m.mockLedgerService.GetLedgerByHash(hash)
}

// newTestServicesAccountTx builds a *types.ServiceContainer wrapping the mock.
func newTestServicesAccountTx(mock *accountTxMock) *types.ServiceContainer {
	return &types.ServiceContainer{Ledger: mock}
}

func TestAccountTxDeliveredAmountHistoricalContext(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	txBlob := encodeSyntheticRPCObject(t, map[string]any{
		"Account":         account,
		"Amount":          "100",
		"Destination":     "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	})
	tests := []struct {
		name              string
		ledgerSequence    uint32
		closeTime         int64
		serializedAmount  string
		expectedDelivered string
	}{
		{
			name:              "pre-cutoff ledger and close time",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_000,
			expectedDelivered: "unavailable",
		},
		{
			name:              "ledger sequence cutoff",
			ledgerSequence:    4_594_095,
			expectedDelivered: "100",
		},
		{
			name:              "post-cutoff close time",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_001,
			expectedDelivered: "100",
		},
		{
			name:              "serialized amount before cutoffs",
			ledgerSequence:    4_594_094,
			closeTime:         446_000_000,
			serializedAmount:  "40",
			expectedDelivered: "40",
		},
	}

	for _, tc := range tests {
		for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
			apiName := "api_v1"
			if apiVersion == types.ApiVersion2 {
				apiName = "api_v2"
			}
			t.Run(tc.name+"/"+apiName, func(t *testing.T) {
				meta := map[string]any{
					"AffectedNodes":     []any{},
					"TransactionIndex":  uint32(0),
					"TransactionResult": "tesSUCCESS",
				}
				if tc.serializedAmount != "" {
					meta["DeliveredAmount"] = tc.serializedAmount
				}
				metaBlob := encodeSyntheticRPCObject(t, meta)

				mock := newAccountTxMock()
				mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
					return &types.AccountTxResult{
						Account: account,
						Transactions: []types.AccountTransaction{{
							Hash:        [32]byte{1},
							LedgerIndex: tc.ledgerSequence,
							TxBlob:      txBlob,
							Meta:        metaBlob,
						}},
						Validated: true,
					}, nil
				}
				mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
					require.Equal(t, tc.ledgerSequence, seq)
					return &mockLedgerReader{
						seq:       seq,
						closeTime: tc.closeTime,
						closed:    true,
						validated: true,
					}, nil
				}
				ctx := &types.RPCContext{
					Context:    context.Background(),
					Role:       types.RoleGuest,
					ApiVersion: apiVersion,
					Services:   newTestServicesAccountTx(mock),
				}

				result, rpcErr := (&handlers.AccountTxMethod{}).Handle(
					ctx,
					json.RawMessage(`{"account":"`+account+`"}`),
				)
				require.Nil(t, rpcErr)
				response := result.(map[string]any)
				entry := response["transactions"].([]map[string]any)[0]
				responseMeta := entry["meta"].(map[string]any)
				require.Equal(t, tc.expectedDelivered, responseMeta["delivered_amount"])
			})
		}
	}
}

// Error Validation Tests
// Based on rippled AccountTx_test.cpp testParameters()

// TestAccountTxErrorValidation tests error handling for invalid inputs
func TestAccountTxErrorValidation(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	ctx := &types.RPCContext{
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
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name:          "Missing account field - nil params",
			params:        nil,
			expectedError: "Missing field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Malformed account address - hex format",
			params: map[string]any{
				"account": "0xDEADBEEF",
			},
			expectedError: "Account malformed.",
			expectedCode:  35, // actMalformed (address validation)
		},
		{
			name: "Malformed account address - bad checksum",
			params: map[string]any{
				"account": "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			},
			expectedError: "Account malformed.",
			expectedCode:  35, // actMalformed (address validation)
		},
		{
			name: "Account not found - valid format but not in ledger",
			params: map[string]any{
				"account": "rDsbeomae4FXwgQTJp9Rs64Qg9vDiTCdBv",
			},
			expectedError: "Account not found.",
			expectedCode:  19, // actNotFound
			setupMock: func() {
				mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
					return nil, svcerr.ErrAccountNotFound
				}
			},
		},
		{
			name: "Invalid account type - integer",
			params: map[string]any{
				"account": 12345,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - boolean",
			params: map[string]any{
				"account": true,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - null",
			params: map[string]any{
				"account": nil,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - object",
			params: map[string]any{
				"account": map[string]any{"nested": "value"},
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - array",
			params: map[string]any{
				"account": []string{"value1", "value2"},
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
		{
			name: "Invalid account type - float",
			params: map[string]any{
				"account": 1.1,
			},
			expectedError: "Invalid field 'account'.",
			expectedCode:  types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Reset mock
			mock.getAccountTransactionsFn = nil

			if tc.setupMock != nil {
				tc.setupMock()
			}

			var paramsJSON json.RawMessage
			if tc.params != nil {
				var err error
				paramsJSON, err = json.Marshal(tc.params)
				require.NoError(t, err)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			assert.Nil(t, result, "Expected nil result for error case")
			require.NotNil(t, rpcErr, "Expected RPC error")
			assert.Contains(t, rpcErr.Message, tc.expectedError,
				"Error message should contain expected text")
			assert.Equal(t, tc.expectedCode, rpcErr.Code,
				"Error code should match expected")
		})
	}
}

func TestAccountTxValidationOrder(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	tests := []struct {
		name    string
		params  string
		message string
		code    int
	}{
		{
			name:    "binary type precedes forward limit and account",
			params:  `{"binary":"true","forward":"true","limit":0}`,
			message: "Invalid field 'binary'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "forward type precedes limit and account",
			params:  `{"binary":false,"forward":"true","limit":0}`,
			message: "Invalid field 'forward'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "limit precedes account",
			params:  `{"binary":false,"forward":false,"limit":0}`,
			message: "Invalid field 'limit'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account presence precedes ledger and marker",
			params:  `{"ledger_index":"2","marker":{}}`,
			message: "Missing field 'account'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account type precedes ledger and marker",
			params:  `{"account":null,"ledger_index":"2","marker":{}}`,
			message: "Invalid field 'account'.",
			code:    types.RpcINVALID_PARAMS,
		},
		{
			name:    "account base58 precedes ledger and marker",
			params:  `{"account":"bad","ledger_index":"2","marker":{}}`,
			message: "Account malformed.",
			code:    types.RpcACT_MALFORMED,
		},
		{
			name:    "ledger arguments precede marker",
			params:  `{"account":"` + account + `","ledger_index":"2","marker":{}}`,
			message: "ledger_index string malformed",
			code:    types.RpcINVALID_PARAMS,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   newTestServicesAccountTx(newAccountTxMock()),
			}
			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(tc.params))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, tc.code, rpcErr.Code)
			assert.Equal(t, tc.message, rpcErr.Message)
		})
	}
}

func TestAccountTxLedgerArguments(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const ledgerHash = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	t.Run("numeric JSON ledger_index selects one validated ledger", func(t *testing.T) {
		mock := newAccountTxMock()
		mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
			require.Equal(t, uint32(2), seq)
			return &mockLedgerReader{seq: seq, closed: true, validated: true}, nil
		}
		mock.getAccountTransactionsFn = func(_ context.Context, gotAccount string, min, max int64, _ uint32, _ *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
			assert.Equal(t, account, gotAccount)
			assert.Equal(t, int64(2), min)
			assert.Equal(t, int64(2), max)
			return &types.AccountTxResult{Account: gotAccount, Transactions: []types.AccountTransaction{}, Validated: true}, nil
		}
		ctx := &types.RPCContext{Context: context.Background(), ApiVersion: types.ApiVersion2, Services: newTestServicesAccountTx(mock)}

		result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`","ledger_index":2}`))
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("numeric string ledger_index is malformed", func(t *testing.T) {
		ctx := &types.RPCContext{Context: context.Background(), ApiVersion: types.ApiVersion2, Services: newTestServicesAccountTx(newAccountTxMock())}
		result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`","ledger_index":"2"}`))
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "ledger_index string malformed", rpcErr.Message)
	})

	for _, tc := range []struct {
		name   string
		params string
	}{
		{
			name:   "null range member conflicts by presence",
			params: `{"account":"` + account + `","ledger_index_min":null,"ledger_index":"validated"}`,
		},
		{
			name:   "empty range member conflicts by presence",
			params: `{"account":"` + account + `","ledger_index_max":"","ledger_hash":"` + ledgerHash + `"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &types.RPCContext{Context: context.Background(), ApiVersion: types.ApiVersion2, Services: newTestServicesAccountTx(newAccountTxMock())}
			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(tc.params))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
			assert.Equal(t, "invalidParams", rpcErr.Message)
		})
	}

	t.Run("ledger_hash wins over ledger_index", func(t *testing.T) {
		mock := newAccountTxMock()
		mock.getLedgerByHashFn = func(hash [32]byte) (types.LedgerReader, error) {
			assert.Equal(t, strings.Repeat("AA", 32), strings.ToUpper(hex.EncodeToString(hash[:])))
			return &mockLedgerReader{seq: 2, closed: true, validated: true}, nil
		}
		mock.getAccountTransactionsFn = func(_ context.Context, gotAccount string, min, max int64, _ uint32, _ *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
			assert.Equal(t, int64(2), min)
			assert.Equal(t, int64(2), max)
			return &types.AccountTxResult{Account: gotAccount, Transactions: []types.AccountTransaction{}, Validated: true}, nil
		}
		ctx := &types.RPCContext{Context: context.Background(), ApiVersion: types.ApiVersion2, Services: newTestServicesAccountTx(mock)}

		result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`","ledger_hash":"`+ledgerHash+`","ledger_index":"2"}`))
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	for _, tc := range []struct {
		name           string
		sequence       uint32
		validated      bool
		validatedIndex uint32
	}{
		{name: "ledger is not validated", sequence: 2, validated: false, validatedIndex: 2},
		{name: "ledger is outside validated range", sequence: 3, validated: true, validatedIndex: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := newAccountTxMock()
			mock.validatedLedgerIndex = tc.validatedIndex
			mock.getLedgerBySequenceFn = func(uint32) (types.LedgerReader, error) {
				return &mockLedgerReader{seq: tc.sequence, closed: true, validated: tc.validated}, nil
			}
			ctx := &types.RPCContext{Context: context.Background(), ApiVersion: types.ApiVersion2, Services: newTestServicesAccountTx(mock)}

			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(ctx, json.RawMessage(`{"account":"`+account+`","ledger_index":`+strconv.FormatUint(uint64(tc.sequence), 10)+`}`))
			assert.Nil(t, result)
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcLGR_NOT_VALIDATED, rpcErr.Code)
			assert.Equal(t, "lgrNotValidated", rpcErr.ErrorString)
			assert.Equal(t, "Ledger not validated.", rpcErr.Message)
		})
	}
}

// Ledger Index Min/Max Handling Tests
// Based on rippled AccountTx_test.cpp testParameters() ledger_index_min/max sections

func TestAccountTxLedgerIndexMinMax(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("Default ledger_index_min=-1 and ledger_index_max=-1", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			// With -1 defaults, the handler should pass through
			assert.Equal(t, validAccount, account)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    2,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account":          validAccount,
			"ledger_index_min": -1,
			"ledger_index_max": -1,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error for default ledger index range")
		require.NotNil(t, result)
	})

	t.Run("ledger_index_min=0 and ledger_index_max=0 (omitted)", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			// When omitted, Go zero values are 0. The handler passes them through.
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    2,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error when min/max omitted")
		require.NotNil(t, result)
	})

	t.Run("Specific ledger range with transactions", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, int64(1), ledgerMin)
			assert.Equal(t, int64(2), ledgerMax)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    2,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account":          validAccount,
			"ledger_index_min": 1,
			"ledger_index_max": 3,
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

		assert.Equal(t, float64(1), resp["ledger_index_min"])
		assert.Equal(t, float64(2), resp["ledger_index_max"])
	})
}

// Binary vs JSON Mode Tests
// Based on rippled AccountTx_test.cpp binary parameter

func TestAccountTxBinaryMode(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Create a sample transaction hash
	txHash := [32]byte{}
	for i := range txHash {
		txHash[i] = byte(i + 1)
	}

	// A minimal valid serialized tx blob and meta for testing binary mode.
	// These are hex-encoded placeholders that represent raw binary data.
	txBlobBytes, _ := hex.DecodeString("1200002200000000240000000361D4838D7EA4C680000000000000000000000000005553440000000000E6C92BF47A692162751F6017CF3E40B4AE15285568400000000000000A7321ED5F5AC43F527AE97194A1B29F2E8831A2AEE056431FC596590B5F3F5769AF70774473045022100")
	metaBytes, _ := hex.DecodeString("201C00000001")

	t.Run("Binary mode returns tx_blob and meta_blob as hex (API v2)", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:   account,
				LedgerMin: 1,
				LedgerMax: 5,
				Limit:     200,
				Transactions: []types.AccountTransaction{
					{
						Hash:        txHash,
						LedgerIndex: 3,
						TxBlob:      txBlobBytes,
						Meta:        metaBytes,
					},
				},
				Validated: true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"binary":  true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error in binary mode")
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txs := resp["transactions"].([]any)
		require.Len(t, txs, 1)

		tx0 := txs[0].(map[string]any)
		// In binary mode (API v2), should have tx_blob and meta_blob as hex strings
		assert.Contains(t, tx0, "tx_blob", "Binary mode should return tx_blob")
		assert.Contains(t, tx0, "meta_blob", "Binary mode should return meta_blob")
		assert.Contains(t, tx0, "ledger_index", "Binary mode should return ledger_index")
		assert.Equal(t, true, tx0["validated"])

		// tx_blob should be uppercase hex
		txBlobStr, ok := tx0["tx_blob"].(string)
		assert.True(t, ok, "tx_blob should be a string")
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(txBlobBytes)), txBlobStr)

		// meta_blob should be uppercase hex
		metaBlobStr, ok := tx0["meta_blob"].(string)
		assert.True(t, ok, "meta_blob should be a string")
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(metaBytes)), metaBlobStr)
	})

	t.Run("JSON mode returns decoded tx and meta objects", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:   account,
				LedgerMin: 1,
				LedgerMax: 5,
				Limit:     200,
				Transactions: []types.AccountTransaction{
					{
						Hash:        txHash,
						LedgerIndex: 3,
						TxBlob:      txBlobBytes,
						Meta:        metaBytes,
					},
				},
				Validated: true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"binary":  false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr, "Expected no error in JSON mode")
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		txs := resp["transactions"].([]any)
		require.Len(t, txs, 1)

		tx0 := txs[0].(map[string]any)
		if txJSON, ok := tx0["tx"].(map[string]any); ok {
			assert.Equal(t, strings.ToUpper(hex.EncodeToString(txHash[:])), txJSON["hash"])
		} else {
			assert.Contains(t, tx0, "tx_blob")
		}
		assert.NotContains(t, tx0, "hash")
		assert.Equal(t, true, tx0["validated"])
	})
}

func TestAccountTxJSONProjectionByAPIVersion(t *testing.T) {
	const (
		account        = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		ledgerSequence = uint32(1)
		transactionSeq = uint32(7)
		transactionNet = uint32(2_048)
		closeTime      = int64(123)
	)
	txBlob := encodeSyntheticRPCObject(t, map[string]any{
		"Account":         account,
		"Fee":             "10",
		"NetworkID":       transactionNet,
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "AccountSet",
	})
	metaBlob := encodeSyntheticRPCObject(t, map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  transactionSeq,
		"TransactionResult": "tesSUCCESS",
	})
	txHash := [32]byte{0xAB}
	ledgerHash := [32]byte{0xCD}
	wantHash := strings.ToUpper(hex.EncodeToString(txHash[:]))
	wantLedgerHash := strings.ToUpper(hex.EncodeToString(ledgerHash[:]))
	const wantCTID = "C000000100070800"

	for _, apiVersion := range []int{types.ApiVersion1, types.ApiVersion2} {
		t.Run("api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
			mock := newAccountTxMock()
			mock.serverInfo.NetworkID = 9
			mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
				return &types.AccountTxResult{
					Account: account,
					Transactions: []types.AccountTransaction{{
						Hash:        txHash,
						LedgerIndex: ledgerSequence,
						TxnSeq:      transactionSeq,
						TxBlob:      txBlob,
						Meta:        metaBlob,
					}},
					Validated: true,
				}, nil
			}
			mock.getLedgerBySequenceFn = func(seq uint32) (types.LedgerReader, error) {
				require.Equal(t, ledgerSequence, seq)
				return &mockLedgerReader{
					seq:       ledgerSequence,
					hash:      ledgerHash,
					closeTime: closeTime,
					closed:    true,
					validated: true,
				}, nil
			}

			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   newTestServicesAccountTx(mock),
			}, json.RawMessage(`{"account":"`+account+`"}`))
			require.Nil(t, rpcErr)
			entry := result.(map[string]any)["transactions"].([]map[string]any)[0]

			if apiVersion == types.ApiVersion1 {
				txJSON := entry["tx"].(map[string]any)
				assert.Equal(t, wantHash, txJSON["hash"])
				assert.EqualValues(t, ledgerSequence, txJSON["inLedger"])
				assert.EqualValues(t, ledgerSequence, txJSON["ledger_index"])
				assert.EqualValues(t, closeTime, txJSON["date"])
				assert.Equal(t, wantCTID, txJSON["ctid"])
				assert.NotContains(t, entry, "hash")
				assert.NotContains(t, entry, "ledger_index")
				assert.NotContains(t, entry, "ledger_hash")
				assert.NotContains(t, entry, "close_time_iso")
				return
			}

			txJSON := entry["tx_json"].(map[string]any)
			assert.NotContains(t, txJSON, "hash")
			assert.NotContains(t, txJSON, "inLedger")
			assert.EqualValues(t, ledgerSequence, txJSON["ledger_index"])
			assert.EqualValues(t, closeTime, txJSON["date"])
			assert.Equal(t, wantCTID, txJSON["ctid"])
			assert.Equal(t, wantHash, entry["hash"])
			assert.EqualValues(t, ledgerSequence, entry["ledger_index"])
			assert.Equal(t, wantLedgerHash, entry["ledger_hash"])
			assert.Equal(t, "2000-01-01T00:02:03Z", entry["close_time_iso"])
		})
	}
}

func TestAccountTxJSONProjectionCTIDBounds(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	metaBlob := encodeSyntheticRPCObject(t, map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	})
	tests := []struct {
		name           string
		ledgerSequence uint32
		transactionSeq uint32
		networkID      uint32
		wantCTID       string
	}{
		{
			name:           "maximum components",
			ledgerSequence: 0x0FFFFFFF,
			transactionSeq: 0xFFFF,
			networkID:      0xFFFF,
			wantCTID:       "CFFFFFFFFFFFFFFF",
		},
		{name: "ledger overflow", ledgerSequence: 0x10000000, transactionSeq: 1, networkID: 1},
		{name: "transaction overflow", ledgerSequence: 1, transactionSeq: 0x10000, networkID: 1},
		{name: "network overflow", ledgerSequence: 1, transactionSeq: 1, networkID: 0x10000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txBlob := encodeSyntheticRPCObject(t, map[string]any{
				"Account":         account,
				"Fee":             "10",
				"NetworkID":       tc.networkID,
				"Sequence":        uint32(1),
				"SigningPubKey":   "",
				"TransactionType": "AccountSet",
			})
			mock := newAccountTxMock()
			mock.serverInfo.NetworkID = 9
			mock.getAccountTransactionsFn = func(context.Context, string, int64, int64, uint32, *types.AccountTxMarker, bool) (*types.AccountTxResult, error) {
				return &types.AccountTxResult{
					Account: account,
					Transactions: []types.AccountTransaction{{
						LedgerIndex: tc.ledgerSequence,
						TxnSeq:      tc.transactionSeq,
						TxBlob:      txBlob,
						Meta:        metaBlob,
					}},
					Validated: true,
				}, nil
			}
			mock.getLedgerBySequenceFn = func(uint32) (types.LedgerReader, error) {
				return &mockLedgerReader{seq: tc.ledgerSequence, closed: true, validated: true}, nil
			}

			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion2,
				Services:   newTestServicesAccountTx(mock),
			}, json.RawMessage(`{"account":"`+account+`"}`))
			require.Nil(t, rpcErr)
			entry := result.(map[string]any)["transactions"].([]map[string]any)[0]
			txJSON := entry["tx_json"].(map[string]any)
			if tc.wantCTID == "" {
				assert.NotContains(t, txJSON, "ctid")
			} else {
				assert.Equal(t, tc.wantCTID, txJSON["ctid"])
			}
		})
	}
}

// Forward / Reverse Ordering Tests
// Based on rippled AccountTx_test.cpp forward parameter

func TestAccountTxForwardReverse(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Forward=true passes forward flag", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.True(t, forward, "forward flag should be true")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"forward": true,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Forward=false (default reverse ordering)", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.False(t, forward, "forward flag should be false")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"forward": false,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("Forward omitted defaults to false", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.False(t, forward, "forward flag should default to false")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    5,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// Marker-Based Pagination Tests
// Based on rippled AccountTx_test.cpp marker handling

func TestAccountTxMarkerPagination(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("No marker returns first page", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Nil(t, marker, "marker should be nil for first page")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        2,
				Transactions: []types.AccountTransaction{},
				Marker:       &types.AccountTxMarker{LedgerSeq: 5, TxnSeq: 1},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"limit":   2,
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

		// Response should include marker for next page
		assert.Contains(t, resp, "marker")
		markerObj := resp["marker"].(map[string]any)
		assert.Equal(t, float64(5), markerObj["ledger"])
		assert.Equal(t, float64(1), markerObj["seq"])
	})

	t.Run("Marker passed to service for next page", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			require.NotNil(t, marker, "marker should be provided for second page")
			assert.Equal(t, uint32(5), marker.LedgerSeq)
			assert.Equal(t, uint32(1), marker.TxnSeq)
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        2,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
				// No marker means last page
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"limit":   2,
			"marker": map[string]any{
				"ledger": 5,
				"seq":    1,
			},
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

		// No marker means last page
		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "Last page should not have marker")
	})

	t.Run("JsonCpp-compatible UInt marker coercion", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(_ context.Context, account string, _, _ int64, _ uint32, marker *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
			require.NotNil(t, marker)
			assert.Equal(t, uint32(0), marker.LedgerSeq)
			assert.Equal(t, uint32(1), marker.TxnSeq)
			return &types.AccountTxResult{Account: account, LedgerMin: 1, LedgerMax: 2, Limit: 2, Validated: true}, nil
		}
		paramsJSON := json.RawMessage(`{"account":"` + validAccount + `","marker":{"ledger":null,"seq":true}}`)
		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})
}

// Response Structure Tests
// Based on rippled AccountTx_test.cpp - validates response fields

func TestAccountTxResponseStructure(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Response contains all required fields", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
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

		// Check required top-level fields
		assert.Contains(t, resp, "account")
		assert.Contains(t, resp, "ledger_index_min")
		assert.Contains(t, resp, "ledger_index_max")
		assert.Contains(t, resp, "limit")
		assert.Contains(t, resp, "transactions")
		assert.Contains(t, resp, "validated")

		// Check field values
		assert.Equal(t, validAccount, resp["account"])
		assert.Equal(t, float64(1), resp["ledger_index_min"])
		assert.Equal(t, float64(10), resp["ledger_index_max"])
		assert.Equal(t, float64(200), resp["limit"])
		assert.Equal(t, true, resp["validated"])

		// transactions should be an array
		txs, ok := resp["transactions"].([]any)
		assert.True(t, ok, "transactions should be an array")
		assert.Len(t, txs, 0)
	})

	t.Run("Response with marker present", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        5,
				Transactions: []types.AccountTransaction{},
				Marker:       &types.AccountTxMarker{LedgerSeq: 7, TxnSeq: 3},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"limit":   5,
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

		assert.Contains(t, resp, "marker")
		markerObj := resp["marker"].(map[string]any)
		assert.Equal(t, float64(7), markerObj["ledger"])
		assert.Equal(t, float64(3), markerObj["seq"])
	})

	t.Run("Response without marker when no more results", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
				// Marker is nil
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
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

		_, hasMarker := resp["marker"]
		assert.False(t, hasMarker, "No marker expected when all results returned")
	})
}

// Empty Account (No Transactions) Tests

func TestAccountTxEmptyAccount(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:      account,
			LedgerMin:    1,
			LedgerMax:    10,
			Limit:        200,
			Transactions: []types.AccountTransaction{},
			Validated:    true,
		}, nil
	}

	params := map[string]any{
		"account": validAccount,
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

	txs := resp["transactions"].([]any)
	assert.Len(t, txs, 0, "Empty account should have no transactions")
	assert.Equal(t, validAccount, resp["account"])
	assert.Equal(t, true, resp["validated"])
}

// Multiple Transactions with Correct Hash Formatting Tests

func TestAccountTxMultipleTransactions(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Create multiple transaction hashes
	hash1 := [32]byte{}
	hash2 := [32]byte{}
	hash3 := [32]byte{}
	for i := range hash1 {
		hash1[i] = byte(i + 1)
		hash2[i] = byte(i + 0x20)
		hash3[i] = byte(i + 0x40)
	}

	txBlob := []byte{0x12, 0x00, 0x00}
	meta := []byte{0x20, 0x1C, 0x00, 0x00, 0x00, 0x01}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 10,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{Hash: hash1, LedgerIndex: 3, TxBlob: txBlob, Meta: meta},
				{Hash: hash2, LedgerIndex: 4, TxBlob: txBlob, Meta: meta},
				{Hash: hash3, LedgerIndex: 5, TxBlob: txBlob, Meta: meta},
			},
			Validated: true,
		}, nil
	}

	t.Run("Binary mode - API v2 fields", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		params := map[string]any{
			"account": validAccount,
			"binary":  true,
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

		txs := resp["transactions"].([]any)
		require.Len(t, txs, 3, "Should return 3 transactions")

		tx0 := txs[0].(map[string]any)
		tx1 := txs[1].(map[string]any)
		tx2 := txs[2].(map[string]any)

		// Verify each transaction has validated=true
		assert.Equal(t, true, tx0["validated"])
		assert.Equal(t, true, tx1["validated"])
		assert.Equal(t, true, tx2["validated"])

		// Verify ledger_index in binary mode
		assert.Equal(t, float64(3), tx0["ledger_index"])
		assert.Equal(t, float64(4), tx1["ledger_index"])
		assert.Equal(t, float64(5), tx2["ledger_index"])

		// Binary mode uses meta_blob, not meta
		assert.Contains(t, tx0, "meta_blob", "Binary v2 should have meta_blob")
		assert.Contains(t, tx0, "tx_blob", "Binary v2 should have tx_blob")
	})

	t.Run("JSON mode - hash at entry level", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion2,
			Services:   services,
		}

		params := map[string]any{
			"account": validAccount,
			"binary":  false,
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

		txs := resp["transactions"].([]any)
		require.Len(t, txs, 3, "Should return 3 transactions")

		// Verify hash formatting - should be uppercase hex at entry level
		expectedHash1 := strings.ToUpper(hex.EncodeToString(hash1[:]))
		expectedHash2 := strings.ToUpper(hex.EncodeToString(hash2[:]))
		expectedHash3 := strings.ToUpper(hex.EncodeToString(hash3[:]))

		tx0 := txs[0].(map[string]any)
		tx1 := txs[1].(map[string]any)
		tx2 := txs[2].(map[string]any)

		assert.Equal(t, expectedHash1, tx0["hash"], "Hash 1 should be uppercase hex")
		assert.Equal(t, expectedHash2, tx1["hash"], "Hash 2 should be uppercase hex")
		assert.Equal(t, expectedHash3, tx2["hash"], "Hash 3 should be uppercase hex")

		// Verify each transaction has validated=true
		assert.Equal(t, true, tx0["validated"])
		assert.Equal(t, true, tx1["validated"])
		assert.Equal(t, true, tx2["validated"])

		// Verify ledger_index at entry level
		assert.Equal(t, float64(3), tx0["ledger_index"])
		assert.Equal(t, float64(4), tx1["ledger_index"])
		assert.Equal(t, float64(5), tx2["ledger_index"])
	})
}

// Service Unavailable / Nil Ledger Tests

func TestAccountTxServiceUnavailable(t *testing.T) {
	method := &handlers.AccountTxMethod{}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	t.Run("Services is nil", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   nil,
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.LogDetail(), "Ledger service not available")
	})

	t.Run("Services.Ledger is nil", func(t *testing.T) {
		ctx := &types.RPCContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   &types.ServiceContainer{Ledger: nil},
		}

		result, rpcErr := method.Handle(ctx, paramsJSON)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.LogDetail(), "Ledger service not available")
	})
}

// Transaction History Not Available Tests

func TestAccountTxTransactionHistoryNotAvailable(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return nil, svcerr.ErrTxHistoryUnavailable
	}

	params := map[string]any{
		"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	result, rpcErr := method.Handle(ctx, paramsJSON)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
	assert.Equal(t, "notEnabled", rpcErr.ErrorString)
	assert.Equal(t, "Not enabled in configuration.", rpcErr.Message)
}

// Method Metadata Tests

func TestAccountTxMethodMetadata(t *testing.T) {
	method := &handlers.AccountTxMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"account_tx should be accessible to guests")
	})

	t.Run("SupportedApiVersions includes v1, v2, and v3", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// Limit Parameter Tests

func TestAccountTxLimitParameter(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Custom limit is passed to service", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, uint32(10), limit, "Limit should be passed through")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        10,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
			"limit":   10,
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

		assert.Equal(t, float64(10), resp["limit"])
	})

	t.Run("Absent limit defaults to accountTx rdefault (200)", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			assert.Equal(t, uint32(200), limit, "absent limit routes through readLimitField -> accountTx default 200")
			return &types.AccountTxResult{
				Account:      account,
				LedgerMin:    1,
				LedgerMax:    10,
				Limit:        200,
				Transactions: []types.AccountTransaction{},
				Validated:    true,
			}, nil
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("explicit limit 0 is rejected (3.1.3 readLimitField)", func(t *testing.T) {
		paramsJSON := []byte(`{"account":"` + validAccount + `","limit":0}`)
		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'limit'.", rpcErr.Message)
	})

	t.Run("malformed limit is expected_field_error", func(t *testing.T) {
		paramsJSON := []byte(`{"account":"` + validAccount + `","limit":"abc"}`)
		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid field 'limit', not unsigned integer.", rpcErr.Message)
	})

	t.Run("below-min limit clamps to 10 for non-admin", func(t *testing.T) {
		var captured uint32
		mock.getAccountTransactionsFn = func(_ context.Context, account string, _, _ int64, limit uint32, _ *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
			captured = limit
			return &types.AccountTxResult{Account: account, Limit: limit, Transactions: []types.AccountTransaction{}, Validated: true}, nil
		}
		paramsJSON := []byte(`{"account":"` + validAccount + `","limit":5}`)
		_, rpcErr := method.Handle(ctx, paramsJSON)
		require.Nil(t, rpcErr)
		assert.Equal(t, uint32(10), captured, "non-admin limit below rmin clamps to 10")
	})
}

func TestAccountTxValidationPrecedence(t *testing.T) {
	mock := newAccountTxMock()
	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name       string
		apiVersion int
		params     string
		code       int
		message    string
	}{
		{
			name:       "limit before account",
			apiVersion: types.ApiVersion1,
			params:     `{"account":"not-an-account","limit":0}`,
			code:       types.RpcINVALID_PARAMS,
			message:    "Invalid field 'limit'.",
		},
		{
			name:       "binary type before limit",
			apiVersion: types.ApiVersion2,
			params:     `{"binary":"true","limit":0}`,
			code:       types.RpcINVALID_PARAMS,
			message:    "Invalid field 'binary'.",
		},
		{
			name:       "forward type before limit",
			apiVersion: types.ApiVersion2,
			params:     `{"forward":"true","limit":0}`,
			code:       types.RpcINVALID_PARAMS,
			message:    "Invalid field 'forward'.",
		},
		{
			name:       "legacy boolean coercion after limit",
			apiVersion: types.ApiVersion1,
			params:     `{"binary":{},"limit":0}`,
			code:       types.RpcINVALID_PARAMS,
			message:    "Invalid field 'limit'.",
		},
		{
			name:       "account before ledger arguments",
			apiVersion: types.ApiVersion2,
			params:     `{"account":"not-an-account","ledger_hash":"bad"}`,
			code:       types.RpcACT_MALFORMED,
			message:    "Account malformed.",
		},
		{
			name:       "ledger arguments before marker",
			apiVersion: types.ApiVersion2,
			params:     `{"account":"` + validAccount + `","ledger_hash":"bad","marker":{}}`,
			code:       types.RpcINVALID_PARAMS,
			message:    "ledgerHashMalformed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := &types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: test.apiVersion,
				Services:   newTestServicesAccountTx(mock),
			}

			_, rpcErr := method.Handle(ctx, json.RawMessage(test.params))
			require.NotNil(t, rpcErr)
			assert.Equal(t, test.code, rpcErr.Code)
			assert.Equal(t, test.message, rpcErr.Message)
		})
	}
}

// InjectDeliveredAmount Tests

func TestAccountTxInjectDeliveredAmount(t *testing.T) {
	// Test the InjectDeliveredAmount function directly via exported function name
	// Since the function is unexported (injectDeliveredAmount), we test it
	// indirectly through the handler's JSON mode behavior.

	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// We test indirectly: when a Payment transaction is decoded in JSON mode,
	// the handler should inject DeliveredAmount into the metadata.
	// Since we can't easily construct a valid binary Payment blob in unit tests
	// without the full codec, we verify the handler doesn't crash with
	// minimal blobs and that the overall flow works.

	txBlob := []byte{0x12, 0x00, 0x00}
	meta := []byte{0x20, 0x1C}

	txHash := [32]byte{0xAA, 0xBB, 0xCC}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 5,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{
					Hash:        txHash,
					LedgerIndex: 3,
					TxBlob:      txBlob,
					Meta:        meta,
				},
			},
			Validated: true,
		}, nil
	}

	params := map[string]any{
		"account": validAccount,
		"binary":  false,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	// This should not panic even with minimal/invalid tx blobs
	result, rpcErr := method.Handle(ctx, paramsJSON)
	require.Nil(t, rpcErr, "Handler should not error on decode failure, should fallback")
	require.NotNil(t, result)

	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	err = json.Unmarshal(resultJSON, &resp)
	require.NoError(t, err)

	// Verify the transactions array exists
	txs := resp["transactions"].([]any)
	require.Len(t, txs, 1)

	tx0 := txs[0].(map[string]any)
	txJSON, ok := tx0["tx"].(map[string]any)
	require.True(t, ok)
	expectedHash := strings.ToUpper(hex.EncodeToString(txHash[:]))
	assert.Equal(t, expectedHash, txJSON["hash"])
	assert.Equal(t, float64(3), txJSON["inLedger"])
	assert.Equal(t, float64(3), txJSON["ledger_index"])
	assert.NotContains(t, tx0, "hash")
	assert.NotContains(t, tx0, "ledger_index")
}

// Service Error Propagation Tests

func TestAccountTxServiceErrors(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Generic service error", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, errors.New("database connection failed")
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
		assert.Contains(t, rpcErr.LogDetail(), "Failed to get account transactions")
	})

	t.Run("Account not found error", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, svcerr.ErrAccountNotFound
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, 19, rpcErr.Code) // actNotFound
		assert.Contains(t, rpcErr.Message, "Account not found.")
	})

	t.Run("Transaction history not available", func(t *testing.T) {
		mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
			return nil, svcerr.ErrTxHistoryUnavailable
		}

		params := map[string]any{
			"account": validAccount,
		}
		paramsJSON, err := json.Marshal(params)
		require.NoError(t, err)

		result, rpcErr := method.Handle(ctx, paramsJSON)
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
		assert.Equal(t, "notEnabled", rpcErr.ErrorString)
		assert.Equal(t, "Not enabled in configuration.", rpcErr.Message)
	})
}

// Validated Field Tests
// Based on rippled AccountTx_test.cpp - validated flag in each transaction

func TestAccountTxValidatedField(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	txHash := [32]byte{0x01}
	txBlob := []byte{0x12, 0x00}
	meta := []byte{0x20, 0x1C}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: 1,
			LedgerMax: 10,
			Limit:     200,
			Transactions: []types.AccountTransaction{
				{Hash: txHash, LedgerIndex: 3, TxBlob: txBlob, Meta: meta},
			},
			Validated: true,
		}, nil
	}

	// Test in binary mode where we can check the validated flag easily
	params := map[string]any{
		"account": validAccount,
		"binary":  true,
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

	// Top-level validated
	assert.Equal(t, true, resp["validated"])

	// Per-transaction validated
	txs := resp["transactions"].([]any)
	require.Len(t, txs, 1)
	tx0 := txs[0].(map[string]any)
	assert.Equal(t, true, tx0["validated"],
		"Each transaction entry should have validated=true")
}

func TestAccountTxCTIDUsesTransactionNetworkID(t *testing.T) {
	const (
		validAccount     = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		ledgerIndex      = uint32(3)
		transactionIndex = uint32(4)
		serverNetworkID  = uint32(7)
		transactionNetID = uint32(9)
		expectedCTID     = "C000000300040009"
	)

	txHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Sequence":        uint32(1),
		"NetworkID":       transactionNetID,
	})
	require.NoError(t, err)
	txBlob, err := hex.DecodeString(txHex)
	require.NoError(t, err)

	mock := newAccountTxMock()
	mock.serverInfo.NetworkID = serverNetworkID
	mock.getAccountTransactionsFn = func(_ context.Context, account string, _, _ int64, _ uint32, _ *types.AccountTxMarker, _ bool) (*types.AccountTxResult, error) {
		return &types.AccountTxResult{
			Account:   account,
			LedgerMin: ledgerIndex,
			LedgerMax: ledgerIndex,
			Limit:     200,
			Transactions: []types.AccountTransaction{{
				LedgerIndex: ledgerIndex,
				TxnSeq:      transactionIndex,
				TxBlob:      txBlob,
			}},
			Validated: true,
		}, nil
	}

	for _, test := range []struct {
		name       string
		apiVersion int
		txKey      string
	}{
		{name: "API v1 tx", apiVersion: types.ApiVersion1, txKey: "tx"},
		{name: "API v2 tx_json", apiVersion: types.ApiVersion2, txKey: "tx_json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{"account": validAccount})
			require.NoError(t, err)
			result, rpcErr := (&handlers.AccountTxMethod{}).Handle(&types.RPCContext{
				Context:    context.Background(),
				Role:       types.RoleUser,
				ApiVersion: test.apiVersion,
				Services:   newTestServicesAccountTx(mock),
			}, params)
			require.Nil(t, rpcErr)
			response, ok := result.(map[string]any)
			require.True(t, ok)
			transactions, ok := response["transactions"].([]map[string]any)
			require.True(t, ok)
			require.Len(t, transactions, 1)
			tx, ok := transactions[0][test.txKey].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, expectedCTID, tx["ctid"])
		})
	}
}

// Account parameter passed to service correctly

func TestAccountTxAccountPassedToService(t *testing.T) {
	mock := newAccountTxMock()
	services := newTestServicesAccountTx(mock)

	method := &handlers.AccountTxMethod{}
	validAccount := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ctx := &types.RPCContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	mock.getAccountTransactionsFn = func(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *types.AccountTxMarker, forward bool) (*types.AccountTxResult, error) {
		assert.Equal(t, validAccount, account, "Account should be passed to service")
		return &types.AccountTxResult{
			Account:      account,
			LedgerMin:    1,
			LedgerMax:    5,
			Limit:        200,
			Transactions: []types.AccountTransaction{},
			Validated:    true,
		}, nil
	}

	params := map[string]any{
		"account": validAccount,
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

	assert.Equal(t, validAccount, resp["account"],
		"Response should echo back the account")
}

package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test Helpers

// mockLedgerServiceMissingMethods extends mockLedgerService for testing new methods
type mockLedgerServiceMissingMethods struct {
	*mockLedgerService
	ownerInfo    *types.OwnerInfoResult
	ownerInfoErr error
}

func newMockLedgerServiceMissingMethods() *mockLedgerServiceMissingMethods {
	return &mockLedgerServiceMissingMethods{
		mockLedgerService: newMockLedgerService(),
	}
}

// GetOwnerInfo implements types.OwnerDirectoryReader so owner_info tests can
// inject owner-directory contents or a specific error.
func (m *mockLedgerServiceMissingMethods) GetOwnerInfo(_ context.Context, _ string, _ string) (*types.OwnerInfoResult, error) {
	if m.ownerInfoErr != nil {
		return nil, m.ownerInfoErr
	}
	if m.ownerInfo != nil {
		return m.ownerInfo, nil
	}
	return &types.OwnerInfoResult{}, nil
}

// servicesForMissingMethods builds a per-test ServiceContainer for tests
// that exercise the "missing methods" handlers.
func servicesForMissingMethods(mock *mockLedgerServiceMissingMethods) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger: mock,
	}
}

// mockValidatorList is a minimal ValidatorListReader for unl_list tests.
type mockValidatorList struct {
	masterKeys [][33]byte
	listed     []types.ListedValidator
}

func (m *mockValidatorList) PublisherCount() int                            { return 0 }
func (m *mockValidatorList) Threshold() int                                 { return 0 }
func (m *mockValidatorList) Publishers() []types.ValidatorListPublisherInfo { return nil }
func (m *mockValidatorList) Sites() []types.ValidatorListSiteInfo           { return nil }
func (m *mockValidatorList) TrustedMasterKeys() [][33]byte                  { return m.masterKeys }
func (m *mockValidatorList) ListedValidators() []types.ListedValidator      { return m.listed }

// FetchInfoMethod Tests
// Reference: rippled/src/xrpld/rpc/handlers/FetchInfo.cpp

func TestFetchInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.FetchInfoMethod{}

	t.Run("Returns response with clear flag", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"clear": true}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]any)
		assert.Contains(t, resultMap, "info")
	})

	t.Run("Returns response without clear flag", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})

	t.Run("Supports all API versions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// OwnerInfoMethod Tests
// Reference: rippled/src/test/rpc/OwnerInfo_test.cpp

func TestOwnerInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.OwnerInfoMethod{}

	t.Run("Missing account parameter returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Empty account returns per-section actMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"account": ""}`)
		result, rpcErr := method.Handle(ctx, params)

		// rippled OwnerInfo.cpp:50-58 — a present-but-empty account is not a
		// top-level error; each section carries actMalformed and the overall
		// response stays a success.
		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		for _, section := range []string{"accepted", "current"} {
			errObj, ok := resultMap[section].(map[string]any)
			require.True(t, ok, "%s section should carry an error object", section)
			assert.Equal(t, "actMalformed", errObj["error"])
			assert.Equal(t, "Account malformed.", errObj["error_message"])
			// rippled inject_error emits only error/error_code/error_message;
			// the embedded object must not carry go-xrpl's internal type field.
			assert.NotContains(t, errObj, "type")
		}
	})

	t.Run("Groups offers and trust lines for both ledgers", func(t *testing.T) {
		objMock := newMockLedgerServiceMissingMethods()
		objMock.ownerInfo = &types.OwnerInfoResult{
			Offers:      []types.AccountObjectItem{{Index: "aa", LedgerEntryType: "Offer", Data: []byte{0x01}}},
			RippleLines: []types.AccountObjectItem{{Index: "bb", LedgerEntryType: "RippleState", Data: []byte{0x02}}},
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   servicesForMissingMethods(objMock),
		}

		params := json.RawMessage(`{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)

		for _, section := range []string{"accepted", "current"} {
			sec, ok := resultMap[section].(map[string]any)
			require.True(t, ok, "%s section should be present", section)
			offers := sec["offers"].([]any)
			lines := sec["ripple_lines"].([]any)
			assert.Len(t, offers, 1, "%s should have one offer", section)
			assert.Len(t, lines, 1, "%s should have one trust line", section)
		}
	})

	t.Run("Account with no owned objects returns empty sections", func(t *testing.T) {
		nfMock := newMockLedgerServiceMissingMethods()
		// The default mock returns an empty OwnerInfoResult, mirroring an
		// account with no owner directory.
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   servicesForMissingMethods(nfMock),
		}

		params := json.RawMessage(`{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		// rippled returns an empty object per section (no offers / ripple_lines
		// keys) when the account owns nothing.
		accepted := resultMap["accepted"].(map[string]any)
		assert.Empty(t, accepted)
		assert.NotContains(t, accepted, "offers")
		assert.NotContains(t, accepted, "ripple_lines")
	})

	t.Run("Malformed account returns per-section actMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"account": "not-a-valid-account"}`)
		result, rpcErr := method.Handle(ctx, params)

		// rippled embeds actMalformed in each section with overall success.
		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		for _, section := range []string{"accepted", "current"} {
			errObj, ok := resultMap[section].(map[string]any)
			require.True(t, ok, "%s section should carry an error object", section)
			assert.Equal(t, "actMalformed", errObj["error"])
		}
	})

	t.Run("X-address is rejected as malformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		// rippled parseBase58<AccountID> is classic-only; an X-address yields
		// per-section actMalformed, not a top-level error.
		params := json.RawMessage(`{"account": "X7AcgcsBL6XDcUb289X4mJ8ZEA3LBj2GAGYjmkPm5Y9XAh7"}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		accepted, ok := resultMap["accepted"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "actMalformed", accepted["error"])
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// LedgerHeaderMethod Tests
// Reference: rippled/src/test/rpc/LedgerHeader_test.cpp

func TestLedgerHeaderMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LedgerHeaderMethod{}

	t.Run("Current ledger returns error when GetLedgerBySequence not implemented", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerBySequence
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"ledger_index": "current"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Returns lgrNotFound because GetLedgerBySequence returns error
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("Validated ledger returns error when GetLedgerBySequence not implemented", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"ledger_index": "validated"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("Supports only API version 1", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.NotContains(t, versions, types.ApiVersion2)
		assert.NotContains(t, versions, types.ApiVersion3)
	})
}

// LedgerRequestMethod Tests
// Reference: rippled/src/test/rpc/LedgerRequestRPC_test.cpp

func TestLedgerRequestMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LedgerRequestMethod{}

	newCtx := func() *types.RpcContext {
		return &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}
	}

	t.Run("Rejects both ledger_hash and ledger_index", func(t *testing.T) {
		_, rpcErr := method.Handle(newCtx(), json.RawMessage(
			`{"ledger_hash":"`+strings.Repeat("A", 64)+`","ledger_index":1}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Rejects neither ledger_hash nor ledger_index", func(t *testing.T) {
		_, rpcErr := method.Handle(newCtx(), json.RawMessage(`{}`))
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Rejects ledger_index at or beyond the validated ledger", func(t *testing.T) {
		// The base mock's validated ledger is seq 2.
		result, rpcErr := method.Handle(newCtx(), json.RawMessage(`{"ledger_index": 100}`))
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Not found and no acquisition subsystem returns lgrNotFound", func(t *testing.T) {
		// ledger_hash path: the mock has no ledger and no RequestLedger wired.
		result, rpcErr := method.Handle(newCtx(), json.RawMessage(
			`{"ledger_hash":"`+strings.Repeat("B", 64)+`"}`))
		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcLGR_NOT_FOUND, rpcErr.Code)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LedgerCleanerMethod Tests

func TestLedgerCleanerMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LedgerCleanerMethod{}

	t.Run("Unavailable when no cleaner is wired", func(t *testing.T) {
		// The verifier is only wired when a node store is configured; with no
		// cleaner the handler reports the service unavailable.
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
	})

	t.Run("Configures and reports status when wired", func(t *testing.T) {
		// The network/sync gate is enforced by the dispatcher (conditionMet);
		// calling Handle directly here exercises the handler in isolation.
		var gotParams types.LedgerCleanerParams
		services.LedgerCleanerConfigure = func(p types.LedgerCleanerParams) types.LedgerCleanerStatus {
			gotParams = p
			return types.LedgerCleanerStatus{
				State:        "running",
				MinLedger:    5,
				MaxLedger:    9,
				CheckNodes:   true,
				MissingNodes: 2,
				Failures:     1,
			}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"min_ledger":5,"max_ledger":9,"full":true}`))
		require.Nil(t, rpcErr)
		resp, ok := result.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "running", resp["status"])
		assert.Equal(t, true, resp["check_nodes"])
		assert.Equal(t, uint64(2), resp["missing_nodes"])
		assert.Equal(t, 1, resp["fail_counts"])
		assert.Equal(t, "Ledger cleaner configured", resp["message"])
		assert.True(t, gotParams.Full)
		require.NotNil(t, gotParams.MinLedger)
		assert.Equal(t, uint32(5), *gotParams.MinLedger)
	})

	t.Run("RequiredCondition is NeedsNetworkConnection", func(t *testing.T) {
		assert.Equal(t, types.NeedsNetworkConnection, method.RequiredCondition())
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LedgerDiffMethod Tests

func TestLedgerDiffMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LedgerDiffMethod{}

	t.Run("Returns gRPC only error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// ledger_diff is gRPC only in rippled
		assert.Contains(t, rpcErr.Message, "gRPC")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// SimulateMethod Tests
// Reference: rippled/src/test/rpc/Simulate_test.cpp

func TestSimulateMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.SimulateMethod{}

	t.Run("Missing tx_json and tx_blob returns error", func(t *testing.T) {
		// Based on Simulate_test.cpp::testParamErrors - "No params"
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Both tx_json and tx_blob returns not implemented (stub)", func(t *testing.T) {
		// simulate is a stub - returns RpcNOT_IMPL regardless of params
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"tx_json": {}, "tx_blob": "1200"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Stub returns NOT_IMPL after parameter validation passes
		assert.True(t, rpcErr.Code == types.RpcINVALID_PARAMS || rpcErr.Code == types.RpcNOT_IMPL)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})

	t.Run("Supports all API versions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TxReduceRelayMethod Tests

func TestTxReduceRelayMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.TxReduceRelayMethod{}

	t.Run("Returns zeroed txr_* metrics when overlay not wired", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		m := result.(map[string]any)
		// Mirrors rippled's txMetrics() shape: txr_* keys, decimal strings.
		assert.Equal(t, "0", m["txr_tx_cnt"])
		assert.Equal(t, "0", m["txr_have_txs_cnt"])
		assert.Contains(t, m, "txr_missing_tx_freq")
		assert.NotContains(t, m, "transactions_relayed")
	})

	t.Run("Renders real metrics as txr_* decimal strings when wired", func(t *testing.T) {
		svc := servicesForMissingMethods(mock)
		svc.TxReduceRelayMetrics = func() types.TxReduceRelayMetrics {
			return types.TxReduceRelayMetrics{
				TxCnt:           12,
				TxSz:            3456,
				HaveTxCnt:       5,
				HaveTxSz:        789,
				TransactionsCnt: 3,
				TransactionsSz:  640,
				MissingTxFreq:   7,
			}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleUser,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		m := result.(map[string]any)
		assert.Equal(t, "12", m["txr_tx_cnt"])
		assert.Equal(t, "3456", m["txr_tx_sz"])
		assert.Equal(t, "5", m["txr_have_txs_cnt"])
		assert.Equal(t, "789", m["txr_have_txs_sz"])
		assert.Equal(t, "3", m["txr_transactions_cnt"])
		assert.Equal(t, "640", m["txr_transactions_sz"])
		assert.Equal(t, "7", m["txr_missing_tx_freq"])
		// Unfed metrics still render as "0".
		assert.Equal(t, "0", m["txr_selected_cnt"])
		assert.Equal(t, "0", m["txr_get_ledger_cnt"])
	})

	t.Run("RequiredRole is User", func(t *testing.T) {
		// rippled: Role::USER (Handler.cpp line 179)
		assert.Equal(t, types.RoleUser, method.RequiredRole())
	})
}

// ConnectMethod Tests
// Reference: rippled/src/test/rpc/Connect_test.cpp

func TestConnectMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	mock.standalone = true // Standalone mode
	services := servicesForMissingMethods(mock)

	method := &handlers.ConnectMethod{}

	t.Run("Standalone mode returns notSynced error", func(t *testing.T) {
		// Based on Connect_test.cpp::testErrors
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"ip": "127.0.0.1", "port": 51235}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_SYNCED, rpcErr.Code)
		assert.Equal(t, "notSynced", rpcErr.ErrorString)
		assert.Equal(t, "Not synced to the network.", rpcErr.Message)
	})

	t.Run("Standalone with empty params returns notSynced", func(t *testing.T) {
		// rippled checks standalone before the ip field (Connect.cpp:41), so
		// connect "{}" in standalone is notSynced, not a missing-field error
		// (Connect_test.cpp::testErrors).
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, json.RawMessage(`{}`))

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_SYNCED, rpcErr.Code)
	})

	t.Run("Missing ip parameter returns error", func(t *testing.T) {
		// On the live (wired-overlay) path rippled is non-standalone, so a
		// missing ip surfaces as a missing-field error.
		svc := &types.ServiceContainer{
			Ledger:      mock,
			PeerConnect: func(string) error { return nil },
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		result, rpcErr := method.Handle(ctx, json.RawMessage(`{}`))

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Wired overlay initiates background connection", func(t *testing.T) {
		got := make(chan string, 1)
		svc := &types.ServiceContainer{
			Ledger: mock,
			PeerConnect: func(addr string) error {
				got <- addr
				return nil
			},
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		params := json.RawMessage(`{"ip": "10.0.0.1", "port": 2459}`)
		result, rpcErr := method.Handle(ctx, params)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		select {
		case addr := <-got:
			assert.Equal(t, "10.0.0.1:2459", addr)
		case <-time.After(time.Second):
			t.Fatal("PeerConnect was not invoked")
		}
	})

	t.Run("Wired overlay defaults the port", func(t *testing.T) {
		got := make(chan string, 1)
		svc := &types.ServiceContainer{
			Ledger:      mock,
			PeerConnect: func(addr string) error { got <- addr; return nil },
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		_, rpcErr := method.Handle(ctx, json.RawMessage(`{"ip": "10.0.0.2"}`))
		require.Nil(t, rpcErr)
		select {
		case addr := <-got:
			assert.Equal(t, "10.0.0.2:51235", addr)
		case <-time.After(time.Second):
			t.Fatal("PeerConnect was not invoked")
		}
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// PrintMethod Tests

func TestPrintMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.PrintMethod{}

	t.Run("Returns status message", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]any)
		assert.Contains(t, resultMap, "ledger")
	})

	t.Run("Aggregates wired subsystem state", func(t *testing.T) {
		svc := servicesForMissingMethods(mock)
		svc.PeerDisconnects = func() (uint64, uint64) { return 7, 3 }
		svc.JqTransOverflow = func() uint64 { return 9 }
		svc.LastCloseInfo = func() (int, int) { return 5, 1900 }
		svc.StateAccounting = func() types.StateAccountingSnapshot {
			return types.StateAccountingSnapshot{
				Modes: map[string]types.StateAccountingEntry{
					"full": {Transitions: 2, DurationUs: 1000},
				},
				CurrentDurationUs: 500,
			}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
			PeerSource: &stubPeerSource{
				peers:   []map[string]any{{"address": "192.0.2.1:51235"}},
				cluster: map[string]any{},
			},
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		m := result.(map[string]any)

		assert.Contains(t, m, "ledger")
		assert.Equal(t, 1, m["overlay"].(map[string]any)["count"])

		// Cumulative counters are decimal strings, matching rippled's
		// std::to_string and go-xrpl's server_info.
		counters := m["counters"].(map[string]any)
		assert.Equal(t, "7", counters["peer_disconnects"])
		assert.Equal(t, "3", counters["peer_disconnects_resources"])
		assert.Equal(t, "9", counters["jq_trans_overflow"])

		assert.Equal(t, 5, m["last_close"].(map[string]any)["proposers"])

		sa := m["state_accounting"].(map[string]any)
		assert.Equal(t, "500", sa["current_duration_us"])
		full := sa["states"].(map[string]any)["full"].(map[string]any)
		assert.Equal(t, "2", full["transitions"])
		assert.Equal(t, "1000", full["duration_us"])
	})

	t.Run("Subtree selector narrows output", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   servicesForMissingMethods(mock),
		}

		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"params":["ledger"]}`))
		require.Nil(t, rpcErr)
		m := result.(map[string]any)
		assert.Equal(t, []string{"ledger"}, keysOf(m))

		// Unknown section yields an empty object, matching rippled's
		// "no such property-stream source" behaviour.
		result, rpcErr = method.Handle(ctx, json.RawMessage(`{"params":["nope"]}`))
		require.Nil(t, rpcErr)
		assert.Empty(t, result.(map[string]any))
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// ValidatorInfoMethod Tests
// Reference: rippled/src/test/rpc/ValidatorInfo_test.cpp

func TestValidatorInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.ValidatorInfoMethod{}

	t.Run("Non-validator returns error", func(t *testing.T) {
		// Based on ValidatorInfo_test.cpp::testErrors
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// Rippled's not_validator_error() = make_param_error("not a validator").
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "not a validator", rpcErr.Message)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// CanDeleteMethod Tests

func TestCanDeleteMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.CanDeleteMethod{}

	t.Run("Returns not enabled error (requires SHAMapStore)", func(t *testing.T) {
		// can_delete requires SHAMapStore advisory delete configuration
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcNOT_ENABLED, rpcErr.Code)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// GetAggregatePriceMethod Tests
// Reference: rippled/src/test/rpc/GetAggregatePrice_test.cpp

func TestGetAggregatePriceMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.GetAggregatePriceMethod{}

	// Convenience helper to make a valid-looking oracle entry for params
	// (uses a real-format r-address so account parsing doesn't fail before
	// the check we actually want to test).
	validOracle := `{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "oracle_document_id": 1}`

	t.Run("Missing oracles parameter returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
		assert.Equal(t, "Missing field 'oracles'.", rpcErr.Message)
	})

	t.Run("Empty oracles array returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": []}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Oracles not array returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": "bad"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Missing base_asset returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Missing field 'base_asset'.", rpcErr.Message)
	})

	t.Run("Missing quote_asset returns missing_field_error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Missing field 'quote_asset'.", rpcErr.Message)
	})

	t.Run("Trim=0 returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 0}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Trim=26 returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 26}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Invalid base_asset returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		// empty string
		params := json.RawMessage(`{"base_asset": "", "quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)

		// invalid currency (4 chars, not hex)
		params = json.RawMessage(`{"base_asset": "ABCD", "quote_asset": "USD", "oracles": [` + validOracle + `]}`)
		result, rpcErr = method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Oracle entry missing account returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [{"oracle_document_id": 1}]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Oracle entry missing oracle_document_id returns oracleMalformed", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [{"account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}]}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcORACLE_MALFORMED, rpcErr.Code)
		assert.Equal(t, "oracleMalformed", rpcErr.ErrorString)
	})

	t.Run("Invalid trim type returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		// float trim
		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "trim": 1.2}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("Invalid time_threshold type returns invalidParams", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		// non-numeric time_threshold
		params := json.RawMessage(`{"base_asset": "XRP", "quote_asset": "USD", "oracles": [` + validOracle + `], "time_threshold": "none"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, "invalidParams", rpcErr.ErrorString)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// GetCountsMethod Tests
// Reference: rippled/src/test/rpc/GetCounts_test.cpp

func TestGetCountsMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.GetCountsMethod{}

	t.Run("Returns server counts info", func(t *testing.T) {
		// get_counts returns server statistics
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)
		resultMap := result.(map[string]any)
		assert.Contains(t, resultMap, "standalone")
	})

	t.Run("Emits node store counters when wired", func(t *testing.T) {
		svc := servicesForMissingMethods(mock)
		svc.GetCounts = func() types.CountsResult {
			return types.CountsResult{
				Standalone: true,
				LocalTxs:   3,
				NodeStore: &types.NodeStoreCounts{
					Reads:      100,
					FetchHits:  90,
					Writes:     40,
					ReadBytes:  2048,
					WriteBytes: 1024,
				},
			}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		m := result.(map[string]any)

		assert.Equal(t, true, m["standalone"])
		assert.Equal(t, 3, m["local_txs"])
		assert.Contains(t, m, "uptime")
		// rippled stringifies the node_* counters via std::to_string.
		assert.Equal(t, "100", m["node_reads_total"])
		// node_reads_hit is the count of reads that found data (fetchHitCount_),
		// not the in-memory cache-hit count.
		assert.Equal(t, "90", m["node_reads_hit"])
		assert.Equal(t, "40", m["node_writes"])
		assert.Equal(t, "1024", m["node_written_bytes"])
		assert.Equal(t, "2048", m["node_read_bytes"])
		// Fields rippled never emits must be absent.
		assert.NotContains(t, m, "node_reads_duration_us")
		assert.NotContains(t, m, "nodestore_backend")
		assert.NotContains(t, m, "node_hit_rate")
		assert.NotContains(t, m, "node_cache_size")
		assert.NotContains(t, m, "node_cache_max_size")
	})

	t.Run("Omits node store block and local_txs when unavailable", func(t *testing.T) {
		svc := servicesForMissingMethods(mock)
		svc.GetCounts = func() types.CountsResult {
			return types.CountsResult{Standalone: false, LocalTxs: 0}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		m := result.(map[string]any)
		assert.NotContains(t, m, "node_reads_total")
		// rippled omits local_txs when the count is zero (GetCounts.cpp:96-100).
		assert.NotContains(t, m, "local_txs")
		assert.Contains(t, m, "standalone")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LogLevelMethod Tests

func TestLogLevelMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LogLevelMethod{}

	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Register a live root config so set operations take effect, restoring
	// the unset state when the test finishes.
	logCfg := &xrpllog.Config{Level: xrpllog.LevelInfo}
	xrpllog.SetRootConfig(logCfg)
	t.Cleanup(func() { xrpllog.SetRootConfig(nil) })

	getLevels := func(t *testing.T) map[string]string {
		t.Helper()
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		levels, ok := result.(map[string]any)["levels"].(map[string]string)
		require.True(t, ok, "levels missing from response")
		return levels
	}

	t.Run("Returns current log levels without params", func(t *testing.T) {
		levels := getLevels(t)
		assert.Equal(t, "Info", levels["base"])
	})

	t.Run("Invalid severity returns error", func(t *testing.T) {
		params := json.RawMessage(`{"severity": "invalid_level"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
		assert.Equal(t, "Invalid parameters.", rpcErr.Message)
	})

	t.Run("Valid severity levels are accepted", func(t *testing.T) {
		// rippled Logs::fromString aliases, matched case-insensitively.
		validLevels := []string{
			"trace", "debug", "info", "information", "warn", "warning",
			"warnings", "error", "errors", "fatal", "fatals", "WARNING",
		}

		for _, level := range validLevels {
			t.Run("severity: "+level, func(t *testing.T) {
				params, _ := json.Marshal(map[string]string{"severity": level})
				result, rpcErr := method.Handle(ctx, params)

				require.Nil(t, rpcErr, "severity %s should be accepted", level)
				require.NotNil(t, result)
			})
		}
	})

	t.Run("Set base severity is reflected in get", func(t *testing.T) {
		params := json.RawMessage(`{"severity": "debug"}`)
		_, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)

		assert.Equal(t, "Debug", getLevels(t)["base"])

		_, rpcErr = method.Handle(ctx, json.RawMessage(`{"severity": "info"}`))
		require.Nil(t, rpcErr)
		assert.Equal(t, "Info", getLevels(t)["base"])
	})

	t.Run("Set partition severity is reflected in get", func(t *testing.T) {
		params := json.RawMessage(`{"severity": "trace", "partition": "Consensus"}`)
		_, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)

		levels := getLevels(t)
		assert.Equal(t, "Trace", levels["Consensus"])
		assert.Equal(t, "Info", levels["base"], "partition set must not change base")
	})

	t.Run("Partition base sets the base threshold", func(t *testing.T) {
		params := json.RawMessage(`{"severity": "warning", "partition": "BASE"}`)
		_, rpcErr := method.Handle(ctx, params)
		require.Nil(t, rpcErr)

		// The global threshold must change; no partition override named
		// "base" may be created (rippled treats partition "base" as the
		// base threshold, matched case-insensitively).
		global, partitions := xrpllog.GetCurrentLevels()
		assert.Equal(t, xrpllog.LevelWarn, global)
		assert.NotContains(t, partitions, "base")
		assert.NotContains(t, partitions, "BASE")

		_, rpcErr = method.Handle(ctx, json.RawMessage(`{"severity": "info"}`))
		require.Nil(t, rpcErr)
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// LogRotateMethod Tests

func TestLogRotateMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.LogRotateMethod{}

	t.Run("Returns rotation message", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]any)
		assert.Contains(t, resultMap, "message")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// AMMInfoMethod Tests
// Reference: rippled/src/test/rpc/AMMInfo_test.cpp

func TestAMMInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.AMMInfoMethod{}

	t.Run("Returns actMalformed when amm_account does not exist", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerEntry; rippled's
		// AMMInfo returns actMalformed when the amm_account is absent from
		// the ledger.
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"amm_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code)
		assert.Equal(t, "actMalformed", rpcErr.ErrorString)
		assert.Equal(t, "Account malformed.", rpcErr.Message)
	})

	t.Run("Returns actMalformed when amm_account is unparseable", func(t *testing.T) {
		// rippled's AMMInfo also returns actMalformed (not invalidParams)
		// when the amm_account fails to parse as an account.
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"amm_account": "not-a-valid-address"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_MALFORMED, rpcErr.Code)
		assert.Equal(t, "actMalformed", rpcErr.ErrorString)
		assert.Equal(t, "Account malformed.", rpcErr.Message)
	})

	t.Run("Returns AMM not found when looking up by assets", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{
			"asset": {"currency": "XRP"},
			"asset2": {"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcACT_NOT_FOUND, rpcErr.Code)
		assert.Equal(t, "actNotFound", rpcErr.ErrorString)
	})

	t.Run("Invalid parameters - neither assets nor amm_account", func(t *testing.T) {
		// Based on AMMInfo_test.cpp::testErrors - "Invalid parameters"
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - both assets and amm_account", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{
			"asset": {"currency": "XRP"},
			"asset2": {"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			"amm_account": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// VaultInfoMethod Tests

func TestVaultInfoMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.VaultInfoMethod{}

	t.Run("Returns vault not found when vault does not exist", func(t *testing.T) {
		// The mock returns "not implemented" for GetLedgerEntry, which becomes Vault not found
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"vault_id": "0000000000000000000000000000000000000000000000000000000000000000"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// entryNotFound is a bare rippled token with no numeric code (-1)
		assert.Equal(t, -1, rpcErr.Code)
	})

	t.Run("Returns vault not found when looking up by owner+seq", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", "seq": 1}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		// entryNotFound is a bare rippled token with no numeric code (-1)
		assert.Equal(t, -1, rpcErr.Code)
	})

	t.Run("Invalid vault_id format returns error", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{"vault_id": "invalid_hex"}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - neither vault_id nor owner+seq", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("Invalid parameters - both vault_id and owner", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		params := json.RawMessage(`{
			"vault_id": "0000000000000000000000000000000000000000000000000000000000000000",
			"owner": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		}`)
		result, rpcErr := method.Handle(ctx, params)

		assert.Nil(t, result)
		require.NotNil(t, rpcErr)
		assert.Equal(t, types.RpcINVALID_PARAMS, rpcErr.Code)
	})

	t.Run("RequiredRole is Guest", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole())
	})
}

// UnlListMethod Tests

func TestUnlListMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.UnlListMethod{}

	t.Run("Returns empty UNL with no validator list", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultMap := result.(map[string]any)
		unl := resultMap["unl"].([]any)
		assert.Empty(t, unl)
	})

	t.Run("Lists every listed validator with its real trusted flag", func(t *testing.T) {
		newKey := func(seed byte) [33]byte {
			var key [33]byte
			key[0] = 0xED
			for i := 1; i < 33; i++ {
				key[i] = seed + byte(i)
			}
			return key
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services: &types.ServiceContainer{
				Ledger: mock,
				ValidatorList: &mockValidatorList{listed: []types.ListedValidator{
					{MasterKey: newKey(0), Trusted: true},
					{MasterKey: newKey(100), Trusted: false},
				}},
			},
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		unl := result.(map[string]any)["unl"].([]any)
		require.Len(t, unl, 2)

		// rippled emits one entry per listed validator, trusted reflecting the
		// real UNL membership (not hardcoded true).
		trustedByKey := map[string]bool{}
		for _, e := range unl {
			entry := e.(map[string]any)
			pub, ok := entry["pubkey_validator"].(string)
			require.True(t, ok)
			assert.NotEmpty(t, pub)
			trustedByKey[pub] = entry["trusted"].(bool)
		}
		var sawTrusted, sawUntrusted bool
		for _, v := range trustedByKey {
			sawTrusted = sawTrusted || v
			sawUntrusted = sawUntrusted || !v
		}
		assert.True(t, sawTrusted, "a trusted listed validator should be present")
		assert.True(t, sawUntrusted, "an untrusted listed validator should be present")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// BlackListMethod Tests

func TestBlackListMethod(t *testing.T) {
	mock := newMockLedgerServiceMissingMethods()
	services := servicesForMissingMethods(mock)

	method := &handlers.BlackListMethod{}

	t.Run("Returns empty object when overlay not wired", func(t *testing.T) {
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}

		result, rpcErr := method.Handle(ctx, nil)

		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		assert.Empty(t, resultMap)
	})

	t.Run("Returns resource manager entries when wired", func(t *testing.T) {
		var gotThreshold *int
		svc := &types.ServiceContainer{
			Ledger: mock,
			ResourceBlacklist: func(threshold *int) map[string]any {
				gotThreshold = threshold
				return map[string]any{
					"1.2.3.4": map[string]any{"local": 6000, "remote": 0, "type": "inbound"},
				}
			},
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		result, rpcErr := method.Handle(ctx, json.RawMessage(`{"threshold": 100}`))

		require.Nil(t, rpcErr)
		resultMap := result.(map[string]any)
		assert.Contains(t, resultMap, "1.2.3.4")
		require.NotNil(t, gotThreshold)
		assert.Equal(t, 100, *gotThreshold)
	})

	t.Run("Omitting threshold passes nil", func(t *testing.T) {
		sawNil := false
		svc := &types.ServiceContainer{
			Ledger: mock,
			ResourceBlacklist: func(threshold *int) map[string]any {
				sawNil = threshold == nil
				return map[string]any{}
			},
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}

		_, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		assert.True(t, sawNil, "absent threshold should pass nil to the resource reader")
	})

	t.Run("RequiredRole is Admin", func(t *testing.T) {
		assert.Equal(t, types.RoleAdmin, method.RequiredRole())
	})
}

// Service Unavailable Tests

func TestMissingMethodsServiceUnavailable(t *testing.T) {
	// Test all methods handle nil Services gracefully
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	methods := []struct {
		name   string
		method types.MethodHandler
	}{
		{"FetchInfoMethod", &handlers.FetchInfoMethod{}},
		{"OwnerInfoMethod", &handlers.OwnerInfoMethod{}},
		{"LedgerHeaderMethod", &handlers.LedgerHeaderMethod{}},
		{"LedgerRequestMethod", &handlers.LedgerRequestMethod{}},
		{"LedgerCleanerMethod", &handlers.LedgerCleanerMethod{}},
		{"LedgerDiffMethod", &handlers.LedgerDiffMethod{}},
		{"SimulateMethod", &handlers.SimulateMethod{}},
		{"TxReduceRelayMethod", &handlers.TxReduceRelayMethod{}},
		{"ConnectMethod", &handlers.ConnectMethod{}},
		{"PrintMethod", &handlers.PrintMethod{}},
		{"ValidatorInfoMethod", &handlers.ValidatorInfoMethod{}},
		{"CanDeleteMethod", &handlers.CanDeleteMethod{}},
		{"GetAggregatePriceMethod", &handlers.GetAggregatePriceMethod{}},
		{"GetCountsMethod", &handlers.GetCountsMethod{}},
		{"LogLevelMethod", &handlers.LogLevelMethod{}},
		{"LogRotateMethod", &handlers.LogRotateMethod{}},
		{"AMMInfoMethod", &handlers.AMMInfoMethod{}},
		{"VaultInfoMethod", &handlers.VaultInfoMethod{}},
		{"UnlListMethod", &handlers.UnlListMethod{}},
		{"BlackListMethod", &handlers.BlackListMethod{}},
	}

	for _, tc := range methods {
		t.Run(tc.name+" handles nil Services", func(t *testing.T) {
			result, rpcErr := tc.method.Handle(ctx, nil)

			// Should return an error, not panic
			// Different methods may return different error codes (RpcINTERNAL, RpcINVALID_PARAMS, RpcNOT_IMPL)
			// The key is that they don't panic and handle nil Services gracefully
			if rpcErr != nil {
				assert.True(t, rpcErr.Code != 0, "Should have a non-zero error code")
				assert.Nil(t, result, "Result should be nil when there's an error")
			}
			// Some methods may return a result without Services (e.g., stub methods)
		})
	}
}

// Nil Ledger Service Tests

func TestMissingMethodsNilLedgerService(t *testing.T) {
	// Test all methods handle nil Ledger gracefully
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: nil},
	}

	// Methods that depend on ledger service should return RpcINTERNAL when Ledger is nil
	methods := []struct {
		name   string
		method types.MethodHandler
	}{
		{"PrintMethod", &handlers.PrintMethod{}},
		{"GetCountsMethod", &handlers.GetCountsMethod{}},
	}

	// Note: FetchInfoMethod is excluded — it reads the router's inbound-ledger
	// tracker, not the ledger, so it returns an empty info object (not an error)
	// with nil Ledger, matching rippled's empty result on a node not acquiring.
	// Note: UnlListMethod is excluded — it reads the validator-list service,
	// not the ledger, so it returns an empty UNL (not an error) with nil Ledger.
	// Note: LogLevelMethod and LogRotateMethod are excluded — they drive the
	// global logger, not the ledger service, so they succeed with a nil ledger
	// (current levels / a rotate-or-not-applicable message).
	// Note: BlackListMethod is excluded — it reads the overlay resource manager,
	// not the ledger, so it returns an empty object (not an error) with nil Ledger.

	for _, tc := range methods {
		t.Run(tc.name+" handles nil Ledger", func(t *testing.T) {
			result, rpcErr := tc.method.Handle(ctx, nil)

			// Should return an internal error, not panic
			require.NotNil(t, rpcErr)
			assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
			assert.Nil(t, result)
		})
	}

	// ValidatorInfoMethod and CanDeleteMethod don't depend on ledger service.
	// They return their own domain-specific errors unconditionally.
	// - ValidatorInfo: returns RpcINVALID_PARAMS / "not a validator" (mirrors rippled's not_validator_error())
	// - CanDelete: returns RpcNOT_ENABLED (advisory delete not configured)
	// This matches rippled where validator_info has NO_CONDITION and can_delete
	// checks advisoryDelete() before touching ledger state.
}

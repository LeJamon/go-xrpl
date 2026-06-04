package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNodePublicKey returns a deterministic node public key for fixtures
// (33-byte secp256k1 compressed form, base58-encoded with the node prefix).
func testNodePublicKey() string {
	var pk [33]byte
	pk[0] = 0x02
	for i := 1; i < 33; i++ {
		pk[i] = byte(i)
	}
	encoded, err := addresscodec.EncodeNodePublicKey(pk[:])
	if err != nil {
		panic(err)
	}
	return encoded
}

// mockLedgerServiceServerInfo extends mockLedgerService with server_info-specific behavior
type mockLedgerServiceServerInfo struct {
	*mockLedgerService
	serverState      string
	buildVersion     string
	peers            int
	loadFactor       float64
	ioLatencyMs      int
	validationQuorum int
	baseFee          uint64
	reserveBase      uint64
	reserveIncrement uint64
}

func newMockLedgerServiceServerInfo() *mockLedgerServiceServerInfo {
	return &mockLedgerServiceServerInfo{
		mockLedgerService: newMockLedgerService(),
		serverState:       "full",
		buildVersion:      "2.0.0-go-xrpl",
		peers:             0,
		loadFactor:        1.0,
		ioLatencyMs:       1,
		validationQuorum:  1,
		baseFee:           10,
		reserveBase:       10000000,
		reserveIncrement:  2000000,
	}
}

func (m *mockLedgerServiceServerInfo) GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64) {
	return m.baseFee, m.reserveBase, m.reserveIncrement
}

func (m *mockLedgerServiceServerInfo) GetServerInfo() types.LedgerServerInfo {
	return types.LedgerServerInfo{
		Standalone:               m.standalone,
		OpenLedgerSeq:            m.currentLedgerIndex,
		ClosedLedgerSeq:          m.closedLedgerIndex,
		ClosedLedgerCloseTime:    m.serverInfo.ClosedLedgerCloseTime,
		HaveValidated:            m.validatedLedgerIndex > 0,
		ValidatedLedgerSeq:       m.validatedLedgerIndex,
		ValidatedLedgerHash:      m.serverInfo.ValidatedLedgerHash,
		ValidatedLedgerCloseTime: m.serverInfo.ValidatedLedgerCloseTime,
		CompleteLedgers:          m.serverInfo.CompleteLedgers,
	}
}

// servicesForServerInfo builds a per-test ServiceContainer with a server_info mock.
func servicesForServerInfo(mock *mockLedgerServiceServerInfo) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger:        mock,
		NodePublicKey: testNodePublicKey(),
	}
}

// Response Field Tests
// Based on rippled ServerInfo_test.cpp testServerInfo()

// TestServerInfoResponseFields tests that server_info returns all expected fields
// Based on rippled ServerInfo_test.cpp: BEAST_EXPECT(info.isMember(jss::build_version));
func TestServerInfoResponseFields(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("info.build_version field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, err := json.Marshal(result)
		require.NoError(t, err)
		var resp map[string]any
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check info wrapper
		assert.Contains(t, resp, "info")
		info := resp["info"].(map[string]any)

		// Check build_version
		assert.Contains(t, info, "build_version")
		assert.NotEmpty(t, info["build_version"])
	})

	t.Run("info.complete_ledgers field present", func(t *testing.T) {
		mock.serverInfo.CompleteLedgers = "32570-75801862"

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "complete_ledgers")
		// Should be a string like "32570-75801862" or "empty"
		completeLedgers, ok := info["complete_ledgers"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, completeLedgers)
	})

	t.Run("info.hostid field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "hostid")
		hostid, ok := info["hostid"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, hostid)
	})

	t.Run("info.io_latency_ms field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "io_latency_ms")
		// io_latency_ms should be a number >= 0
		ioLatency, ok := info["io_latency_ms"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, ioLatency, float64(0))
	})

	t.Run("info.last_close fields present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "last_close")
		lastClose := info["last_close"].(map[string]any)

		// Check last_close.converge_time_s
		assert.Contains(t, lastClose, "converge_time_s")
		convergeTime, ok := lastClose["converge_time_s"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, convergeTime, float64(0))

		// Check last_close.proposers
		assert.Contains(t, lastClose, "proposers")
		proposers, ok := lastClose["proposers"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, proposers, float64(0))
	})

	t.Run("info.load_factor field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "load_factor")
		loadFactor, ok := info["load_factor"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, loadFactor, float64(1))
	})

	t.Run("info.peers field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "peers")
		peers, ok := info["peers"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, peers, float64(0))
	})

	t.Run("info.pubkey_node field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "pubkey_node")
		pubkeyNode, ok := info["pubkey_node"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, pubkeyNode)
		// pubkey_node should start with 'n' prefix
		assert.True(t, len(pubkeyNode) > 0 && pubkeyNode[0] == 'n',
			"pubkey_node should start with 'n'")
	})

	t.Run("info.server_state field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "server_state")
		serverState, ok := info["server_state"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, serverState)
	})

	t.Run("info.uptime field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "uptime")
		uptime, ok := info["uptime"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, uptime, float64(0))
	})

	t.Run("info.validation_quorum field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "validation_quorum")
		validationQuorum, ok := info["validation_quorum"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, validationQuorum, float64(1))
	})
}

// TestServerInfoValidatedLedgerFields tests the validated_ledger nested object fields
func TestServerInfoValidatedLedgerFields(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("validated_ledger.age field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "validated_ledger")
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "age")
		age, ok := validatedLedger["age"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, age, float64(0))
	})

	t.Run("validated_ledger.base_fee_xrp field present", func(t *testing.T) {
		mock.baseFee = 10 // 10 drops

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "base_fee_xrp")
		baseFeeXRP, ok := validatedLedger["base_fee_xrp"].(float64)
		assert.True(t, ok)
		// 10 drops = 0.00001 XRP
		assert.Equal(t, 0.00001, baseFeeXRP)
	})

	t.Run("validated_ledger.hash field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "hash")
		hash, ok := validatedLedger["hash"].(string)
		assert.True(t, ok)
		// Hash should be 64 hex characters
		assert.Len(t, hash, 64)
	})

	t.Run("validated_ledger.reserve_base_xrp field present", func(t *testing.T) {
		mock.reserveBase = 10000000 // 10 XRP in drops

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "reserve_base_xrp")
		reserveBaseXRP, ok := validatedLedger["reserve_base_xrp"].(float64)
		assert.True(t, ok)
		// 10000000 drops = 10 XRP
		assert.Equal(t, float64(10), reserveBaseXRP)
	})

	t.Run("validated_ledger.reserve_inc_xrp field present", func(t *testing.T) {
		mock.reserveIncrement = 2000000 // 2 XRP in drops

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "reserve_inc_xrp")
		reserveIncXRP, ok := validatedLedger["reserve_inc_xrp"].(float64)
		assert.True(t, ok)
		// 2000000 drops = 2 XRP
		assert.Equal(t, float64(2), reserveIncXRP)
	})

	t.Run("validated_ledger.seq field present", func(t *testing.T) {
		mock.validatedLedgerIndex = 75801862

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)
		validatedLedger := info["validated_ledger"].(map[string]any)

		assert.Contains(t, validatedLedger, "seq")
		seq, ok := validatedLedger["seq"].(float64)
		assert.True(t, ok)
		assert.Equal(t, float64(75801862), seq)
	})
}

// Server State Tests

// TestServerInfoServerStates tests different server state values
// Based on rippled's NetworkOPs operating modes
func TestServerInfoServerStates(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// Valid server states per XRPL documentation
	validStates := []struct {
		name        string
		standalone  bool
		description string
	}{
		{"standalone", true, "Server is running in standalone mode"},
		{"full", false, "Server has full history and is synced"},
	}

	for _, tc := range validStates {
		t.Run("server_state: "+tc.name, func(t *testing.T) {
			mock.standalone = tc.standalone

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]any)

			serverState := info["server_state"].(string)
			assert.NotEmpty(t, serverState)
			t.Logf("Server state for standalone=%v: %s", tc.standalone, serverState)
		})
	}
}

// TestServerInfoStandaloneMode tests standalone-specific behavior
func TestServerInfoStandaloneMode(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	mock.standalone = true
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("Standalone mode returns correct server_state", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		serverState := info["server_state"].(string)
		assert.Equal(t, "standalone", serverState)
	})

	t.Run("Standalone mode has zero peers", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		peers := info["peers"].(float64)
		assert.Equal(t, float64(0), peers)
	})

	t.Run("Standalone mode has validation_quorum of 1", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		validationQuorum := info["validation_quorum"].(float64)
		assert.Equal(t, float64(1), validationQuorum)
	})
}

// API Version Tests

// TestServerInfoApiVersions tests server_info across different API versions
func TestServerInfoApiVersions(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}

	apiVersions := []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

	for _, apiVersion := range apiVersions {
		t.Run("API version "+string(rune('0'+apiVersion)), func(t *testing.T) {
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: apiVersion,
				Services:   services,
			}

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr, "server_info should work with API version %d", apiVersion)
			require.NotNil(t, result)

			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)

			// Basic structure should be present in all versions
			assert.Contains(t, resp, "info")
			info := resp["info"].(map[string]any)
			assert.Contains(t, info, "build_version")
			assert.Contains(t, info, "server_state")
		})
	}
}

// TestServerInfoMethodSupportedApiVersions tests the method's API version support
func TestServerInfoMethodSupportedApiVersions(t *testing.T) {
	method := &handlers.ServerInfoMethod{}

	versions := method.SupportedApiVersions()

	assert.Contains(t, versions, types.ApiVersion1, "Should support API version 1")
	assert.Contains(t, versions, types.ApiVersion2, "Should support API version 2")
	assert.Contains(t, versions, types.ApiVersion3, "Should support API version 3")
}

// Error Cases

// TestServerInfoServiceUnavailable tests behavior when ledger service is not available
func TestServerInfoServiceUnavailable(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// TestServerInfoServiceNilLedger tests behavior when ledger service is nil
func TestServerInfoServiceNilLedger(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   &types.ServiceContainer{Ledger: nil},
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// Method Metadata Tests

// TestServerInfoMethodMetadata tests the method's metadata functions
func TestServerInfoMethodMetadata(t *testing.T) {
	method := &handlers.ServerInfoMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"server_info should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// Complete Ledgers String Format Tests

// TestServerInfoCompleteLedgersFormat tests various complete_ledgers string formats
func TestServerInfoCompleteLedgersFormat(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name             string
		completeLedgers  string
		expectedContains string
	}{
		{
			name:             "Single range",
			completeLedgers:  "32570-75801862",
			expectedContains: "32570-75801862",
		},
		{
			name:             "Empty ledgers",
			completeLedgers:  "",
			expectedContains: "empty",
		},
		{
			name:             "Multiple ranges",
			completeLedgers:  "1-100,200-300",
			expectedContains: "1-100,200-300",
		},
		{
			name:             "Single ledger",
			completeLedgers:  "1-1",
			expectedContains: "1-1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.serverInfo.CompleteLedgers = tc.completeLedgers

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]any)

			completeLedgers := info["complete_ledgers"].(string)
			assert.Equal(t, tc.expectedContains, completeLedgers)
		})
	}
}

// State Accounting Tests

// TestServerInfoStateAccounting tests the state_accounting field
func TestServerInfoStateAccounting(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("state_accounting contains all states", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "state_accounting")
		stateAccounting := info["state_accounting"].(map[string]any)

		// Check all expected states
		expectedStates := []string{"connected", "disconnected", "full", "syncing", "tracking"}
		for _, state := range expectedStates {
			assert.Contains(t, stateAccounting, state, "state_accounting should contain '%s'", state)

			stateInfo := stateAccounting[state].(map[string]any)
			assert.Contains(t, stateInfo, "duration_us")
			assert.Contains(t, stateInfo, "transitions")
		}
	})
}

// Time Field Tests

// TestServerInfoTimeField tests the time field format
func TestServerInfoTimeField(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("time field present and formatted", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]any)

		assert.Contains(t, info, "time")
		timeStr, ok := info["time"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, timeStr)
		// Time format should include UTC
		assert.Contains(t, timeStr, "UTC")
	})
}

// Fee Calculation Tests

// TestServerInfoFeeCalculations tests fee conversions from drops to XRP
func TestServerInfoFeeCalculations(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name             string
		baseFeeDrops     uint64
		reserveBaseDrops uint64
		reserveIncDrops  uint64
		expectedBaseFee  float64
		expectedReserve  float64
		expectedInc      float64
	}{
		{
			name:             "Standard fees",
			baseFeeDrops:     10,
			reserveBaseDrops: 10000000,
			reserveIncDrops:  2000000,
			expectedBaseFee:  0.00001,
			expectedReserve:  10.0,
			expectedInc:      2.0,
		},
		{
			name:             "Higher base fee",
			baseFeeDrops:     100,
			reserveBaseDrops: 10000000,
			reserveIncDrops:  2000000,
			expectedBaseFee:  0.0001,
			expectedReserve:  10.0,
			expectedInc:      2.0,
		},
		{
			name:             "Alternative reserves",
			baseFeeDrops:     10,
			reserveBaseDrops: 20000000,
			reserveIncDrops:  5000000,
			expectedBaseFee:  0.00001,
			expectedReserve:  20.0,
			expectedInc:      5.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.baseFee = tc.baseFeeDrops
			mock.reserveBase = tc.reserveBaseDrops
			mock.reserveIncrement = tc.reserveIncDrops

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]any)
			validatedLedger := info["validated_ledger"].(map[string]any)

			baseFeeXRP := validatedLedger["base_fee_xrp"].(float64)
			reserveBaseXRP := validatedLedger["reserve_base_xrp"].(float64)
			reserveIncXRP := validatedLedger["reserve_inc_xrp"].(float64)

			assert.InDelta(t, tc.expectedBaseFee, baseFeeXRP, 0.0000001)
			assert.InDelta(t, tc.expectedReserve, reserveBaseXRP, 0.0001)
			assert.InDelta(t, tc.expectedInc, reserveIncXRP, 0.0001)
		})
	}
}

// Server State Method Tests

// TestServerStateMethod tests the server_state RPC method
func TestServerStateMethod(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerStateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	t.Run("server_state returns state wrapper", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		require.NotNil(t, result)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)

		// server_state uses "state" wrapper instead of "info"
		assert.Contains(t, resp, "state")
	})

	t.Run("server_state contains expected fields", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]any
		json.Unmarshal(resultJSON, &resp)
		state := resp["state"].(map[string]any)

		expectedFields := []string{
			"build_version",
			"complete_ledgers",
			"io_latency_ms",
			"load_factor",
			"peers",
			"pubkey_node",
			"server_state",
			"time",
			"uptime",
			"validated_ledger",
			"validation_quorum",
		}

		for _, field := range expectedFields {
			assert.Contains(t, state, field, "server_state should contain '%s'", field)
		}
	})
}

// TestServerStateMethodMetadata tests the server_state method's metadata functions
func TestServerStateMethodMetadata(t *testing.T) {
	method := &handlers.ServerStateMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"server_state should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestServerStateServiceUnavailable tests behavior when ledger service is not available
func TestServerStateServiceUnavailable(t *testing.T) {
	method := &handlers.ServerStateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   nil,
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Contains(t, rpcErr.Message, "Ledger service not available")
}

// Integration-like Tests

// TestServerInfoWithDifferentLedgerStates tests server_info with various ledger states
func TestServerInfoWithDifferentLedgerStates(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	tests := []struct {
		name                 string
		currentLedgerIndex   uint32
		closedLedgerIndex    uint32
		validatedLedgerIndex uint32
		completeLedgers      string
	}{
		{
			name:                 "Fresh genesis state",
			currentLedgerIndex:   3,
			closedLedgerIndex:    2,
			validatedLedgerIndex: 2,
			completeLedgers:      "1-2",
		},
		{
			name:                 "Synced mainnet-like state",
			currentLedgerIndex:   75801863,
			closedLedgerIndex:    75801862,
			validatedLedgerIndex: 75801862,
			completeLedgers:      "32570-75801862",
		},
		{
			name:                 "Partial history",
			currentLedgerIndex:   1000003,
			closedLedgerIndex:    1000002,
			validatedLedgerIndex: 1000002,
			completeLedgers:      "1000000-1000002",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock.currentLedgerIndex = tc.currentLedgerIndex
			mock.closedLedgerIndex = tc.closedLedgerIndex
			mock.validatedLedgerIndex = tc.validatedLedgerIndex
			mock.serverInfo.CompleteLedgers = tc.completeLedgers
			mock.serverInfo.ValidatedLedgerSeq = tc.validatedLedgerIndex

			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)

			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]any)

			// Verify complete_ledgers
			assert.Equal(t, tc.completeLedgers, info["complete_ledgers"])

			// Verify validated_ledger.seq
			validatedLedger := info["validated_ledger"].(map[string]any)
			assert.Equal(t, float64(tc.validatedLedgerIndex), validatedLedger["seq"])
		})
	}
}

// Parameterless Call Tests

// TestServerInfoWithParams tests that server_info ignores any parameters passed
func TestServerInfoWithParams(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	// server_info takes no parameters, but should not error if params are passed
	tests := []struct {
		name   string
		params any
	}{
		{"nil params", nil},
		{"empty object", map[string]any{}},
		{"with random param", map[string]any{"random": "value"}},
		{"with nested object", map[string]any{"nested": map[string]any{"key": "value"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var paramsJSON json.RawMessage
			if tc.params != nil {
				paramsJSON, _ = json.Marshal(tc.params)
			}

			result, rpcErr := method.Handle(ctx, paramsJSON)

			// Should succeed regardless of params
			require.Nil(t, rpcErr, "server_info should succeed with params: %v", tc.params)
			require.NotNil(t, result)

			// Verify response structure
			resultJSON, _ := json.Marshal(result)
			var resp map[string]any
			json.Unmarshal(resultJSON, &resp)
			assert.Contains(t, resp, "info")
		})
	}
}

// TestServerInfo_DynamicMetrics_FromHooks pins that server_info surfaces
// live values from the TxQ, peer, and state-accounting hooks.
func TestServerInfo_DynamicMetrics_FromHooks(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	// Use a recent ripple-epoch close time so the age computation is
	// non-zero but well under the high-age threshold.
	nowUnix := time.Now().Unix()
	closeRippleEpoch := nowUnix - protocol.RippleEpochUnix - 5
	mock.serverInfo.ValidatedLedgerSeq = 100
	mock.serverInfo.ClosedLedgerSeq = 101
	mock.serverInfo.ValidatedLedgerCloseTime = closeRippleEpoch
	mock.serverInfo.ClosedLedgerCloseTime = closeRippleEpoch + 1

	services := servicesForServerInfo(mock)
	services.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 512,
			OpenLedgerFeeLevel:    1024,
		}
	}
	services.JqTransOverflow = func() uint64 { return 13 }
	services.PeerDisconnects = func() (uint64, uint64) { return 42, 9 }
	services.StateAccounting = func() types.StateAccountingSnapshot {
		return types.StateAccountingSnapshot{
			Modes: map[string]types.StateAccountingEntry{
				"disconnected": {Transitions: 1, DurationUs: 1500},
				"connected":    {Transitions: 2, DurationUs: 2500},
				"syncing":      {Transitions: 1, DurationUs: 750},
				"tracking":     {Transitions: 1, DurationUs: 500},
				"full":         {Transitions: 1, DurationUs: 9000},
			},
			CurrentDurationUs: 4321,
			InitialSyncUs:     1234,
		}
	}

	method := &handlers.ServerInfoMethod{}
	// IsAdmin=true so load_factor_fee_escalation is emitted even when
	// loadFactorFeeEscalation == loadFactor; mirrors rippled's
	// NetworkOPs.cpp:2902-2907 (admin || loadFactorFeeEscalation != loadFactor).
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		IsAdmin:    true,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	require.NotNil(t, result)

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	info := resp["info"].(map[string]any)

	assert.Equal(t, "13", info["jq_trans_overflow"])
	_, hasTxqFull := info["txq_full"]
	assert.False(t, hasTxqFull, "txq_full must NOT be emitted — rippled NetworkOPs.cpp:2986-2991 surfaces no such field")
	assert.Equal(t, "42", info["peer_disconnects"])
	assert.Equal(t, "9", info["peer_disconnects_resources"])

	// Top-level companions of state_accounting reflect the tracker's
	// current-state and initial-sync values, not process uptime.
	assert.Equal(t, "4321", info["server_state_duration_us"])
	assert.Equal(t, "1234", info["initial_sync_duration_us"])

	// human-mode load_factor is the float ratio openLedgerFeeLevel/loadBase.
	assert.InDelta(t, 4.0, info["load_factor"].(float64), 0.0001)
	// load_factor_fee_escalation / _queue are emitted in human mode
	// only when they diverge from the reference level, with an extra
	// admin gate on _escalation matching rippled's predicate.
	assert.InDelta(t, 4.0, info["load_factor_fee_escalation"].(float64), 0.0001)
	assert.InDelta(t, 2.0, info["load_factor_fee_queue"].(float64), 0.0001)

	sa := info["state_accounting"].(map[string]any)
	full := sa["full"].(map[string]any)
	assert.Equal(t, "9000", full["duration_us"])
	assert.Equal(t, "1", full["transitions"])
	disconnected := sa["disconnected"].(map[string]any)
	assert.Equal(t, "1500", disconnected["duration_us"])
}

// TestServerInfo_MachineMode_LoadFactorFees verifies the server_state
// (machine) variant surfaces the load_factor_fee_* triple from TxQ
// metrics.
func TestServerInfo_MachineMode_LoadFactorFees(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)
	services.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 768,
			OpenLedgerFeeLevel:    2048,
		}
	}

	method := &handlers.ServerStateMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)

	raw, _ := json.Marshal(result)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	state := resp["state"].(map[string]any)

	// Machine mode emits these as JSON numbers — unmarshal as float64.
	assert.EqualValues(t, 2048, state["load_factor_fee_escalation"])
	assert.EqualValues(t, 768, state["load_factor_fee_queue"])
	assert.EqualValues(t, 256, state["load_factor_fee_reference"])
	assert.EqualValues(t, 2048, state["load_factor"])
	assert.EqualValues(t, 256, state["load_base"])
}

// TestServerInfo_ValidatedLedgerAge_HighAgeThreshold guards against
// regressing the threshold below rippled's 1,000,000-second limit
// (NetworkOPs.cpp:2951). A 1-hour-old ledger must report an actual
// age, not the threshold-clamped 0.
func TestServerInfo_ValidatedLedgerAge_HighAgeThreshold(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	nowUnix := time.Now().Unix()
	mock.serverInfo.ValidatedLedgerSeq = 5
	mock.serverInfo.ValidatedLedgerCloseTime = nowUnix - protocol.RippleEpochUnix - 3600

	services := servicesForServerInfo(mock)
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	raw, _ := json.Marshal(result)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	info := resp["info"].(map[string]any)
	validated := info["validated_ledger"].(map[string]any)

	age, ok := validated["age"].(float64)
	require.True(t, ok)
	assert.InDelta(t, 3600, age, 5, "1-hour-old ledger must surface its real age; rippled clamps only above 1,000,000s")
}

// TestServerInfo_HumanMode_LoadFactorFeeEscalation_NonAdminGate pins
// rippled NetworkOPs.cpp:2902-2907: in human mode, non-admin callers
// only see load_factor_fee_escalation when it actually changes the
// overall load_factor (i.e. loadFactorFeeEscalation != loadFactor).
// With feeEscalation > loadBase and no separate LoadFeeTrack,
// loadFactorFeeEscalation == loadFactor, so the field is hidden.
func TestServerInfo_HumanMode_LoadFactorFeeEscalation_NonAdminGate(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)
	services.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 768, // diverges -> _queue still emitted
			OpenLedgerFeeLevel:    1024,
		}
	}

	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	raw, _ := json.Marshal(result)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	info := resp["info"].(map[string]any)

	_, hasEscalation := info["load_factor_fee_escalation"]
	assert.False(t, hasEscalation,
		"non-admin: field must be omitted when loadFactorFeeEscalation == loadFactor (rippled gate)")
	// _queue has no admin gate in rippled — only the != reference check.
	assert.InDelta(t, 3.0, info["load_factor_fee_queue"].(float64), 0.0001)
}

// TestServerInfo_ClosedLedgerAge_OmittedOnFutureCloseTime mirrors
// rippled NetworkOPs.cpp:2962-2969: when the closed ledger's close
// time is in the future (clock skew), `age` is omitted from the
// closed_ledger object rather than emitted as 0.
func TestServerInfo_ClosedLedgerAge_OmittedOnFutureCloseTime(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	// Force the closed-ledger branch by zeroing the validated index
	// (drives HaveValidated=false in the mock); rippled emits exactly
	// one of validated_ledger / closed_ledger.
	mock.validatedLedgerIndex = 0
	mock.closedLedgerIndex = 7
	// 1 hour in the future
	mock.serverInfo.ClosedLedgerCloseTime = time.Now().Unix() - protocol.RippleEpochUnix + 3600

	services := servicesForServerInfo(mock)
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	raw, _ := json.Marshal(result)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	info := resp["info"].(map[string]any)
	closed := info["closed_ledger"].(map[string]any)
	_, hasAge := closed["age"]
	assert.False(t, hasAge, "closed_ledger.age must be omitted when close_time is in the future")
}

// TestServerInfo_SingleLedgerEmit pins rippled NetworkOPs.cpp:2915-2975:
// exactly one of validated_ledger / closed_ledger is emitted, sourced
// from the validated ledger when haveValidated() and otherwise from the
// closed ledger. Suppressed entirely when neither is available.
func TestServerInfo_SingleLedgerEmit(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	newCtx := func(svc *types.ServiceContainer) *types.RpcContext {
		return &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   svc,
		}
	}
	dispatch := func(ctx *types.RpcContext) map[string]any {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		return resp["info"].(map[string]any)
	}

	t.Run("validated present → only validated_ledger", func(t *testing.T) {
		mock := newMockLedgerServiceServerInfo()
		mock.validatedLedgerIndex = 42
		mock.closedLedgerIndex = 43
		info := dispatch(newCtx(servicesForServerInfo(mock)))
		assert.Contains(t, info, "validated_ledger")
		assert.NotContains(t, info, "closed_ledger")
	})

	t.Run("validated absent → only closed_ledger", func(t *testing.T) {
		mock := newMockLedgerServiceServerInfo()
		mock.validatedLedgerIndex = 0
		mock.closedLedgerIndex = 7
		info := dispatch(newCtx(servicesForServerInfo(mock)))
		assert.NotContains(t, info, "validated_ledger")
		assert.Contains(t, info, "closed_ledger")
	})

	t.Run("neither present → neither emitted", func(t *testing.T) {
		mock := newMockLedgerServiceServerInfo()
		mock.validatedLedgerIndex = 0
		mock.closedLedgerIndex = 0
		info := dispatch(newCtx(servicesForServerInfo(mock)))
		assert.NotContains(t, info, "validated_ledger")
		assert.NotContains(t, info, "closed_ledger")
	})
}

// TestServerInfo_HumanMode_LoadFactorServer pins rippled
// NetworkOPs.cpp:2883-2885: in human mode, load_factor_server is
// emitted only when it differs from the overall load_factor. With no
// LoadFeeTrack the server-side factor is loadBase, so the field fires
// whenever fee escalation drives load_factor above 1.0.
func TestServerInfo_HumanMode_LoadFactorServer(t *testing.T) {
	method := &handlers.ServerInfoMethod{}

	t.Run("escalation > loadBase → field present", func(t *testing.T) {
		mock := newMockLedgerServiceServerInfo()
		services := servicesForServerInfo(mock)
		services.TxQMetrics = func() types.TxQServerMetrics {
			return types.TxQServerMetrics{
				ReferenceFeeLevel:  256,
				OpenLedgerFeeLevel: 1024,
			}
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		info := resp["info"].(map[string]any)
		v, ok := info["load_factor_server"]
		require.True(t, ok, "load_factor_server must be emitted when escalation > loadBase")
		assert.InDelta(t, 1.0, v.(float64), 0.0001)
	})

	t.Run("escalation == loadBase → field absent", func(t *testing.T) {
		mock := newMockLedgerServiceServerInfo()
		services := servicesForServerInfo(mock)
		// No TxQMetrics → escalation falls back to loadBase.
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		info := resp["info"].(map[string]any)
		_, present := info["load_factor_server"]
		assert.False(t, present, "load_factor_server must be omitted when loadFactorServer == loadFactor")
	})
}

// TestServerInfo_HumanMode_LoadFactorLocalNetCluster_AdminGate pins
// rippled NetworkOPs.cpp:2887-2901: admin-only emission, each field
// gated on its fee != loadBase. Non-admin callers must never see them
// regardless of fee divergence.
func TestServerInfo_HumanMode_LoadFactorLocalNetCluster_AdminGate(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	feesHook := func() types.LoadFactorFees {
		return types.LoadFactorFees{Local: 512, Net: 256, Cluster: 768}
	}
	build := func(admin bool, withHook bool) map[string]any {
		mock := newMockLedgerServiceServerInfo()
		services := servicesForServerInfo(mock)
		if withHook {
			services.LoadFactorFees = feesHook
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
			IsAdmin:    admin,
		}
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		return resp["info"].(map[string]any)
	}

	t.Run("admin + hook → diverging fields emitted, matching ones suppressed", func(t *testing.T) {
		info := build(true, true)
		v, ok := info["load_factor_local"].(float64)
		require.True(t, ok)
		assert.InDelta(t, 2.0, v, 0.0001)
		_, hasNet := info["load_factor_net"] // Net == loadBase, must be absent.
		assert.False(t, hasNet)
		v, ok = info["load_factor_cluster"].(float64)
		require.True(t, ok)
		assert.InDelta(t, 3.0, v, 0.0001)
	})

	t.Run("non-admin + hook → all three suppressed", func(t *testing.T) {
		info := build(false, true)
		for _, k := range []string{"load_factor_local", "load_factor_net", "load_factor_cluster"} {
			_, present := info[k]
			assert.Falsef(t, present, "%s must be admin-only", k)
		}
	})

	t.Run("admin without hook → all three suppressed", func(t *testing.T) {
		info := build(true, false)
		for _, k := range []string{"load_factor_local", "load_factor_net", "load_factor_cluster"} {
			_, present := info[k]
			assert.Falsef(t, present, "%s must be absent when hook is nil", k)
		}
	})
}

// TestServerInfo_CloseTimeOffset_Threshold pins rippled
// NetworkOPs.cpp:2946-2949: close_time_offset is surfaced on the
// ledger object only when |offset| reaches a full minute, and is cast
// through static_cast<uint32_t> — preserving the two's-complement bit
// pattern, so negative offsets surface as large positives on the wire.
func TestServerInfo_CloseTimeOffset_Threshold(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	// Helper so the two's-complement reinterpretation can sit in the
	// table literal without tripping Go's compile-time overflow check on
	// `uint32(int32(<negative-const>))`.
	asU32 := func(v int32) uint32 { return uint32(v) }
	cases := []struct {
		name      string
		offset    time.Duration
		wantEmit  bool
		wantValue uint32
	}{
		{"below threshold", 59 * time.Second, false, 0},
		{"at threshold positive", 60 * time.Second, true, 60},
		{"at threshold negative", -60 * time.Second, true, asU32(-60)},
		{"large negative", -125 * time.Second, true, asU32(-125)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockLedgerServiceServerInfo()
			services := servicesForServerInfo(mock)
			offset := tc.offset
			services.CloseTimeOffset = func() time.Duration { return offset }
			ctx := &types.RpcContext{
				Context:    context.Background(),
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion1,
				Services:   services,
			}
			result, rpcErr := method.Handle(ctx, nil)
			require.Nil(t, rpcErr)
			raw, _ := json.Marshal(result)
			var resp map[string]any
			require.NoError(t, json.Unmarshal(raw, &resp))
			info := resp["info"].(map[string]any)
			validated, ok := info["validated_ledger"].(map[string]any)
			require.True(t, ok, "validated_ledger must be present for the offset assertion")
			v, present := validated["close_time_offset"]
			if !tc.wantEmit {
				assert.False(t, present, "close_time_offset must be omitted below threshold")
				return
			}
			require.True(t, present, "close_time_offset must be emitted at/above threshold")
			assert.EqualValues(t, tc.wantValue, v)
		})
	}
}

// fakeManifestLookupServerInfo maps a single signing key to a master key
// to exercise the token-mode resolution path in resolveValidatorPubKey.
type fakeManifestLookupServerInfo struct {
	masterFor map[[33]byte][33]byte
}

func (f *fakeManifestLookupServerInfo) GetMasterKey(signing [33]byte) [33]byte {
	if m, ok := f.masterFor[signing]; ok {
		return m
	}
	return signing
}
func (f *fakeManifestLookupServerInfo) GetSigningKey([33]byte) ([33]byte, bool) {
	return [33]byte{}, false
}
func (f *fakeManifestLookupServerInfo) GetManifest([33]byte) ([]byte, bool) { return nil, false }
func (f *fakeManifestLookupServerInfo) GetSequence([33]byte) (uint32, bool) { return 0, false }
func (f *fakeManifestLookupServerInfo) GetDomain([33]byte) (string, bool)   { return "", false }

func makeSigningKey(prefix byte) []byte {
	pk := make([]byte, 33)
	pk[0] = prefix
	for i := 1; i < 33; i++ {
		pk[i] = byte(i)
	}
	return pk
}

// TestServerInfoPubkeyValidator pins rippled NetworkOPs.cpp:2779-2791:
// pubkey_validator is admin-only, carries the validator's MASTER public
// key (base58 NodePublic), and is "none" when the node is not a
// validator. Regression guard for issue #724, where the field was absent
// entirely and the underlying ValidatorPublicKey was a zero-padded
// 20-byte NodeID rather than the 33-byte signing key.
func TestServerInfoPubkeyValidator(t *testing.T) {
	infoMethod := &handlers.ServerInfoMethod{}

	buildInfo := func(t *testing.T, admin bool, pk []byte, manifests types.ManifestLookup) (map[string]any, bool) {
		t.Helper()
		mock := newMockLedgerServiceServerInfo()
		services := servicesForServerInfo(mock)
		services.ValidatorPublicKey = pk
		services.Manifests = manifests
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
			IsAdmin:    admin,
		}
		result, rpcErr := infoMethod.Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		info := resp["info"].(map[string]any)
		_, present := info["pubkey_validator"]
		return info, present
	}

	t.Run("admin + validator (seed mode) → master==signing base58", func(t *testing.T) {
		signing := makeSigningKey(0x02)
		want, err := addresscodec.EncodeNodePublicKey(signing)
		require.NoError(t, err)
		info, present := buildInfo(t, true, signing, nil)
		require.True(t, present, "pubkey_validator must be present for admin")
		assert.Equal(t, want, info["pubkey_validator"])
	})

	t.Run("admin + validator (token mode) → resolves to master key", func(t *testing.T) {
		signing := makeSigningKey(0x02)
		var signingArr, masterArr [33]byte
		copy(signingArr[:], signing)
		copy(masterArr[:], makeSigningKey(0x03))
		manifests := &fakeManifestLookupServerInfo{
			masterFor: map[[33]byte][33]byte{signingArr: masterArr},
		}
		wantMaster, err := addresscodec.EncodeNodePublicKey(masterArr[:])
		require.NoError(t, err)
		wantSigning, err := addresscodec.EncodeNodePublicKey(signingArr[:])
		require.NoError(t, err)
		info, present := buildInfo(t, true, signing, manifests)
		require.True(t, present)
		assert.Equal(t, wantMaster, info["pubkey_validator"], "must emit master, not signing")
		assert.NotEqual(t, wantSigning, info["pubkey_validator"])
	})

	t.Run("admin + not a validator → none", func(t *testing.T) {
		info, present := buildInfo(t, true, nil, nil)
		require.True(t, present)
		assert.Equal(t, "none", info["pubkey_validator"])
	})

	t.Run("non-admin → field absent", func(t *testing.T) {
		_, present := buildInfo(t, false, makeSigningKey(0x02), nil)
		assert.False(t, present, "pubkey_validator is admin-only")
	})

	t.Run("server_state parity: admin + validator", func(t *testing.T) {
		signing := makeSigningKey(0x02)
		want, err := addresscodec.EncodeNodePublicKey(signing)
		require.NoError(t, err)
		mock := newMockLedgerServiceServerInfo()
		services := servicesForServerInfo(mock)
		services.ValidatorPublicKey = signing
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleGuest,
			ApiVersion: types.ApiVersion1,
			Services:   services,
			IsAdmin:    true,
		}
		result, rpcErr := (&handlers.ServerStateMethod{}).Handle(ctx, nil)
		require.Nil(t, rpcErr)
		raw, _ := json.Marshal(result)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(raw, &resp))
		state := resp["state"].(map[string]any)
		assert.Equal(t, want, state["pubkey_validator"])
	})
}

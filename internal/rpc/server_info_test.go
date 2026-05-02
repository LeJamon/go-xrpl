package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
		buildVersion:      "2.0.0-goXRPLd",
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
		Standalone:          m.standalone,
		OpenLedgerSeq:       m.currentLedgerIndex,
		ClosedLedgerSeq:     m.closedLedgerIndex,
		ValidatedLedgerSeq:  m.validatedLedgerIndex,
		CompleteLedgers:     m.serverInfo.CompleteLedgers,
		ValidatedLedgerHash: m.serverInfo.ValidatedLedgerHash,
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
		var resp map[string]interface{}
		err = json.Unmarshal(resultJSON, &resp)
		require.NoError(t, err)

		// Check info wrapper
		assert.Contains(t, resp, "info")
		info := resp["info"].(map[string]interface{})

		// Check build_version
		assert.Contains(t, info, "build_version")
		assert.NotEmpty(t, info["build_version"])
	})

	t.Run("info.complete_ledgers field present", func(t *testing.T) {
		mock.serverInfo.CompleteLedgers = "32570-75801862"

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "hostid")
		hostid, ok := info["hostid"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, hostid)
	})

	t.Run("info.io_latency_ms field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "last_close")
		lastClose := info["last_close"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "load_factor")
		loadFactor, ok := info["load_factor"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, loadFactor, float64(1))
	})

	t.Run("info.peers field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "peers")
		peers, ok := info["peers"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, peers, float64(0))
	})

	t.Run("info.pubkey_node field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "server_state")
		serverState, ok := info["server_state"].(string)
		assert.True(t, ok)
		assert.NotEmpty(t, serverState)
	})

	t.Run("info.uptime field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "uptime")
		uptime, ok := info["uptime"].(float64)
		assert.True(t, ok)
		assert.GreaterOrEqual(t, uptime, float64(0))
	})

	t.Run("info.validation_quorum field present", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "validated_ledger")
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})
		validatedLedger := info["validated_ledger"].(map[string]interface{})

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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		serverState := info["server_state"].(string)
		assert.Equal(t, "standalone", serverState)
	})

	t.Run("Standalone mode has zero peers", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		peers := info["peers"].(float64)
		assert.Equal(t, float64(0), peers)
	})

	t.Run("Standalone mode has validation_quorum of 1", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)

			// Basic structure should be present in all versions
			assert.Contains(t, resp, "info")
			info := resp["info"].(map[string]interface{})
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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Contains(t, info, "state_accounting")
		stateAccounting := info["state_accounting"].(map[string]interface{})

		// Check all expected states
		expectedStates := []string{"connected", "disconnected", "full", "syncing", "tracking"}
		for _, state := range expectedStates {
			assert.Contains(t, stateAccounting, state, "state_accounting should contain '%s'", state)

			stateInfo := stateAccounting[state].(map[string]interface{})
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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]interface{})
			validatedLedger := info["validated_ledger"].(map[string]interface{})

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
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)

		// server_state uses "state" wrapper instead of "info"
		assert.Contains(t, resp, "state")
	})

	t.Run("server_state contains expected fields", func(t *testing.T) {
		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		state := resp["state"].(map[string]interface{})

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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)
			info := resp["info"].(map[string]interface{})

			// Verify complete_ledgers
			assert.Equal(t, tc.completeLedgers, info["complete_ledgers"])

			// Verify validated_ledger.seq
			validatedLedger := info["validated_ledger"].(map[string]interface{})
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
		params interface{}
	}{
		{"nil params", nil},
		{"empty object", map[string]interface{}{}},
		{"with random param", map[string]interface{}{"random": "value"}},
		{"with nested object", map[string]interface{}{"nested": map[string]interface{}{"key": "value"}}},
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
			var resp map[string]interface{}
			json.Unmarshal(resultJSON, &resp)
			assert.Contains(t, resp, "info")
		})
	}
}

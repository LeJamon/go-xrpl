package rpc

import (
	"context"
	"encoding/json"
	"math"
	"os"
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
		ServerState:              m.serverState,
		NeedsNetworkLedger:       m.serverInfo.NeedsNetworkLedger,
		OpenLedgerSeq:            m.currentLedgerIndex,
		ClosedLedgerSeq:          m.closedLedgerIndex,
		ClosedLedgerCloseTime:    m.serverInfo.ClosedLedgerCloseTime,
		HaveValidated:            m.validatedLedgerIndex > 0,
		ValidatedLedgerSeq:       m.validatedLedgerIndex,
		ValidatedLedgerHash:      m.serverInfo.ValidatedLedgerHash,
		ValidatedLedgerCloseTime: m.serverInfo.ValidatedLedgerCloseTime,
		CompleteLedgers:          m.serverInfo.CompleteLedgers,
		HavePublished:            m.serverInfo.HavePublished,
		PublishedLedgerSeq:       m.serverInfo.PublishedLedgerSeq,
	}
}

func TestServerInfoRoleAliasesAreAdminOnly(t *testing.T) {
	for _, state := range []string{"proposing", "validating"} {
		t.Run(state, func(t *testing.T) {
			mock := newMockLedgerServiceServerInfo()
			mock.standalone = false
			mock.serverState = state
			services := servicesForServerInfo(mock)

			for _, tc := range []struct {
				name  string
				admin bool
				want  string
			}{
				{name: "guest", want: "full"},
				{name: "admin", admin: true, want: state},
			} {
				t.Run(tc.name, func(t *testing.T) {
					role := types.RoleGuest
					if tc.admin {
						role = types.RoleAdmin
					}
					ctx := &types.RpcContext{Context: t.Context(), Role: role, Services: services}
					infoResult, rpcErr := (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
					require.Nil(t, rpcErr)
					assert.Equal(t, tc.want, infoResult.(map[string]any)["info"].(map[string]any)["server_state"])

					stateResult, rpcErr := (&handlers.ServerStateMethod{}).Handle(ctx, nil)
					require.Nil(t, rpcErr)
					assert.Equal(t, tc.want, stateResult.(map[string]any)["state"].(map[string]any)["server_state"])
				})
			}
		})
	}
}

func TestServerInfoHostIDPrivacy(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	services := types.NewTestServiceGraph(container)

	guest := callServerStatus(t, &types.RpcContext{
		Context:  t.Context(),
		Services: services,
	}, true)
	assert.Equal(t, "LANG", guest["hostid"])

	admin := callServerStatus(t, &types.RpcContext{
		Context:  t.Context(),
		Role:     types.RoleAdmin,
		Services: services,
	}, true)
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "go-xrpl"
	}
	assert.Equal(t, hostname, admin["hostid"])

	state := callServerStatus(t, &types.RpcContext{
		Context:  t.Context(),
		Role:     types.RoleAdmin,
		Services: services,
	}, false)
	assert.NotContains(t, state, "hostid")

	container.NodePublicKey = "invalid"
	services = types.NewTestServiceGraph(container)
	guest = callServerStatus(t, &types.RpcContext{Context: t.Context(), Services: services}, true)
	assert.Equal(t, "go-xrpl", guest["hostid"])
}

func TestServerInfoNetworkLedgerWaiting(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	services := servicesForServerInfo(mock)
	ctx := &types.RpcContext{Context: t.Context(), Services: services}

	mock.serverInfo.NeedsNetworkLedger = true
	infoResult, rpcErr := (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	assert.Equal(t, "waiting", infoResult.(map[string]any)["info"].(map[string]any)["network_ledger"])
	stateResult, rpcErr := (&handlers.ServerStateMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	assert.Equal(t, "waiting", stateResult.(map[string]any)["state"].(map[string]any)["network_ledger"])

	mock.serverInfo.NeedsNetworkLedger = false
	infoResult, rpcErr = (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	assert.NotContains(t, infoResult.(map[string]any)["info"].(map[string]any), "network_ledger")
}

// servicesForServerInfo builds a per-test ServiceContainer with a server_info mock.
func servicesForServerInfo(mock *mockLedgerServiceServerInfo) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{
		Ledger:        mock,
		NodePublicKey: testNodePublicKey(),
	})
}

func mutableServicesForServerInfo(mock *mockLedgerServiceServerInfo) *types.ServiceContainer {
	return &types.ServiceContainer{
		Ledger:        mock,
		NodePublicKey: testNodePublicKey(),
	}
}

func callServerStatus(t *testing.T, ctx *types.RpcContext, human bool) map[string]any {
	t.Helper()
	if human {
		result, rpcErr := (&handlers.ServerInfoMethod{}).Handle(ctx, nil)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["info"].(map[string]any)
	}
	result, rpcErr := (&handlers.ServerStateMethod{}).Handle(ctx, nil)
	require.Nil(t, rpcErr)
	return result.(map[string]any)["state"].(map[string]any)
}

func TestServerStatusRuntimeFields(t *testing.T) {
	const gitHash = "0123456789abcdef0123456789abcdef01234567"
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.ServerInfoConfig = types.ServerInfoConfigSnapshot{
		Ports: []types.ServerInfoPortSnapshot{
			{Port: 5005, Protocol: "ws2 http,http ignored"},
			{Port: 6006, Protocol: "wss2, https", Admin: true},
			{Port: 50051, Protocol: "grpc", Admin: true},
		},
		ServerDomain: "example.test",
		NodeSize:     "large",
		GitHash:      gitHash,
	}
	container.FetchPackCacheSize = func() uint32 { return 7 }
	services := types.NewTestServiceGraph(container)

	publicPorts := []map[string]any{
		{"port": "5005", "protocol": []string{"http", "ws2"}},
		{"port": "50051", "protocol": []string{"grpc"}},
	}
	adminPorts := []map[string]any{
		{"port": "5005", "protocol": []string{"http", "ws2"}},
		{"port": "6006", "protocol": []string{"https", "wss2"}},
		{"port": "50051", "protocol": []string{"grpc"}},
	}

	for _, human := range []bool{true, false} {
		mode := "server_state"
		if human {
			mode = "server_info"
		}
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				admin     bool
				wantPorts []map[string]any
			}{
				{name: "guest", wantPorts: publicPorts},
				{name: "admin", admin: true, wantPorts: adminPorts},
			} {
				t.Run(tc.name, func(t *testing.T) {
					role := types.RoleGuest
					if tc.admin {
						role = types.RoleAdmin
					}
					ctx := &types.RpcContext{
						Context:  t.Context(),
						Role:     role,
						Services: services,
					}
					info := callServerStatus(t, ctx, human)

					assert.Equal(t, tc.wantPorts, info["ports"])
					assert.Equal(t, "example.test", info["server_domain"])
					assert.IsType(t, uint32(0), info["fetch_pack"])
					assert.Equal(t, uint32(7), info["fetch_pack"])

					if tc.admin {
						assert.Equal(t, "large", info["node_size"])
						assert.Equal(t, map[string]any{"hash": gitHash}, info["git"])
					} else {
						assert.NotContains(t, info, "node_size")
						assert.NotContains(t, info, "git")
					}
				})
			}
		})
	}
}

func TestServerStatusRuntimeFieldOmissions(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.FetchPackCacheSize = func() uint32 { return 0 }
	services := types.NewTestServiceGraph(container)

	for _, human := range []bool{true, false} {
		ctx := &types.RpcContext{
			Context:  t.Context(),
			Role:     types.RoleAdmin,
			Services: services,
		}
		info := callServerStatus(t, ctx, human)
		assert.Equal(t, []map[string]any{}, info["ports"])
		assert.NotContains(t, info, "fetch_pack")
		assert.NotContains(t, info, "server_domain")
		assert.NotContains(t, info, "git")
		assert.Equal(t, "medium", info["node_size"])

		encoded, err := json.Marshal(info)
		require.NoError(t, err)
		assert.Contains(t, string(encoded), `"ports":[]`)
	}
}

func TestServerStatusPublishedLedger(t *testing.T) {
	tests := []struct {
		name          string
		closed        uint32
		validated     uint32
		havePublished bool
		published     uint32
		want          any
		wantPresent   bool
	}{
		{
			name: "no selected ledger omits field",
		},
		{
			name:        "no published ledger",
			closed:      10,
			want:        "none",
			wantPresent: true,
		},
		{
			name:          "published equals closed",
			closed:        10,
			havePublished: true,
			published:     10,
		},
		{
			name:          "publication lags closed",
			closed:        10,
			havePublished: true,
			published:     9,
			want:          uint32(9),
			wantPresent:   true,
		},
		{
			name:          "published equals validated selection",
			closed:        11,
			validated:     10,
			havePublished: true,
			published:     10,
		},
		{
			name:          "publication lags validated selection",
			closed:        11,
			validated:     10,
			havePublished: true,
			published:     9,
			want:          uint32(9),
			wantPresent:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mock := newMockLedgerServiceServerInfo()
			mock.closedLedgerIndex = test.closed
			mock.validatedLedgerIndex = test.validated
			mock.serverInfo.HavePublished = test.havePublished
			mock.serverInfo.PublishedLedgerSeq = test.published
			services := servicesForServerInfo(mock)

			for _, human := range []bool{true, false} {
				info := callServerStatus(t, &types.RpcContext{
					Context:  t.Context(),
					Services: services,
				}, human)
				got, present := info["published_ledger"]
				assert.Equal(t, test.wantPresent, present)
				if test.wantPresent {
					assert.Equal(t, test.want, got)
				}
			}
		})
	}
}

type serverInfoValidatorList struct {
	publisherCount int
	threshold      int
	blocked        bool
	publishers     []types.ValidatorListPublisherInfo
}

func (v *serverInfoValidatorList) PublisherCount() int { return v.publisherCount }
func (v *serverInfoValidatorList) Threshold() int      { return v.threshold }
func (v *serverInfoValidatorList) IsUNLBlocked() bool  { return v.blocked }
func (v *serverInfoValidatorList) Publishers() []types.ValidatorListPublisherInfo {
	return v.publishers
}
func (v *serverInfoValidatorList) Sites() []types.ValidatorListSiteInfo      { return nil }
func (v *serverInfoValidatorList) TrustedMasterKeys() [][33]byte             { return nil }
func (v *serverInfoValidatorList) ListedValidators() []types.ListedValidator { return nil }

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
		assert.Equal(t, handlers.BuildVersion, info["build_version"])
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

func TestServerInfoDisabledQuorumUsesRippledWireValue(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.ValidationQuorum = func() int { return math.MaxInt }
	services := types.NewTestServiceGraph(container)
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context: context.Background(), Role: types.RoleGuest,
		ApiVersion: types.ApiVersion1, Services: services,
	}

	result, rpcErr := method.Handle(ctx, nil)
	require.Nil(t, rpcErr)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(encoded, &resp))
	info := resp["info"].(map[string]any)
	assert.Equal(t, float64(math.MaxUint32), info["validation_quorum"])
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
	assert.Equal(t, "Internal error.", rpcErr.Message)
}

// TestServerInfoServiceNilLedger tests behavior when ledger service is nil
func TestServerInfoServiceNilLedger(t *testing.T) {
	method := &handlers.ServerInfoMethod{}
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   types.NewTestServiceGraph(&types.ServiceContainer{Ledger: nil}),
	}

	result, rpcErr := method.Handle(ctx, nil)

	assert.Nil(t, result)
	require.NotNil(t, rpcErr)
	assert.Equal(t, types.RpcINTERNAL, rpcErr.Code)
	assert.Equal(t, "Internal error.", rpcErr.Message)
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
	container := mutableServicesForServerInfo(mock)
	container.SystemTime = func() time.Time {
		return time.Date(2026, time.August, 3, 14, 5, 6, 789012345, time.FixedZone("test", 2*60*60))
	}
	services := types.NewTestServiceGraph(container)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	assert.Equal(t, "2026-Aug-03 12:05:06.789012 UTC", callServerStatus(t, ctx, true)["time"])
	assert.Equal(t, "2026-Aug-03 12:05:06.789012 UTC", callServerStatus(t, ctx, false)["time"])
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
	assert.Equal(t, "Internal error.", rpcErr.Message)
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

	container := mutableServicesForServerInfo(mock)
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 512,
			OpenLedgerFeeLevel:    1024,
		}
	}
	container.JqTransOverflow = func() uint64 { return 13 }
	container.PeerDisconnects = func() (uint64, uint64) { return 42, 9 }
	container.StateAccounting = func() types.StateAccountingSnapshot {
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
	services := types.NewTestServiceGraph(container)

	method := &handlers.ServerInfoMethod{}
	// An Admin role makes load_factor_fee_escalation emit even when
	// loadFactorFeeEscalation == loadFactor; mirrors rippled's
	// NetworkOPs.cpp:2902-2907 (admin || loadFactorFeeEscalation != loadFactor).
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
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
	container := mutableServicesForServerInfo(mock)
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 768,
			OpenLedgerFeeLevel:    2048,
		}
	}
	services := types.NewTestServiceGraph(container)

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

func TestServerInfo_LoadFactorEscalationAvoidsIntermediateOverflow(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:  256,
			OpenLedgerFeeLevel: 1 << 56,
		}
	}
	services := types.NewTestServiceGraph(container)
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}

	machine := callServerStatus(t, ctx, false)
	assert.EqualValues(t, math.MaxUint32, machine["load_factor"])

	human := callServerStatus(t, ctx, true)
	assert.Equal(t, float64(uint64(1)<<48), human["load_factor"])
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
	container := mutableServicesForServerInfo(mock)
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 768, // diverges -> _queue still emitted
			OpenLedgerFeeLevel:    1024,
		}
	}
	services := types.NewTestServiceGraph(container)

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
	newCtx := func(svc *types.ServiceGraph) *types.RpcContext {
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
		container := mutableServicesForServerInfo(mock)
		container.TxQMetrics = func() types.TxQServerMetrics {
			return types.TxQServerMetrics{
				ReferenceFeeLevel:  256,
				OpenLedgerFeeLevel: 1024,
			}
		}
		services := types.NewTestServiceGraph(container)
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
		container := mutableServicesForServerInfo(mock)
		if withHook {
			container.LoadFactorFees = feesHook
		}
		services := types.NewTestServiceGraph(container)
		role := types.RoleGuest
		if admin {
			role = types.RoleAdmin
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       role,
			ApiVersion: types.ApiVersion1,
			Services:   services,
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
			container := mutableServicesForServerInfo(mock)
			offset := tc.offset
			container.CloseTimeOffset = func() time.Duration { return offset }
			services := types.NewTestServiceGraph(container)
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
		container := mutableServicesForServerInfo(mock)
		container.ValidatorPublicKey = pk
		container.Manifests = manifests
		services := types.NewTestServiceGraph(container)
		role := types.RoleGuest
		if admin {
			role = types.RoleAdmin
		}
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       role,
			ApiVersion: types.ApiVersion1,
			Services:   services,
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
		container := mutableServicesForServerInfo(mock)
		container.ValidatorPublicKey = signing
		services := types.NewTestServiceGraph(container)
		ctx := &types.RpcContext{
			Context:    context.Background(),
			Role:       types.RoleAdmin,
			ApiVersion: types.ApiVersion1,
			Services:   services,
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

func TestServerInfoValidatorListVisibility(t *testing.T) {
	rippleExpiry := uint32(time.Now().Unix()-protocol.RippleEpochUnix) + 3600
	expiryUnix := int64(rippleExpiry) + protocol.RippleEpochUnix
	validatorList := &serverInfoValidatorList{
		publisherCount: 1,
		threshold:      1,
		publishers: []types.ValidatorListPublisherInfo{{
			ExpirationUnix: expiryUnix,
		}},
	}
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.ValidatorList = validatorList
	services := types.NewTestServiceGraph(container)

	infoFor := func(t *testing.T, admin bool) map[string]any {
		t.Helper()
		role := types.RoleGuest
		if admin {
			role = types.RoleAdmin
		}
		result, rpcErr := (&handlers.ServerInfoMethod{}).Handle(&types.RpcContext{
			Context:    context.Background(),
			Role:       role,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}, nil)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["info"].(map[string]any)
	}

	assert.NotContains(t, infoFor(t, false), "validator_list")
	adminInfo := infoFor(t, true)
	summary := adminInfo["validator_list"].(map[string]any)
	assert.Equal(t, 1, summary["count"])
	assert.Equal(t, "active", summary["status"])
	assert.Equal(t, time.Unix(expiryUnix, 0).UTC().Format("2006-Jan-02 15:04:05 UTC"), summary["expiration"])
	assert.NotContains(t, summary, "validator_list_threshold")

	stateFor := func(t *testing.T, admin bool) map[string]any {
		t.Helper()
		role := types.RoleGuest
		if admin {
			role = types.RoleAdmin
		}
		result, rpcErr := (&handlers.ServerStateMethod{}).Handle(&types.RpcContext{
			Context:    context.Background(),
			Role:       role,
			ApiVersion: types.ApiVersion1,
			Services:   services,
		}, nil)
		require.Nil(t, rpcErr)
		return result.(map[string]any)["state"].(map[string]any)
	}

	assert.NotContains(t, stateFor(t, false), "validator_list_expires")
	assert.Equal(t, rippleExpiry, stateFor(t, true)["validator_list_expires"])
}

func TestServerInfoExpiredValidatorListWarningIsPublic(t *testing.T) {
	mock := newMockLedgerServiceServerInfo()
	container := mutableServicesForServerInfo(mock)
	container.ValidatorList = &serverInfoValidatorList{blocked: true}
	services := types.NewTestServiceGraph(container)
	result, rpcErr := (&handlers.ServerInfoMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}, nil)
	require.Nil(t, rpcErr)

	info := result.(map[string]any)["info"].(map[string]any)
	warnings := info["warnings"].([]types.WarningObject)
	require.Len(t, warnings, 1)
	assert.Equal(t, types.WarningExpiredValidatorList, warnings[0].ID)
	assert.Equal(t, "This server has an expired validator list. validators.txt may be incorrectly configured or some [validator_list_sites] may be unreachable.", warnings[0].Message)

	result, rpcErr = (&handlers.ServerStateMethod{}).Handle(&types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
		Services:   services,
	}, nil)
	require.Nil(t, rpcErr)
	state := result.(map[string]any)["state"].(map[string]any)
	warnings = state["warnings"].([]types.WarningObject)
	require.Len(t, warnings, 1)
	assert.Equal(t, types.WarningExpiredValidatorList, warnings[0].ID)
}

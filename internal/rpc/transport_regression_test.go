package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const transportRegressionClientIP = "203.0.113.5"

func postTransportRegressionRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.RemoteAddr = transportRegressionClientIP + ":1234"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func newTransportRegressionServer(t *testing.T) *Server {
	t.Helper()
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	server.registry.Register("stop", &stubHandler{role: types.RoleAdmin})
	server.registry.Register("fail", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		},
	})
	server.loadTracker = loadtrack.New()
	return server
}

func TestTransportConstructorsShareInjectedLoadTracker(t *testing.T) {
	tracker := loadtrack.New()
	httpServer := NewServer(ServerOptions{Timeout: time.Second, Services: nil, LoadTracker: tracker})
	wsServer := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, Services: nil, LoadTracker: tracker})

	require.Same(t, tracker, httpServer.loadTracker)
	require.Same(t, tracker, wsServer.loadTracker)
	tracker.Charge("shared-client", loadtrack.LoadMalformed)
	assert.Greater(t, wsServer.loadTracker.Balance("shared-client"), float64(0))

	defaultHTTP := NewServer(ServerOptions{Timeout: time.Second})
	defaultWS := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second})
	require.NotNil(t, defaultHTTP.loadTracker)
	require.NotNil(t, defaultWS.loadTracker)
	require.NotSame(t, defaultHTTP.loadTracker, defaultWS.loadTracker)
}

func TestRPCConstructorsUseExplicitDependencies(t *testing.T) {
	services := types.NewServiceContainer(nil)
	clientLoad := types.NewClientLoadShedder()
	services.ClientLoad = clientLoad
	manager := subscription.NewManager()
	provider := stubLedgerInfoProvider{ledgerAvailable: true}
	peerSource := &stubPeerSource{}

	httpServer := NewServer(ServerOptions{
		Timeout:     time.Second,
		Services:    services,
		LoadTracker: loadtrack.New(),
		PeerSource:  peerSource,
	})
	wsServer := NewWebSocketServer(WebSocketServerOptions{
		Timeout:             time.Second,
		Services:            services,
		LoadTracker:         httpServer.loadTracker,
		PeerSource:          peerSource,
		PingInterval:        5 * time.Second,
		LedgerInfoProvider:  provider,
		SubscriptionManager: manager,
	})

	if services.ClientLoad != clientLoad || services.Dispatcher != nil || services.URLSubscriptions != nil {
		t.Fatal("RPC constructors mutated the service container")
	}
	clientLoad.Begin()
	if httpServer.services.ClientLoad.InFlight() != 1 || wsServer.services.ClientLoad.InFlight() != 1 {
		t.Fatal("HTTP and WebSocket servers did not observe the shared client-load shedder")
	}
	if wsServer.SubscriptionManager() != manager {
		t.Fatal("WebSocket server did not retain the explicitly supplied subscription manager")
	}
	if wsServer.pingInterval != 5*time.Second || wsServer.ledgerInfoProvider != provider {
		t.Fatal("WebSocket options were not applied")
	}
	if _, ok := httpServer.registry.Get("ping"); !ok {
		t.Fatal("HTTP method registry was not ready at construction return")
	}
	if _, ok := wsServer.methodRegistry.Get("ping"); !ok {
		t.Fatal("WebSocket method registry was not ready at construction return")
	}
}

func seedTransportRegressionLoad(tracker *loadtrack.Tracker, balance int) {
	tracker.Import("transport-regression", loadtrack.Gossip{Items: []loadtrack.GossipItem{{
		Key:     transportRegressionClientIP,
		Balance: balance,
	}}})
}

func transportRegressionLocalBalance(t *testing.T, tracker *loadtrack.Tracker) uint32 {
	t.Helper()
	return uint32(tracker.LocalBalance(transportRegressionClientIP))
}

func batchRegressionResult(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var replies []map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &replies))
	require.Len(t, replies, 1)
	result, ok := replies[0]["result"].(map[string]any)
	require.True(t, ok, "batch response has no result: %v", replies[0])
	return result
}

func batchRegressionError(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var replies []map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &replies))
	require.Len(t, replies, 1)
	return nestedBatchError(t, replies[0])
}

func TestHTTPAdmissionOrderingAtTransportBoundary(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "single"
		body := `{"method":"ping","params":[{"api_version":99}]}`
		if batch {
			name = "batch"
			body = `{"method":"batch","params":[{"method":"ping","api_version":99}]}`
		}
		t.Run("invalid API precedes overload/"+name, func(t *testing.T) {
			server := newTransportRegressionServer(t)
			seedTransportRegressionLoad(server.loadTracker, loadtrack.DropThreshold)

			recorder := postTransportRegressionRequest(t, server, body)

			if batch {
				assert.Equal(t, http.StatusOK, recorder.Code)
				errorObject := batchRegressionError(t, recorder)
				assert.Equal(t, float64(types.WrongVersionJSONRPCCode), errorObject["code"])
				assert.Equal(t, types.InvalidApiVersionToken, errorObject["message"])
			} else {
				assert.Equal(t, http.StatusBadRequest, recorder.Code)
				assert.Equal(t, types.InvalidApiVersionToken+"\r\n", recorder.Body.String())
			}
			assert.Equal(t, uint32(0), transportRegressionLocalBalance(t, server.loadTracker))
		})
	}

	overloadCases := []struct {
		name  string
		body  string
		batch bool
	}{
		{name: "single forbidden", body: `{"method":"stop","params":[{}]}`},
		{name: "single malformed method", body: `{"method":7,"params":[{}]}`},
		{name: "single malformed params", body: `{"method":"ping","params":[]}`},
		{name: "single malformed ripplerpc", body: `{"method":"ping","params":[{"ripplerpc":2}]}`},
		{name: "batch forbidden", body: `{"method":"batch","params":[{"method":"stop"}]}`, batch: true},
		{name: "batch malformed method", body: `{"method":"batch","params":[{"method":7}]}`, batch: true},
		{name: "batch malformed ripplerpc", body: `{"method":"batch","params":[{"method":"ping","ripplerpc":2}]}`, batch: true},
	}
	for _, test := range overloadCases {
		t.Run("overload precedes "+test.name, func(t *testing.T) {
			server := newTransportRegressionServer(t)
			seedTransportRegressionLoad(server.loadTracker, loadtrack.DropThreshold)

			recorder := postTransportRegressionRequest(t, server, test.body)

			if test.batch {
				assert.Equal(t, http.StatusOK, recorder.Code)
				errorObject := batchRegressionError(t, recorder)
				assert.Equal(t, float64(serverOverloadedJSONRPCCode), errorObject["code"])
				assert.Equal(t, "Server is overloaded", errorObject["message"])
			} else {
				assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
				assert.Equal(t, "Server is overloaded\r\n", recorder.Body.String())
			}
			assert.Equal(t, loadtrack.ChargeDrop/uint32(loadtrack.DecayWindow/time.Second), transportRegressionLocalBalance(t, server.loadTracker))
		})
	}
}

func TestHTTPForbiddenPrecedesMalformedTransportFields(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		batch bool
	}{
		{name: "single params", body: `{"method":"stop","params":[]}`},
		{name: "single ripplerpc", body: `{"method":"stop","params":[{"ripplerpc":2}]}`},
		{name: "batch ripplerpc", body: `{"method":"batch","params":[{"method":"stop","ripplerpc":2}]}`, batch: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTransportRegressionServer(t)

			recorder := postTransportRegressionRequest(t, server, test.body)

			if test.batch {
				assert.Equal(t, http.StatusOK, recorder.Code)
				errorObject := batchRegressionError(t, recorder)
				assert.Equal(t, float64(forbiddenJSONRPCCode), errorObject["code"])
				assert.Equal(t, "Forbidden", errorObject["message"])
			} else {
				assert.Equal(t, http.StatusForbidden, recorder.Code)
				assert.Equal(t, "Forbidden\r\n", recorder.Body.String())
			}
			assert.Equal(t, loadtrack.ChargeMalformed/uint32(loadtrack.DecayWindow/time.Second), transportRegressionLocalBalance(t, server.loadTracker))
		})
	}
}

func TestHTTPMalformedAndForbiddenExactLoadCharge(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "single malformed method", body: `{"method":7,"params":[{}]}`},
		{name: "single malformed params", body: `{"method":"ping","params":[]}`},
		{name: "single malformed ripplerpc", body: `{"method":"ping","params":[{"ripplerpc":2}]}`},
		{name: "single forbidden", body: `{"method":"stop","params":[{}]}`},
		{name: "batch malformed method", body: `{"method":"batch","params":[{"method":7}]}`},
		{name: "batch malformed ripplerpc", body: `{"method":"batch","params":[{"method":"ping","ripplerpc":2}]}`},
		{name: "batch forbidden", body: `{"method":"batch","params":[{"method":"stop"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTransportRegressionServer(t)

			postTransportRegressionRequest(t, server, test.body)

			assert.Equal(t, loadtrack.ChargeMalformed/uint32(loadtrack.DecayWindow/time.Second), transportRegressionLocalBalance(t, server.loadTracker))
		})
	}
}

func TestHTTPEarlyDispatchErrorsChargeReferenceAndWarn(t *testing.T) {
	tests := []struct {
		name   string
		method string
		token  string
		setup  func(*Server)
	}{
		{
			name:   "busy",
			method: "ping",
			token:  "tooBusy",
			setup: func(server *Server) {
				server.services.ClientLoad = saturatedShedder()
			},
		},
		{name: "unknown", method: "does_not_exist", token: "unknownCmd", setup: func(*Server) {}},
		{
			name:   "condition",
			method: "gated",
			token:  "noNetwork",
			setup: func(server *Server) {
				server.registry.Register("gated", &condStubHandler{cond: types.NeedsNetworkConnection})
				server.services.Ledger = newMockLedgerService()
			},
		},
	}
	for _, test := range tests {
		for _, batch := range []bool{false, true} {
			name := "single"
			body := `{"method":"` + test.method + `","params":[{}]}`
			if batch {
				name = "batch"
				body = `{"method":"batch","params":[{"method":"` + test.method + `"}]}`
			}
			t.Run(test.name+"/"+name, func(t *testing.T) {
				server := newTransportRegressionServer(t)
				test.setup(server)
				seedTransportRegressionLoad(server.loadTracker, loadtrack.WarningThreshold)

				recorder := postTransportRegressionRequest(t, server, body)

				assert.Equal(t, http.StatusOK, recorder.Code)
				var result map[string]any
				if batch {
					result = batchRegressionResult(t, recorder)
				} else {
					result = decodeEnvelope(t, recorder.Body.Bytes())
				}
				assert.Equal(t, test.token, result["error"])
				assert.Equal(t, "load", result["warning"])
				assert.Equal(t, (loadtrack.ChargeReference+loadtrack.ChargeWarning)/uint32(loadtrack.DecayWindow/time.Second), transportRegressionLocalBalance(t, server.loadTracker))
			})
		}
	}
}

const recursiveCredentialFields = `"SeCrEt":"private-1","nested":{"SEED":"private-2","items":[{"PassPhrase":"private-3","Seed_Hex":"private-4"},{"SeedHex":"private-5","Password":"private-6","public":"visible"}],"deeper":{"Url_Password":"private-7","UrlPassword":"private-8","Admin_Password":"private-9","AdminPassword":"private-10"}}`

func assertRecursiveCredentialRedaction(t *testing.T, body []byte) {
	t.Helper()
	for _, secret := range []string{
		"private-1", "private-2", "private-3", "private-4", "private-5",
		"private-6", "private-7", "private-8", "private-9", "private-10",
	} {
		assert.NotContains(t, string(body), secret)
	}
	assert.Contains(t, string(body), "visible")

	var value any
	require.NoError(t, json.Unmarshal(body, &value))
	credentialNames := map[string]struct{}{
		"SeCrEt":         {},
		"SEED":           {},
		"PassPhrase":     {},
		"Seed_Hex":       {},
		"SeedHex":        {},
		"Password":       {},
		"Url_Password":   {},
		"UrlPassword":    {},
		"Admin_Password": {},
		"AdminPassword":  {},
	}
	masked := 0
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, item := range value {
				if _, credential := credentialNames[key]; credential {
					masked++
					assert.Equal(t, maskedValue, item, "credential %s was not masked", key)
					continue
				}
				walk(item)
			}
		case []any:
			for _, item := range value {
				walk(item)
			}
		}
	}
	walk(value)
	assert.Equal(t, len(credentialNames), masked)
}

func TestBatchAllErrorEchoesRecursivelyRedactCredentials(t *testing.T) {
	tests := []struct {
		name       string
		element    string
		overloaded bool
	}{
		{name: "non-object", element: `[{` + recursiveCredentialFields + `}]`},
		{name: "invalid API", element: `{"method":"ping","api_version":99,` + recursiveCredentialFields + `}`},
		{name: "overload", element: `{"method":"ping",` + recursiveCredentialFields + `}`, overloaded: true},
		{name: "forbidden", element: `{"method":"stop",` + recursiveCredentialFields + `}`},
		{name: "missing method", element: `{` + recursiveCredentialFields + `}`},
		{name: "null method", element: `{"method":null,` + recursiveCredentialFields + `}`},
		{name: "non-string method", element: `{"method":7,` + recursiveCredentialFields + `}`},
		{name: "empty method", element: `{"method":"",` + recursiveCredentialFields + `}`},
		{name: "malformed ripplerpc", element: `{"method":"ping","ripplerpc":2,` + recursiveCredentialFields + `}`},
		{name: "unknown method", element: `{"method":"does_not_exist",` + recursiveCredentialFields + `}`},
		{name: "handler error", element: `{"method":"fail",` + recursiveCredentialFields + `}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTransportRegressionServer(t)
			if test.overloaded {
				seedTransportRegressionLoad(server.loadTracker, loadtrack.DropThreshold)
			}
			body := `{"method":"batch","params":[` + test.element + `]}`

			recorder := postTransportRegressionRequest(t, server, body)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assertRecursiveCredentialRedaction(t, recorder.Body.Bytes())
		})
	}
}

func TestBuildXrplResponseBodyDoesNotMutateHandlerResult(t *testing.T) {
	result := map[string]any{
		"value":  7,
		"nested": map[string]any{"ok": true},
	}
	want := map[string]any{
		"value":  7,
		"nested": map[string]any{"ok": true},
	}

	response := buildXrplResponseBody(nil, result, nil, &JsonRpcResponseOptions{Warning: "load"})

	assert.True(t, reflect.DeepEqual(want, result), "handler result mutated: %v", result)
	assert.NotContains(t, result, "status")
	assert.NotContains(t, result, "warning")
	responseResult, ok := response["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "success", responseResult["status"])
	assert.Equal(t, "load", responseResult["warning"])
}

func TestBuildXrplResponseBodyHandlesTypedNilMap(t *testing.T) {
	var result map[string]any

	response := buildXrplResponseBody(nil, result, nil, nil)

	responseResult, ok := response["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"status": "success"}, responseResult)
	assert.NotContains(t, responseResult, "data")
}

func TestTransportJSONNumberBoundsAndCanonicalization(t *testing.T) {
	valid := []struct {
		raw       string
		wantType  reflect.Type
		canonical string
	}{
		{raw: `-2147483648`, wantType: reflect.TypeOf(int32(0)), canonical: `-2147483648`},
		{raw: `2147483647`, wantType: reflect.TypeOf(int32(0)), canonical: `2147483647`},
		{raw: `2147483648`, wantType: reflect.TypeOf(uint32(0)), canonical: `2147483648`},
		{raw: `4294967295`, wantType: reflect.TypeOf(uint32(0)), canonical: `4294967295`},
		{raw: `-0`, wantType: reflect.TypeOf(int32(0)), canonical: `0`},
		{raw: `1.0`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1`},
		{raw: `1e0`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1`},
		{raw: `1.25e2`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `125`},
		{raw: `999999999999999.0`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `999999999999999`},
		{raw: `1000000000000000.0`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1000000000000000`},
		{raw: `10000000000000000.0`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1e+16`},
		{raw: `0.0001`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `0.0001`},
		{raw: `0.00001`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1e-05`},
		{raw: `1e20`, wantType: reflect.TypeOf(jsonReal(0)), canonical: `1e+20`},
	}
	for _, test := range valid {
		t.Run(test.raw, func(t *testing.T) {
			var value any
			require.NoError(t, decodeJSONUseNumber([]byte(test.raw), &value))
			assert.Equal(t, test.wantType, reflect.TypeOf(value))
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			assert.Equal(t, test.canonical, string(encoded))
		})
	}

	for _, raw := range []string{`4294967296`, `-2147483649`} {
		t.Run("reject "+raw, func(t *testing.T) {
			var value any
			err := decodeJSONUseNumber([]byte(raw), &value)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "exceeds the allowable range")
		})
	}
}

func TestHTTPJSONIntegerBoundsAndCanonicalization(t *testing.T) {
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	for _, rawID := range []string{`4294967296`, `-2147483649`} {
		t.Run("reject "+rawID, func(t *testing.T) {
			body := `{"method":"ping","params":[{"id":` + rawID + `}]}`

			recorder := postTransportRegressionRequest(t, server, body)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Equal(t, unableToParseRequest+"\r\n", recorder.Body.String())
		})
	}

	valid := []struct {
		rawID string
		want  string
	}{
		{rawID: `4294967295`, want: "{\"id\":4294967295,\"result\":{\"ok\":true,\"status\":\"success\"}}\n\r\n"},
		{rawID: `-2147483648`, want: "{\"id\":-2147483648,\"result\":{\"ok\":true,\"status\":\"success\"}}\n\r\n"},
		{rawID: `1e0`, want: "{\"id\":1,\"result\":{\"ok\":true,\"status\":\"success\"}}\n\r\n"},
		{rawID: `1e20`, want: "{\"id\":1e+20,\"result\":{\"ok\":true,\"status\":\"success\"}}\n\r\n"},
	}
	for _, test := range valid {
		t.Run("accept "+test.rawID, func(t *testing.T) {
			body := `{"method":"ping","params":[{"id":` + test.rawID + `}]}`

			recorder := postTransportRegressionRequest(t, server, body)

			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Equal(t, test.want, recorder.Body.String())
		})
	}
}

func TestHTTPParseBoundaryUsesFixedResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{not json`},
		{name: "null", body: `null`},
		{name: "boolean", body: `true`},
		{name: "number", body: `7`},
		{name: "string", body: `"request"`},
		{name: "array", body: `[{"method":"ping"}]`},
		{name: "integer below range", body: `{"method":"ping","params":[{"id":-2147483649}]}`},
		{name: "integer above range", body: `{"method":"ping","params":[{"id":4294967296}]}`},
		{name: "real out of range", body: `{"method":"ping","params":[{"id":1e999}]}`},
		{name: "too deeply nested", body: nestedJSONObject(26)},
		{name: "oversized", body: strings.Repeat(" ", MaxRequestBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTransportRegressionServer(t)
			seedTransportRegressionLoad(server.loadTracker, loadtrack.DropThreshold)

			recorder := postTransportRegressionRequest(t, server, test.body)

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.Equal(t, jsonContentType, recorder.Header().Get("Content-Type"))
			assert.Equal(t, unableToParseRequest+"\r\n", recorder.Body.String())
			assert.Equal(t, uint32(0), transportRegressionLocalBalance(t, server.loadTracker))
		})
	}
}

func nestedJSONObject(containers int) string {
	return strings.Repeat(`{"a":`, containers) + `0` + strings.Repeat(`}`, containers)
}

func TestBatchFallsBackToTopLevelAPIVersion(t *testing.T) {
	server := newBatchServer(t)
	body := `{"method":"batch","params":[
		{"method":"ping","api_version":2,"params":[{}]},
		{"method":"ping","api_version":2,"params":[{"api_version":1}]}
	]}`

	recorder := postTransportRegressionRequest(t, server, body)

	assert.Equal(t, http.StatusOK, recorder.Code)
	var replies []map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &replies))
	require.Len(t, replies, 2)
	for i, reply := range replies {
		result, ok := reply["result"].(map[string]any)
		require.True(t, ok, "reply %d has no result: %v", i, reply)
		assert.Equal(t, float64(types.ApiVersion2), result["api_version"])
	}
}

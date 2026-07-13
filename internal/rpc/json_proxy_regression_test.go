package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONProxyUsesSingleDispatchAccounting(t *testing.T) {
	services := types.NewServiceContainer(nil)
	server := NewServer(time.Second, services)
	services.SetDispatcher(server)

	for range types.MaxJobQueueClients - 1 {
		services.ClientLoad.Begin()
	}
	t.Cleanup(func() {
		for range types.MaxJobQueueClients - 1 {
			services.ClientLoad.End()
		}
	})

	var targetCalls int
	server.registry.Register("json_proxy_target", &stubHandler{
		handle: func(ctx *types.RpcContext, _ json.RawMessage) (any, *types.RpcError) {
			targetCalls++
			assert.Equal(t, types.MaxJobQueueClients, services.ClientLoad.InFlight())
			ctx.LoadCost = loadtrack.ChargeHeavy
			return map[string]any{"proxied": true}, nil
		},
	})

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"method":"json","params":[{"method":"json_proxy_target","params":[{}]}]}`,
	))
	request.RemoteAddr = transportRegressionClientIP + ":1234"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 1, targetCalls)
	assert.Equal(t, int64(types.MaxJobQueueClients-1), services.ClientLoad.InFlight())
	assert.Equal(t,
		float64(loadtrack.ChargeHeavy/uint32(loadtrack.DecayWindow/time.Second)),
		server.loadTracker.LocalBalance(transportRegressionClientIP),
	)

	envelope := decodeEnvelope(t, response.Body.Bytes())
	result, ok := envelope["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, result["proxied"])
	assert.Equal(t, "success", result["status"])
}

func TestHTTPStructuredIDRedactionAcrossDispatchShapes(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		secrets []string
		result  func(*testing.T, []byte) map[string]any
	}{
		{
			name:    "single",
			body:    `{"method":"capture","params":[{"id":{"SeCrEt":"single-private"},"payload":"kept"}]}`,
			secrets: []string{"single-private"},
			result: func(t *testing.T, body []byte) map[string]any {
				envelope := decodeEnvelope(t, body)
				assertMaskedStructuredID(t, envelope["id"])
				result, ok := envelope["result"].(map[string]any)
				require.True(t, ok)
				return result
			},
		},
		{
			name:    "batch",
			body:    `{"method":"batch","params":[{"method":"capture","id":{"SeCrEt":"batch-private"},"payload":"kept"}]}`,
			secrets: []string{"batch-private"},
			result: func(t *testing.T, body []byte) map[string]any {
				var envelopes []map[string]any
				require.NoError(t, json.Unmarshal(body, &envelopes))
				require.Len(t, envelopes, 1)
				assertMaskedStructuredID(t, envelopes[0]["id"])
				result, ok := envelopes[0]["result"].(map[string]any)
				require.True(t, ok)
				return result
			},
		},
		{
			name: "nested json",
			body: `{"method":"json","params":[{"id":{"SeCrEt":"outer-private"},"method":"capture","params":[{"id":{"SeCrEt":"inner-private"},"payload":"kept"}]}]}`,
			secrets: []string{
				"outer-private",
				"inner-private",
			},
			result: func(t *testing.T, body []byte) map[string]any {
				envelope := decodeEnvelope(t, body)
				assertMaskedStructuredID(t, envelope["id"])
				result, ok := envelope["result"].(map[string]any)
				require.True(t, ok)
				return result
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			services := types.NewServiceContainer(nil)
			server := NewServer(time.Second, services)
			services.SetDispatcher(server)
			server.registry.Register("capture", &stubHandler{
				handle: func(_ *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
					var received map[string]any
					require.NoError(t, json.Unmarshal(params, &received))
					assertMaskedStructuredID(t, received["id"])
					return map[string]any{"received": received}, nil
				},
			})

			response := postTransportRegressionRequest(t, server, test.body)
			require.Equal(t, http.StatusOK, response.Code)
			for _, secret := range test.secrets {
				assert.NotContains(t, response.Body.String(), secret)
			}

			result := test.result(t, response.Body.Bytes())
			received, ok := result["received"].(map[string]any)
			require.True(t, ok)
			assertMaskedStructuredID(t, received["id"])
			assert.Equal(t, "kept", received["payload"])
		})
	}
}

func TestURLUserinfoIsRedactedAcrossTransportEchoes(t *testing.T) {
	const maskedURL = `"url":"<masked>"`

	fail := &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		},
	}
	httpServer := NewServer(time.Second, types.NewServiceContainer(nil))
	httpServer.registry.Register("userinfo_fail", fail)

	urls := []struct {
		name  string
		value string
	}{
		{name: "valid", value: "https://alice:private-password@example.com/events"},
		{name: "malformed path", value: "https://alice:private-password@example.com/%zz"},
	}
	for _, testURL := range urls {
		t.Run(testURL.name, func(t *testing.T) {
			requests := []struct {
				name string
				body string
			}{
				{name: "HTTP single", body: `{"method":"userinfo_fail","params":[{"url":"` + testURL.value + `"}]}`},
				{name: "HTTP batch", body: `{"method":"batch","params":[{"method":"userinfo_fail","url":"` + testURL.value + `"}]}`},
			}
			for _, request := range requests {
				t.Run(request.name, func(t *testing.T) {
					response := postTransportRegressionRequest(t, httpServer, request.body)
					require.Equal(t, http.StatusOK, response.Code)
					assert.NotContains(t, response.Body.String(), testURL.value)
					assert.NotContains(t, response.Body.String(), "alice")
					assert.NotContains(t, response.Body.String(), "private-password")
					assert.Contains(t, response.Body.String(), maskedURL)
				})
			}

			t.Run("WebSocket", func(t *testing.T) {
				server := NewWebSocketServer(time.Second, nil)
				server.methodRegistry.Register("userinfo_fail", fail)
				body := wsRawRoundTrip(t, server,
					`{"command":"userinfo_fail","url":"`+testURL.value+`"}`,
				)
				assert.NotContains(t, string(body), testURL.value)
				assert.NotContains(t, string(body), "alice")
				assert.NotContains(t, string(body), "private-password")
				assert.Contains(t, string(body), maskedURL)
			})
		})
	}
}

func assertMaskedStructuredID(t *testing.T, value any) {
	t.Helper()
	id, ok := value.(map[string]any)
	require.True(t, ok, "id is not an object: %v", value)
	assert.Equal(t, map[string]any{"SeCrEt": maskedValue}, id)
}

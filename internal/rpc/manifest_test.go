package rpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rpcManifestFixture struct {
	cache      *manifest.Cache
	master     string
	ephemeral  string
	serialized []byte
}

func newRPCManifestFixture(t *testing.T) rpcManifestFixture {
	t.Helper()

	masterPubBytes, masterPriv := deterministicEd25519KeypairRPC(0x11)
	ephPubBytes, ephPriv := deterministicEd25519KeypairRPC(0x22)

	master, err := addresscodec.EncodeNodePublicKey(masterPubBytes)
	require.NoError(t, err)
	ephemeral, err := addresscodec.EncodeNodePublicKey(ephPubBytes)
	require.NoError(t, err)

	jsonMap := map[string]any{
		"PublicKey":     hex.EncodeToString(masterPubBytes),
		"SigningPubKey": hex.EncodeToString(ephPubBytes),
		"Sequence":      uint32(7),
		"Domain":        hex.EncodeToString([]byte("example.org")),
	}
	preimage := manifestPreimageRPC(t, jsonMap)
	jsonMap["Signature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(ephPriv), preimage))
	jsonMap["MasterSignature"] = hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(masterPriv), preimage))

	encoded, err := binarycodec.Encode(jsonMap)
	require.NoError(t, err)
	serialized, err := hex.DecodeString(encoded)
	require.NoError(t, err)

	m, err := manifest.Deserialize(serialized)
	require.NoError(t, err)
	cache := manifest.NewCache()
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(m))

	return rpcManifestFixture{
		cache:      cache,
		master:     master,
		ephemeral:  ephemeral,
		serialized: serialized,
	}
}

func manifestPreimageRPC(t *testing.T, src map[string]any) []byte {
	t.Helper()
	filtered := make(map[string]any, len(src))
	for key, value := range src {
		if key == "Signature" || key == "MasterSignature" {
			continue
		}
		filtered[key] = value
	}
	encoded, err := binarycodec.Encode(filtered)
	require.NoError(t, err)
	body, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	prefix := protocol.HashPrefixManifest()
	return append(prefix[:], body...)
}

func deterministicEd25519KeypairRPC(seed byte) ([]byte, []byte) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	return append([]byte{0xED}, public...), private
}

func manifestServices(cache *manifest.Cache) *types.ServiceGraph {
	return types.NewTestServiceGraph(&types.ServiceContainer{Manifests: cache})
}

func callManifestHTTP(t *testing.T, services *types.ServiceGraph, params string) (map[string]any, int) {
	t.Helper()
	server := NewServer(ServerOptions{Timeout: time.Second, Services: services})
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		`{"method":"manifest","params":[`+params+`]}`,
	))
	request.RemoteAddr = "203.0.113.5:1234"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), response.Body.String())
	return body, response.Code
}

func callManifestWebSocket(t *testing.T, services *types.ServiceGraph, requestBody string) map[string]any {
	t.Helper()
	server := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, Services: services})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, server.Close(ctx))
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	defer httpServer.Close()

	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http")
	connection, _, err := websocket.DefaultDialer.Dial(websocketURL, nil)
	require.NoError(t, err)
	defer connection.Close()

	require.NoError(t, connection.WriteMessage(websocket.TextMessage, []byte(requestBody)))
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, responseBody, err := connection.ReadMessage()
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(responseBody, &body), string(responseBody))
	require.Equal(t, "response", body["type"])
	return body
}

func manifestResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	require.True(t, ok, "response has no result: %#v", response)
	return result
}

func manifestErrorFields(t *testing.T, transport string, response map[string]any) map[string]any {
	t.Helper()
	if transport == "http" {
		return manifestResult(t, response)
	}
	return response
}

func assertManifestSuccessEnvelope(t *testing.T, transport string, response map[string]any) map[string]any {
	t.Helper()
	if transport == "http" {
		result := manifestResult(t, response)
		assert.Equal(t, "success", result["status"])
		assert.NotContains(t, response, "error")
		return result
	}
	assert.Equal(t, "success", response["status"])
	assert.NotContains(t, response, "error")
	result, ok := response["result"].(map[string]any)
	require.True(t, ok, "WebSocket response has no result: %#v", response)
	return result
}

func TestManifestTransportSuccessShapes(t *testing.T) {
	fixture := newRPCManifestFixture(t)
	const uncached = "n9LXMXFTeVL6o9fxdFHfeVZWf6YzWCBzt7YyeK1HV7wZ4ZFRNgUV"

	cases := []struct {
		name      string
		requested string
		cached    bool
	}{
		{name: "cached master", requested: fixture.master, cached: true},
		{name: "cached ephemeral", requested: fixture.ephemeral, cached: true},
		{name: "valid uncached", requested: uncached},
	}

	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					var response map[string]any
					if transport == "http" {
						var status int
						response, status = callManifestHTTP(t, manifestServices(fixture.cache),
							`{"public_key":"`+test.requested+`"}`)
						require.Equal(t, http.StatusOK, status)
					} else {
						response = callManifestWebSocket(t, manifestServices(fixture.cache),
							`{"command":"manifest","id":1,"public_key":"`+test.requested+`"}`)
					}

					result := assertManifestSuccessEnvelope(t, transport, response)
					assert.Equal(t, test.requested, result["requested"])
					assert.NotContains(t, result, "data")

					if !test.cached {
						assert.Equal(t, map[string]any{
							"master_key":    uncached,
							"ephemeral_key": uncached,
						}, result["details"])
						assert.NotContains(t, result, "manifest")
						details, ok := result["details"].(map[string]any)
						require.True(t, ok, "uncached result has no details: %#v", result)
						assert.NotContains(t, details, "seq")
						assert.NotContains(t, details, "domain")
						return
					}

					manifestValue, ok := result["manifest"].(string)
					require.True(t, ok)
					decoded, err := base64.StdEncoding.DecodeString(manifestValue)
					require.NoError(t, err)
					assert.Equal(t, fixture.serialized, decoded)

					details, ok := result["details"].(map[string]any)
					require.True(t, ok)
					assert.Equal(t, fixture.master, details["master_key"])
					assert.Equal(t, fixture.ephemeral, details["ephemeral_key"])
					assert.Equal(t, float64(7), details["seq"])
					assert.Equal(t, "example.org", details["domain"])
				})
			}
		})
	}
}

func TestManifestTransportParameterErrors(t *testing.T) {
	cases := []struct {
		name          string
		value         string
		wantCode      int
		wantMessage   string
		wantRequested string
		hasRequested  bool
	}{
		{
			name:        "missing",
			wantCode:    rpcerrors.RpcINVALID_PARAMS,
			wantMessage: "Missing field 'public_key'.",
		},
		{
			name:          "empty string",
			value:         `""`,
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "",
			hasRequested:  true,
		},
		{
			name:          "null",
			value:         "null",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "",
			hasRequested:  true,
		},
		{
			name:          "integer",
			value:         "42",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "42",
			hasRequested:  true,
		},
		{
			name:          "real",
			value:         "42.0",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "42.000000",
			hasRequested:  true,
		},
		{
			name:          "scientific real",
			value:         "4.2e1",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "42.000000",
			hasRequested:  true,
		},
		{
			name:          "negative zero",
			value:         "-0",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "0",
			hasRequested:  true,
		},
		{
			name:          "true",
			value:         "true",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "true",
			hasRequested:  true,
		},
		{
			name:          "false",
			value:         "false",
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "false",
			hasRequested:  true,
		},
		{
			name:          "account id",
			value:         `"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"`,
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			hasRequested:  true,
		},
		{
			name:          "malformed key",
			value:         `"abcdef12345"`,
			wantCode:      rpcerrors.RpcINVALID_PARAMS,
			wantMessage:   "Invalid parameters.",
			wantRequested: "abcdef12345",
			hasRequested:  true,
		},
		{
			name:        "array",
			value:       "[]",
			wantCode:    rpcerrors.RpcINTERNAL,
			wantMessage: "Internal error.",
		},
		{
			name:        "object",
			value:       "{}",
			wantCode:    rpcerrors.RpcINTERNAL,
			wantMessage: "Internal error.",
		},
	}

	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			for _, test := range cases {
				t.Run(test.name, func(t *testing.T) {
					var response map[string]any
					if transport == "http" {
						params := `{}`
						if test.value != "" {
							params = `{"public_key":` + test.value + `}`
						}
						var status int
						response, status = callManifestHTTP(t, manifestServices(manifest.NewCache()), params)
						require.Equal(t, http.StatusOK, status)
					} else {
						request := `{"command":"manifest","id":1}`
						if test.value != "" {
							request = `{"command":"manifest","id":1,"public_key":` + test.value + `}`
						}
						response = callManifestWebSocket(t, manifestServices(manifest.NewCache()), request)
					}

					fields := manifestErrorFields(t, transport, response)
					assert.Equal(t, "error", fields["status"])
					assert.Equal(t, test.wantMessage, fields["error_message"])
					assert.Equal(t, float64(test.wantCode), fields["error_code"])
					wantError := "invalidParams"
					if test.wantCode == rpcerrors.RpcINTERNAL {
						wantError = "internal"
					}
					assert.Equal(t, wantError, fields["error"])
					assert.NotContains(t, fields, "data")
					if test.hasRequested {
						assert.Equal(t, test.wantRequested, fields["requested"])
					} else {
						assert.NotContains(t, fields, "requested")
					}
				})
			}
		})
	}
}

func TestManifestTransportRipplerpcV2Projection(t *testing.T) {
	const malformed = "abcdef12345"
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			var response map[string]any
			if transport == "http" {
				params := `{"public_key":"` + malformed + `","ripplerpc":"2.0","api_version":2}`
				var status int
				response, status = callManifestHTTP(t, manifestServices(manifest.NewCache()), params)
				require.Equal(t, http.StatusOK, status)
			} else {
				request := `{"command":"manifest","id":7,"public_key":"` + malformed + `","ripplerpc":"2.0","api_version":2}`
				response = callManifestWebSocket(t, manifestServices(manifest.NewCache()), request)
			}

			assert.Equal(t, "2.0", response["ripplerpc"])
			if transport == "http" {
				errorObject, ok := response["error"].(map[string]any)
				require.True(t, ok, "HTTP v2 response has no error object: %#v", response)
				assert.Equal(t, "error", errorObject["status"])
				assert.Equal(t, "invalidParams", errorObject["error"])
				assert.Equal(t, float64(rpcerrors.RpcINVALID_PARAMS), errorObject["code"])
				assert.Equal(t, "Invalid parameters.", errorObject["message"])
				assert.Equal(t, malformed, errorObject["requested"])
				assert.NotContains(t, errorObject, "error_message")
				assert.NotContains(t, response, "result")
				return
			}

			assert.Equal(t, "error", response["status"])
			assert.Equal(t, float64(7), response["id"])
			assert.Equal(t, float64(2), response["api_version"])
			assert.Equal(t, "invalidParams", response["error"])
			assert.Equal(t, float64(rpcerrors.RpcINVALID_PARAMS), response["error_code"])
			assert.Equal(t, "Invalid parameters.", response["error_message"])
			assert.Equal(t, malformed, response["requested"])
		})
	}
}

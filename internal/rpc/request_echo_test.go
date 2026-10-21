package rpc

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestNULRequestEcho(t *testing.T) {
	for _, test := range []struct {
		name      string
		key       string
		requested string
	}{
		{name: "embedded", key: `"a\u0000b"`, requested: "a"},
		{name: "leading", key: `"\u0000b"`, requested: ""},
		{name: "unicode", key: `"é\t\u0000b"`, requested: "é\t"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, version := range []string{"1.0", "2.0"} {
				t.Run(version, func(t *testing.T) {
					params := `"public_key":` + test.key + `,"id":"echo\u0000tail","ripplerpc":"` + version +
						`","extra":["kept\u0000tail",{"SeCrEt":"private\u0000tail","url":"https://user:password@example.com/\u0000tail"}]`
					for _, transport := range []string{"http", "batch", "websocket"} {
						t.Run(transport, func(t *testing.T) {
							var response map[string]any
							if transport == "websocket" {
								response = callManifestWebSocket(t, nil, `{"command":"manifest",`+params+`}`)
							} else {
								server := NewServer(ServerOptions{Timeout: time.Second})
								body := `{"method":"manifest","params":[{` + params + `}]}`
								if transport == "batch" {
									body = `{"method":"batch","params":[{"method":"manifest",` + params + `}]}`
								}
								recorder := postTransportRegressionRequest(t, server, body)
								require.Equal(t, http.StatusOK, recorder.Code)
								if transport == "batch" {
									var responses []map[string]any
									require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &responses))
									require.Len(t, responses, 1)
									response = responses[0]
								} else {
									require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
								}
							}

							assert.Equal(t, "echo", response["id"])
							fields := response
							if transport != "websocket" {
								field := "result"
								if version == "2.0" {
									field = "error"
								}
								var ok bool
								fields, ok = response[field].(map[string]any)
								require.True(t, ok)
							}
							assert.Equal(t, "invalidParams", fields["error"])
							assert.Equal(t, test.requested, fields["requested"])
							if transport != "websocket" && version == "2.0" {
								assert.NotContains(t, fields, "request")
								return
							}
							echo, ok := fields["request"].(map[string]any)
							require.True(t, ok)
							assert.Equal(t, test.requested, echo["public_key"])
							assert.Equal(t, "echo", echo["id"])
							assert.Equal(t, []any{"kept", map[string]any{
								"SeCrEt": maskedValue,
								"url":    maskedValue,
							}}, echo["extra"])
						})
					}
				})
			}
		})
	}
}

func TestRequestEchoPreservesInput(t *testing.T) {
	request := map[string]any{"nested": []any{map[string]any{
		"value": "prefix\x00tail", "secret": "private\x00tail",
	}}}
	before, err := json.Marshal(request)
	require.NoError(t, err)
	echo := redactedRequestMap(request)
	assert.Equal(t, map[string]any{"nested": []any{map[string]any{
		"value": "prefix", "secret": maskedValue,
	}}}, echo)
	after, err := json.Marshal(request)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

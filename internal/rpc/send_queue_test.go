package rpc

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestResolveSendQueueLimit(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		want    int
		wantErr bool
	}{
		{name: "nil context", want: DefaultSendQueueLimit},
		{name: "zero uses default", value: 0, want: DefaultSendQueueLimit},
		{name: "minimum explicit value", value: 1, want: 1},
		{name: "maximum explicit value", value: maxSendQueueLimit, want: maxSendQueueLimit},
		{name: "negative rejected", value: -1, wantErr: true},
		{name: "above uint16 rejected", value: maxSendQueueLimit + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var portCtx *PortContext
			if test.name != "nil context" {
				portCtx = &PortContext{SendQueue: test.value}
			}
			got, err := resolveSendQueueLimit(portCtx)
			if test.wantErr {
				require.Error(t, err)
				require.Zero(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestWebSocketSendQueueLimitRealHandshake(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "zero uses default", value: 0, want: DefaultSendQueueLimit},
		{name: "minimum explicit value", value: 1, want: 1},
		{name: "maximum explicit value", value: maxSendQueueLimit, want: maxSendQueueLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws := NewWebSocketServer(time.Second, nil)
			httpServer := httptest.NewServer(PortMiddleware(
				&PortContext{SendQueue: test.value},
				http.HandlerFunc(ws.ServeHTTP),
			))
			t.Cleanup(func() {
				httpServer.Close()
				_ = ws.Close(context.Background())
			})

			client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
			require.NoError(t, err)
			require.NotNil(t, response)
			require.Equal(t, http.StatusSwitchingProtocols, response.StatusCode)
			t.Cleanup(func() { _ = client.Close() })

			var connection *WebSocketConnection
			require.Eventually(t, func() bool {
				ws.connectionsMutex.RLock()
				defer ws.connectionsMutex.RUnlock()
				for _, candidate := range ws.connections {
					connection = candidate
					return true
				}
				return false
			}, time.Second, time.Millisecond)
			require.Equal(t, test.want, cap(connection.sendChannel))
		})
	}
}

func TestWebSocketSendQueueLimitInvalidConfigFailsBeforeUpgrade(t *testing.T) {
	for _, value := range []int{-1, maxSendQueueLimit + 1} {
		t.Run(strings.ReplaceAll(strconv.Itoa(value), "-", "negative_"), func(t *testing.T) {
			ws := NewWebSocketServer(time.Second, nil)
			httpServer := httptest.NewServer(PortMiddleware(
				&PortContext{SendQueue: value},
				http.HandlerFunc(ws.ServeHTTP),
			))
			t.Cleanup(func() {
				httpServer.Close()
				_ = ws.Close(context.Background())
			})

			client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
			require.Error(t, err)
			require.Nil(t, client)
			require.NotNil(t, response)
			require.Equal(t, http.StatusInternalServerError, response.StatusCode)
			_, _ = io.Copy(io.Discard, response.Body)
			require.NoError(t, response.Body.Close())
			ws.connectionsMutex.RLock()
			require.Empty(t, ws.connections)
			ws.connectionsMutex.RUnlock()
		})
	}
}

package node

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestWebSocketInvalidSendQueueReleasesListenerSlot(t *testing.T) {
	for _, value := range []int{-1, 65536} {
		t.Run(strconv.Itoa(value), func(t *testing.T) {
			ws := rpc.NewWebSocketServer(time.Second, nil)
			bound, err := bindRPCTransports(
				t.Context(),
				xrpllog.Discard(),
				&config.Config{Ports: map[string]config.PortConfig{
					"http": {IP: "127.0.0.1", Port: 0, Protocol: "http"},
					"ws":   {IP: "127.0.0.1", Port: 0, Protocol: "ws", Limit: 1, SendQueueLimit: value},
				}},
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}),
				ws,
				nil,
				systemListen,
			)
			require.NoError(t, err)
			require.NoError(t, bound.serve(xrpllog.Discard()))
			t.Cleanup(func() {
				for _, server := range append(append([]*boundHTTPServer(nil), bound.ws...), bound.http...) {
					_ = server.server.Close()
				}
				_ = bound.closeListeners()
				bound.wait()
				_ = ws.Close(context.Background())
			})

			url := "ws://" + bound.ws[0].address + "/"
			for range 2 {
				client, response, dialErr := websocket.DefaultDialer.Dial(url, nil)
				require.Error(t, dialErr)
				require.Nil(t, client)
				require.NotNil(t, response)
				require.Equal(t, http.StatusInternalServerError, response.StatusCode)
				_, _ = io.Copy(io.Discard, response.Body)
				require.NoError(t, response.Body.Close())

				require.Eventually(t, func() bool {
					bound.limiter.mu.Lock()
					defer bound.limiter.mu.Unlock()
					return bound.limiter.total == 0 && bound.limiter.counts["ws"] == 0
				}, time.Second, time.Millisecond)
			}
		})
	}
}

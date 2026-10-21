package rpc

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONRealZeroAndSubnormalBounds(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want float64
	}{
		{raw: "0e-400"},
		{raw: "0e-99999999999999"},
		{raw: "-0.0e9999", want: math.Copysign(0, -1)},
		{raw: "5e-324", want: math.SmallestNonzeroFloat64},
		{raw: "2.5e-324", want: math.SmallestNonzeroFloat64},
		{raw: "-5e-324", want: -math.SmallestNonzeroFloat64},
	} {
		t.Run(test.raw, func(t *testing.T) {
			var value any
			require.NoError(t, decodeJSONUseNumber([]byte(test.raw), &value))
			real, ok := value.(jsonReal)
			require.True(t, ok)
			assert.Equal(t, math.Float64bits(test.want), math.Float64bits(float64(real)))
		})
	}
}

func TestJSONOutOfRangeNumbersRejectedBeforeDispatch(t *testing.T) {
	for _, raw := range []string{
		"1e-400", "-1e-400", "123e-500", "2.4e-324", "-2.4e-324",
		"0." + strings.Repeat("0", 400) + "1", "1e400", "-1e400",
		"4294967296", "-2147483649",
	} {
		t.Run(raw, func(t *testing.T) {
			registry := mustTestMethodRegistry(t, map[string]types.MethodHandler{
				"capture": &stubHandler{handle: func(*types.RpcContext, json.RawMessage) (any, *rpcerrors.RpcError) {
					t.Error("out-of-range JSON number reached handler dispatch")
					return nil, nil
				}},
			})
			for _, body := range []string{
				`{"method":"capture","params":[{"nested":[{"value":` + raw + `}]}]}`,
				`{"method":"batch","params":[{"method":"capture"},{"method":"capture","value":` + raw + `}]}`,
			} {
				server := NewServer(ServerOptions{Timeout: time.Second, Registry: registry})
				response := postTransportRegressionRequest(t, server, body)
				assert.Equal(t, http.StatusBadRequest, response.Code)
				assert.Equal(t, unableToParseRequest+"\r\n", response.Body.String())
			}
			server := NewWebSocketServer(WebSocketServerOptions{Timeout: time.Second, Registry: registry})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, server.Close(ctx))
			})
			body := wsRawRoundTrip(t, server, `{"command":"capture","nested":[{"value":`+raw+`}]}`)
			var response map[string]any
			require.NoError(t, json.Unmarshal(body, &response))
			assert.Equal(t, "jsonInvalid", response["error"])
		})
	}
}

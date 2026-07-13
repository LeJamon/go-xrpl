package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func postTransportRequest(t *testing.T, server *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.5:1234"
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	return recorder
}

func TestHTTPRipplerpcVersionedInternalError(t *testing.T) {
	server := newHardeningServer(t, time.Second, "fail", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, rpcInternalError()
		},
	})

	tests := []struct {
		version string
		status  int
		want    string
	}{
		{
			version: "1.0",
			status:  http.StatusOK,
			want:    "{\"id\":7,\"jsonrpc\":\"2.0\",\"result\":{\"error\":\"internal\",\"error_code\":73,\"error_message\":\"Internal error.\",\"request\":{\"command\":\"fail\",\"id\":7,\"jsonrpc\":\"2.0\",\"ripplerpc\":\"1.0\",\"secret\":\"<masked>\"},\"status\":\"error\"},\"ripplerpc\":\"1.0\"}\n\r\n",
		},
		{
			version: "2.0",
			status:  http.StatusOK,
			want:    "{\"error\":{\"code\":73,\"error\":\"internal\",\"error_code\":73,\"message\":\"Internal error.\",\"status\":\"error\"},\"id\":7,\"jsonrpc\":\"2.0\",\"ripplerpc\":\"2.0\"}\n\r\n",
		},
		{
			version: "2.00",
			status:  http.StatusOK,
			want:    "{\"error\":{\"code\":73,\"error\":\"internal\",\"error_code\":73,\"message\":\"Internal error.\",\"status\":\"error\"},\"id\":7,\"jsonrpc\":\"2.0\",\"ripplerpc\":\"2.00\"}\n\r\n",
		},
		{
			version: "3.0",
			status:  http.StatusInternalServerError,
			want:    "{\"error\":{\"code\":73,\"error\":\"internal\",\"error_code\":73,\"message\":\"Internal error.\",\"status\":\"error\"},\"id\":7,\"jsonrpc\":\"2.0\",\"ripplerpc\":\"3.0\"}\n\r\n",
		},
	}

	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			body := `{"method":"fail","params":[{"id":7,"jsonrpc":"2.0","ripplerpc":"` + test.version + `","secret":"private seed"}]}`
			recorder := postTransportRequest(t, server, body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, test.status, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); got != jsonContentType {
				t.Fatalf("content type = %q, want %q", got, jsonContentType)
			}
			if got := recorder.Body.String(); got != test.want {
				t.Fatalf("body = %q, want %q", got, test.want)
			}
			if strings.Contains(recorder.Body.String(), "private seed") {
				t.Fatalf("response leaked credential: %s", recorder.Body.String())
			}
		})
	}
}

func TestHTTPRipplerpcV3StatusMapping(t *testing.T) {
	tests := []struct {
		name    string
		version string
		rpcErr  *types.RpcError
		status  int
	}{
		{name: "v2 too busy remains 200", version: "2.0", rpcErr: types.RpcErrorTooBusy(), status: http.StatusOK},
		{name: "v3 too busy", version: "3.0", rpcErr: types.RpcErrorTooBusy(), status: http.StatusServiceUnavailable},
		{name: "v3 database deserialization", version: "3.0", rpcErr: types.RpcErrorDBDeserialization(), status: http.StatusBadGateway},
		{name: "v3 invalid params", version: "3.0", rpcErr: types.RpcErrorInvalidParams("Invalid parameters."), status: http.StatusBadRequest},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newHardeningServer(t, time.Second, "fail", &stubHandler{
				handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
					return nil, test.rpcErr
				},
			})
			recorder := postTransportRequest(t, server, `{"method":"fail","params":[{"ripplerpc":"`+test.version+`"}]}`)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, test.status, recorder.Body.String())
			}
		})
	}
}

func TestHTTPResponseFraming(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
		recorder := postTransportRequest(t, server, `{"method":"ping","params":[{}]}`)
		const want = "{\"result\":{\"ok\":true,\"status\":\"success\"}}\n\r\n"
		if got := recorder.Body.String(); got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
	})

	t.Run("batch", func(t *testing.T) {
		server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
		recorder := postTransportRequest(t, server, `{"method":"batch","params":[]}`)
		if got := recorder.Body.String(); got != "[]\n\r\n" {
			t.Fatalf("body = %q, want empty batch framing", got)
		}
	})

	t.Run("plain error", func(t *testing.T) {
		server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
		recorder := postTransportRequest(t, server, `{"method":"batch","params":null}`)
		if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "Malformed batch request\r\n" {
			t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != jsonContentType {
			t.Fatalf("content type = %q, want %q", got, jsonContentType)
		}
	})

	t.Run("versioned success metadata", func(t *testing.T) {
		server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
		recorder := postTransportRequest(t, server, `{"method":"ping","params":[{"id":7,"jsonrpc":"2.0","ripplerpc":"2.0"}]}`)
		const want = "{\"id\":7,\"jsonrpc\":\"2.0\",\"result\":{\"ok\":true,\"status\":\"success\"},\"ripplerpc\":\"2.0\"}\n\r\n"
		if recorder.Code != http.StatusOK || recorder.Body.String() != want {
			t.Fatalf("status/body = %d/%q, want 200/%q", recorder.Code, recorder.Body.String(), want)
		}
	})
}

func TestHTTPParamsShape(t *testing.T) {
	var observed string
	server := newHardeningServer(t, time.Second, "capture", &stubHandler{
		handle: func(_ *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
			observed = string(params)
			return map[string]any{"ok": true}, nil
		},
	})

	valid := []struct {
		name string
		body string
		want string
	}{
		{name: "missing", body: `{"method":"capture"}`, want: `{}`},
		{name: "top-level null", body: `{"method":"capture","params":null}`, want: `{}`},
		{name: "array null", body: `{"method":"capture","params":[null]}`, want: `null`},
		{name: "object", body: `{"method":"capture","params":[{}]}`, want: `{}`},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			observed = ""
			recorder := postTransportRequest(t, server, test.body)
			if recorder.Code != http.StatusOK || observed != test.want {
				t.Fatalf("status/params = %d/%q, want 200/%q", recorder.Code, observed, test.want)
			}
		})
	}

	invalid := []string{
		`{"method":"capture","params":[]}`,
		`{"method":"capture","params":[{},{}]}`,
		`{"method":"capture","params":["x"]}`,
	}
	for _, body := range invalid {
		recorder := postTransportRequest(t, server, body)
		if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "params unparseable\r\n" {
			t.Fatalf("body %s produced %d/%q", body, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHTTPRipplerpcValidation(t *testing.T) {
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})

	single := postTransportRequest(t, server, `{"method":"ping","params":[{"ripplerpc":2}]}`)
	if single.Code != http.StatusBadRequest || single.Body.String() != "ripplerpc is not a string\r\n" {
		t.Fatalf("single status/body = %d/%q", single.Code, single.Body.String())
	}

	batch := postTransportRequest(t, server, `{"method":"batch","params":[{"method":"ping","ripplerpc":2}]}`)
	const wantBatch = "[{\"error\":{\"error\":{\"code\":-32601,\"message\":\"ripplerpc is not a string\"}},\"method\":\"ping\",\"ripplerpc\":2}]\n\r\n"
	if batch.Code != http.StatusOK || batch.Body.String() != wantBatch {
		t.Fatalf("batch status/body = %d/%q", batch.Code, batch.Body.String())
	}
}

func TestHTTPErrorExceptionEnvelope(t *testing.T) {
	server := newHardeningServer(t, time.Second, "simulate", &stubHandler{
		handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
			return nil, types.RpcErrorInvalidTransaction("invalid transaction detail")
		},
	})
	recorder := postTransportRequest(t, server, `{"method":"simulate","params":[{"id":9,"secret":"private seed"}]}`)
	const want = "{\"id\":9,\"result\":{\"error\":\"invalidTransaction\",\"error_exception\":\"invalid transaction detail\",\"request\":{\"command\":\"simulate\",\"id\":9,\"secret\":\"<masked>\"},\"status\":\"error\"}}\n\r\n"
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("status/body = %d/%q, want 200/%q", recorder.Code, recorder.Body.String(), want)
	}
	if strings.Contains(recorder.Body.String(), "error_code") || strings.Contains(recorder.Body.String(), "error_message") {
		t.Fatalf("manual error gained injected fields: %s", recorder.Body.String())
	}
}

func TestHTTPRipplerpcV2SparseErrorFields(t *testing.T) {
	tests := []struct {
		name   string
		rpcErr *types.RpcError
		want   string
	}{
		{
			name:   "exception",
			rpcErr: types.RpcErrorInvalidTransaction("invalid transaction detail"),
			want:   "{\"error\":{\"code\":null,\"error\":\"invalidTransaction\",\"error_code\":null,\"error_exception\":\"invalid transaction detail\",\"message\":null,\"status\":\"error\"},\"id\":9,\"ripplerpc\":\"2.0\"}\n\r\n",
		},
		{
			name:   "bare token",
			rpcErr: types.RpcErrorEntryNotFound("Entry not found."),
			want:   "{\"error\":{\"code\":null,\"error\":\"entryNotFound\",\"error_code\":null,\"message\":null,\"status\":\"error\"},\"id\":9,\"ripplerpc\":\"2.0\"}\n\r\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newHardeningServer(t, time.Second, "fail", &stubHandler{
				handle: func(*types.RpcContext, json.RawMessage) (any, *types.RpcError) {
					return nil, test.rpcErr
				},
			})
			recorder := postTransportRequest(t, server, `{"method":"fail","params":[{"id":9,"ripplerpc":"2.0"}]}`)
			if recorder.Code != http.StatusOK || recorder.Body.String() != test.want {
				t.Fatalf("status/body = %d/%q, want 200/%q", recorder.Code, recorder.Body.String(), test.want)
			}
		})
	}
}

func TestHTTPApiVersionRequiresJSONInteger(t *testing.T) {
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	for _, rawVersion := range []string{`"2"`, `1.5`, `1.0`, `1e0`} {
		t.Run(rawVersion, func(t *testing.T) {
			recorder := postTransportRequest(t, server, `{"method":"ping","params":[{"api_version":`+rawVersion+`}]}`)
			if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "invalid_API_version\r\n" {
				t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHTTPApiVersionValidationPrecedesMethodValidation(t *testing.T) {
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	recorder := postTransportRequest(t, server, `{"params":[{"api_version":"2"}]}`)
	if recorder.Code != http.StatusBadRequest || recorder.Body.String() != "invalid_API_version\r\n" {
		t.Fatalf("status/body = %d/%q", recorder.Code, recorder.Body.String())
	}
}

func TestBatchApiVersionRequiresJSONIntegerAndPrecedesMethodValidation(t *testing.T) {
	server := newHardeningServer(t, time.Second, "ping", &stubHandler{})
	recorder := postTransportRequest(t, server, `{"method":"batch","params":[{"method":7,"api_version":1e0,"id":4294967295}]}`)
	const want = "[{\"error\":{\"error\":{\"code\":-32606,\"message\":\"invalid_API_version\"}},\"request\":{\"api_version\":1,\"id\":4294967295,\"method\":7}}]\n\r\n"
	if recorder.Code != http.StatusOK || recorder.Body.String() != want {
		t.Fatalf("status/body = %d/%q, want 200/%q", recorder.Code, recorder.Body.String(), want)
	}
}

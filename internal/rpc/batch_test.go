package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// echoHandler returns the params it received plus the resolved api_version, so
// tests can assert per-element dispatch and version resolution.
func echoHandler() *stubHandler {
	return &stubHandler{
		handle: func(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
			var p map[string]interface{}
			_ = json.Unmarshal(params, &p)
			return map[string]interface{}{
				"echo":        p,
				"api_version": ctx.ApiVersion,
			}, nil
		},
	}
}

func newBatchServer(t *testing.T) *Server {
	t.Helper()
	srv := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  time.Second,
		services: types.NewServiceContainer(nil),
	}
	srv.registry.Register("ping", echoHandler())
	srv.registry.Register("account_info", echoHandler())
	return srv
}

func postBatch(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.1:1234"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

// TestBatch_DispatchesEachElement verifies a batch envelope returns a JSON
// array with one reply per element, each in the standard result envelope and in
// request order, mirroring rippled's ServerHandler.cpp:651-683.
func TestBatch_DispatchesEachElement(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[
		{"method":"ping","value":1},
		{"method":"account_info","account":"rABC"}
	]}`
	rr := postBatch(t, srv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}

	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("batch reply is not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	if len(replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(replies))
	}

	for i, reply := range replies {
		result, ok := reply["result"].(map[string]interface{})
		if !ok {
			t.Fatalf("reply %d missing result object: %v", i, reply)
		}
		if result["status"] != "success" {
			t.Fatalf("reply %d status = %v, want success", i, result["status"])
		}
	}

	echo0 := replies[0]["result"].(map[string]interface{})["echo"].(map[string]interface{})
	if echo0["value"] != float64(1) {
		t.Fatalf("first element lost its params: %v", echo0)
	}
	echo1 := replies[1]["result"].(map[string]interface{})["echo"].(map[string]interface{})
	if echo1["account"] != "rABC" {
		t.Fatalf("second element lost its params: %v", echo1)
	}
}

// TestBatch_UnknownMethodPerElement verifies that an unregistered method in one
// element produces a method_not_found error for that element only, leaving the
// other elements successful.
func TestBatch_UnknownMethodPerElement(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[
		{"method":"ping"},
		{"method":"does_not_exist"}
	]}`
	rr := postBatch(t, srv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	if len(replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(replies))
	}

	if got := replies[0]["result"].(map[string]interface{})["status"]; got != "success" {
		t.Fatalf("element 0 status = %v, want success", got)
	}
	errResult := replies[1]["result"].(map[string]interface{})
	if errResult["status"] != "error" {
		t.Fatalf("element 1 status = %v, want error", errResult["status"])
	}
	if errResult["error"] != "unknownCmd" {
		t.Fatalf("element 1 error = %v, want unknownCmd", errResult["error"])
	}
}

// TestBatch_PerElementApiVersion verifies api_version is resolved independently
// per element, both from a top-level field and from params[0], matching
// rippled's two-level lookup (ServerHandler.cpp:668-683).
func TestBatch_PerElementApiVersion(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[
		{"method":"ping","api_version":1},
		{"method":"ping","params":[{"api_version":2}]}
	]}`
	rr := postBatch(t, srv, body)

	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}

	v0 := replies[0]["result"].(map[string]interface{})["api_version"]
	if v0 != float64(1) {
		t.Fatalf("element 0 api_version = %v, want 1", v0)
	}
	v1 := replies[1]["result"].(map[string]interface{})["api_version"]
	if v1 != float64(2) {
		t.Fatalf("element 1 api_version = %v, want 2", v1)
	}
}

// TestBatch_MalformedReturns400 verifies that a batch whose params is missing,
// null, or not an array is rejected with HTTP 400, matching rippled's
// "Malformed batch request" guard (ServerHandler.cpp:643-647). An empty array
// is NOT malformed — see TestBatch_EmptyArrayReturnsEmptyReply.
func TestBatch_MalformedReturns400(t *testing.T) {
	cases := map[string]string{
		"no params":     `{"method":"batch"}`,
		"null params":   `{"method":"batch","params":null}`,
		"object params": `{"method":"batch","params":{"method":"ping"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newBatchServer(t)
			rr := postBatch(t, srv, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d\nbody: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestBatch_EmptyArrayReturnsEmptyReply verifies an empty params array is valid:
// size is 0, no elements are dispatched, and the reply is an empty JSON array
// with HTTP 200, matching rippled's zero-iteration path (ServerHandler.cpp:648-653).
func TestBatch_EmptyArrayReturnsEmptyReply(t *testing.T) {
	srv := newBatchServer(t)
	rr := postBatch(t, srv, `{"method":"batch","params":[]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d\nbody: %s", rr.Code, rr.Body.String())
	}
	if got := strings.TrimSpace(rr.Body.String()); got != "[]" {
		t.Fatalf("expected empty array reply %q, got %q", "[]", got)
	}
}

// TestBatch_NonObjectElement verifies a non-object batch element is echoed under
// "request" with a double-nested method_not_found JSON-RPC error, rather than
// aborting the whole batch (ServerHandler.cpp:658-665).
func TestBatch_NonObjectElement(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[42,{"method":"ping"}]}`
	rr := postBatch(t, srv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	if len(replies) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(replies))
	}
	// rippled echoes the raw value under "request" with error.error.{code,message}.
	if replies[0]["request"] != float64(42) {
		t.Fatalf("element 0 should echo raw request 42, got %v", replies[0]["request"])
	}
	inner := nestedBatchError(t, replies[0])
	if inner["code"] != float64(-32601) {
		t.Fatalf("element 0 error code = %v, want -32601", inner["code"])
	}
	if inner["message"] != "Method not found" {
		t.Fatalf("element 0 error message = %v, want 'Method not found'", inner["message"])
	}
	if got := replies[1]["result"].(map[string]interface{})["status"]; got != "success" {
		t.Fatalf("element 1 status = %v, want success", got)
	}
}

// TestBatch_MalformedMethodElements verifies each method-less element shape is
// echoed at the top level with the rippled-faithful per-element error message:
// missing/null method → "Null method", non-string → "method is not string",
// empty string → "method is empty" (ServerHandler.cpp:764-808).
func TestBatch_MalformedMethodElements(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[
		{"id":1},
		{"method":null,"id":2},
		{"method":123,"id":3},
		{"method":"","id":4}
	]}`
	rr := postBatch(t, srv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	want := []string{"Null method", "Null method", "method is not string", "method is empty"}
	if len(replies) != len(want) {
		t.Fatalf("expected %d replies, got %d", len(want), len(replies))
	}
	for i, msg := range want {
		inner := nestedBatchError(t, replies[i])
		if inner["code"] != float64(-32601) {
			t.Fatalf("element %d code = %v, want -32601", i, inner["code"])
		}
		if inner["message"] != msg {
			t.Fatalf("element %d message = %v, want %q", i, inner["message"], msg)
		}
		// The element's own fields are echoed at the top level.
		if replies[i]["id"] != float64(i+1) {
			t.Fatalf("element %d should echo id=%d, got %v", i, i+1, replies[i]["id"])
		}
	}
}

// nestedBatchError extracts the rippled double-nested error.error object
// (make_json_error, ServerHandler.cpp:594-603) from a malformed batch element.
func nestedBatchError(t *testing.T, reply map[string]interface{}) map[string]interface{} {
	t.Helper()
	errObj, ok := reply["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("reply missing error object: %v", reply)
	}
	inner, ok := errObj["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("error is not double-nested (rippled make_json_error): %v", errObj)
	}
	return inner
}

// TestBatch_CredentialsMaskedInErrorEcho verifies the per-element error echo
// masks credential fields before they leave the process.
func TestBatch_CredentialsMaskedInErrorEcho(t *testing.T) {
	srv := newBatchServer(t)

	body := `{"method":"batch","params":[{"method":"does_not_exist","secret":"sssh"}]}`
	rr := postBatch(t, srv, body)

	var replies []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &replies); err != nil {
		t.Fatalf("not a JSON array: %v\nbody: %s", err, rr.Body.String())
	}
	result := replies[0]["result"].(map[string]interface{})
	echo, ok := result["request"].(map[string]interface{})
	if !ok {
		t.Fatalf("error reply missing request echo: %v", result)
	}
	if echo["secret"] != maskedValue {
		t.Fatalf("secret not masked in echo: %v", echo["secret"])
	}
}
